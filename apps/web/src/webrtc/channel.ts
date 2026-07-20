// Canal seguro AES-256-GCM para DataChannels Relay (mesmo formato do Go).
// Frame: [4 bytes len][1 byte type][4 bytes seq][12 bytes nonce][ciphertext+tag]
// AAD = label + deviceID + sessionID + channelID
const NONCE_SIZE = 12
const TAG_SIZE = 16
const HEADER_SIZE = 21 // 1 + 4 + 12 + 4 len

function asArrayBuffer(view: Uint8Array): ArrayBuffer {
  return view.buffer.slice(view.byteOffset, view.byteOffset + view.byteLength) as ArrayBuffer
}

export enum MsgType {
  Control = 0x01,
  Clipboard = 0x02,
  File = 0x03,
  Geometry = 0x04,
  Input = 0x05,
}

const labels: Record<MsgType, string> = {
  [MsgType.Control]: 'relay-ctl-v1',
  [MsgType.Clipboard]: 'relay-clip-v1',
  [MsgType.File]: 'relay-file-v1',
  [MsgType.Geometry]: 'relay-geom-v1',
  [MsgType.Input]: 'relay-input-v1',
}

export interface DecryptedMessage {
  type: MsgType
  sequence: number
  plaintext: Uint8Array
}

export class SecureChannel {
  private key: CryptoKey
  private deviceID: string
  private sessionID: string
  private channelID: string
  private seqOut = 0
  private seen = new Map<number, number>()
  private maxAge = 5 * 60 * 1000
  private windowSize = 1024

  constructor(
    key: CryptoKey,
    deviceID: string,
    sessionID: string,
    channelID = 'webrtc',
  ) {
    this.key = key
    this.deviceID = deviceID
    this.sessionID = sessionID
    this.channelID = channelID
  }

  async encrypt(type: MsgType, plaintext: Uint8Array): Promise<Uint8Array> {
    if (plaintext.byteLength > 64 * 1024 * 1024) {
      throw new Error('payload muito grande')
    }
    const label = labels[type]
    const aad = this.buildAAD(label)
    const nonce = crypto.getRandomValues(new Uint8Array(NONCE_SIZE))
    const ciphertext = new Uint8Array(
      await crypto.subtle.encrypt(
        { name: 'AES-GCM', iv: asArrayBuffer(nonce), additionalData: asArrayBuffer(aad) },
        this.key,
        asArrayBuffer(plaintext),
      ),
    )
    this.seqOut += 1
    const seq = this.seqOut
    const total = HEADER_SIZE + ciphertext.byteLength
    const out = new Uint8Array(4 + total)
    const dv = new DataView(out.buffer)
    dv.setUint32(0, total, false)
    out[4] = type
    dv.setUint32(5, seq, false)
    out.set(nonce, 9)
    out.set(ciphertext, 21)
    return out
  }

  async decrypt(data: Uint8Array): Promise<DecryptedMessage> {
    if (data.byteLength < 4 + HEADER_SIZE + TAG_SIZE) {
      throw new Error('mensagem muito curta')
    }
    const dv = new DataView(data.buffer, data.byteOffset)
    const frameLen = dv.getUint32(0, false)
    if (4 + frameLen !== data.byteLength) {
      throw new Error('tamanho de frame inconsistente')
    }
    const type = data[4] as MsgType
    const label = labels[type]
    if (!label) {
      throw new Error(`tipo de mensagem inválido: ${type}`)
    }
    const seq = dv.getUint32(5, false)
    const nonce = data.subarray(9, 21)
    const cipherLen = frameLen - HEADER_SIZE
    if (cipherLen < TAG_SIZE || 21 + cipherLen > data.byteLength) {
      throw new Error('tamanho de ciphertext inválido')
    }
    const ciphertext = data.subarray(21, 21 + cipherLen)
    const aad = this.buildAAD(label)
    const plaintext = new Uint8Array(
      await crypto.subtle.decrypt(
        { name: 'AES-GCM', iv: asArrayBuffer(nonce), additionalData: asArrayBuffer(aad) },
        this.key,
        asArrayBuffer(ciphertext),
      ),
    )
    this.checkReplay(seq)
    return { type, sequence: seq, plaintext }
  }

  private buildAAD(label: string): Uint8Array {
    const enc = new TextEncoder()
    const parts = [
      enc.encode(label),
      new Uint8Array([0]),
      enc.encode(this.deviceID),
      new Uint8Array([0]),
      enc.encode(this.sessionID),
      new Uint8Array([0]),
      enc.encode(this.channelID),
    ]
    let len = 0
    for (const p of parts) len += p.byteLength
    const out = new Uint8Array(len)
    let offset = 0
    for (const p of parts) {
      out.set(p, offset)
      offset += p.byteLength
    }
    return out
  }

  private checkReplay(seq: number): void {
    if (this.seen.has(seq)) {
      throw new Error(`replay detectado: seq=${seq}`)
    }
    const now = Date.now()
    this.seen.set(seq, now)
    if (this.seen.size > this.windowSize) {
      for (const [k, v] of this.seen) {
        if (now - v > this.maxAge) {
          this.seen.delete(k)
        }
      }
    }
  }
}
