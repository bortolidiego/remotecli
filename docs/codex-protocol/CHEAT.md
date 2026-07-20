# Codex App Server — cheat sheet para adapter Relay

Transporte: JSON-RPC 2.0 sobre stdio OU unix socket.
Preferir: `codex app-server proxy` (stdio → socket de controle) ou conectar em `~/.codex/ipc/ipc.sock` se existir.
Spawn fallback: `codex app-server --listen stdio://`.

## Handshake
Client → `initialize` params:
```json
{"clientInfo":{"name":"remote-clicontrol","version":"0.1.0"},"capabilities":{"experimentalApi":true}}
```
Depois notificação `initialized` (se o protocolo exigir — ver respostas reais).

## Métodos cliente→servidor
- `thread/resume` params: `{"threadId":"<CODEX_THREAD_ID>"}`
- `turn/start` params: `{"threadId":"...","input":[{"type":"text","text":"prompt do usuário"}]}`
- `turn/interrupt` params: `{"threadId":"...","turnId":"..."}`

## Aprovações (servidor→cliente REQUEST, responder com result)
Método novo: `item/commandExecution/requestApproval`
params: threadId, turnId, itemId, command?, cwd?, reason?, approvalId?
resposta result: `{"decision":"accept"}` ou `{"decision":"decline"}` (MVP: só accept uma vez ou decline)
NÃO implementar acceptForSession nesta fatia.

## Eventos (notificações)
Normalizar no adapter:
- turn/started, turn/completed, turn/interrupted → status
- item/* agent message/command → timeline
- error → erro

## Socket local
`ls ~/.codex/ipc/` — ipc.sock presente nesta máquina.
`codex app-server generate-json-schema -o DIR` já gerou schemas em /tmp/relay-codex-schema.

wrote cheat
