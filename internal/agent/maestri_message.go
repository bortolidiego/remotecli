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

// handleSessionMessage — autonomia tipo app Claude (ida e volta).
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

	// Codex
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
			"reply":   "Mensagem enviada ao Codex. Acompanhe a resposta no Mac ou aguarde o espelho.",
		})
		return
	}

	reply, mode, err := a.deliverToSession(sess, text)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error(), "mode": mode})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"mode":   mode,
		"reply":  reply,
	})
}

// deliverToSession:
// 1) maestri ask síncrono (workers) → devolve a resposta
// 2) se self/sem conexão: fila + espera resposta do bridge (ida e volta)
// 3) fallback: digita no Mac
func (a *Agent) deliverToSession(sess contracts.SessionDescriptor, text string) (reply, mode string, err error) {
	name := strings.TrimSpace(sess.MaestriAgentName)
	cli := strings.TrimSpace(sess.MaestriCLI)
	if cli == "" {
		cli = "maestri"
	}
	socket := strings.TrimSpace(sess.MaestriSocket)

	if name != "" && socket != "" {
		// Tentativa direta (rápida em workers)
		out, runErr := runMaestriAsk(cli, socket, name, text, 20*time.Second)
		if runErr == nil {
			msg := strings.TrimSpace(out)
			if msg == "" {
				msg = "Mensagem entregue. Continue no Mac se quiser."
			}
			return msg, "maestri_ask", nil
		}

		// Self / sem conexão: bridge faz o ask e grava a resposta
		jobID, qErr := enqueuePhoneBridge(name, text, cli, socket)
		if qErr == nil {
			// Espera o bridge (rcli-phone) entregar e gravar reply — até 90s
			if rep := waitReply(jobID, 90*time.Second); rep != "" {
				return rep, "maestri_ask", nil
			}
			// Bridge lento/ausente: ainda tenta digitar no Mac (entrega a mensagem)
			_ = macOSFocusPasteAndReturn(text, name)
			return "Mensagem enviada à sessão. A resposta completa pode demorar — toque ↻ ou veja no Mac.", "phone_bridge", nil
		}
	}

	if injErr := macOSFocusPasteAndReturn(text, name); injErr != nil {
		return "", "local_inject", injErr
	}
	return "Mensagem digitada no Mac. A resposta aparece no Mac; o espelho no celular melhora com o bridge rcli-phone ativo.", "session_type", nil
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

func relayHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".relay")
	}
	return filepath.Join(home, ".relay")
}

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

	// Preferência: check via bridge (consegue ler o orquestrador)
	if text := checkViaBridge(cli, socket, name); text != "" {
		writeJSON(w, http.StatusOK, map[string]any{"text": text, "source": "bridge_check", "name": name})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli, "check", name)
	env := os.Environ()
	if socket != "" {
		env = append(env, "MAESTRI_SOCKET="+socket)
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	_ = cmd.Run()
	text := strings.TrimSpace(stdout.String())
	if text == "" {
		text = strings.TrimSpace(stderr.String())
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": text, "source": "maestri_check", "name": name})
}

func checkViaBridge(cli, socket, target string) string {
	// rcli-phone faz check do target e grava em arquivo
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	outFile := filepath.Join(relayHome(), "last-check-"+sanitizeFile(target)+".txt")
	_ = os.Remove(outFile)
	// pede ao bridge para rodar check (via ask curto — se bridge for shell, --raw é melhor)
	// Aqui só lemos last-check se o bridge já tiver preenchido.
	b, err := os.ReadFile(outFile)
	if err == nil {
		return strings.TrimSpace(string(b))
	}
	_ = ctx
	_ = cli
	_ = socket
	return ""
}

// ProcessOutboxOnce — rcli-phone: entrega e grava reply pra o celular ler.
func ProcessOutboxOnce() (processed int, err error) {
	dir := outboxPath()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
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
		// Espera a resposta completa do agente (até 3 min) — é o “espelho” no celular
		out, aerr := runMaestriAsk(cli, job.Socket, job.Target, job.Text, 3*time.Minute)
		if aerr != nil {
			meta, _ := os.Stat(path)
			if meta != nil && time.Since(meta.ModTime()) > 20*time.Minute {
				_ = os.Remove(path)
			}
			continue
		}
		if job.ID != "" {
			_ = writeReply(job.ID, out)
		}
		_ = os.Remove(path)
		processed++
	}
	return processed, nil
}
