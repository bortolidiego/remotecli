import type { AgentStatus, AuthenticatedStatus, Capability, CodexApproval, CodexEvent, PairResponse, SessionDescriptor, SignedEnvelope } from '../types/api'

const API_PREFIX = '/api'
const DB_NAME = 'relay-keys-v1'
const STORE_NAME = 'keys'
const PAIR_CHALLENGE_VERSION = 'relay-pair-v1'
const SESSION_INFO = new TextEncoder().encode('relay-session-v1')

export interface LeaseAuth {
  deviceId: string
  leaseToken: string
}

export interface PairState extends LeaseAuth {
  leaseExpiry: string
  hostName: string
  sessionId: string
  hostId: string
}

async function api<T>(path: string, init: RequestInit = {}, auth?: LeaseAuth | string): Promise<T> {
  const headers = new Headers(init.headers || {})
  headers.set('Accept', 'application/json')
  if (typeof auth === 'string') {
    headers.set('X-Relay-Local-Token', auth)
  } else if (auth) {
    headers.set('X-Relay-Device-ID', auth.deviceId)
    headers.set('X-Relay-Lease-Token', auth.leaseToken)
  }
  const res = await fetch(`${API_PREFIX}${path}`, { ...init, headers, credentials: 'same-origin' })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(`${res.status}: ${text}`)
  }
  return res.json() as Promise<T>
}

export async function fetchHealth(): Promise<AgentStatus> {
  const res = await fetch('/health', { headers: { Accept: 'application/json' }, credentials: 'same-origin' })
  if (!res.ok) throw new Error(`${res.status}: ${await res.text()}`)
  return res.json() as Promise<AgentStatus>
}

export async function fetchStatus(auth: LeaseAuth | string): Promise<AuthenticatedStatus> {
  return api('/status', {}, auth)
}

export async function fetchSessions(auth: LeaseAuth | string): Promise<SessionDescriptor[]> {
  return api('/sessions', {}, auth)
}

export async function fetchSessionDetail(id: string, auth: LeaseAuth | string): Promise<SessionDescriptor> {
  return api(`/sessions/${encodeURIComponent(id)}`, {}, auth)
}

export async function startTurn(sessionId: string, text: string, auth: LeaseAuth | string): Promise<{ turn_id: string; status: string }> {
  return api(`/sessions/${encodeURIComponent(sessionId)}/turn`, { method: 'POST', body: JSON.stringify({ text }) }, auth)
}

export async function sendSessionMessage(
  sessionId: string,
  text: string,
  auth: LeaseAuth | string,
): Promise<{ status: string; mode: string; reply?: string; turn_id?: string }> {
  return api(`/sessions/${encodeURIComponent(sessionId)}/message`, {
    method: 'POST',
    body: JSON.stringify({ text }),
  }, auth)
}

export async function fetchSessionOutput(
  sessionId: string,
  auth: LeaseAuth | string,
): Promise<{ text: string; source?: string; name?: string }> {
  return api(`/sessions/${encodeURIComponent(sessionId)}/output`, {}, auth)
}

export async function interruptTurn(sessionId: string, auth: LeaseAuth | string): Promise<{ status: string }> {
  return api(`/sessions/${encodeURIComponent(sessionId)}/interrupt`, { method: 'POST' }, auth)
}

export async function fetchEvents(sessionId: string, auth: LeaseAuth | string): Promise<CodexEvent[]> {
  return api(`/sessions/${encodeURIComponent(sessionId)}/events`, {}, auth)
}

export async function fetchApprovals(sessionId: string, auth: LeaseAuth | string): Promise<CodexApproval[]> {
  return api(`/sessions/${encodeURIComponent(sessionId)}/approvals`, {}, auth)
}

export async function decideApproval(sessionId: string, approvalId: string, decision: 'accept' | 'deny', auth: LeaseAuth | string): Promise<{ status: string }> {
  return api(`/sessions/${encodeURIComponent(sessionId)}/approvals/${encodeURIComponent(approvalId)}`, { method: 'POST', body: JSON.stringify({ decision }) }, auth)
}

export async function releaseLease(auth: LeaseAuth): Promise<{ status: string }> {
  return api('/lease/release', { method: 'POST' }, auth)
}

export async function revoke(deviceId: string, token: string): Promise<{ status: string }> {
  return api(`/revoke?device_id=${encodeURIComponent(deviceId)}`, { method: 'POST' }, token)
}

export async function pair(offerJson: string, deviceName = 'Web PWA', deviceId = stableDeviceId()): Promise<PairState> {
  const parsed = parseOfferInput(offerJson)
  const verified = await verifyOffer(parsed)
  const keys = await loadOrCreateKeys()
  const clientKey = bytesToBase64(new Uint8Array(await crypto.subtle.exportKey('spki', keys.signing.publicKey)))
  const clientECDH = bytesToBase64(new Uint8Array(await crypto.subtle.exportKey('raw', keys.ecdh.publicKey)))
  const capabilities: Capability[] = ['native_control']
  const challenge = buildPairChallenge({
    session_id: verified.offer.session_id,
    host_id: verified.offer.host_id,
    device_id: deviceId,
    name: deviceName,
    nonce: verified.offer.nonce,
    client_key: clientKey,
    client_ecdh: clientECDH,
    capabilities,
  })
  const signature = new Uint8Array(await crypto.subtle.sign(
    { name: 'ECDSA', hash: 'SHA-256' },
    keys.signing.privateKey,
    challenge,
  ))

  const sessionKey = await deriveSessionKey(keys.ecdh.privateKey, verified.offer.host_ecdh)
  await putKey(sessionKeyID(verified.offer.host_id, verified.offer.session_id), sessionKey)

  const res = await fetch(`${API_PREFIX}/pair`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    credentials: 'same-origin',
    body: JSON.stringify({
      session_id: verified.offer.session_id,
      host_id: verified.offer.host_id,
      device_id: deviceId,
      name: deviceName,
      client_key: clientKey,
      client_ecdh: clientECDH,
      client_signature: bytesToBase64(signature),
      nonce: verified.offer.nonce,
      capabilities,
    }),
  })
  if (!res.ok) throw new Error(await res.text())
  const response = (await res.json()) as PairResponse
  return {
    deviceId: response.device_id,
    leaseToken: response.lease_token,
    leaseExpiry: response.lease_expiry,
    hostName: response.host_name,
    sessionId: response.session_id,
    hostId: verified.offer.host_id,
  }
}

function parseOfferInput(input: string): SignedEnvelope | { payload: string } {
  const raw = input.trim()
  if (!raw) throw new Error('Cole o envelope ou payload do QR.')
  const parsed = JSON.parse(raw)
  if (parsed.payload && parsed.signature && parsed.signer_key) return parsed as SignedEnvelope
  return { payload: bytesToBase64(new TextEncoder().encode(JSON.stringify(parsed))) }
}

async function verifyOffer(envelope: SignedEnvelope | { payload: string }) {
  const payloadBytes = base64ToBytes(envelope.payload)
  const offer = JSON.parse(new TextDecoder().decode(payloadBytes))
  if (!offer.host_key || !offer.host_ecdh || !offer.host_id || !offer.nonce) {
    throw new Error('Oferta incompleta.')
  }
  const hostKeyBytes = base64ToBytes(offer.host_key)
  const fingerprint = await fingerprintHost(hostKeyBytes)
  if (fingerprint !== offer.host_id) {
    throw new Error('Fingerprint da oferta não confere com a chave do host.')
  }
  if ('signature' in envelope) {
    const signer = base64ToBytes(envelope.signer_key)
    if (bytesToBase64(signer) !== bytesToBase64(hostKeyBytes)) {
      throw new Error('Assinante da oferta difere da chave do host.')
    }
    const pub = await crypto.subtle.importKey(
      'spki',
      bytesToArrayBuffer(hostKeyBytes),
      { name: 'ECDSA', namedCurve: 'P-256' },
      false,
      ['verify'],
    )
    const sig = derToP1363(base64ToBytes(envelope.signature))
    const ok = await crypto.subtle.verify({ name: 'ECDSA', hash: 'SHA-256' }, pub, bytesToArrayBuffer(sig), bytesToArrayBuffer(payloadBytes))
    if (!ok) throw new Error('Assinatura da oferta inválida.')
  } else {
    throw new Error('Envelope assinado obrigatório para pareamento real.')
  }
  return { offer }
}

function buildPairChallenge(input: {
  session_id: string
  host_id: string
  device_id: string
  name: string
  nonce: string
  client_key: string
  client_ecdh: string
  capabilities: Capability[]
}) {
  return new TextEncoder().encode(JSON.stringify({
    version: PAIR_CHALLENGE_VERSION,
    session_id: input.session_id,
    host_id: input.host_id,
    device_id: input.device_id,
    name: input.name,
    nonce: input.nonce,
    client_key: input.client_key,
    client_ecdh: input.client_ecdh,
    capabilities: input.capabilities,
  }))
}

async function loadOrCreateKeys() {
  const existing = await getKey<{ signing: CryptoKeyPair; ecdh: CryptoKeyPair }>('device:p256')
  if (existing) return existing
  const signing = await crypto.subtle.generateKey(
    { name: 'ECDSA', namedCurve: 'P-256' },
    false,
    ['sign', 'verify'],
  ) as CryptoKeyPair
  const ecdh = await crypto.subtle.generateKey(
    { name: 'ECDH', namedCurve: 'P-256' },
    false,
    ['deriveBits', 'deriveKey'],
  ) as CryptoKeyPair
  const keys = { signing, ecdh }
  await putKey('device:p256', keys)
  return keys
}

async function deriveSessionKey(privateKey: CryptoKey, hostECDHBase64: string) {
  const hostKey = await crypto.subtle.importKey(
    'raw',
    bytesToArrayBuffer(base64ToBytes(hostECDHBase64)),
    { name: 'ECDH', namedCurve: 'P-256' },
    false,
    [],
  )
  const bits = await crypto.subtle.deriveBits({ name: 'ECDH', public: hostKey }, privateKey, 256)
  const hkdfKey = await crypto.subtle.importKey('raw', bits, 'HKDF', false, ['deriveKey'])
  return crypto.subtle.deriveKey(
    { name: 'HKDF', hash: 'SHA-256', salt: new Uint8Array(), info: bytesToArrayBuffer(SESSION_INFO) },
    hkdfKey,
    { name: 'AES-GCM', length: 256 },
    false,
    ['encrypt', 'decrypt'],
  )
}

async function fingerprintHost(bytes: Uint8Array) {
  const digest = new Uint8Array(await crypto.subtle.digest('SHA-256', bytesToArrayBuffer(bytes)))
  return bytesToBase64Url(digest.slice(0, 12))
}

/** UUID v4 — Safari em HTTP na LAN não tem crypto.randomUUID; fallback com getRandomValues. */
function randomUUID() {
  const c = globalThis.crypto
  if (c && typeof c.randomUUID === 'function') {
    return c.randomUUID()
  }
  const bytes = new Uint8Array(16)
  if (!c || typeof c.getRandomValues !== 'function') {
    // Último recurso (não deve ocorrer em browsers modernos)
    for (let i = 0; i < 16; i++) bytes[i] = Math.floor(Math.random() * 256)
  } else {
    c.getRandomValues(bytes)
  }
  bytes[6] = (bytes[6] & 0x0f) | 0x40
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

function stableDeviceId() {
  const stored = localStorage.getItem('relay:device-id')
  if (stored) return stored
  const id = `web-${randomUUID()}`
  localStorage.setItem('relay:device-id', id)
  return id
}

function sessionKeyID(hostID: string, sessionID: string) {
  return `session:${hostID}:${sessionID}:aes`
}

export async function getSessionKey(hostID: string, sessionID: string): Promise<CryptoKey | undefined> {
  return getKey<CryptoKey>(sessionKeyID(hostID, sessionID))
}

function openDB(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, 1)
    req.onupgradeneeded = () => req.result.createObjectStore(STORE_NAME)
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
}

async function getKey<T>(id: string): Promise<T | undefined> {
  const db = await openDB()
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE_NAME, 'readonly')
    const req = tx.objectStore(STORE_NAME).get(id)
    req.onsuccess = () => resolve(req.result as T | undefined)
    req.onerror = () => reject(req.error)
  })
}

async function putKey(id: string, value: unknown): Promise<void> {
  const db = await openDB()
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE_NAME, 'readwrite')
    tx.objectStore(STORE_NAME).put(value, id)
    tx.oncomplete = () => resolve()
    tx.onerror = () => reject(tx.error)
  })
}

function bytesToBase64(bytes: Uint8Array) {
  let binary = ''
  bytes.forEach((b) => { binary += String.fromCharCode(b) })
  return btoa(binary)
}

function bytesToArrayBuffer(bytes: Uint8Array): ArrayBuffer {
  return bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) as ArrayBuffer
}

function bytesToBase64Url(bytes: Uint8Array) {
  return bytesToBase64(bytes).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/g, '')
}

function base64ToBytes(value: string) {
  const normalized = value.replace(/-/g, '+').replace(/_/g, '/')
  const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, '=')
  const binary = atob(padded)
  const out = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i += 1) out[i] = binary.charCodeAt(i)
  return out
}

function derToP1363(der: Uint8Array) {
  if (der.length === 64) return der
  if (der[0] !== 0x30) throw new Error('Assinatura DER inválida.')
  let offset = der[1] & 0x80 ? 2 + (der[1] & 0x7f) : 2
  const r = readASN1Int(der, offset)
  offset = r.next
  const s = readASN1Int(der, offset)
  const out = new Uint8Array(64)
  out.set(leftPad32(r.value), 0)
  out.set(leftPad32(s.value), 32)
  return out
}

function readASN1Int(bytes: Uint8Array, offset: number) {
  if (bytes[offset] !== 0x02) throw new Error('Assinatura DER inválida.')
  const length = bytes[offset + 1]
  const start = offset + 2
  return { value: bytes.slice(start, start + length), next: start + length }
}

function leftPad32(bytes: Uint8Array) {
  const trimmed = bytes[0] === 0 ? bytes.slice(1) : bytes
  if (trimmed.length > 32) throw new Error('Inteiro ECDSA inválido.')
  const out = new Uint8Array(32)
  out.set(trimmed, 32 - trimmed.length)
  return out
}
