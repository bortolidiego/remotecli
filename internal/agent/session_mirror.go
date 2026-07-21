package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const maxSnapshotLen = 8192

// watchTarget descreve uma sessão maestri que o bridge deve espelhar.
type watchTarget struct {
	Name   string `json:"name"`
	CLI    string `json:"cli"`
	Socket string `json:"socket"`
}

// snapshotDir retorna o diretório onde os snapshots de terminal são armazenados.
func snapshotDir() string {
	return filepath.Join(relayHome(), "snapshots")
}

func watchTargetsPath() string {
	return filepath.Join(relayHome(), "watch-targets.json")
}

// runMaestriCheck executa `maestri check <name>` com timeout e variáveis de ambiente.
func runMaestriCheck(cli, socket, name string, timeout time.Duration) (string, error) {
	if cli == "" {
		cli = "maestri"
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli, "check", name)
	env := os.Environ()
	if socket != "" {
		env = append(env, "MAESTRI_SOCKET="+socket)
	}
	if cli != "" {
		env = append(env, "MAESTRI_CLI="+cli)
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		out = strings.TrimSpace(stderr.String())
	}
	return out, err
}

// snapshotPath retorna o caminho do snapshot para um determinado alvo.
func snapshotPath(name string) string {
	return filepath.Join(snapshotDir(), sanitizeFile(name)+".txt")
}

// writeSnapshot grava o snapshot de terminal em disco.
func writeSnapshot(name, text string) error {
	if name == "" {
		return nil
	}
	if err := os.MkdirAll(snapshotDir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(snapshotPath(name), []byte(text), 0o600)
}

// readSnapshot lê o snapshot de terminal mais recente.
func readSnapshot(name string) string {
	if name == "" {
		return ""
	}
	b, err := os.ReadFile(snapshotPath(name))
	if err != nil {
		return ""
	}
	return string(b)
}

// cleanTerminalText remove ANSI, colapsa espaços e limpa ruído de TUI.
// Mantém no máximo ~8k caracteres, preferindo o final do texto.
func cleanTerminalText(s string) string {
	// Remove sequências ANSI.
	s = stripANSI(s)
	// Remove caracteres de controle exceto quebras de linha e tabulação.
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	// Remove caracteres de desenho de caixas comuns em TUIs.
	s = stripBoxDrawing(s)
	// Normaliza várias quebras de linha em no máximo duas.
	s = collapseNewlines(s)
	// Colapsa espaços em branco consecutivos.
	s = collapseSpaces(s)
	// Remove linhas que parecem puramente decorativas/restos de TUI.
	s = stripDecorativeLines(s)
	s = strings.TrimSpace(s)
	if len(s) > maxSnapshotLen {
		// Prefere o final do texto — é onde a ação recente costuma estar.
		s = s[len(s)-maxSnapshotLen:]
		// Corta para começar numa quebra de linha, se possível.
		if idx := strings.Index(s, "\n"); idx > 0 && idx < 200 {
			s = s[idx+1:]
		}
	}
	return strings.TrimSpace(s)
}

var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;:?]*[A-Za-z]`)

func stripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

func stripBoxDrawing(s string) string {
	// Conjunto amplo de caracteres de borda/caixa, setas e blocos usados por TUIs.
	box := []rune("─│┌┐└┘├┤┬┴┼═║╒╓╔╕╖╗╘╙╚╛╜╝╞╟╠╡╢╣╤╥╦╧╨╩╪╫╬▀▄█▌▐░▒▓■□▪▫▬►◄▲▼→←↑↓↔↕»«┏┓┗┛┣┫┳┻╋")
	return strings.Map(func(r rune) rune {
		for _, b := range box {
			if r == b {
				return -1
			}
		}
		return r
	}, s)
}

func collapseNewlines(s string) string {
	var out strings.Builder
	var newlineCount int
	for _, r := range s {
		if r == '\n' {
			newlineCount++
			if newlineCount <= 2 {
				out.WriteByte('\n')
			}
			continue
		}
		newlineCount = 0
		out.WriteRune(r)
	}
	return out.String()
}

func collapseSpaces(s string) string {
	var out strings.Builder
	var lastSpace bool
	for _, r := range s {
		if unicode.IsSpace(r) && r != '\n' && r != '\r' {
			if !lastSpace {
				out.WriteByte(' ')
				lastSpace = true
			}
			continue
		}
		lastSpace = false
		out.WriteRune(r)
	}
	return out.String()
}

func stripDecorativeLines(s string) string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Descarta linhas compostas apenas por ─/═/│/┃/┈/┉/━ repetidos.
		if isDecorativeLine(trimmed) {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func isDecorativeLine(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch r {
		case '─', '━', '═', '│', '┃', '┈', '┉', '┄', '┅', '░', '▒', '▓', '█', '▀', '▄', '▌', '▐':
			continue
		default:
			return false
		}
	}
	return true
}

// isNoConnection indica se o output do maestri check é "não conectado".
func isNoConnection(s string) bool {
	low := strings.ToLower(s)
	return strings.Contains(low, "no connection") || strings.Contains(low, "not connected")
}

// isUsefulSnapshot garante que o texto capturado não é apenas mensagem de ausência de conexão.
func isUsefulSnapshot(s string) bool {
	return s != "" && !isNoConnection(s)
}

// loadWatchTargets lê os alvos configurados para espelhamento.
func loadWatchTargets() ([]watchTarget, error) {
	path := watchTargetsPath()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var targets []watchTarget
	if err := json.Unmarshal(b, &targets); err != nil {
		return nil, err
	}
	return targets, nil
}

// saveWatchTargets persiste a lista de alvos observados.
func saveWatchTargets(targets []watchTarget) error {
	if err := os.MkdirAll(relayHome(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(targets, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(watchTargetsPath(), b, 0o600)
}

// mergeWatchTarget adiciona ou atualiza um alvo na lista, mergeando por nome.
func mergeWatchTarget(targets []watchTarget, t watchTarget) []watchTarget {
	t.Name = strings.TrimSpace(t.Name)
	t.CLI = strings.TrimSpace(t.CLI)
	t.Socket = strings.TrimSpace(t.Socket)
	if t.Name == "" {
		return targets
	}
	for i, cur := range targets {
		if cur.Name == t.Name {
			if t.CLI != "" {
				targets[i].CLI = t.CLI
			}
			if t.Socket != "" {
				targets[i].Socket = t.Socket
			}
			return targets
		}
	}
	return append(targets, t)
}

// RegisterWatchTarget registra (persiste) um alvo para o bridge espelhar.
func RegisterWatchTarget(name, cli, socket string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	targets, err := loadWatchTargets()
	if err != nil {
		targets = nil
	}
	targets = mergeWatchTarget(targets, watchTarget{Name: name, CLI: cli, Socket: socket})
	return saveWatchTargets(targets)
}

// snapshotNow captura o estado atual de um alvo e grava o snapshot.
// Retorna o texto limpo e um booleano indicando se foi útil.
func snapshotNow(cli, socket, name string, timeout time.Duration) (string, bool) {
	out, err := runMaestriCheck(cli, socket, name, timeout)
	if err != nil {
		return cleanTerminalText(out), false
	}
	clean := cleanTerminalText(out)
	if !isUsefulSnapshot(clean) {
		return clean, false
	}
	_ = writeSnapshot(name, clean)
	return clean, true
}

// snapshotTargetsFromJobs retorna os alvos únicos presentes nos jobs recentes da outbox.
func snapshotTargetsFromJobs() ([]watchTarget, error) {
	dir := outboxPath()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	seen := map[string]watchTarget{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var job outboxJob
		if err := json.Unmarshal(b, &job); err != nil {
			continue
		}
		name := strings.TrimSpace(job.Target)
		if name == "" {
			continue
		}
		key := name + "\x00" + job.CLI + "\x00" + job.Socket
		seen[key] = watchTarget{Name: name, CLI: job.CLI, Socket: job.Socket}
	}
	out := make([]watchTarget, 0, len(seen))
	for _, t := range seen {
		out = append(out, t)
	}
	return out, nil
}

// snapshotAllTargets itera sobre watch-targets e jobs recentes gravando snapshots.
func snapshotAllTargets(timeout time.Duration) error {
	targets, err := loadWatchTargets()
	if err != nil {
		targets = nil
	}
	jobTargets, err := snapshotTargetsFromJobs()
	if err != nil {
		jobTargets = nil
	}
	for _, t := range jobTargets {
		targets = mergeWatchTarget(targets, t)
	}
	cli := os.Getenv("MAESTRI_CLI")
	if cli == "" {
		cli = "maestri"
	}
	for _, t := range targets {
		c := t.CLI
		if c == "" {
			c = cli
		}
		_, _ = snapshotNow(c, t.Socket, t.Name, timeout)
	}
	return nil
}

// terminalDelta retorna o trecho de `now` que veio depois de `before`.
// Se não conseguir extrair delta, devolve o texto completo (limpo).
func terminalDelta(before, now string) string {
	before = cleanTerminalText(before)
	now = cleanTerminalText(now)
	if before == "" {
		return now
	}
	if now == "" {
		return ""
	}
	if now == before {
		return ""
	}
	if strings.HasPrefix(now, before) {
		delta := strings.TrimSpace(now[len(before):])
		if delta != "" {
			return delta
		}
	}
	// Se before estiver contido no meio, pega o que vem depois da última ocorrência.
	if idx := strings.LastIndex(now, before); idx >= 0 {
		rest := now[idx+len(before):]
		if rest = strings.TrimSpace(rest); rest != "" {
			return rest
		}
	}
	return now
}

// formatSnapshotReply monta uma resposta legível para o celular a partir de um snapshot.
func formatSnapshotReply(delta string) string {
	delta = strings.TrimSpace(delta)
	if delta == "" {
		return ""
	}
	return delta
}

// extractAssistantReply tenta extrair o parágrafo de resposta humana de uma saída TUI.
// Se não encontrar nada que pareça resposta, retorna "Resposta no Mac — toque Ver terminal se quiser o raw".
func extractAssistantReply(raw string) string {
	clean := cleanTerminalText(raw)
	if clean == "" {
		return "Resposta no Mac — toque Ver terminal se quiser o raw"
	}

	// Primeiro: procura por frases curtas que soam como resposta natural (ex. "Perfeito.", "Claro.")
	paragraphs := strings.Split(clean, "\n")
	var humanLines []string
	for _, p := range paragraphs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if isTUINoiseLine(p) {
			continue
		}
		if looksHumanReply(p) {
			humanLines = append(humanLines, p)
		}
	}
	if len(humanLines) > 0 {
		return strings.Join(humanLines, "\n")
	}

	// Fallback: remove linhas de status/hook e retorna o resto se houver algo substancial.
	var filtered []string
	for _, p := range paragraphs {
		p = strings.TrimSpace(p)
		if p == "" || isTUINoiseLine(p) {
			continue
		}
		filtered = append(filtered, p)
	}
	joined := strings.Join(filtered, "\n")
	if len(strings.Join(filtered, "")) > 20 {
		return joined
	}
	return "Resposta no Mac — toque Ver terminal se quiser o raw"
}

// isTUINoiseLine detecta linhas de ruído de TUI/hooks que não devem ir pro chat.
func isTUINoiseLine(s string) bool {
	lower := strings.ToLower(s)
	hookPrefixes := []string{
		"thought for", "user_prompt_submit", "shift+tab", "always-approve",
		"hooks:", "running hooks", "hook output", "token", "/", "~/.maestri",
		"model:", "usage:", "context:", "total tokens", "completion tokens",
	}
	for _, prefix := range hookPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	if strings.Contains(lower, "149k / 500k") {
		return true
	}
	if strings.Contains(lower, "hooks") && strings.Contains(lower, "ms") {
		return true
	}
	// Remove status de tokens tipo "149K / 500K".
	if tokenStatusPattern.MatchString(s) {
		return true
	}
	return false
}

var tokenStatusPattern = regexp.MustCompile(`\d+\s*[km]?\s*/\s*\d+\s*[km]?`)

// looksHumanReply identifica frases que provavelmente são resposta natural do agente.
func looksHumanReply(s string) bool {
	lower := strings.ToLower(s)
	// Frases curtas comuns de confirmação/resposta.
	humanStarters := []string{
		"perfeito", "claro", "ok", "tudo bem", "entendi", "show", "legal",
		"vou ", "vamos ", "pode ", "farei ", "feito", "pronto", "certo",
		"sugiro ", "recomendo ", "aqui está", "aqui estão", "segue", "anexo",
		"ótimo", "beleza", "blz", "sim", "não", "claro que", "sem problemas",
	}
	for _, starter := range humanStarters {
		if strings.HasPrefix(lower, starter) {
			return true
		}
	}
	// Se começa com artigo/pronome pessoal ou pergunta, provavelmente é humano.
	firstWord := strings.TrimRight(strings.Fields(s)[0], ",.!?")
	personal := []string{"eu", "você", "ele", "ela", "nós", "eles", "isso", "esse", "esta", "o", "a", "os", "as", "um", "uma", "me", "te", "se", "para", "por"}
	for _, w := range personal {
		if strings.EqualFold(firstWord, w) {
			return true
		}
	}
	// Pergunta normalmente é humano.
	if strings.HasSuffix(s, "?") {
		return true
	}
	// Se a linha tem muitos caracteres TUI ou parece caminho/status, não é humano.
	tuiRatio := countTUIRunes(s) / float64(len([]rune(s)))
	if tuiRatio > 0.15 {
		return false
	}
	return false
}

func countTUIRunes(s string) float64 {
	var count int
	for _, r := range s {
		switch r {
		case '─', '━', '═', '║', '│', '┃', '┌', '┐', '└', '┘', '├', '┤', '┬', '┴', '┼',
			'▀', '▄', '█', '▌', '▐', '░', '▒', '▓', '■', '□', '▪', '▫',
			'►', '◄', '▲', '▼', '→', '←', '↑', '↓':
			count++
		}
	}
	return float64(count)
}

// snapshotTimestamp retorna a hora da última modificação do snapshot, se existir.
func snapshotTimestamp(name string) string {
	info, err := os.Stat(snapshotPath(name))
	if err != nil {
		return ""
	}
	return info.ModTime().UTC().Format(time.RFC3339)
}

// ensurePhoneBridgeName é o nome canônico do agente ponte no Maestri.
const phoneBridgeAgent = "rcli-phone"

// writeSnapshotForSession grava o snapshot de uma sessão a partir do nome/configuração.
func writeSnapshotForSession(name, cli, socket, text string) error {
	if name == "" {
		return fmt.Errorf("nome da sessão vazio")
	}
	return writeSnapshot(name, cleanTerminalText(text))
}
