# Relay - Marco 4.1

MVP pessoal para macOS arm64 com agente Go local, PWA embarcada/offline, CLI e helper Swift de menu bar. **Marco 4.1** adiciona scaffold do Cloudflare Tunnel (`internal/tunnel`) com manager real/fake, preferências salvas no Keychain e integração CLI.

## Estrutura

```text
apps/web              React + Vite + TypeScript PWA com WebRTC/signaling
cmd/relay             CLI Go
internal/agent        Servidor HTTP local 127.0.0.1:24109 + signaling WebRTC
internal/channel      Envelope AES-256-GCM com AAD e replay guard
internal/crypto       ECDSA P-256, ECDH P-256, HKDF, AES-256-GCM
internal/geometry     Coordenadas normalizadas entre vídeo e captura
internal/ipc          Unix domain socket Go↔helper com framing binário
internal/keychain     Store interface: macOS security CLI / fake para testes
internal/pairing      Registry, QR one-time, nonce replay guard, lease
internal/sandbox      Path canonico + bloqueios + limite 25MB
internal/tunnel       Cloudflare Tunnel: Start/Stop/Status, runner real/fake
internal/web          go:embed de apps/web/dist com fallback SPA/PWA
internal/webrtc       PeerConnection Pion por lease, DataChannels, ICE
shared/contracts      SessionDescriptor e payloads compartilhados
helper/RelayHelper    SwiftPM helper LSUIElement/menu bar + ScreenCaptureKit/VideoToolbox
```

## Build e Testes

```bash
make web-build
make go-build
make go-test
make race
make web-test
make swift-build
make swift-test
```

Equivalentes diretos:

```bash
go test ./...
go test -race ./...
cd apps/web && npm run build && npm test
cd helper/RelayHelper && swift build && swift test
```

## Uso Local

```bash
go build ./cmd/relay
./relay setup minha-sessao "Meu Mac" "$PWD" --frontmost
```

O `setup` cria identidade, ECDH e token local no Keychain. O token não é impresso. Em outro terminal, informe só a sessão; o CLI recupera o token automaticamente do Keychain:

```bash
export RELAY_SESSION_ID=minha-sessao
./relay share
./relay status
./relay devices
./relay stop
```

### Cloudflare Tunnel (Marco 4.1)

Habilite no setup (o token pode vir de `RELAY_TUNNEL_TOKEN`):

```bash
export RELAY_TUNNEL_TOKEN="<seu-token-do-cloudflare-tunnel>"
./relay setup minha-sessao "Meu Mac" "$PWD" --tunnel-enabled --tunnel-name relay-diego --tunnel-hostname relay.kbtech.com.br
./relay share   # inicia o tunnel automaticamente se configurado
./relay status  # mostra estado do tunnel em tunnel.*
./relay stop    # encerra agente + tunnel
```

Sem `cloudflared` instalado, `share` imprime um aviso claro em vez de falhar.

`RELAY_LOCAL_TOKEN` ainda existe como override explícito para testes. O uso normal não precisa dele.

`share` detecta `CODEX_THREAD_ID` e `MAESTRI_TERMINAL_ID` quando `RELAY_SESSION_ID` não foi definido, envia metadata local autenticada antes da oferta, e emite envelope assinado, payload textual e PNG QR local one-time. Use `--pid` ou `RELAY_TARGET_PID` para informar o processo alvo; sem isso, o default seguro é o processo pai do CLI.

## Pareamento PWA

A PWA servida pelo agente aceita somente envelope assinado real. O navegador:

- valida assinatura ECDSA P-256 da oferta e fingerprint do host;
- gera chaves ECDSA P-256 e ECDH P-256 via WebCrypto;
- persiste chaves privadas nao extraiveis em IndexedDB;
- envia apenas chaves publicas e assinatura do desafio `relay-pair-v1`;
- deriva a chave de sessao via ECDH P-256 + HKDF-SHA256.
- guarda a chave AES da sessao por `host_id + session_id`, não globalmente.

Antes de autenticar, a PWA consulta apenas `/health`; nao mostra sessao, cwd, devices ou metadados.

## Endpoints

- Publico minimo: `GET /health`.
- Local admin: `POST /api/offer`, `POST /api/metadata`, `POST /api/revoke`, `POST /api/stop`.
- Autenticados por local token ou lease: `GET /api/status`, `GET /api/devices`, `GET /api/sessions`, `GET /api/sessions/{id}`.
- Lease: `POST /api/lease/release`, `GET /api/read?path=...`.
- WebRTC signaling (lease): `POST /api/webrtc/offer`, `POST /api/webrtc/answer`, `POST /api/webrtc/ice`, `GET /api/webrtc/status`.

## Transporte WebRTC

- PeerConnection por lease com DataChannels `relay-control`, `relay-clipboard`, `relay-files`.
- Mensagens cifradas com AES-256-GCM: AAD inclui `label + device_id + session_id + channel_id`; nonce 12 bytes; replay guard por sequência.
- Vídeo H.264 baseline/main via VideoToolbox; resolução 720p/30 default, resize até 1080p.
- STUN default seguro (`stun:stun.cloudflare.com:3478`, `stun:stun.l.google.com:19302`) para descoberta de candidatos na LAN/WAN. TURN real fica para Marco 4.
- Interfaces `ICEProvider`/`TURNProvider` permitem substituir configuração futuramente.

## IPC Go ↔ Helper Swift

- Unix domain socket em diretório seguro `0700`, path exposto em `/api/webrtc/status`.
- Framing binário: `[4 bytes length][1 byte type][payload]`.
- Handshake com nonce 16 bytes + HMAC-SHA256 do segredo compartilhado (no Keychain).
- Helper envia H264 NAL/access units e geometry; Go envia eventos input/clipboard.

## Marco 3 — Fechado

Transporte visual/controle local completo: WebRTC + DataChannels cifrados + IPC Go↔helper + STUN default. Pronto para uso local entre Mac e celular na mesma LAN.

## Marco 4.1 — Cloudflare Tunnel (scaffold)

- `internal/tunnel` com `Manager` real/fake (`Start`, `Stop`, `Status`).
- Default: nome `relay-diego`, hostname `relay.kbtech.com.br`, URL `http://127.0.0.1:24109`.
- Token via `RELAY_TUNNEL_TOKEN`, env ou preferência salva no Keychain.
- Erros claros quando `cloudflared` ou token estão ausentes.
- CLI `setup` grava preferências; `share` inicia tunnel se configurado; `status` expõe estado; `stop` encerra agente + tunnel.
- Testes unitários com runner fake; `go test ./...` e `-race` passam.

## Marco 4 — Próximos passos

- TURN real (`ShortLivedTURNProvider` já é stub).
- Adapter Codex.
- Transferência de arquivos e aceite real no iPhone.
- Rebrand visual/CLI de “Relay” → “Remote CliControl” quando autorizado.
