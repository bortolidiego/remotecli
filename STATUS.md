# Remote CliControl — ponto de parada

Atualizado em: 2026-07-20  
Sessão Codex de origem: `019f7c05-24b5-7a40-8909-61c17c41c07a`

## Nome

- **Nome oficial do app:** Remote CliControl  
- **Nome anterior no plano/código:** Relay  
- **Pasta atual:** `/Users/diegobortoli/Desktop/apps/relay`  
- **Hostname planejado:** `relay.kbtech.com.br` (pode ser revisado no rebrand)

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

## O que fica pro Marco 4

- TURN real (`ShortLivedTURNProvider` já existe como stub)
- Adapter Codex
- Transferência de arquivos/aceite real no iPhone
- Rebrand visual/CLI de “Relay” → “Remote CliControl” quando autorizado

## Bloqueios

- `swift test` pode falhar com `no such module XCTest` no ambiente de build sem Xcode/test framework; `swift build` passa. Não é bloqueio funcional.
- WebRTC real entre celular↔Mac depende de rota ICE (LAN/WAN). STUN default resolve a maioria dos casos de LAN; TURN será necessário para NAT simétrico restrito.
- Tunnel real ainda não foi ligado em produção: falta criar o tunnel no dashboard da Cloudflare e injetar o token.

## Observação da sessão

- Pedido “salva na memória” falhou no Codex por **cota de uso do Codex**, não por falha do app.
- Este arquivo é o registro local durável até a memória Hindsight gravar de novo.
