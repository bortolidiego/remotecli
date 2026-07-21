package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bortolidiego/relay/shared/contracts"
)

// handleSessionMessage — fire-and-forget: entrega rápida e resposta chega via /output.
func (a *Agent) handleSessionMessage(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text obrigatório"})
		return
	}

	sess, ok := a.registry.SessionByID(sessionID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "sessão não encontrada"})
		return
	}

	// Codex mantém ack rápido (já era assíncrono).
	if sess.CodexThreadID != nil && *sess.CodexThreadID != "" {
		threadID := *sess.CodexThreadID
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c, err := a.ensureCodexSession(threadID)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
			return
		}
		turnID, err := c.TurnStart(ctx, threadID, text)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"mode":    "codex_turn",
			"turn_id": turnID,
			"reply":   "",
		})
		return
	}

	reply, mode, err := a.deliverToSession(sess, text)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error(), "mode": mode})
		return
	}
	// Fire-and-forget: reply vem vazio; PWA poll /output para pegar a resposta real.
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"mode":   mode,
		"reply":  reply,
	})
}

// deliverToSession:
// 1) maestri ask síncrono (até 3s) → devolve a resposta se for rápida
// 2) se demorar/falhar: fire-and-forget (inject paste agora + outbox) e retorna imediatamente
// 3) sem nome: inject direto e retorna imediatamente
func (a *Agent) deliverToSession(sess contracts.SessionDescriptor, text string) (reply, mode string, err error) {
	name := strings.TrimSpace(sess.MaestriAgentName)
	cli := strings.TrimSpace(sess.MaestriCLI)
	if cli == "" {
		cli = "maestri"
	}
	socket := strings.TrimSpace(sess.MaestriSocket)

	// Sempre registra o alvo para o bridge poder espelhar.
	_ = RegisterWatchTarget(name, cli, socket)

	if name != "" {
		// Tentativa direta (rápida em workers) — no hot path não esperamos mais que 3s.
		out, runErr := runMaestriAsk(cli, socket, name, text, 3*time.Second)
		if runErr == nil {
			msg := cleanTerminalText(out)
			if msg == "" {
				msg = "Mensagem entregue. Continue no Mac se quiser."
			}
			_ = writeSnapshot(name, msg)
			return msg, "maestri_ask", nil
		}

		// Fire-and-forget: enfileira para o bridge e injecta no Mac AGORA.
		_, _ = enqueuePhoneBridge(name, text, cli, socket)
		_ = macOSFocusPasteAndReturn(text, name)

		return "", "delivered", nil
	}

	// Sem nome: fallback de inject direto.
	if injErr := macOSFocusPasteAndReturn(text, name); injErr != nil {
		return "", "local_inject", injErr
	}
	return "", "session_type", nil
}

// waitForReplyOrDelta espera até 90s por uma reply do bridge ou por um delta no terminal.
func waitForReplyOrDelta(jobID, name, cli, socket, baseline string, timeout time.Duration) (reply, delta string) {
	deadline := time.Now().Add(timeout)
	replyPath := filepath.Join(repliesPath(), jobID+".txt")
	last := baseline
	for time.Now().Before(deadline) {
		// Poll non-blocking no arquivo de reply.
		if b, err := os.ReadFile(replyPath); err == nil {
			_ = os.Remove(replyPath)
			return strings.TrimSpace(string(b)), ""
		}

		// Espelho: snapshot ou check direto.
		text := readSnapshot(name)
		if text == "" || text == last {
			if fresh, ok := snapshotNow(cli, socket, name, 4*time.Second); ok {
				text = fresh
			}
		}
		text = cleanTerminalText(text)
		if text != "" && text != last && !isNoConnection(text) {
			d := terminalDelta(baseline, text)
			if d == "" {
				d = text
			}
			return "", formatSnapshotReply(d)
		}
		if text != "" {
			last = text
		}
		time.Sleep(1500 * time.Millisecond)
	}
	return "", ""
}

// waitForTerminalDelta monitora snapshot/check até detectar mudança vs baseline.
func waitForTerminalDelta(name, cli, socket, baseline string, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	last := baseline
	for time.Now().Before(deadline) {
		text := readSnapshot(name)
		if text == "" || text == last {
			if fresh, ok := snapshotNow(cli, socket, name, 4*time.Second); ok {
				text = fresh
			}
		}
		text = cleanTerminalText(text)
		if text != "" && text != last && !isNoConnection(text) {
			delta := terminalDelta(baseline, text)
			if delta == "" {
				delta = text
			}
			return formatSnapshotReply(delta)
		}
		if text != "" {
			last = text
		}
		time.Sleep(2 * time.Second)
	}
	return ""
}

func runMaestriAsk(cli, socket, agentName, text string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli, "ask", agentName, text)
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
	out := strings.TrimSpace(stdout.String() + "\n" + stderr.String())
	return out, err
}

// relayHomeFn pode ser sobrescrito em testes para isolar o diretório de trabalho.
var relayHomeFn = defaultRelayHome

func defaultRelayHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".relay")
	}
	return filepath.Join(home, ".relay")
}

func relayHome() string { return relayHomeFn() }

func outboxPath() string  { return filepath.Join(relayHome(), "outbox") }
func repliesPath() string { return filepath.Join(relayHome(), "replies") }

type outboxJob struct {
	ID      string `json:"id"`
	Target  string `json:"target"`
	Text    string `json:"text"`
	CLI     string `json:"cli"`
	Socket  string `json:"socket"`
	Created string `json:"created"`
}

func enqueuePhoneBridge(target, text, cli, socket string) (jobID string, err error) {
	if err := os.MkdirAll(outboxPath(), 0o700); err != nil {
		return "", err
	}
	jobID = fmt.Sprintf("%d", time.Now().UnixNano())
	job := outboxJob{
		ID:      jobID,
		Target:  target,
		Text:    text,
		CLI:     cli,
		Socket:  socket,
		Created: time.Now().UTC().Format(time.RFC3339),
	}
	b, err := json.Marshal(job)
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%s.json", jobID, sanitizeFile(target))
	return jobID, os.WriteFile(filepath.Join(outboxPath(), name), b, 0o600)
}

func waitReply(jobID string, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	path := filepath.Join(repliesPath(), jobID+".txt")
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil {
			_ = os.Remove(path)
			return strings.TrimSpace(string(b))
		}
		time.Sleep(1500 * time.Millisecond)
	}
	return ""
}

func writeReply(jobID, text string) error {
	if err := os.MkdirAll(repliesPath(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(repliesPath(), jobID+".txt"), []byte(text), 0o600)
}

func sanitizeFile(s string) string {
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, s)
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

func macOSFocusPasteAndReturn(text, agentHint string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pbcopy: %w", err)
	}
	script := `
tell application "Maestri" to activate
delay 0.35
tell application "System Events"
  keystroke "v" using command down
  delay 0.12
  key code 36
end tell
`
	osa := exec.Command("osascript", "-e", script)
	var stderr bytes.Buffer
	osa.Stderr = &stderr
	if err := osa.Run(); err != nil {
		return fmt.Errorf("osascript: %w (%s). Libere Acessibilidade para o Terminal em Ajustes do Mac", err, strings.TrimSpace(stderr.String()))
	}
	_ = agentHint
	return nil
}

// handleSessionOutput — snapshot da sessão (maestri check) para o celular.
// Ordem: maestri check → snapshot → vazio. Sempre passa por cleanTerminalText.
func (a *Agent) handleSessionOutput(w http.ResponseWriter, r *http.Request, sessionID string) {
	sess, ok := a.registry.SessionByID(sessionID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "sessão não encontrada"})
		return
	}
	name := strings.TrimSpace(sess.MaestriAgentName)
	if name == "" {
		writeJSON(w, http.StatusOK, map[string]any{"text": "", "source": "none"})
		return
	}
	cli := strings.TrimSpace(sess.MaestriCLI)
	if cli == "" {
		cli = "maestri"
	}
	socket := strings.TrimSpace(sess.MaestriSocket)

	// 1) Tenta check direto.
	if out, err := runMaestriCheck(cli, socket, name, 10*time.Second); err == nil {
		clean := cleanTerminalText(out)
		if isUsefulSnapshot(clean) {
			_ = writeSnapshot(name, clean)
			writeJSON(w, http.StatusOK, map[string]any{
				"text":       clean,
				"source":     "maestri_check",
				"name":       name,
				"updated_at": snapshotTimestamp(name),
			})
			return
		}
	}

	// 2) Fallback para snapshot gravado pelo bridge.
	snap := cleanTerminalText(readSnapshot(name))
	if isUsefulSnapshot(snap) {
		writeJSON(w, http.StatusOK, map[string]any{
			"text":       snap,
			"source":     "snapshot",
			"name":       name,
			"updated_at": snapshotTimestamp(name),
		})
		return
	}

	// 3) Nada disponível.
	writeJSON(w, http.StatusOK, map[string]any{"text": "", "source": "none", "name": name})
}

// ProcessOutboxOnce — tick da ponte rcli-phone.
// A) entrega mensagens do celular nas sessões e grava replies (sem bloquear snapshot).
// B) atualiza snapshots dos alvos observados (watch-targets + jobs).
func ProcessOutboxOnce() (processed int, err error) {
	// --- A) Processa outbox sem travar o resto ------------------------------
	dir := outboxPath()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			entries = nil
		} else {
			return 0, err
		}
	}

	askJobs := make(chan outboxJob, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			continue
		}
		var job outboxJob
		if jerr := json.Unmarshal(b, &job); jerr != nil {
			_ = os.Remove(path)
			continue
		}
		cli := job.CLI
		if cli == "" {
			cli = "maestri"
		}
		// Garante que o alvo está sendo espelhado.
		_ = RegisterWatchTarget(job.Target, cli, job.Socket)
		askJobs <- job
	}
	close(askJobs)

	// Roda asks em goroutine para não bloquear o snapshot geral (timeout menor: 60s).
	go func() {
		for job := range askJobs {
			cli := job.CLI
			if cli == "" {
				cli = "maestri"
			}
			out, aerr := runMaestriAsk(cli, job.Socket, job.Target, job.Text, 60*time.Second)
			path := filepath.Join(dir, fmt.Sprintf("%s-%s.json", job.ID, sanitizeFile(job.Target)))
			if aerr != nil {
				// Mesmo sem reply, atualiza o snapshot do painel via check.
				_, _ = snapshotNow(cli, job.Socket, job.Target, 8*time.Second)
				meta, _ := os.Stat(path)
				if meta != nil && time.Since(meta.ModTime()) > 20*time.Minute {
					_ = os.Remove(path)
				}
				continue
			}
			clean := cleanTerminalText(out)
			if job.ID != "" {
				_ = writeReply(job.ID, clean)
			}
			_ = writeSnapshot(job.Target, clean)
			_ = os.Remove(path)
		}
	}()

	// --- B) Espelho contínuo de todos os alvos observados -------------------
	// Erros aqui não falham o tick — apenas logamos no stderr.
	if serr := snapshotAllTargets(8 * time.Second); serr != nil {
		fmt.Fprintf(os.Stderr, "snapshotAllTargets: %v\n", serr)
	}

	// Não retornamos contagem exata (ask async); callers só precisam saber que o tick rodou.
	return 0, nil
}
