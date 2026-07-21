# Remote CliControl (`remotecli`)

Controle remoto de CLIs e agentes no Mac a partir do celular (mesma Wi‑Fi).

**1 QR por Mac**, vários terminais na lista do iPhone. Parecido com “continuar a sessão no telefone” — ainda em evolução (vídeo da tela e ida-e-volta de chat).

| | |
|---|---|
| **CLI** | `remotecli` |
| **Plataforma** | macOS (desenvolvimento em arm64) |
| **Licença** | [GPL-3.0](./LICENSE) |
| **Pasta legada** | o repositório ainda se chama `relay` em alguns paths internos |

---

## O que faz

1. Sobe um **agente local** no Mac (HTTPS na LAN, porta `24109`).
2. Gera **QR** para o iPhone parear (certificado autoassinado — o Safari pede confiança uma vez).
3. Com `remotecli here`, cada terminal **aparece na lista** do celular.
4. No celular você escolhe a sessão e **envia mensagens** (Maestri/Codex/terminal — conforme o que estiver rodando).

---

## Requisitos

- macOS
- Go 1.22+ (build)
- Node 20+ (PWA)
- (Opcional) Xcode / Swift para o helper de captura
- Celular na **mesma Wi‑Fi** do Mac (sem tunnel Cloudflare por padrão)

---

## Instalação (desenvolvimento)

```bash
git clone https://github.com/bortolidiego/remotecli.git
cd remotecli

# Frontend embutido no binário
make web-build   # ou: cd apps/web && npm ci && npm run build

# Binário
make go-build    # gera ./remotecli (conforme Makefile)
make install     # opcional: copia para ~/.local/bin e symlink relay
```

Confira se `~/.local/bin` está no `PATH`.

---

## Uso rápido

```bash
# 1) No Mac — liga o serviço e mostra QR (se ainda não pareou)
remotecli relay

# Forçar novo QR
remotecli relay --force-qr

# 2) Em cada terminal que quiser no celular
remotecli here

# 3) No iPhone: abra o link do QR (https://<IP-do-Mac>:24109/?c=…)
#    Aceite o certificado → Parear → escolha a sessão
```

Outros comandos úteis:

```bash
remotecli status
remotecli devices
remotecli stop
remotecli --help
```

---

## Modos de acesso (LAN / tunnel / hosted)

O `remotecli` pode ser alcançado de 3 formas. O padrão é **LAN**: nenhuma conta nem configuração extra.

| Modo | Quando usar | Requisito |
|---|---|---|
| **LAN** (padrão) | iPhone na mesma Wi‑Fi do Mac | nenhum |
| **tunnel** | Fora da Wi‑Fi, via Cloudflare Tunnel do próprio usuário | conta Cloudflare + token + `cloudflared` no PATH |
| **hosted** | Serviço central relay (roadmap) | URL do serviço — ainda não disponível |

### Comandos

```bash
# Ver modo atual
remotecli access

# Só Wi‑Fi local (zero config) — padrão seguro
remotecli access lan

# Cloudflare Tunnel do usuário
remotecli access tunnel --token SEU_TOKEN [--hostname seu.dominio]

# Serviço central (roadmap; salva preferência, mas ainda não conecta)
remotecli access hosted --hosted-url https://relay.seudominio.com
```

Depois de configurar tunnel, `remotecli relay` sobe o agente **e** o tunnel. O token pode vir da env `REMOTECLI_TUNNEL_TOKEN` (útil para scripts), mas `--token` é mais explícito.

### Segurança do tunnel

- O token do Cloudflare é **seu**, do seu dashboard. Nada é roteado por servidores nossos.
- O token fica salvo no Keychain do Mac (conta de acesso da máquina).
- Instale o connector: `brew install cloudflared`.

---

## Estrutura do repo

```text
apps/web              PWA (React + Vite + TypeScript)
cmd/relay             CLI Go (remotecli)
internal/             agente, pairing, WebRTC, codex, tunnel, …
shared/contracts      contratos compartilhados
helper/RelayHelper    helper Swift (menu bar / captura — WIP)
```

---

## Testes

```bash
make go-test
make web-test
# opcional
make race
make swift-build
```

---

## Segurança (importante)

- O agente escuta na **rede local**. Use só em Wi‑Fi de confiança.
- Certificado **autoassinado** — o navegador do iPhone vai avisar; isso é esperado em LAN.
- **Não** commite tokens, `.env`, certificados de produção, nem dados de `~/.relay/`.
- Pareamento e lease são por dispositivo; use `remotecli devices` / revogar se precisar.

---

## Status do produto (honesto)

| Área | Estado |
|---|---|
| Pareamento QR + LAN HTTPS | Funciona |
| Multi-sessão (`here`) | Funciona |
| UI celular (lista + digitar) | MVP |
| Chat ida-e-volta (espelhar resposta no celular) | Em evolução |
| Vídeo da tela do Mac | Incompleto |
| Tunnel Cloudflare (fora da Wi‑Fi) | Scaffold / opcional |

Detalhes de desenvolvimento interno podem estar em `STATUS.md` (pode estar defasado em relação ao código).

---

## Contribuindo

1. Fork o repositório  
2. Crie uma branch: `git checkout -b feat/minha-ideia`  
3. Faça commits claros  
4. Abra um **Pull Request** descrevendo o que mudou e como testar  

Sugestões bem-vindas: multi-CLI, UX mobile, helper de captura, docs, testes.

Por enquanto não há `CONTRIBUTING.md` formal — PRs pequenos e testáveis ajudam a revisar mais rápido.

---

## Licença

Distribuído sob a **GNU General Public License v3.0 (GPL-3.0)**.

Isso significa, em resumo:

- você pode usar, estudar, modificar e redistribuir;
- se **distribuir** uma versão modificada (ou um programa que incorpore este código de forma que a GPL se aplique), em geral precisa **abrir o código** sob GPL-3.0 também;
- o software é oferecido **sem garantias**.

Texto completo: [LICENSE](./LICENSE).

---

## Aviso

Software experimental (“as is”). Use por sua conta e risco. Não envie dados sensíveis por redes não confiáveis.
