import type { LeaseAuth } from '../api/client'
import { MsgType, SecureChannel } from './channel'

export type ConnectionState =
  | 'idle'
  | 'connecting'
  | 'connected'
  | 'reconnecting'
  | 'disconnected'
  | 'failed'

export interface SignalingClientOptions {
  auth: LeaseAuth
  sessionId: string
  sessionKey: CryptoKey
  deviceId: string
  onRemoteTrack?: (track: MediaStreamTrack) => void
  onDataChannelOpen?: () => void
  onDataChannelClose?: () => void
  onStateChange?: (state: ConnectionState) => void
  onError?: (err: Error) => void
}

interface SDPMessage {
  sdp: RTCSessionDescriptionInit
}

interface StatusMessage {
  connected: boolean
  state?: string
  ipc_path?: string
}

export class SignalingClient {
  private auth: LeaseAuth
  private sessionId: string
  private sessionKey: CryptoKey
  private deviceId: string
  private pc?: RTCPeerConnection
  private control?: RTCDataChannel
  private controlSecure?: SecureChannel
  private clipboard?: RTCDataChannel
  private clipboardSecure?: SecureChannel
  private files?: RTCDataChannel
  private filesSecure?: SecureChannel
  private pendingCandidates: RTCIceCandidateInit[] = []
  private onRemoteTrack?: (track: MediaStreamTrack) => void
  private onDataChannelOpen?: () => void
  private onDataChannelClose?: () => void
  private onStateChange?: (state: ConnectionState) => void
  private onError?: (err: Error) => void
  private reconnectTimer?: number
  private state: ConnectionState = 'idle'

  constructor(options: SignalingClientOptions) {
    this.auth = options.auth
    this.sessionId = options.sessionId
    this.sessionKey = options.sessionKey
    this.deviceId = options.deviceId
    this.onRemoteTrack = options.onRemoteTrack
    this.onDataChannelOpen = options.onDataChannelOpen
    this.onDataChannelClose = options.onDataChannelClose
    this.onStateChange = options.onStateChange
    this.onError = options.onError
  }

  private setState(state: ConnectionState) {
    this.state = state
    this.onStateChange?.(state)
  }

  async connect() {
    if (this.pc) {
      await this.close()
    }
    this.setState('connecting')
    // STUN públicos — ajuda celular↔Mac na mesma Wi‑Fi (e redes com NAT).
    this.pc = new RTCPeerConnection({
      iceServers: [
        { urls: 'stun:stun.cloudflare.com:3478' },
        { urls: 'stun:stun.l.google.com:19302' },
      ],
      iceTransportPolicy: 'all',
    })

    this.pc.ontrack = (event) => {
      for (const track of event.streams[0]?.getTracks() ?? []) {
        this.onRemoteTrack?.(track)
      }
    }

    this.pc.onicecandidate = async (event) => {
      if (event.candidate) {
        await this.sendICE(event.candidate.toJSON())
      }
    }

    this.pc.onconnectionstatechange = () => {
      const state = this.pc?.connectionState
      if (state === 'connected') {
        this.setState('connected')
      } else if (state === 'failed' || state === 'disconnected') {
        this.setState(state === 'failed' ? 'failed' : 'disconnected')
        this.scheduleReconnect()
      }
    }

    this.controlSecure = new SecureChannel(
      this.sessionKey,
      this.deviceId,
      this.sessionId,
      'webrtc',
    )
    this.clipboardSecure = new SecureChannel(
      this.sessionKey,
      this.deviceId,
      this.sessionId,
      'webrtc',
    )
    this.filesSecure = new SecureChannel(
      this.sessionKey,
      this.deviceId,
      this.sessionId,
      'webrtc',
    )

    this.control = this.pc.createDataChannel('relay-control', {
      ordered: true,
    })
    this.bindDataChannel(this.control, this.controlSecure, [
      MsgType.Control,
      MsgType.Geometry,
      MsgType.Input,
    ])

    this.clipboard = this.pc.createDataChannel('relay-clipboard', {
      ordered: true,
    })
    this.bindDataChannel(this.clipboard, this.clipboardSecure, [
      MsgType.Clipboard,
    ])

    this.files = this.pc.createDataChannel('relay-files', { ordered: true })
    this.bindDataChannel(this.files, this.filesSecure, [MsgType.File])

    const offer = await this.pc.createOffer()
    await this.pc.setLocalDescription(offer)
    // Aguarda ICE gathering para embutir candidatos no SDP (LAN Mac↔iPhone).
    await waitIceGatheringComplete(this.pc)
    const completeOffer = this.pc.localDescription
    if (!completeOffer) {
      throw new Error('SDP local vazio após ICE gather')
    }
    // post() já prefixa /api
    const answer = await this.post<SDPMessage>('/webrtc/offer', {
      sdp: completeOffer,
    })
    if (!answer?.sdp) {
      throw new Error('resposta WebRTC sem SDP')
    }
    await this.pc.setRemoteDescription(answer.sdp)
    this.flushCandidates()
  }

  private bindDataChannel(
    dc: RTCDataChannel,
    secure: SecureChannel,
    accepted: MsgType[],
  ) {
    dc.onopen = () => {
      if (dc.label === 'relay-control') {
        this.onDataChannelOpen?.()
      }
    }
    dc.onclose = () => {
      if (dc.label === 'relay-control') {
        this.onDataChannelClose?.()
      }
    }
    dc.onmessage = async (event) => {
      try {
        const data = new Uint8Array(await (event.data as Blob).arrayBuffer())
        const msg = await secure.decrypt(data)
        if (!accepted.includes(msg.type)) {
          return
        }
        this.dispatch(msg.type, msg.plaintext)
      } catch (err) {
        // plaintext or invalid message is rejected
        console.warn('datachannel message rejected:', err)
      }
    }
  }

  private dispatch(type: MsgType, payload: Uint8Array) {
    if (type === MsgType.Geometry) {
      try {
        const geometry = JSON.parse(new TextDecoder().decode(payload))
        this.currentGeometry = geometry
      } catch {
        // ignore
      }
    }
    if (type === MsgType.Clipboard) {
      this.onClipboard?.(new TextDecoder().decode(payload))
    }
  }

  private currentGeometry?: {
    capture: { x: number; y: number; width: number; height: number }
    video: { x: number; y: number; width: number; height: number }
  }

  onClipboard?: (text: string) => void

  private async sendICE(candidate: RTCIceCandidateInit) {
    try {
      await this.post('/webrtc/ice', { candidate })
    } catch (err) {
      this.onError?.(err as Error)
    }
  }

  private flushCandidates() {
    for (const c of this.pendingCandidates) {
      this.pc?.addIceCandidate(c).catch((err) => this.onError?.(err))
    }
    this.pendingCandidates = []
  }

  async sendInput(event: object) {
    if (!this.control || this.control.readyState !== 'open') {
      throw new Error('canal de controle não está aberto')
    }
    const payload = new TextEncoder().encode(JSON.stringify(event))
    const data = await this.controlSecure!.encrypt(MsgType.Input, payload)
    this.control.send(data.buffer as ArrayBuffer)
  }

  async sendClipboard(text: string) {
    if (!this.clipboard || this.clipboard.readyState !== 'open') {
      throw new Error('canal de clipboard não está aberto')
    }
    const data = await this.clipboardSecure!.encrypt(
      MsgType.Clipboard,
      new TextEncoder().encode(text),
    )
    this.clipboard.send(data.buffer as ArrayBuffer)
  }

  async restartICE() {
    if (!this.pc) return
    const offer = await this.pc.createOffer({ iceRestart: true })
    await this.pc.setLocalDescription(offer)
    await waitIceGatheringComplete(this.pc)
    const complete = this.pc.localDescription
    if (!complete) return
    const answer = await this.post<SDPMessage>('/webrtc/offer', { sdp: complete })
    if (answer?.sdp) {
      await this.pc.setRemoteDescription(answer.sdp)
    }
  }

  private scheduleReconnect() {
    if (this.reconnectTimer) return
    this.setState('reconnecting')
    this.reconnectTimer = window.setTimeout(() => {
      this.reconnectTimer = undefined
      this.connect().catch((err) => this.onError?.(err))
    }, 2000)
  }

  async close() {
    if (this.reconnectTimer) {
      window.clearTimeout(this.reconnectTimer)
      this.reconnectTimer = undefined
    }
    this.control?.close()
    this.clipboard?.close()
    this.files?.close()
    this.pc?.close()
    this.pc = undefined
    this.setState('disconnected')
  }

  getState(): ConnectionState {
    return this.state
  }

  isDataChannelOpen(): boolean {
    return this.control?.readyState === 'open'
  }

  getGeometry() {
    return this.currentGeometry
  }

  private async post<T = { status: string }>(
    path: string,
    body: object,
  ): Promise<T> {
    const res = await fetch(`/api${path}`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Accept: 'application/json',
        'X-Relay-Device-ID': this.auth.deviceId,
        'X-Relay-Lease-Token': this.auth.leaseToken,
      },
      credentials: 'same-origin',
      body: JSON.stringify(body),
    })
    if (!res.ok) {
      throw new Error(`${res.status}: ${await res.text()}`)
    }
    return res.json() as Promise<T>
  }

  async status(): Promise<StatusMessage> {
    const res = await fetch('/api/webrtc/status', {
      headers: {
        Accept: 'application/json',
        'X-Relay-Device-ID': this.auth.deviceId,
        'X-Relay-Lease-Token': this.auth.leaseToken,
      },
      credentials: 'same-origin',
    })
    if (!res.ok) {
      throw new Error(`${res.status}: ${await res.text()}`)
    }
    return res.json() as Promise<StatusMessage>
  }
}

function waitIceGatheringComplete(pc: RTCPeerConnection, timeoutMs = 4000): Promise<void> {
  if (pc.iceGatheringState === 'complete') {
    return Promise.resolve()
  }
  return new Promise((resolve) => {
    const done = () => {
      pc.removeEventListener('icegatheringstatechange', onChange)
      window.clearTimeout(timer)
      resolve()
    }
    const onChange = () => {
      if (pc.iceGatheringState === 'complete') done()
    }
    const timer = window.setTimeout(done, timeoutMs)
    pc.addEventListener('icegatheringstatechange', onChange)
  })
}
