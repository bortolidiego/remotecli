package agent

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bortolidiego/relay/internal/codex"
	"github.com/bortolidiego/relay/shared/contracts"
)

// codexSession guarda estado de uma sessão Codex ativa no agente.
type codexSession struct {
	threadID string
	client   *codex.Client
	mu       sync.RWMutex
	closed   bool
}

func (a *Agent) initCodexManager() {
	a.codexMu.Lock()
	defer a.codexMu.Unlock()
	if a.codexManager != nil {
		return
	}
	a.codexManager = codex.NewManagerWithOptions(codex.TransportFactory, nil, true)
}

func (a *Agent) ensureCodexSession(threadID string) (*codex.Client, error) {
	a.codexMu.Lock()
	defer a.codexMu.Unlock()
	if a.codexManager == nil {
		return nil, errors.New("Codex não configurado")
	}
	return a.codexManager.Connect(context.Background(), threadID)
}

func (a *Agent) codexThreadIDFromSession(sessionID string) string {
	// O session_id do contrato Relay é o nativeSessionID; codexThreadID vem dos metadados.
	sess, ok := a.registry.SessionByID(sessionID)
	if !ok {
		return ""
	}
	if sess.CodexThreadID != nil {
		return *sess.CodexThreadID
	}
	return ""
}

func (a *Agent) registerCodexRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/sessions/", a.requireAuth(a.handleSessionRoot))
}

// handleSessionRoot despacha sub-rotas de /api/sessions/{id}/*.
func (a *Agent) handleSessionRoot(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	parts := strings.SplitN(path, "/", 3)
	if len(parts) == 0 || parts[0] == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session_id obrigatório"})
		return
	}
	sessionID := strings.TrimSpace(parts[0])
	if len(parts) == 1 || parts[1] == "" {
		// /api/sessions/{id}
		if r.Method == http.MethodGet {
			a.handleSessionDetail(w, r)
			return
		}
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}

	sub := parts[1]
	if sub == "turn" {
		if r.Method == http.MethodPost {
			a.handleSessionTurn(w, r, sessionID)
			return
		}
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	if sub == "interrupt" {
		if r.Method == http.MethodPost {
			a.handleSessionInterrupt(w, r, sessionID)
			return
		}
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	if sub == "events" {
		if r.Method == http.MethodGet {
			a.handleSessionEvents(w, r, sessionID)
			return
		}
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	if sub == "approvals" {
		if len(parts) == 2 {
			if r.Method == http.MethodGet {
				a.handleSessionApprovals(w, r, sessionID)
				return
			}
			http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
			return
		}
		approvalID := parts[2]
		if r.Method == http.MethodPost {
			a.handleSessionApprovalDecision(w, r, sessionID, approvalID)
			return
		}
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	if sub == "message" {
		if r.Method == http.MethodPost {
			a.handleSessionMessage(w, r, sessionID)
			return
		}
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	if sub == "output" {
		if r.Method == http.MethodGet {
			a.handleSessionOutput(w, r, sessionID)
			return
		}
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	if sub == "upload" || sub == "files" {
		// /api/sessions/{id}/upload  ou  /api/sessions/{id}/files/{fileId}
		extra := sub
		if sub == "files" && len(parts) == 3 && parts[2] != "" {
			extra = "files/" + parts[2]
		}
		a.handleUploadOrFiles(w, r, sessionID, extra)
		return
	}

	http.NotFound(w, r)
}

func (a *Agent) handleSessionTurn(w http.ResponseWriter, r *http.Request, sessionID string) {
	threadID := a.codexThreadIDFromSession(sessionID)
	if threadID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sessão não possui codexThreadId"})
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text obrigatório"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := a.ensureCodexSession(threadID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	turnID, err := c.TurnStart(ctx, threadID, req.Text)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"turn_id": turnID, "status": "busy"})
}

func (a *Agent) handleSessionInterrupt(w http.ResponseWriter, r *http.Request, sessionID string) {
	threadID := a.codexThreadIDFromSession(sessionID)
	if threadID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sessão não possui codexThreadId"})
		return
	}
	a.codexMu.RLock()
	mgr := a.codexManager
	a.codexMu.RUnlock()
	if mgr == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Codex não configurado"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := mgr.Interrupt(ctx, threadID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "interrupted"})
}

func (a *Agent) handleSessionEvents(w http.ResponseWriter, r *http.Request, sessionID string) {
	threadID := a.codexThreadIDFromSession(sessionID)
	if threadID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sessão não possui codexThreadId"})
		return
	}
	a.codexMu.RLock()
	mgr := a.codexManager
	a.codexMu.RUnlock()
	if mgr == nil {
		writeJSON(w, http.StatusOK, []codex.Event{})
		return
	}
	writeJSON(w, http.StatusOK, mgr.Events(threadID))
}

func (a *Agent) handleSessionApprovals(w http.ResponseWriter, r *http.Request, sessionID string) {
	threadID := a.codexThreadIDFromSession(sessionID)
	if threadID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sessão não possui codexThreadId"})
		return
	}
	a.codexMu.RLock()
	mgr := a.codexManager
	a.codexMu.RUnlock()
	if mgr == nil {
		writeJSON(w, http.StatusOK, []codex.Approval{})
		return
	}
	writeJSON(w, http.StatusOK, mgr.Approvals(threadID))
}

func (a *Agent) handleSessionApprovalDecision(w http.ResponseWriter, r *http.Request, sessionID, approvalID string) {
	threadID := a.codexThreadIDFromSession(sessionID)
	if threadID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sessão não possui codexThreadId"})
		return
	}
	var req struct {
		Decision string `json:"decision"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	decision, err := codex.ResolveDecision(req.Decision)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	a.codexMu.RLock()
	mgr := a.codexManager
	a.codexMu.RUnlock()
	if mgr == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Codex não configurado"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := mgr.DecideApproval(ctx, threadID, approvalID, decision); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": string(decision)})
}

// codexSessionDescriptorJSON mantém compatibilidade com contrato antigo.
func (a *Agent) codexSessionDescriptor() contracts.SessionDescriptor {
	return a.registry.Sessions()[0]
}
