# Contribuindo

Obrigado por considerar contribuir com o **Remote CliControl**.

## Como abrir um PR

1. Fork + clone  
2. Branch a partir de `main`  
3. Implemente + teste o que puder (`make go-test`, `make web-test`)  
4. PR com:
   - o que mudou (em 2–5 linhas)
   - como testou
   - prints/GIFs se for UI

## O que evitar

- Secrets, `.env`, certificados, tokens  
- Binários (`remotecli`, `relay`)  
- PNGs de QR gerados localmente  
- Refactors gigantes sem contexto  

## Código

- Go: código e testes perto do pacote alterado  
- Web: `apps/web` — rode `npm test` e `npm run build`  
- Commits: mensagens claras (pode usar Conventional Commits se quiser)

## Comunicação

Issues e PRs em português ou inglês estão ok.

## Licença das contribuições

Ao abrir um PR, você concorda em licenciar sua contribuição sob a **GPL-3.0**, a mesma licença do projeto.
