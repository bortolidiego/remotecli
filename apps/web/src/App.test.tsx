import '@testing-library/jest-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import App from './App'

describe('App', () => {
  afterEach(() => {
    localStorage.clear()
    vi.restoreAllMocks()
  })

  it('renderiza estado pairing-only sem buscar metadados privados', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      json: async () => ({ listening: true, address: '127.0.0.1:24109', version: 'relay-m2' }),
    } as Response)

    render(<App />)

    expect(await screen.findByText('Pairing-only')).toBeInTheDocument()
    expect(screen.getByText('Emparelhar este dispositivo')).toBeInTheDocument()
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/health', expect.any(Object)))
    expect(fetchMock).not.toHaveBeenCalledWith('/api/status', expect.any(Object))
  })

  it('renderiza estado offline quando o agente não responde', async () => {
    vi.spyOn(globalThis, 'fetch').mockRejectedValue(new Error('offline'))

    render(<App />)

    expect(await screen.findByText(/Agente local indisponível/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Tentar novamente/ })).toBeInTheDocument()
  })

  it('renderiza controles de sessão, janela e modal de aprovação', async () => {
    vi.stubGlobal('indexedDB', {
      open: () => ({
        onupgradeneeded: null,
        onsuccess: null,
        onerror: null,
        result: {
          transaction: () => ({
            objectStore: () => ({
              get: () => ({ onsuccess: null, onerror: null, result: undefined }),
              put: () => ({ oncomplete: null, onerror: null }),
            }),
          }),
        },
      }),
    } as unknown as IDBFactory)
    localStorage.setItem('relay:pair-state', JSON.stringify({
      deviceId: 'dev-web',
      leaseToken: 'lease-token',
      leaseExpiry: new Date(Date.now() + 60_000).toISOString(),
      hostName: 'Mac Relay',
      sessionId: 'sess-1',
      hostId: 'host-1',
    }))
    // Evita que o polling de aprovações/eventos abra modal com resposta vazia.
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const session = {
      id: 'host-1',
      harness: 'codex',
      nativeSessionId: 'native-1',
      codexThreadId: 'thread-1',
      cwd: '/Users/diegobortoli/Desktop/apps/relay',
      pid: 1234,
      windowId: 'win-1',
      frontmost: true,
      status: 'active',
      capabilities: ['native_control'],
      session_id: 'sess-1',
      host_id: 'host-1',
      devices: [],
      created_at: new Date().toISOString(),
      expires_at: new Date(Date.now() + 60_000).toISOString(),
    }
    const approval = {
      id: 'approval-1',
      thread_id: 'thread-1',
      turn_id: 'turn-1',
      item_id: 'item-1',
      command: 'cat package.json',
      cwd: '/Users/diegobortoli/Desktop/apps/relay',
      reason: 'Pré-visualizar um comando local antes de permitir execução única.',
      started_at_ms: Date.now(),
      created_at: new Date().toISOString(),
    }
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = String(input)
      if (url === '/health') {
        return ok({ listening: true, address: '127.0.0.1:24109', version: 'relay-m2' })
      }
      if (url === '/api/status') {
        return ok({ listening: true, address: '127.0.0.1:24109', version: 'relay-m2', session_id: 'sess-1', session_path: session.cwd, devices: [], sessions: [session] })
      }
      if (url === '/api/sessions') return ok([session])
      if (url.startsWith('/api/sessions/host-1')) {
        if (url.includes('/approvals')) return ok([approval])
        if (url.includes('/events')) return ok([])
        return ok(session)
      }
      return ok({ status: 'released' })
    })

    render(<App />)

    // Janela/Tela primeiro — qualquer CLI; chat só se houver Codex.
    expect(await screen.findByText('Codex + tela')).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Tela' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Chat' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Colar' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Teclado' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Sair' })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('tab', { name: 'Chat' }))
    expect(screen.getByRole('button', { name: 'Enviar' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Parar' })).toBeEnabled()

    expect(screen.getByRole('dialog', { name: 'Aprovação pendente' })).toBeInTheDocument()
    expect(screen.getByText('cat package.json')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Permitir' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Negar' })).toBeInTheDocument()
    vi.useRealTimers()
  })
})

function ok(body: unknown): Response {
  return {
    ok: true,
    json: async () => body,
    text: async () => JSON.stringify(body),
  } as Response
}
