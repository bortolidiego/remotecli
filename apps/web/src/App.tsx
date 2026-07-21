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
  fetchSessionOutput,
  sendSessionMessage,
  startTurn,
  type LeaseAuth,
  type PairState,
} from './api/client'
import type { AgentStatus, AuthenticatedStatus, CodexApproval, CodexEvent, SessionDescriptor } from './types/api'
import { SignalingClient, type ConnectionState } from './webrtc/signaling'

const STORED_PAIR = 'relay:pair-state'

type View = 'list' | 'session'

export default function App() {
  const [health, setHealth] = useState<AgentStatus | null>(null)
  const [pairState, setPairState] = useState<PairState | null>(() => readPairState())
  const [, setStatus] = useState<AuthenticatedStatus | null>(null)
  const [sessions, setSessions] = useState<SessionDescriptor[]>([])
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [view, setView] = useState<View>('list')
  const [detail, setDetail] = useState<SessionDescriptor | null>(null)
  const [offer, setOffer] = useState('')
  const [deviceName, setDeviceName] = useState('iPhone')
  const [message, setMessage] = useState<string | null>(null)
  const [offline, setOffline] = useState(false)
  const [, setApprovals] = useState<CodexApproval[]>([])
  const [events, setEvents] = useState<CodexEvent[]>([])
  const [selectedApproval, setSelectedApproval] = useState<CodexApproval | null>(null)
  const [busy, setBusy] = useState(false)
  const [rtcState, setRtcState] = useState<ConnectionState>('idle')
  const [dataChannelOpen, setDataChannelOpen] = useState(false)
  const [remoteStream, setRemoteStream] = useState<MediaStream | null>(null)
  const [target, setTarget] = useState<'window' | 'display'>('display')
  const [fullScreen, setFullScreen] = useState(false)
  const [promptText, setPromptText] = useState('')
  const [localLog, setLocalLog] = useState<{ id: string; role: 'user' | 'assistant'; text: string }[]>([])
  const [scannedOffer, setScannedOffer] = useState('')
  const [tab, setTab] = useState<'digitar' | 'tela'>('digitar')
  const signalingRef = useRef<SignalingClient | null>(null)
  const auth = useMemo<LeaseAuth | null>(
    () => (pairState ? { deviceId: pairState.deviceId, leaseToken: pairState.leaseToken } : null),
    [pairState],
  )

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
            setMessage(`QR inválido ou expirado (${res.status}). Rode remotecli relay de novo.`)
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
      setMessage('Oferta detectada no QR. Confirme o pareamento.')
    }
  }, [pairState])

  useEffect(() => {
    if (!auth || !selectedId || !detail?.codexThreadId) return
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
  }, [auth, selectedId, detail?.codexThreadId])

  useEffect(() => {
    if (!auth) return
    const currentAuth = auth
    let active = true
    async function tick() {
      try {
        const nextSessions = await fetchSessions(currentAuth)
        if (!active) return
        setSessions(nextSessions)
        setSelectedId((current) => {
          if (!current) return null
          const still = nextSessions.find((s) => s.id === current)
          if (!still) {
            setView('list')
            setDetail(null)
            return null
          }
          return current
        })
      } catch (err) {
        if (active) setMessage(String(err))
      }
    }
    void tick()
    const id = setInterval(tick, 3000)
    return () => {
      active = false
      clearInterval(id)
    }
  }, [auth])

  useEffect(() => {
    void refreshPublic()
  }, [])

  useEffect(() => {
    if (!auth) return
    void refreshPrivate(auth)
  }, [auth])

  useEffect(() => {
    if (!auth || !selectedId) {
      setDetail(null)
      return
    }
    fetchSessionDetail(selectedId, auth)
      .then(setDetail)
      .catch((err) => setMessage(String(err)))
  }, [auth, selectedId])

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
          setRemoteStream((prev) => {
            const stream = prev ?? new MediaStream()
            if (!stream.getTracks().includes(track)) stream.addTrack(track)
            return stream
          })
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
    void start()
    return () => {
      active = false
      client?.close().catch(() => {})
      signalingRef.current = null
    }
  }, [auth, pairState])

  async function sendTurn() {
    if (!auth || !selectedId || !promptText.trim() || busy) return
    const text = promptText.trim()
    setBusy(true)
    setMessage(null)
    setLocalLog((log) => [...log, { id: `u-${Date.now()}`, role: 'user', text }])
    setPromptText('')
    try {
      await startTurn(selectedId, text, auth)
    } catch (err) {
      setMessage(String(err))
    } finally {
      setBusy(false)
    }
  }

  /** Envia e espera resposta real da sessão (ida e volta, tipo Claude). */
  async function sendToSession() {
    if (!auth || !selectedId || !promptText.trim() || busy) return
    const text = promptText.trim()
    setBusy(true)
    setMessage(null)
    setLocalLog((log) => [...log, { id: `u-${Date.now()}`, role: 'user', text }])
    setPromptText('')
    setLocalLog((log) => [
      ...log,
      { id: `a-${Date.now()}-wait`, role: 'assistant', text: 'Enviado. Aguardando resposta da sessão…' },
    ])
    try {
      // Pode demorar: no orquestrador o bridge espera o agente responder (até ~90s)
      const res = await sendSessionMessage(selectedId, text, auth)
      const reply = (res.reply || '').trim()
      const isStatusOnly =
        !reply ||
        reply.startsWith('Mensagem enviada') ||
        reply.startsWith('Mensagem a caminho') ||
        reply.startsWith('Mensagem digitada')

      setLocalLog((log) => {
        const base = log.filter((m) => !m.id.endsWith('-wait'))
        if (!isStatusOnly) {
          return [...base, { id: `a-${Date.now()}`, role: 'assistant', text: reply }]
        }
        // Sem texto de resposta ainda — tenta espelhar o terminal
        return base
      })

      if (isStatusOnly) {
        // poll output por um tempo
        const before = await fetchSessionOutput(selectedId, auth).then((s) => s.text || '').catch(() => '')
        const deadline = Date.now() + 60_000
        let last = before
        while (Date.now() < deadline) {
          await new Promise((r) => setTimeout(r, 3000))
          const snap = await fetchSessionOutput(selectedId, auth).catch(() => null)
          const now = snap?.text || ''
          if (now && now !== last && now.length > last.length + 10) {
            let delta = now
            if (last && now.startsWith(last)) delta = now.slice(last.length)
            delta = delta.trim()
            if (delta.length > 15 && !delta.includes('No connection')) {
              setLocalLog((log) => [...log, { id: `a-${Date.now()}`, role: 'assistant', text: delta.slice(0, 4000) }])
              return
            }
          }
          last = now || last
        }
        setLocalLog((log) => [
          ...log,
          {
            id: `a-${Date.now()}`,
            role: 'assistant',
            text: reply || 'Resposta ainda não espelhada no celular. Veja o Mac — a mensagem já entrou na sessão.',
          },
        ])
      }
    } catch (err) {
      setMessage(String(err))
      setLocalLog((log) => {
        const base = log.filter((m) => !m.id.endsWith('-wait'))
        return [...base, { id: `a-${Date.now()}`, role: 'assistant', text: `Falha: ${String(err)}` }]
      })
    } finally {
      setBusy(false)
    }
  }

  async function interruptCurrentTurn() {
    if (!auth || !selectedId || busy) return
    setBusy(true)
    try {
      await interruptTurn(selectedId, auth)
    } catch (err) {
      setMessage(String(err))
    } finally {
      setBusy(false)
    }
  }

  async function submitDecision(approval: CodexApproval, decision: 'accept' | 'deny') {
    if (!auth || !selectedId) return
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
      setOffline(false)
      setMessage(null)
    } catch (err) {
      setMessage(String(err))
      setStatus(null)
    }
  }

  async function submitPair() {
    setMessage('Validando oferta e gerando chaves…')
    try {
      const next = await pair(offer, deviceName)
      localStorage.setItem(STORED_PAIR, JSON.stringify(next))
      setPairState(next)
      setOffer('')
      setMessage(null)
      setView('list')
    } catch (err) {
      setMessage(String(err))
    }
  }

  async function releaseCurrentLease() {
    const previous = auth
    try {
      if (previous) await releaseLease(previous)
    } catch {
      /* lease morto */
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
    setView('list')
    setRemoteStream(null)
    setDataChannelOpen(false)
    setRtcState('idle')
    setSelectedApproval(null)
    setLocalLog([])
    setMessage('Desconectado. Escaneie um QR novo no Mac.')
  }

  function openSession(session: SessionDescriptor) {
    setSelectedId(session.id)
    setView('session')
    setTab('digitar')
    setPromptText('')
    setLocalLog([])
    setEvents([])
    setMessage(null)
  }

  function backToList() {
    setView('list')
    setSelectedId(null)
    setDetail(null)
    setLocalLog([])
    setPromptText('')
  }

  if (offline) {
    return (
      <Shell>
        <div className="center-screen">
          <span className="status-dot offline" />
          <h1>Mac offline</h1>
          <p>O agente local não responde. No Mac rode <code>remotecli relay</code> e tente de novo.</p>
          <button className="primary-btn" type="button" onClick={() => void refreshPublic()}>
            Tentar novamente
          </button>
        </div>
      </Shell>
    )
  }

  if (!pairState) {
    return (
      <Shell>
        <header className="app-header bordered">
          <div style={{ width: 40 }} />
          <h1>
            Remote CliControl
            <span className="subtitle">{health?.listening ? 'Mac online' : 'Aguardando Mac'}</span>
          </h1>
          <div style={{ width: 40 }} />
        </header>
        <div className="center-screen">
          <h1>Emparelhar</h1>
          <p>Escaneie o QR do Mac (mesma Wi‑Fi). Nada privado é lido antes do pareamento.</p>
          <label className="field">
            Nome deste iPhone
            <input value={deviceName} onChange={(e) => setDeviceName(e.target.value)} />
          </label>
          <label className="field">
            Oferta do QR
            <textarea value={offer} onChange={(e) => setOffer(e.target.value)} rows={6} />
          </label>
          <button
            className="primary-btn"
            type="button"
            disabled={!offer.trim() || !deviceName.trim()}
            onClick={() => void submitPair()}
          >
            {scannedOffer ? 'Parear via QR' : 'Parear'}
          </button>
          {message && <p className="notice">{message}</p>}
        </div>
      </Shell>
    )
  }

  if (view === 'list') {
    return (
      <Shell>
        <header className="app-header">
          <button className="icon-btn ghost" type="button" onClick={() => void releaseCurrentLease()} aria-label="Menu">
            ☰
          </button>
          <h1>
            Terminais
            <span className="subtitle">Seu Mac · até {formatTime(pairState.leaseExpiry)}</span>
          </h1>
          <button className="icon-btn" type="button" onClick={() => void refreshPrivate()} aria-label="Atualizar">
            ↻
          </button>
        </header>

        <div className="screen">
          <div className="screen-body">
            <div className="sessions-toolbar">
              <strong>Sessões</strong>
              <span className="filter-chip">Todas ▾</span>
            </div>

            {sessions.length === 0 ? (
              <div className="empty-state">
                <h2>Nenhum terminal</h2>
                <p>
                  No Mac, em cada terminal que quiser controlar, rode:
                  <br />
                  <code>remotecli here</code>
                </p>
              </div>
            ) : (
              <div className="session-cards" aria-label="Sessões">
                {sessions.map((session) => (
                  <button
                    key={session.id}
                    type="button"
                    className="session-card"
                    onClick={() => openSession(session)}
                  >
                    <div className="session-card-top">
                      <div className={`session-avatar ${session.harness}`}>{avatarLetter(session)}</div>
                      <div className="session-card-meta">
                        <p className="session-card-title">{sessionTitle(session)}</p>
                        <div className="session-card-status">
                          <span className="dot" />
                          Conectado · {harnessLabel(session)}
                        </div>
                      </div>
                    </div>
                    <p className="session-card-preview">
                      {session.codexThreadId
                        ? 'Toque para conversar com o Codex deste terminal.'
                        : 'Toque para digitar e enviar comandos ao Mac.'}
                    </p>
                    <p className="session-card-path">{shortCwd(session.cwd)}</p>
                  </button>
                ))}
              </div>
            )}
            {message && <p className="notice" style={{ marginTop: 16 }}>{message}</p>}
          </div>
        </div>

        <button
          type="button"
          className="fab"
          onClick={() =>
            setMessage('No Mac: abra um terminal e rode remotecli here para aparecer aqui.')
          }
        >
          + Novo terminal
        </button>
      </Shell>
    )
  }

  // Session detail view
  const session = detail
  const hasCodex = Boolean(session?.codexThreadId)
  const isMaestri = Boolean(session?.harness === 'maestri' || session?.maestri_agent_name)
  const linkLabel = rtcStateLabel(rtcState, dataChannelOpen)

  return (
    <Shell>
      <header className="app-header bordered">
        <button className="icon-btn" type="button" onClick={backToList} aria-label="Voltar">
          ←
        </button>
        <h1>
          {session ? sessionTitle(session) : 'Sessão'}
          <span className="subtitle">
            {session ? harnessLabel(session) : '…'} · {linkLabel}
          </span>
        </h1>
        <button className="icon-btn" type="button" onClick={() => void refreshPrivate()} aria-label="Atualizar">
          ↻
        </button>
      </header>

      <div className="tabs-row" role="tablist" aria-label="Modo">
        <button
          type="button"
          className={`tab-pill ${tab === 'digitar' ? 'active' : ''}`}
          role="tab"
          aria-selected={tab === 'digitar'}
          onClick={() => setTab('digitar')}
        >
          {hasCodex ? 'Chat' : 'Digitar'}
        </button>
        <button
          type="button"
          className={`tab-pill ${tab === 'tela' ? 'active' : ''}`}
          role="tab"
          aria-selected={tab === 'tela'}
          onClick={() => setTab('tela')}
        >
          Tela
        </button>
      </div>

      {tab === 'digitar' ? (
        <div className="screen">
          <div className="chat-scroll">
            <div className="chat-intro">
              {hasCodex ? (
                <>
                  Converse com o Codex deste terminal. As mensagens vão para a thread no Mac.
                  <p className="muted">Pasta: {shortCwd(session?.cwd)}</p>
                </>
              ) : isMaestri ? (
                <>
                  Como no app Claude: digite aqui e a mensagem entra na sessão{' '}
                  <strong>{session?.maestri_agent_name || sessionTitle(session!)}</strong>. No Mac a conversa continua de onde parou.
                  <p className="muted">Pasta: {shortCwd(session?.cwd)}</p>
                </>
              ) : (
                <>
                  Digite e envie — a mensagem vai para esta sessão no Mac (como se você tivesse digitado).
                  <p className="muted">
                    Canal: {linkLabel}. Pasta: {shortCwd(session?.cwd)}
                  </p>
                </>
              )}
            </div>

            {localLog.map((msg) => (
              <div key={msg.id} className={`msg-row ${msg.role}`}>
                <div className="msg-bubble">{msg.text}</div>
              </div>
            ))}

            {hasCodex && events.length > 0 && (
              <div className="event-feed">
                {events.slice(-8).map((ev) => (
                  <div key={ev.id} className="event-item">
                    <strong>{ev.kind}</strong>
                    {ev.text || ev.status || ''}
                  </div>
                ))}
              </div>
            )}
          </div>

          <div className="composer-wrap">
            <div className="composer">
              <textarea
                value={promptText}
                onChange={(e) => setPromptText(e.target.value)}
                placeholder={
                  hasCodex
                    ? 'Mensagem para o Codex…'
                    : isMaestri
                      ? 'Mensagem para a sessão Maestri…'
                      : 'Comando ou texto para o Mac…'
                }
                rows={1}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && !e.shiftKey) {
                    e.preventDefault()
                    if (hasCodex) void sendTurn()
                    else void sendToSession()
                  }
                }}
              />
              <div className="composer-actions">
                {hasCodex && (
                  <button
                    type="button"
                    className="composer-chip"
                    onClick={() => void interruptCurrentTurn()}
                    disabled={busy}
                    aria-label="Parar"
                    title="Parar"
                  >
                    ■
                  </button>
                )}
                <span className="spacer" />
                <button
                  type="button"
                  className="send-btn"
                  disabled={busy || !promptText.trim()}
                  onClick={() => {
                    if (hasCodex) void sendTurn()
                    else void sendToSession()
                  }}
                  aria-label="Enviar"
                >
                  ↑
                </button>
              </div>
            </div>
            {busy && <p className="composer-hint">Enviando para a sessão…</p>}
            {message && <p className="notice" style={{ marginTop: 8 }}>{message}</p>}
          </div>
        </div>
      ) : (
        <div className="screen">
          <div className="screen-body screen-panel">
            <div className={`video-stage ${fullScreen ? 'fullscreen' : ''}`}>
              <video
                muted
                playsInline
                autoPlay
                className="relay-video"
                ref={(el) => {
                  if (el && remoteStream) {
                    el.srcObject = remoteStream
                    el.play().catch(() => {})
                  }
                }}
              />
              {!remoteStream && (
                <p className="video-placeholder">
                  {dataChannelOpen
                    ? 'Canal aberto. Vídeo da tela ainda em construção.'
                    : 'Conectando ao Mac…'}
                </p>
              )}
            </div>
            <div className="screen-tools">
              <button
                type="button"
                className={`pill-btn ${target === 'display' ? 'active' : ''}`}
                onClick={() => setTarget('display')}
              >
                Tela cheia
              </button>
              <button
                type="button"
                className={`pill-btn ${target === 'window' ? 'active' : ''}`}
                onClick={() => setTarget('window')}
              >
                Janela
              </button>
              <button type="button" className="pill-btn" onClick={() => setFullScreen((v) => !v)}>
                {fullScreen ? 'Sair' : 'Expandir'}
              </button>
              <button type="button" className="pill-btn danger" onClick={() => void releaseCurrentLease()}>
                Desconectar
              </button>
            </div>
          </div>
        </div>
      )}

      {selectedApproval && hasCodex && (
        <ApprovalModal
          approval={selectedApproval}
          onDecide={submitDecision}
          onClose={() => setSelectedApproval(null)}
        />
      )}
    </Shell>
  )
}

function ApprovalModal({
  approval,
  onDecide,
  onClose,
}: {
  approval: CodexApproval
  onDecide: (a: CodexApproval, decision: 'accept' | 'deny') => void
  onClose: () => void
}) {
  return (
    <div className="modal-backdrop" role="presentation">
      <section className="modal" role="dialog" aria-modal="true" aria-labelledby="approval-title">
        <h2 id="approval-title">Aprovação pendente</h2>
        <dl>
          <div>
            <dt>Comando</dt>
            <dd>{approval.command || 'n/d'}</dd>
          </div>
          <div>
            <dt>Pasta</dt>
            <dd>{approval.cwd || 'n/d'}</dd>
          </div>
          <div>
            <dt>Motivo</dt>
            <dd>{approval.reason || 'n/d'}</dd>
          </div>
        </dl>
        <div className="modal-actions">
          <button type="button" onClick={() => onDecide(approval, 'accept')}>
            Permitir
          </button>
          <button type="button" className="secondary" onClick={() => onDecide(approval, 'deny')}>
            Negar
          </button>
          <button type="button" className="secondary" onClick={onClose}>
            Fechar
          </button>
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
    return raw ? (JSON.parse(raw) as PairState) : null
  } catch {
    return null
  }
}

function shortCwd(cwd: string | undefined): string {
  if (!cwd) return ''
  return cwd
    .replace(/^\/Users\/[^/]+/, '~')
    .replace(/^\/home\/[^/]+/, '~')
}

function sessionTitle(session: SessionDescriptor): string {
  return session.title || session.nativeSessionId || session.id
}

function harnessLabel(session: SessionDescriptor): string {
  if (session.codexThreadId || session.harness === 'codex') return 'Codex'
  if (session.harness === 'maestri') return 'Maestri'
  return 'Terminal'
}

function avatarLetter(session: SessionDescriptor): string {
  const t = sessionTitle(session)
  return (t[0] || 'T').toUpperCase()
}

function rtcStateLabel(state: ConnectionState, dataOpen: boolean): string {
  if (dataOpen || state === 'connected') return 'Pronto'
  if (state === 'connecting' || state === 'reconnecting') return 'Conectando…'
  if (state === 'failed') return 'Falhou'
  if (state === 'disconnected') return 'Desconectado'
  return 'Aguardando'
}

function formatTime(value: string): string {
  try {
    return new Date(value).toLocaleTimeString('pt-BR', { hour: '2-digit', minute: '2-digit' })
  } catch {
    return value
  }
}
