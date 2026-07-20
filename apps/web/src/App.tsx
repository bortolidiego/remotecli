import { useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import {
  decideApproval,
  fetchApprovals,
  fetchEvents,
  fetchHealth,
  fetchSessionDetail,
  fetchSessions,
  fetchStatus,
  getSessionKey,
  interruptTurn,
  pair,
  releaseLease,
  startTurn,
  type LeaseAuth,
  type PairState,
} from './api/client'
import type { AgentStatus, AuthenticatedStatus, CodexApproval, CodexEvent, SessionDescriptor } from './types/api'
import { SignalingClient, type ConnectionState } from './webrtc/signaling'

const STORED_PAIR = 'relay:pair-state'

export default function App() {
  const [health, setHealth] = useState<AgentStatus | null>(null)
  const [pairState, setPairState] = useState<PairState | null>(() => readPairState())
  const [, setStatus] = useState<AuthenticatedStatus | null>(null)
  const [sessions, setSessions] = useState<SessionDescriptor[]>([])
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [detail, setDetail] = useState<SessionDescriptor | null>(null)
  const [offer, setOffer] = useState('')
  const [deviceName, setDeviceName] = useState('Web PWA')
  const [message, setMessage] = useState<string | null>(null)
  const [offline, setOffline] = useState(false)
  const [, setApprovals] = useState<CodexApproval[]>([])
  const [, setEvents] = useState<CodexEvent[]>([])
  const [selectedApproval, setSelectedApproval] = useState<CodexApproval | null>(null)
  const [busy, setBusy] = useState(false)
  const [rtcState, setRtcState] = useState<ConnectionState>('idle')
  const [dataChannelOpen, setDataChannelOpen] = useState(false)
  const [remoteStream, setRemoteStream] = useState<MediaStream | null>(null)
  const [target, setTarget] = useState<'window' | 'display'>('display')
  const [fullScreen, setFullScreen] = useState(false)
  const [promptText, setPromptText] = useState('')
  const [scannedOffer, setScannedOffer] = useState('')
  const videoRef = useRef<HTMLVideoElement>(null)
  const signalingRef = useRef<SignalingClient | null>(null)
  const auth = useMemo<LeaseAuth | null>(() => pairState && ({ deviceId: pairState.deviceId, leaseToken: pairState.leaseToken }), [pairState])

  // Carrega oferta via QR: ?c=código curto (preferido) ou ?offer=envelope completo.
  useEffect(() => {
    if (pairState) return
    const params = new URLSearchParams(window.location.search)
    const claim = params.get('c')
    const offerParam = params.get('offer')
    if (claim) {
      void (async () => {
        try {
          const res = await fetch(`/api/claim?c=${encodeURIComponent(claim)}`, {
            headers: { Accept: 'application/json' },
          })
          if (!res.ok) {
            setMessage(`QR inválido ou expirado (${res.status}). Rode relay share de novo.`)
            return
          }
          const text = await res.text()
          setOffer(text)
          setScannedOffer(text)
          setMessage('Oferta do QR carregada. Toque em Parear.')
        } catch (err) {
          setMessage(String(err))
        }
      })()
      return
    }
    if (offerParam) {
      setOffer(offerParam)
      setScannedOffer(offerParam)
      setMessage('Oferta detectada no QR. Confirme o pareamento abaixo.')
    }
  }, [pairState])

  // Polling leve de aprovações e eventos Codex.
  useEffect(() => {
    if (!auth || !selectedId) return
    const sessionId = selectedId
    const currentAuth = auth
    let active = true
    async function tick() {
      try {
        const [nextApprovals, nextEvents] = await Promise.all([
          fetchApprovals(sessionId, currentAuth),
          fetchEvents(sessionId, currentAuth),
        ])
        if (!active) return
        setApprovals(nextApprovals)
        setEvents(nextEvents)
        setSelectedApproval((current) => {
          const list = Array.isArray(nextApprovals) ? nextApprovals : []
          if (current) {
            const stillPending = list.find((a) => a.id === current.id)
            return stillPending ?? list[0] ?? null
          }
          return list[0] ?? null
        })
      } catch (err) {
        if (active) setMessage(String(err))
      }
    }
    void tick()
    const id = setInterval(tick, 2000)
    return () => {
      active = false
      clearInterval(id)
    }
  }, [auth, selectedId])



  async function sendTurn() {
    if (!auth || !selectedId || !promptText.trim() || busy) return
    setBusy(true)
    setMessage(null)
    try {
      await startTurn(selectedId, promptText.trim(), auth)
      setPromptText('')
      void refreshPrivate()
    } catch (err) {
      setMessage(String(err))
    } finally {
      setBusy(false)
    }
  }

  async function interruptCurrentTurn() {
    if (!auth || !selectedId || busy) return
    setBusy(true)
    setMessage(null)
    try {
      await interruptTurn(selectedId, auth)
      void refreshPrivate()
    } catch (err) {
      setMessage(String(err))
    } finally {
      setBusy(false)
    }
  }

  async function submitDecision(approval: CodexApproval, decision: 'accept' | 'deny') {
    if (!auth || !selectedId) return
    setMessage(null)
    try {
      await decideApproval(selectedId, approval.id, decision, auth)
      setSelectedApproval(null)
      const next = await fetchApprovals(selectedId, auth)
      setApprovals(next)
      setSelectedApproval(next[0] ?? null)
    } catch (err) {
      setMessage(String(err))
    }
  }

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
    // Sempre limpa o celular — mesmo se o Mac já reiniciou / lease expirou (401).
    const previous = auth
    try {
      if (previous) {
        await releaseLease(previous)
      }
    } catch {
      // ignorar erro de rede/lease morto
    }
    try {
      signalingRef.current?.close().catch(() => {})
    } catch {
      /* ignore */
    }
    signalingRef.current = null
    localStorage.removeItem(STORED_PAIR)
    setPairState(null)
    setStatus(null)
    setSessions([])
    setDetail(null)
    setSelectedId(null)
    setRemoteStream(null)
    setDataChannelOpen(false)
    setRtcState('idle')
    setSelectedApproval(null)
    setMessage('Desconectado. Emparelhe de novo com um QR atualizado.')
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
          <p className="muted">Cole o envelope assinado gerado pelo CLI ou escaneie o QR do Mac. Antes do pareamento, nenhuma sessão, cwd, device ou metadado é consultado.</p>
          <label>
            Nome do dispositivo
            <input value={deviceName} onChange={(event) => setDeviceName(event.target.value)} />
          </label>
          <label>
            Envelope ou payload QR
            <textarea value={offer} onChange={(event) => setOffer(event.target.value)} rows={8} />
          </label>
          <button onClick={submitPair} disabled={!offer.trim() || !deviceName.trim()}>
            {scannedOffer ? 'Parear via QR' : 'Parear com WebCrypto'}
          </button>
          {message && <p className="notice">{message}</p>}
        </section>
      </Shell>
    )
  }

  return (
    <Shell>
      <header className="topbar">
        <div>
          <p className="eyebrow">Remote CliControl</p>
          <h1>Seu Mac</h1>
        </div>
        <button className="icon-button" onClick={() => void refreshPrivate()} aria-label="Atualizar">↻</button>
      </header>

      <section className="panel compact">
        <div className="lease-row">
          <div>
            <strong>Conectado</strong>
            <span>Até {formatTime(pairState.leaseExpiry)}</span>
          </div>
          <button className="secondary" onClick={releaseCurrentLease}>Desconectar</button>
        </div>
      </section>

      {sessions.length > 1 && (
        <section className="session-list" aria-label="Sessões">
          {sessions.map((session) => (
            <button
              key={session.id}
              className={`session-row ${selectedId === session.id ? 'selected' : ''}`}
              onClick={() => setSelectedId(session.id)}
            >
              <strong>{session.nativeSessionId || 'Mac'}</strong>
              <small>{session.codexThreadId ? 'com chat' : 'tela'}</small>
            </button>
          ))}
        </section>
      )}

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
          onSendTurn={sendTurn}
          onInterruptTurn={interruptCurrentTurn}
          busy={busy}
          promptText={promptText}
          onPromptChange={setPromptText}
        />
      )}
      {selectedApproval && detail?.codexThreadId && (
        <ApprovalModal approval={selectedApproval} onDecide={submitDecision} onClose={() => setSelectedApproval(null)} />
      )}
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
  onSendTurn,
  onInterruptTurn,
  busy,
  promptText,
  onPromptChange,
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
  onSendTurn: () => void
  onInterruptTurn: () => void
  busy: boolean
  promptText: string
  onPromptChange: (text: string) => void
}) {
  // Janela primeiro: funciona com qualquer CLI (Codex, Grok, terminal…).
  const [tab, setTab] = useState<'janela' | 'chat'>('janela')
  const [clipboardText, setClipboardText] = useState('')
  const [showDetails, setShowDetails] = useState(false)
  const videoRef = useRef<HTMLVideoElement>(null)
  const hasCodex = Boolean(session.codexThreadId)
  const linkLabel = rtcStateLabel(rtcState, dataChannelOpen)

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
          <p className="eyebrow">Controle remoto</p>
          <h2>{hasCodex ? 'Codex + tela' : 'Tela do Mac'}</h2>
        </div>
        <span className={`status-pill ${rtcState}`}>{linkLabel}</span>
      </div>
      <div className="tabs" role="tablist" aria-label="Modo">
        <button className={tab === 'janela' ? 'active' : ''} onClick={() => setTab('janela')} role="tab" aria-selected={tab === 'janela'}>
          Tela
        </button>
        {hasCodex && (
          <button className={tab === 'chat' ? 'active' : ''} onClick={() => setTab('chat')} role="tab" aria-selected={tab === 'chat'}>
            Chat
          </button>
        )}
      </div>

      {tab === 'janela' ? (
        <div className="control-panel">
          <div className={`video-wrapper ${fullScreen ? 'fullscreen' : ''}`}>
            <video ref={videoRef} muted playsInline autoPlay className="relay-video" />
            {!remoteStream && (
              <p className="video-placeholder muted">
                {dataChannelOpen
                  ? 'Canal aberto. Vídeo da tela ainda em construção — teclado/colar já podem funcionar.'
                  : 'Conectando ao Mac…'}
              </p>
            )}
          </div>
          <div className="controls">
            <button className={target === 'display' ? 'active' : ''} onClick={() => onTargetChange('display')}>
              Tela cheia
            </button>
            <button className={target === 'window' ? 'active' : ''} onClick={() => onTargetChange('window')}>
              Janela
            </button>
            <button onClick={onToggleFullScreen}>{fullScreen ? 'Sair' : 'Expandir'}</button>
          </div>
          <div className="controls">
            <button disabled={!dataChannelOpen} onClick={() => onSendInput({ type: 'keyDown', key: 'space' })}>
              Teclado
            </button>
            <button disabled={!dataChannelOpen} onClick={() => onSendInput({ type: 'mouseMove', x: 0.5, y: 0.5 })}>
              Mouse
            </button>
            <button
              disabled={!dataChannelOpen}
              onClick={() => onSendClipboard(clipboardText || ' ')}
              title={dataChannelOpen ? 'Colar no Mac' : 'Aguardando conexão'}
            >
              Colar
            </button>
            <button onClick={onReleaseControl}>Sair</button>
          </div>
          <input
            value={clipboardText}
            onChange={(e) => setClipboardText(e.target.value)}
            placeholder="Texto para colar no Mac"
          />
          <p className="muted">
            Serve pra qualquer CLI aberta no Mac. Vídeo completo e helper de captura ainda evoluem; o caminho principal é ver e digitar na tela.
          </p>
          <button type="button" className="secondary" onClick={() => setShowDetails((v) => !v)}>
            {showDetails ? 'Ocultar detalhes' : 'Detalhes técnicos'}
          </button>
          {showDetails && (
            <dl className="tech-details">
              <div><dt>Pasta</dt><dd>{session.cwd}</dd></div>
              <div><dt>Processo</dt><dd>{session.pid ?? '—'}</dd></div>
              <div><dt>Tipo</dt><dd>{session.harness}</dd></div>
              {session.codexThreadId && <div><dt>Codex</dt><dd>{session.codexThreadId}</dd></div>}
            </dl>
          )}
        </div>
      ) : (
        <div className="control-panel">
          <p className="muted">Mensagens vão para a thread Codex desta sessão.</p>
          <textarea
            value={promptText}
            onChange={(e) => onPromptChange(e.target.value)}
            placeholder="Escreva o prompt…"
            rows={3}
          />
          <div className="controls">
            <button disabled={busy || !promptText.trim()} onClick={onSendTurn}>
              {busy ? 'Enviando…' : 'Enviar'}
            </button>
            <button disabled={busy} onClick={onInterruptTurn}>
              Parar
            </button>
            <button onClick={onCopyCwd}>Copiar pasta</button>
            <button onClick={onReleaseControl}>Sair</button>
          </div>
        </div>
      )}
    </section>
  )
}

function rtcStateLabel(state: ConnectionState, dataOpen: boolean): string {
  if (dataOpen || state === 'connected') return 'Pronto'
  if (state === 'connecting' || state === 'reconnecting') return 'Conectando…'
  if (state === 'failed') return 'Falhou'
  if (state === 'disconnected') return 'Desconectado'
  return 'Aguardando'
}

function ApprovalModal({ approval, onDecide, onClose }: { approval: CodexApproval; onDecide: (a: CodexApproval, decision: 'accept' | 'deny') => void; onClose: () => void }) {
  return (
    <div className="modal-backdrop" role="presentation">
      <section className="modal" role="dialog" aria-modal="true" aria-labelledby="approval-title">
        <h2 id="approval-title">Aprovação pendente</h2>
        <dl>
          <div><dt>Comando</dt><dd>{approval.command || 'n/d'}</dd></div>
          <div><dt>CWD</dt><dd>{approval.cwd || 'n/d'}</dd></div>
          <div><dt>Motivo</dt><dd>{approval.reason || 'n/d'}</dd></div>
        </dl>
        <div className="modal-actions">
          <button onClick={() => onDecide(approval, 'accept')}>Permitir</button>
          <button className="secondary" onClick={() => onDecide(approval, 'deny')}>Negar</button>
          <button className="secondary" onClick={onClose}>Fechar</button>
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
