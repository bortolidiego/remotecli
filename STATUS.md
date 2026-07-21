# Remote CliControl — ponto de parada

Atualizado em: 2026-07-20  
Sessão Codex de origem: `019f7c05-24b5-7a40-8909-61c17c41c07a`

## Nome

- **Nome oficial do app:** Remote CliControl  
- **Nome anterior no plano/código:** Relay  
- **Pasta atual:** `/Users/diegobortoli/Desktop/apps/relay`  
- **Hostname planejado:** `relay.kbtech.com.br` (pode ser revisado no rebrand)

> **Defaults de produto atualizados (2026-07-20):** os defaults antigos do Marco 4.1 — nome `relay-diego` e hostname `relay.kbtech.com.br` — não são mais hardcoded no binário. O `internal/tunnel` agora usa `remotecli` como nome padrão e nenhum hostname forçado; cada usuário define o próprio tunnel/token no Cloudflare. O `relay.kbtech.com.br` permanece apenas como planejamento futuro de serviço hosted.

## O que já está pronto

- **Marco 1 — base e segurança:** monorepo, agente Go local (`127.0.0.1:24109`), crypto (ECDSA/ECDH/HKDF/AES-GCM), pareamento QR, keychain, CLI, PWA embutida.
- **Marco 2 — PWA + agente + pareamento:** validado no fluxo da sessão Codex (aceite de Marco 2 concluído).
- **Marco 3 — FECHADO (transporte visual/controle local):**
  - WebRTC (Pion), DataChannels cifrados, IPC Go↔helper Swift
  - helper ScreenCaptureKit/VideoToolbox
  - endpoints de signaling autenticados por lease
  - STUN default (Cloudflare + Google) para descoberta de candidatos na LAN/WAN
  - README e testes atualizados
- **Marco 4.1 — Cloudflare Tunnel (scaffold):**
  - `internal/tunnel` com `Manager` real/fake (`Start`, `Stop`, `Status`)
  - defaults: nome `relay-diego`, hostname `relay.kbtech.com.br`, URL local `http://127.0.0.1:24109`
  - token via `RELAY_TUNNEL_TOKEN` ou preferência salva no Keychain
  - erros claros quando `cloudflared` ou token estão ausentes
  - CLI: `setup --tunnel-enabled` grava preferências; `share` inicia tunnel se configurado; `status` mostra tunnel; `stop` encerra agente + tunnel
  - testes unitários com runner fake; `go test ./...` e `go test -race ./internal/tunnel/...` passam
- **Marco 4.2 — Adapter Codex:**
  - `internal/codex`: JSON-RPC 2.0 client com transporte injetável (`stdio`, Unix socket) e `FakeTransport` para testes
  - métodos implementados: `initialize`, `thread/resume`, `turn/start`, `turn/interrupt`
  - aprovações `item/commandExecution/requestApproval` → `accept`/`decline` (MVP, sem `acceptForSession`)
  - eventos normalizados em `status`, `timeline`, `error`, `approval`; resume busy vira `waiting_local`
  - transporte real via `RELAY_CODEX_TRANSPORT` (`stdio` default, `socket` para `~/.codex/ipc/ipc.sock`)
  - endpoints de lease no agente: `/api/sessions/{id}/turn`, `/interrupt`, `/events`, `/approvals`, `/approvals/{id}`
  - PWA: envio/interrupção real, modal de aprovação com Permitir/Negar e polling de aprovações/eventos
  - CLI renomeado para `remotecli` (comandos `relay` e `here` principais)
  - `remotecli relay` auto-inicia agente em background e gera QR sem precisar `setup` primeiro
  - `remotecli here` registra o terminal atual na lista do celular sem gerar QR se já pareado
  - múltiplos CLIs no mesmo Mac aparecem como itens separados na PWA (1 QR por Mac, N terminais)
  - `remotecli share` é alias legado oculto
  - comando `remotecli serve` sobe agente em foreground; `make install` copia binário `remotecli` para `~/.local/bin` e mantém symlink `relay`
  - `go test ./...`, `go test -race ./...` e `npm test -- --run` passam

## O que fica pro Marco 4

- TURN real (`ShortLivedTURNProvider` já existe como stub)
- Transferência de arquivos/aceite real no iPhone
- Rebrand visual/CLI de “Relay” → “Remote CliControl” quando autorizado (parcial: CLI já renomeado para `remotecli`)

## Bloqueios

- `swift test` pode falhar com `no such module XCTest` no ambiente de build sem Xcode/test framework; `swift build` passa. Não é bloqueio funcional.
- WebRTC real entre celular↔Mac depende de rota ICE (LAN/WAN). STUN default resolve a maioria dos casos de LAN; TURN será necessário para NAT simétrico restrito.
- Tunnel real ainda não foi ligado em produção: falta criar o tunnel no dashboard da Cloudflare e injetar o token.

## Observação da sessão

- Pedido “salva na memória” falhou no Codex por **cota de uso do Codex**, não por falha do app.
- Este arquivo é o registro local durável até a memória Hindsight gravar de novo.

## Teste real (2026-07-20)

Rodado no Mac com agente local `127.0.0.1:24109` + Codex App Server real (`ipc.sock`) + thread `019f7c05-24b5-7a40-8909-61c17c41c07a`:

| Passo | Resultado |
|---|---|
| `remotecli setup` | OK — agente sobe, token no Keychain |
| `GET /health` | OK — sem metadados privados |
| `remotecli relay` | OK — QR URL para o celular escanear |
| `remotecli here` | OK — registra terminal sem QR se já pareado |
| `GET /api/status` sem token | 401 |
| Sessão com `codexThreadId` | OK |
| `POST .../turn` | OK — `thread/resume` + `turn/started` + `turn/completed` no app-server real |
| `turn_id` na resposta | OK após hotfix (parse `turn.id`) |
| `POST .../interrupt` | OK |
| PWA HTML em `/` | OK (200) |
| Pareamento WebCrypto no iPhone | não exercitado neste smoke (precisa do celular/QR) |
| Tunnel Cloudflare | não exercitado (sem token) |

Agente de smoke pode ser parado com `./relay stop` na sessão correspondente.

## Multi-CLI (2026-07-20)

- Registry Go armazena N sessões (`cliSessions`) com chave estável (`session_key || nativeSessionId`), sem misturar com dispositivos emparelhados.
- `POST /api/metadata` faz upsert; segundo `remotecli here` não apaga o primeiro.
- Título derivado: env `Title` > Codex > Maestri > basename(cwd) > nativeSessionId > "Terminal".
- TTL de 2h remove sessões sem heartbeat; após 5min sem update vira `offline`.
- PWA: lista sempre visível quando pareada (mesmo que 1 item), polling a cada 3s, badge de harness, cwd curto e empty state claro.
- Testes: `TestMetadataEndpointUpdatesSessionDescriptor` cobre 2 POSTs e upsert; `go test ./internal/pairing/... ./internal/agent/...` passa.

### Como testar

```sh
# terminal 1
remotecli relay          # QR se ainda não pareado
# terminal 2 (outro cwd ou MAESTRI_TERMINAL_ID)
remotecli here
# terminal 3
CODEX_THREAD_ID=fake remotecli here
# terminal 4 (sessionID diferente)
RELAY_SESSION_ID=cli-beta remotecli here
# validar via curl com local token (único por host):
TOKEN=$(security find-generic-password -s relay-local-token -a host -w)
curl -sk https://127.0.0.1:24109/api/sessions -H "X-Relay-Local-Token: $TOKEN"
```

PWA após parear: lista com N itens; tocar muda o detalhe.

### UX Digitar primeiro (2026-07-20)

- Tocar numa sessão entra no detalhe com ação primária **Digitar / Enviar**, não tela preta.
- Sessão Codex: aba "Chat" padrão — textarea grande, Enviar (startTurn), Parar, eventos recentes.
- Sessão native/Maestri: textarea "Escreva e envie pro terminal do Mac" → cola no Mac e aperta Enter via data channel.
- Aba "Tela" fica secundária, com placeholder honesto de vídeo em construção.
- Testes web passam; build gera novo `internal/web/dist`.

### Hotfix token único por host

- Token local agora usa conta fixa `host` no Keychain, não `host-{sessionID}`.
- Fallback: `host` → `host-{sessionID}` → `host-default`.
- Isso permite `RELAY_SESSION_ID=cli-alpha here` e `RELAY_SESSION_ID=cli-beta here` no mesmo agente sem erro "outra sessão".

## UX Janela-first (2026-07-20)

- Produto = qualquer CLI via **Tela** (não só Codex).
- UI: aba Tela padrão; Chat só se houver `codexThreadId`.
- Sem erro técnico de Codex na sessão default.
- WebRTC: host answerer + STUN no browser; vídeo de captura ainda incompleto (helper).
