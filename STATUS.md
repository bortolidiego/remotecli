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

## O que fica pro Marco 4

- Cloudflare Tunnel
- TURN real (`ShortLivedTURNProvider` já existe como stub)
- Adapter Codex
- Transferência de arquivos/aceite real no iPhone
- Rebrand visual/CLI de “Relay” → “Remote CliControl” quando autorizado

## Bloqueios

- `swift test` pode falhar com `no such module XCTest` no ambiente de build sem Xcode/test framework; `swift build` passa. Não é bloqueio funcional.
- WebRTC real entre celular↔Mac depende de rota ICE (LAN/WAN). STUN default resolve a maioria dos casos de LAN; TURN será necessário para NAT simétrico restrito.

## Observação da sessão

- Pedido “salva na memória” falhou no Codex por **cota de uso do Codex**, não por falha do app.
- Este arquivo é o registro local durável até a memória Hindsight gravar de novo.
