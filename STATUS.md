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
- **Marco 3 — parcial (vídeo/controle local):**
  - WebRTC (Pion), DataChannels cifrados, IPC Go↔helper Swift
  - helper ScreenCaptureKit/VideoToolbox
  - endpoints de signaling
  - README já descreve o Marco 3

## O que falta / bloqueios

1. **Git ainda não foi inicializado** na pasta do app (sem commits).
2. **Marco 3 incompleto:** sessão Codex registrou bloqueio em **ICE/Pion** e pediu correção dos testes WebRTC (hoje os testes unitários de `internal/webrtc` e `internal/agent` passam em smoke local — revalidar se o bloqueio era de integração real, não unitário).
3. **Fora do Marco 3 ainda:**
   - Cloudflare Tunnel
   - TURN real (só stub)
   - adapter Codex / arquivos / aceite real no iPhone

## Próximos passos recomendados (ordem)

1. Inicializar git e fazer checkpoint do estado atual (Marco 2 + Marco 3 parcial).
2. Fechar Marco 3: ICE/Pion estável + testes de integração WebRTC.
3. Só depois: Tunnel + TURN + adapter Codex + aceite real no celular.
4. Rebrand visual/CLI de “Relay” → “Remote CliControl” quando o Diego autorizar (pasta/repo/hostname).

## Observação da sessão

- Pedido “salva na memória” falhou no Codex por **cota de uso do Codex**, não por falha do app.
- Este arquivo é o registro local durável até a memória Hindsight gravar de novo.
