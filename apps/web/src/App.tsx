import { useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { fetchHealth, fetchSessionDetail, fetchSessions, fetchStatus, getSessionKey, pair, releaseLease, type LeaseAuth, type PairState } from './api/client'
import { pendingApprovalFixture, type ApprovalFixture } from './fixtures/approvals'
import type { AgentStatus, AuthenticatedStatus, SessionDescriptor } from './types/api'
import { SignalingClient, type ConnectionState } from './webrtc/signaling'

const STORED_PAIR = 'relay:pair-state'

export default function App() {
  const [health, setHealth] = useState<AgentStatus | null>(null)
  const [pairState, setPairState] = useState<PairState | null>(() => readPairState())
  const [status, setStatus] = useState<AuthenticatedStatus | null>(null)
  const [sessions, setSessions] = useState<SessionDescriptor[]>([])
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [detail, setDetail] = useState<SessionDescriptor | null>(null)
  const [offer, setOffer] = useState('')
  const [deviceName, setDeviceName] = useState('Web PWA')
  const [message, setMessage] = useState<string | null>(null)
  const [offline, setOffline] = useState(false)
  const [approval, setApproval] = useState<ApprovalFixture | null>(pendingApprovalFixture)
  const [rtcState, setRtcState] = useState<ConnectionState>('idle')
  const [dataChannelOpen, setDataChannelOpen] = useState(false)
  const [remoteStream, setRemoteStream] = useState<MediaStream | null>(null)
  const [target, setTarget] = useState<'window' | 'display'>('display')
  const [fullScreen, setFullScreen] = useState(false)
  const videoRef = useRef<HTMLVideoElement>(null)
  const signalingRef = useRef<SignalingClient | null>(null)
  const auth = useMemo<LeaseAuth | null>(() => pairState && ({ deviceId: pairState.deviceId, leaseToken: pairState.leaseToken }), [pairState])

  useEffect(() => {
    void refreshPublic()
  }, [])

  useEffect(() => {
    if (!auth) return
    void refreshPrivate(auth)
  }, [auth])

  useEffect(() => {
    if (!auth || !selectedId) return
    fetchSessionDetail(selectedId, auth)
      .then(setDetail)
      .catch((err) => setMessage(String(err)))
  }, [auth, selectedId])

  // Inicia WebRTC quando o par está autenticado.
  useEffect(() => {
    if (!auth || !pairState) return
    const state = pairState
    let client: SignalingClient | null = null
    let active = true
    async function start() {
      const key = await getSessionKey(state.hostId, state.sessionId)
      if (!key) {
        if (active) setMessage('Chave de sessão não encontrada. Emparelhe novamente.')
        return
      }
      client = new SignalingClient({
        auth: auth as LeaseAuth,
        sessionId: state.sessionId,
        sessionKey: key,
        deviceId: state.deviceId,
        onRemoteTrack: (track) => {
          if (!active) return
          const stream = remoteStream ?? new MediaStream()
          if (!stream.getTracks().includes(track)) {
            stream.addTrack(track)
          }
          setRemoteStream(stream)
        },
        onDataChannelOpen: () => {
          if (!active) return
          setDataChannelOpen(true)
          setRtcState('connected')
        },
        onDataChannelClose: () => {
          if (!active) return
          setDataChannelOpen(false)
        },
        onStateChange: (s) => {
          if (!active) return
          setRtcState(s)
        },
        onError: (err) => {
          if (!active) return
          setMessage(`WebRTC: ${err.message}`)
        },
      })
      signalingRef.current = client
      try {
        await client.connect()
      } catch (err) {
        if (active) setMessage(`WebRTC: ${err}`)
      }
    }
    start()
    return () => {
      active = false
      client?.close().catch(() => {})
      signalingRef.current = null
    }
  }, [auth, pairState, remoteStream])

  useEffect(() => {
    const video = videoRef.current
    if (video && remoteStream) {
      video.srcObject = remoteStream
      video.play().catch(() => {})
    }
  }, [remoteStream])

  async function refreshPublic() {
    try {
      const next = await fetchHealth()
      setHealth(next)
      setOffline(false)
    } catch {
      setOffline(true)
      setHealth(null)
    }
  }

  async function refreshPrivate(nextAuth = auth) {
    if (!nextAuth) return
    try {
      const [nextStatus, nextSessions] = await Promise.all([
        fetchStatus(nextAuth),
        fetchSessions(nextAuth),
      ])
      setStatus(nextStatus)
      setSessions(nextSessions)
      setSelectedId((current) => current ?? nextSessions[0]?.id ?? null)
      setOffline(false)
      setMessage(null)
    } catch (err) {
      setMessage(String(err))
      setStatus(null)
    }
  }

  async function submitPair() {
    setMessage('Validando oferta e gerando chaves locais...')
    try {
      const next = await pair(offer, deviceName)
      localStorage.setItem(STORED_PAIR, JSON.stringify(next))
      setPairState(next)
      setOffer('')
      setMessage('Pareamento concluído com lease ativo.')
    } catch (err) {
      setMessage(String(err))
    }
  }

  async function releaseCurrentLease() {
    if (!auth) return
    try {
      await releaseLease(auth)
      localStorage.removeItem(STORED_PAIR)
      setPairState(null)
      setStatus(null)
      setSessions([])
      setDetail(null)
      setSelectedId(null)
      setMessage('Lease liberado neste dispositivo.')
    } catch (err) {
      setMessage(String(err))
    }
  }

  if (offline) {
    return (
      <Shell>
        <section className="panel center">
          <div className="status-dot offline" />
          <h1>Relay</h1>
          <p>Agente local indisponível. A PWA segue instalada e pronta para reconectar quando o Mac voltar.</p>
          <button onClick={refreshPublic}>Tentar novamente</button>
        </section>
      </Shell>
    )
  }

  if (!pairState) {
    return (
      <Shell>
        <header className="topbar">
          <div>
            <p className="eyebrow">Pairing-only</p>
            <h1>Relay</h1>
          </div>
          <span className="status-pill">{health?.listening ? 'Agente online' : 'Aguardando'}</span>
        </header>
        <section className="panel">
          <h2>Emparelhar este dispositivo</h2>
          <p className="muted">Cole o envelope assinado gerado pelo CLI. Antes do pareamento, nenhuma sessão, cwd, device ou metadado é consultado.</p>
          <label>
            Nome do dispositivo
            <input value={deviceName} onChange={(event) => setDeviceName(event.target.value)} />
          </label>
          <label>
            Envelope ou payload QR
            <textarea value={offer} onChange={(event) => setOffer(event.target.value)} rows={8} />
          </label>
          <button onClick={submitPair} disabled={!offer.trim() || !deviceName.trim()}>Parear com WebCrypto</button>
          {message && <p className="notice">{message}</p>}
        </section>
      </Shell>
    )
  }

  return (
    <Shell>
      <header className="topbar">
        <div>
          <p className="eyebrow">Sessões</p>
          <h1>{pairState.hostName || 'Relay'}</h1>
        </div>
        <button className="icon-button" onClick={() => void refreshPrivate()} aria-label="Atualizar">↻</button>
      </header>

      <section className="panel compact">
        <div className="lease-row">
          <div>
            <strong>{status?.version ?? health?.version ?? 'relay'}</strong>
            <span>Lease até {formatTime(pairState.leaseExpiry)}</span>
          </div>
          <button className="secondary" onClick={releaseCurrentLease}>Liberar</button>
        </div>
      </section>

      <section className="session-list" aria-label="Sessões">
        {sessions.map((session) => (
          <button
            key={session.id}
            className={`session-row ${selectedId === session.id ? 'selected' : ''}`}
            onClick={() => setSelectedId(session.id)}
          >
            <span>{session.harness}</span>
            <strong>{session.nativeSessionId}</strong>
            <small>{session.status}</small>
          </button>
        ))}
      </section>

      {detail && (
        <SessionDetail
          session={detail}
          rtcState={rtcState}
          dataChannelOpen={dataChannelOpen}
          remoteStream={remoteStream}
          target={target}
          fullScreen={fullScreen}
          onTargetChange={setTarget}
          onToggleFullScreen={() => setFullScreen((v) => !v)}
          onSendInput={(event) => signalingRef.current?.sendInput(event).catch((err) => setMessage(String(err)))}
          onSendClipboard={(text) => signalingRef.current?.sendClipboard(text).catch((err) => setMessage(String(err)))}
          onCopyCwd={() => void navigator.clipboard?.writeText(detail.cwd)}
          onReleaseControl={releaseCurrentLease}
          onShowApproval={() => setApproval(pendingApprovalFixture)}
        />
      )}
      {approval && <ApprovalModal approval={approval} onClose={() => setApproval(null)} />}
      {message && <p className="notice">{message}</p>}
    </Shell>
  )
}

function SessionDetail({
  session,
  rtcState,
  dataChannelOpen,
  remoteStream,
  target,
  fullScreen,
  onTargetChange,
  onToggleFullScreen,
  onSendInput,
  onSendClipboard,
  onCopyCwd,
  onReleaseControl,
  onShowApproval,
}: {
  session: SessionDescriptor
  rtcState: ConnectionState
  dataChannelOpen: boolean
  remoteStream: MediaStream | null
  target: 'window' | 'display'
  fullScreen: boolean
  onTargetChange: (t: 'window' | 'display') => void
  onToggleFullScreen: () => void
  onSendInput: (event: object) => void
  onSendClipboard: (text: string) => void
  onCopyCwd: () => void
  onReleaseControl: () => void
  onShowApproval: () => void
}) {
  const [tab, setTab] = useState<'sessao' | 'janela'>('sessao')
  const [clipboardText, setClipboardText] = useState('')
  const videoRef = useRef<HTMLVideoElement>(null)

  useEffect(() => {
    const video = videoRef.current
    if (video && remoteStream) {
      video.srcObject = remoteStream
      video.play().catch(() => {})
    }
  }, [remoteStream])

  return (
    <section className="panel detail">
      <div className="detail-head">
        <div>
          <p className="eyebrow">Sessão / Janela</p>
          <h2>{session.nativeSessionId}</h2>
        </div>
        <span className={`status-pill ${session.status}`}>{rtcState}</span>
      </div>
      <div className="tabs" role="tablist" aria-label="Detalhe">
        <button className={tab === 'sessao' ? 'active' : ''} onClick={() => setTab('sessao')} role="tab" aria-selected={tab === 'sessao'}>Sessão</button>
        <button className={tab === 'janela' ? 'active' : ''} onClick={() => setTab('janela')} role="tab" aria-selected={tab === 'janela'}>Janela</button>
      </div>
      {tab === 'sessao' ? (
        <div className="control-panel">
          <dl>
            <div><dt>CWD</dt><dd>{session.cwd}</dd></div>
            <div><dt>PID</dt><dd>{session.pid ?? 'n/d'}</dd></div>
            <div><dt>Janela</dt><dd>{session.windowId ?? (session.frontmost ? 'frontmost' : 'n/d')}</dd></div>
            <div><dt>Maestri</dt><dd>{session.maestriTerminalId ?? 'n/d'}</dd></div>
            <div><dt>Codex</dt><dd>{session.codexThreadId ?? 'n/d'}</dd></div>
          </dl>
          <div className="controls">
            <button disabled title="Envio remoto ainda não é executado neste marco.">Enviar</button>
            <button disabled title="Interrupção remota ainda não é suportada neste marco.">Interromper</button>
            <button disabled={!dataChannelOpen} onClick={() => onSendClipboard(clipboardText || 'clipboard-test')} title={dataChannelOpen ? 'Colar no Mac' : 'Canal seguro não aberto'}>Colar</button>
            <button onClick={onCopyCwd}>Arquivos</button>
            <button onClick={onReleaseControl}>Liberar controle</button>
          </div>
          <input value={clipboardText} onChange={(e) => setClipboardText(e.target.value)} placeholder="Texto para colar no Mac" />
          <button className="secondary" onClick={onShowApproval}>Abrir aprovação pendente</button>
          <p className="muted">Enviar e Interromper ainda não executam. Colar funciona quando o canal seguro estiver aberto.</p>
        </div>
      ) : (
        <div className="control-panel">
          <div className="controls">
            <button className={target === 'window' ? 'active' : ''} onClick={() => onTargetChange('window')}>Janela</button>
            <button className={target === 'display' ? 'active' : ''} onClick={() => onTargetChange('display')}>Tela</button>
            <button onClick={onToggleFullScreen}>{fullScreen ? 'Sair' : 'Tela inteira'}</button>
            <button disabled={!dataChannelOpen} onClick={() => onSendInput({ type: 'keyDown', key: 'space' })}>Teclado</button>
            <button disabled={!dataChannelOpen} onClick={() => onSendInput({ type: 'mouseMove', x: 0.5, y: 0.5 })}>Mouse</button>
          </div>
          <div className={`video-wrapper ${fullScreen ? 'fullscreen' : ''}`}>
            <video ref={videoRef} muted playsInline autoPlay className="relay-video" />
          </div>
          <p className="muted">Vídeo real via WebRTC. Controles de teclado/mouse só funcionam com canal seguro aberto.</p>
        </div>
      )}
    </section>
  )
}

function ApprovalModal({ approval, onClose }: { approval: ApprovalFixture; onClose: () => void }) {
  return (
    <div className="modal-backdrop" role="presentation">
      <section className="modal" role="dialog" aria-modal="true" aria-labelledby="approval-title">
        <h2 id="approval-title">Aprovação pendente</h2>
        <dl>
          <div><dt>Comando</dt><dd>{approval.command}</dd></div>
          <div><dt>CWD</dt><dd>{approval.cwd}</dd></div>
          <div><dt>Justificativa</dt><dd>{approval.justification}</dd></div>
          <div><dt>Permissões</dt><dd>{approval.permissions.join(', ')}</dd></div>
        </dl>
        <div className="modal-actions">
          <button onClick={onClose}>Aprovar uma vez</button>
          <button className="secondary" onClick={onClose}>Negar</button>
        </div>
      </section>
    </div>
  )
}

function Shell({ children }: { children: ReactNode }) {
  return <main className="app-shell">{children}</main>
}

function readPairState(): PairState | null {
  try {
    const raw = localStorage.getItem(STORED_PAIR)
    return raw ? JSON.parse(raw) as PairState : null
  } catch {
    return null
  }
}

function formatTime(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return 'n/d'
  return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}
