import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
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
  openSessionFile,
  pair,
  releaseLease,
  fetchSessionOutput,
  sendSessionMessage,
  startTurn,
  uploadSessionFile,
  type LeaseAuth,
  type PairState,
} from './api/client'
import type { AgentStatus, Attachment, AuthenticatedStatus, CodexApproval, CodexEvent, SessionDescriptor } from './types/api'
import { SignalingClient, type ConnectionState } from './webrtc/signaling'

const STORED_PAIR = 'relay:pair-state'

const ACCEPT_TYPES = 'image/*,.pdf,.txt,.md,.json,.csv,.png,.jpg,.jpeg,.webp,.gif'

const MAX_ATTACHMENT_SIZE = 15 << 20

type View = 'list' | 'session'

type PendingAttachment = Attachment & {
  file: File
  previewUrl?: string
  uploading?: boolean
}

type ChatMessage = {
  id: string
  role: 'user' | 'assistant'
  text: string
  attachments?: Attachment[]
  isFallback?: boolean
}

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
  const [localLog, setLocalLog] = useState<ChatMessage[]>([])
  const [scannedOffer, setScannedOffer] = useState('')
  const [tab, setTab] = useState<'digitar' | 'tela'>('digitar')
  const [terminalMirror, setTerminalMirror] = useState<{ text: string; source?: string; updatedAt?: string }>({ text: '' })
  const [terminalOpen, setTerminalOpen] = useState(false) // fechado: chat limpo tipo Claude
  const [pendingAttachments, setPendingAttachments] = useState<PendingAttachment[]>([])
  const fileInputRef = useRef<HTMLInputElement | null>(null)
  const cameraInputRef = useRef<HTMLInputElement | null>(null)
  const chatScrollRef = useRef<HTMLDivElement | null>(null)
  const lastChatReplyRef = useRef('')
  const signalingRef = useRef<SignalingClient | null>(null)
  const auth = useMemo<LeaseAuth | null>(
    () => (pairState ? { deviceId: pairState.deviceId, leaseToken: pairState.leaseToken } : null),
    [pairState],
  )

  const scrollToBottom = useCallback(() => {
    const el = chatScrollRef.current
    if (el && typeof el.scrollTo === 'function') {
      el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
    }
  }, [])

  useEffect(() => {
    scrollToBottom()
  }, [localLog, scrollToBottom])

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
    if (!auth || !selectedId || view !== 'session' || tab !== 'digitar') return
    const sessionId = selectedId
    const currentAuth = auth
    let active = true
    async function tick() {
      try {
        const snap = await fetchSessionOutput(sessionId, currentAuth)
        if (!active) return
        const raw = snap.text || ''
        if (raw || snap.updated_at) {
          setTerminalMirror((prev) => {
            if (prev.text === raw && prev.updatedAt === snap.updated_at) return prev
            return { text: raw, source: snap.source, updatedAt: snap.updated_at }
          })
          // Espelha resposta limpa no chat (sem dump TUI)
          const reply = extractAssistantReply(raw)
          if (
            reply &&
            reply !== lastChatReplyRef.current &&
            !isPlaceholderReply(reply) &&
            reply.length > 25
          ) {
            lastChatReplyRef.current = reply
            setLocalLog((log) => {
              if (log.some((m) => m.role === 'assistant' && m.text === reply)) return log
              return [...log, { id: `a-${Date.now()}`, role: 'assistant', text: reply.slice(0, 6000) }]
            })
          }
        }
      } catch {
        // silencioso
      }
    }
    void tick()
    const id = setInterval(tick, 1200)
    return () => {
      active = false
      clearInterval(id)
    }
  }, [auth, selectedId, view, tab])

  const refreshMirror = useCallback(async () => {
    if (!auth || !selectedId) return
    try {
      const snap = await fetchSessionOutput(selectedId, auth)
      setTerminalMirror({ text: snap.text || '', source: snap.source, updatedAt: snap.updated_at })
    } catch (err) {
      setMessage(String(err))
    }
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

  function addPendingAttachment(file: File) {
    if (file.size > MAX_ATTACHMENT_SIZE) {
      setMessage(`Arquivo muito grande: ${file.name} (máx 15MB)`)
      return
    }
    const id = `att-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`
    const previewUrl = file.type.startsWith('image/') ? URL.createObjectURL(file) : undefined
    const att: PendingAttachment = {
      id,
      name: file.name,
      mime: file.type || 'application/octet-stream',
      size: file.size,
      url: '',
      file,
      previewUrl,
    }
    setPendingAttachments((prev) => [...prev, att])
  }

  function removePendingAttachment(id: string) {
    setPendingAttachments((prev) => {
      const found = prev.find((a) => a.id === id)
      if (found?.previewUrl) URL.revokeObjectURL(found.previewUrl)
      return prev.filter((a) => a.id !== id)
    })
  }

  function clearPendingAttachments() {
    setPendingAttachments((prev) => {
      prev.forEach((a) => {
        if (a.previewUrl) URL.revokeObjectURL(a.previewUrl)
      })
      return []
    })
  }

  function handlePaste(e: React.ClipboardEvent) {
    const items = e.clipboardData?.items
    if (!items) return
    let foundImage = false
    for (let i = 0; i < items.length; i++) {
      const item = items[i]
      if (item.kind === 'file' && item.type.startsWith('image/')) {
        const file = item.getAsFile()
        if (file) {
          addPendingAttachment(file)
          foundImage = true
        }
      }
    }
    if (foundImage) {
      e.preventDefault()
    }
  }

  async function uploadAndSendAttachments(sessionId: string, currentAuth: LeaseAuth | string, caption?: string) {
    const files = pendingAttachments.filter((a) => a.file && !a.uploading)
    if (files.length === 0) return []
    setPendingAttachments((prev) => prev.map((a) => (files.some((f) => f.id === a.id) ? { ...a, uploading: true } : a)))
    const uploaded: Attachment[] = []
    for (const att of files) {
      try {
        const rec = await uploadSessionFile(sessionId, att.file, caption || att.caption || '', currentAuth)
        uploaded.push(rec)
      } catch (err) {
        setMessage(`Falha ao enviar ${att.name}: ${String(err)}`)
      }
    }
    setPendingAttachments((prev) => prev.filter((a) => !files.some((f) => f.id === a.id)))
    return uploaded
  }

  async function sendTurn() {
    if (!auth || !selectedId || (!promptText.trim() && pendingAttachments.length === 0) || busy) return
    const text = promptText.trim()
    const caption = text || undefined
    const currentAuth = auth
    setBusy(true)
    setMessage(null)

    const uploaded = await uploadAndSendAttachments(selectedId, currentAuth, caption)
    const userText = text || (uploaded.length > 0 ? '[imagem anexada]' : '')
    setLocalLog((log) => [...log, { id: `u-${Date.now()}`, role: 'user', text: userText, attachments: uploaded }])
    setPromptText('')
    clearPendingAttachments()
    try {
      // Só texto vai para o turn; anexo já notificou a sessão no upload.
      if (text) {
        await startTurn(selectedId, text, currentAuth)
      }
    } catch (err) {
      setMessage(String(err))
    } finally {
      setBusy(false)
    }
  }

  /** Uma mensagem só (texto + anexos). Composer libera na hora; resposta via poll. */
  async function sendToSession() {
    if (!auth || !selectedId || (!promptText.trim() && pendingAttachments.length === 0) || busy) return
    const text = promptText.trim()
    const currentAuth = auth
    const sessionId = selectedId
    setBusy(true)
    setMessage(null)

    // Upload só salva arquivos (sem injetar no Mac ainda)
    const uploaded = await uploadAndSendAttachments(sessionId, currentAuth, '')
    const parts: string[] = []
    for (const att of uploaded) {
      const path = att.path || att.name
      parts.push(`[anexo] ${att.name}${path ? ` (${path})` : ''}`)
    }
    if (text) parts.push(text)
    const payload = parts.join('\n')
    if (!payload) {
      setBusy(false)
      return
    }

    setLocalLog((log) => [
      ...log,
      {
        id: `u-${Date.now()}`,
        role: 'user',
        text: text || (uploaded.length ? uploaded.map((a) => a.name).join(', ') : ''),
        attachments: uploaded,
      },
    ])
    setPromptText('')
    clearPendingAttachments()

    // Snapshot baseline antes do envio
    const baseline = await fetchSessionOutput(sessionId, currentAuth)
      .then((s) => s.text || '')
      .catch(() => '')
    lastChatReplyRef.current = extractAssistantReply(baseline)

    try {
      const controller = new AbortController()
      const timeout = setTimeout(() => controller.abort(), 12_000)
      await sendSessionMessage(sessionId, payload, currentAuth, controller.signal)
      clearTimeout(timeout)
    } catch (err) {
      if (!(err instanceof Error && err.name === 'AbortError')) {
        setMessage(String(err))
      }
    } finally {
      setBusy(false)
    }

    // Poll agressivo até achar resposta limpa
    void pollForAssistantReply(sessionId, currentAuth, lastChatReplyRef.current)
  }

  async function pollForAssistantReply(
    sessionId: string,
    currentAuth: LeaseAuth | string,
    beforeReply: string,
  ) {
    const deadline = Date.now() + 150_000
    let lastRaw = ''
    while (Date.now() < deadline) {
      await new Promise((r) => setTimeout(r, 700))
      const snap = await fetchSessionOutput(sessionId, currentAuth).catch(() => null)
      const raw = snap?.text || ''
      if (!raw || raw === lastRaw) continue
      lastRaw = raw
      setTerminalMirror({ text: raw, source: snap?.source, updatedAt: snap?.updated_at })
      const now = extractAssistantReply(raw)
      if (!now || isPlaceholderReply(now)) continue
      if (now === beforeReply || now === lastChatReplyRef.current) continue
      // Aceita se mudou de forma clara ou cresceu
      if (beforeReply && now.length < beforeReply.length + 15 && now.includes(beforeReply.slice(0, 40))) {
        continue
      }
      lastChatReplyRef.current = now
      setLocalLog((log) => {
        if (log.some((m) => m.role === 'assistant' && m.text === now)) return log
        return [...log, { id: `a-${Date.now()}`, role: 'assistant', text: now.slice(0, 6000) }]
      })
      return
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
    setPendingAttachments([])
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
    setTerminalMirror({ text: '' })
    setTerminalOpen(false)
    setPendingAttachments([])
  }

  function backToList() {
    setView('list')
    setSelectedId(null)
    setDetail(null)
    setLocalLog([])
    setPromptText('')
    setPendingAttachments([])
  }

  async function handleOpenFile(att: Attachment) {
    if (!auth || !selectedId) return
    try {
      const blobUrl = await openSessionFile(selectedId, att.id, auth)
      window.open(blobUrl, '_blank')
    } catch (err) {
      setMessage(`Não foi possível abrir ${att.name}: ${String(err)}`)
    }
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
          <p>Escaneie o QR do Mac (mesma Wi‑Fi).</p>
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
                      {session.codexThreadId ? 'Codex' : harnessLabel(session)}
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
  const linkLabel = rtcStateLabel(rtcState, dataChannelOpen)
  const canSend = promptText.trim().length > 0 || pendingAttachments.length > 0

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
        <button
          className="icon-btn"
          type="button"
          onClick={() => { void refreshPrivate(); void refreshMirror() }}
          aria-label="Atualizar"
        >
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
          Chat
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
          <div className="chat-scroll" ref={chatScrollRef}>
            {localLog.length === 0 && (
              <div className="chat-empty">Envie uma mensagem</div>
            )}

            {localLog.map((msg) => (
              <div key={msg.id} className={`msg-row ${msg.role} ${msg.isFallback ? 'fallback' : ''}`}>
                <div className="msg-bubble">
                  {msg.text}
                  {msg.attachments && msg.attachments.length > 0 && (
                    <div className="attachment-strip">
                      {msg.attachments.map((att) => (
                        <button
                          key={att.id}
                          type="button"
                          className="attachment-thumb"
                          onClick={() => void handleOpenFile(att)}
                          aria-label={att.name}
                        >
                          {att.mime.startsWith('image/') ? (
                            <img src={att.url} alt={att.name} loading="lazy" />
                          ) : (
                            <span className="attachment-file">{att.name}</span>
                          )}
                        </button>
                      ))}
                    </div>
                  )}
                </div>
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

            {/* Terminal bruto opcional (fechado) — não é o chat */}
            {terminalMirror.text && (
              <div className={`terminal-panel ${terminalOpen ? 'open' : ''}`}>
                <button
                  type="button"
                  className="terminal-toggle"
                  onClick={() => setTerminalOpen((v) => !v)}
                  aria-expanded={terminalOpen}
                >
                  <span>{terminalOpen ? '▾' : '▸'} Raw (opcional)</span>
                </button>
                {terminalOpen && <pre className="terminal-body">{terminalMirror.text}</pre>}
              </div>
            )}
          </div>

          <div className="composer-wrap">
            {pendingAttachments.length > 0 && (
              <div className="pending-attachments">
                {pendingAttachments.map((att) => (
                  <div key={att.id} className="pending-att">
                    {att.previewUrl ? (
                      <img src={att.previewUrl} alt={att.name} />
                    ) : (
                      <span className="pending-att-file">{att.name}</span>
                    )}
                    <button
                      type="button"
                      className="pending-att-remove"
                      onClick={() => removePendingAttachment(att.id)}
                      aria-label={`Remover ${att.name}`}
                    >
                      ×
                    </button>
                    {att.uploading && <span className="pending-att-loading" />}
                  </div>
                ))}
              </div>
            )}
            <div className="composer">
              <div className="composer-row">
                <button
                  type="button"
                  className="attach-btn"
                  onClick={() => fileInputRef.current?.click()}
                  aria-label="Anexar"
                  title="Anexar arquivo ou foto"
                >
                  📎
                </button>
                <button
                  type="button"
                  className="attach-btn"
                  onClick={() => cameraInputRef.current?.click()}
                  aria-label="Foto"
                  title="Tirar foto"
                >
                  📷
                </button>
                <textarea
                  value={promptText}
                  onChange={(e) => setPromptText(e.target.value)}
                  onPaste={handlePaste}
                  placeholder="Mensagem…"
                  rows={1}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' && !e.shiftKey) {
                      e.preventDefault()
                      if (hasCodex) void sendTurn()
                      else void sendToSession()
                    }
                  }}
                />
                <input
                  ref={fileInputRef}
                  type="file"
                  accept={ACCEPT_TYPES}
                  style={{ display: 'none' }}
                  onChange={(e) => {
                    const files = e.target.files
                    if (!files) return
                    for (let i = 0; i < files.length; i++) addPendingAttachment(files[i])
                    e.target.value = ''
                  }}
                />
                <input
                  ref={cameraInputRef}
                  type="file"
                  accept="image/*"
                  capture="environment"
                  style={{ display: 'none' }}
                  onChange={(e) => {
                    const files = e.target.files
                    if (!files) return
                    for (let i = 0; i < files.length; i++) addPendingAttachment(files[i])
                    e.target.value = ''
                  }}
                />
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
                <button
                  type="button"
                  className="send-btn"
                  disabled={busy || !canSend}
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

function isPlaceholderReply(s: string): boolean {
  if (!s) return true
  const lower = s.toLowerCase()
  return (
    lower.includes('ver terminal se quiser') ||
    lower.startsWith('mensagem enviada') ||
    lower.startsWith('mensagem digitada') ||
    lower.includes('ainda não espelhada') ||
    lower.includes('no connection')
  )
}

/** Extrai texto útil da saída do terminal (sem TUI, sem “Resposta no Mac”). */
function extractAssistantReply(raw: string): string {
  if (!raw) return ''
  // Mantém quebras de linha (antes colapsava tudo e quebrava o extrator)
  const cleaned = raw.replace(/\x1b\[[0-9;?]*[A-Za-z]/g, '')
  const lines = cleaned.split(/\r?\n/)
  const out: string[] = []
  for (const line of lines) {
    const trimmed = line.trim()
    if (!trimmed) {
      if (out.length > 0 && out[out.length - 1] !== '') out.push('')
      continue
    }
    if (isTUINoiseLine(trimmed)) continue
    out.push(trimmed)
  }
  while (out.length && out[out.length - 1] === '') out.pop()
  if (out.length === 0) return ''

  // Preferir o trecho final (resposta mais recente)
  const joined = out.join('\n').trim()
  const human = out.filter(looksHumanReply)
  if (human.length >= 2) {
    // Últimos blocos humanos costumam ser a resposta
    return human.slice(-12).join('\n').slice(0, 6000)
  }
  if (human.length === 1 && human[0].length > 20) return human[0].slice(0, 6000)
  if (joined.length < 20) return ''
  // Últimas ~40 linhas limpas
  return out.slice(-40).join('\n').slice(0, 6000)
}

function isTUINoiseLine(s: string): boolean {
  const lower = s.toLowerCase()
  const prefixes = [
    'thought for',
    'user_prompt_submit',
    'shift+tab',
    'always-approve',
    'hooks:',
    'running hooks',
    'hook output',
    'token',
    'model:',
    'usage:',
    'context:',
    'total tokens',
    'completion tokens',
    'worked for',
    'format:',
    'dimensions:',
    'size:',
    'path:',
    'image #',
    'ctrl+',
    'weekly limit',
  ]
  for (const prefix of prefixes) {
    if (lower.startsWith(prefix)) return true
  }
  if (lower.includes('hooks:') && lower.includes('[')) return true
  if (/\d+\s*[km]?\s*\/\s*\d+\s*[km]?/.test(s)) return true
  if (s.startsWith('~/.maestri') || s.includes('/.maestri/')) return true
  if (s.includes('Grok 4.5') || s.includes('always-approve')) return true
  if (/^\[hooks/i.test(s)) return true
  if (s.startsWith('/') && s.split(' ').length === 1 && s.length < 80) return true
  return false
}

function looksHumanReply(s: string): boolean {
  const lower = s.toLowerCase()
  const starters = [
    'perfeito', 'claro', 'ok', 'tudo bem', 'entendi', 'show', 'legal',
    'vou ', 'vamos ', 'pode ', 'farei ', 'feito', 'pronto', 'certo',
    'sugiro ', 'recomendo ', 'aqui está', 'aqui estão', 'segue', 'anexo',
    'ótimo', 'beleza', 'blz', 'sim', 'não', 'claro que', 'sem problemas',
  ]
  for (const starter of starters) {
    if (lower.startsWith(starter)) return true
  }
  const words = s.split(/\s+/)
  if (words.length > 0) {
    const first = words[0].replace(/[,.!?]$/, '').toLowerCase()
    const personal = ['eu', 'você', 'ele', 'ela', 'nós', 'eles', 'isso', 'esse', 'esta', 'o', 'a', 'os', 'as', 'um', 'uma', 'me', 'te', 'se', 'para', 'por']
    if (personal.includes(first)) return true
  }
  if (s.endsWith('?')) return true
  return false
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
