package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// Client é o adapter JSON-RPC para o Codex app-server.
type Client struct {
	transport      Transport
	handler        ServerRequestHandler
	asyncApprovals bool
	mu             sync.RWMutex
	initialized    bool
	closed         bool
	pending        map[uint64]chan *jsonRPCMessage
	events         []Event
	approvals      []Approval
	threads        map[string]*ThreadInfo
	onEvent        func(Event)
	wg             sync.WaitGroup
}

// ServerRequestHandler processa requests do app-server (ex: aprovações).
type ServerRequestHandler interface {
	// HandleCommandExecutionRequestApproval decide uma aprovação. Deve retornar um result não-nil.
	HandleCommandExecutionRequestApproval(ctx context.Context, params CommandExecutionRequestApprovalParams) (any, error)
}

// StaticApprovalHandler decide aprovações com base em uma função fixa.
type StaticApprovalHandler struct {
	Decider func(ctx context.Context, params CommandExecutionRequestApprovalParams) (ApprovalDecision, error)
}

func (h *StaticApprovalHandler) HandleCommandExecutionRequestApproval(ctx context.Context, params CommandExecutionRequestApprovalParams) (any, error) {
	dec, err := h.Decider(ctx, params)
	if err != nil {
		return nil, err
	}
	return CommandExecutionRequestApprovalResponse{Decision: dec}, nil
}

// NewClient cria um client com transporte injetável.
// Se handler for nil, aprovações ficam pendentes até DecideApproval (modo async).
func NewClient(transport Transport, handler ServerRequestHandler) *Client {
	return NewClientWithOptions(transport, handler, AsyncApprovals)
}

// NewClientWithOptions cria client controlando auto-resposta de aprovações.
func NewClientWithOptions(transport Transport, handler ServerRequestHandler, asyncApprovals bool) *Client {
	return &Client{
		transport:      transport,
		handler:        handler,
		asyncApprovals: asyncApprovals,
		pending:        make(map[uint64]chan *jsonRPCMessage),
		threads:        make(map[string]*ThreadInfo),
	}
}

// SetEventHandler configura callback para eventos normalizados.
func (c *Client) SetEventHandler(fn func(Event)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onEvent = fn
}

// Initialize executa o handshake initialize/initialized.
func (c *Client) Initialize(ctx context.Context) error {
	if c.transport == nil {
		return errors.New("transporte não configurado")
	}
	if err := c.transport.Start(ctx, c.handleMessage); err != nil {
		return err
	}
	resp, err := c.request(ctx, "initialize", InitializeParams{
		ClientInfo:   ClientInfo{Name: "remote-clicontrol", Version: "0.1.0"},
		Capabilities: InitializeCapabilities{ExperimentalAPI: true},
	})
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("initialize error: %s", resp.Error.Message)
	}
	// Algumas implementações exigem a notificação initialized após o handshake.
	if err := c.notify(ctx, "initialized", struct{}{}); err != nil {
		return err
	}
	c.mu.Lock()
	c.initialized = true
	c.mu.Unlock()
	return nil
}

// ThreadResume inicia/retoma uma thread.
func (c *Client) ThreadResume(ctx context.Context, threadID string) error {
	_, err := c.request(ctx, "thread/resume", ThreadResumeParams{ThreadID: threadID})
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.threads[threadID] = &ThreadInfo{ThreadID: threadID, Status: "resumed"}
	c.mu.Unlock()
	c.emit(Event{
		ID:       threadID,
		ThreadID: threadID,
		Kind:     EventKindStatus,
		Method:   "thread/resume",
		Status:   "resumed",
		CreatedAt: time.Now(),
	})
	return nil
}

// TurnStart envia uma mensagem de texto como novo turno.
func (c *Client) TurnStart(ctx context.Context, threadID, text string) (string, error) {
	resp, err := c.request(ctx, "turn/start", TurnStartParams{
		ThreadID: threadID,
		Input:    []UserInput{{Type: "text", Text: text}},
	})
	if err != nil {
		return "", err
	}
	turnID := ""
	if resp.Result != nil {
		var result struct {
			TurnID string `json:"turnId"`
		}
		if err := json.Unmarshal(resp.Result, &result); err == nil {
			turnID = result.TurnID
		}
	}
	c.mu.Lock()
	info := c.threads[threadID]
	if info == nil {
		info = &ThreadInfo{ThreadID: threadID}
		c.threads[threadID] = info
	}
	info.TurnID = turnID
	info.Status = "busy"
	c.mu.Unlock()
	c.emit(Event{
		ID:        turnID,
		ThreadID:  threadID,
		TurnID:    turnID,
		Kind:      EventKindStatus,
		Method:    "turn/start",
		Status:    "busy",
		Text:      text,
		CreatedAt: time.Now(),
	})
	return turnID, nil
}

// TurnInterrupt interrompe o turno atual.
func (c *Client) TurnInterrupt(ctx context.Context, threadID, turnID string) error {
	_, err := c.request(ctx, "turn/interrupt", TurnInterruptParams{ThreadID: threadID, TurnID: turnID})
	if err != nil {
		return err
	}
	c.mu.Lock()
	if info := c.threads[threadID]; info != nil {
		info.Status = "interrupted"
	}
	c.mu.Unlock()
	c.emit(Event{
		ThreadID:  threadID,
		TurnID:    turnID,
		Kind:      EventKindStatus,
		Method:    "turn/interrupt",
		Status:    "interrupted",
		CreatedAt: time.Now(),
	})
	return nil
}

// Running indica se o client ainda está ativo.
func (c *Client) Running() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return !c.closed
}

// Close finaliza o client e o transporte.
func (c *Client) Close(ctx context.Context) error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	if c.transport != nil {
		return c.transport.Stop(ctx)
	}
	return nil
}

// Events retorna cópia dos eventos acumulados.
func (c *Client) Events() []Event {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Event, len(c.events))
	copy(out, c.events)
	return out
}

// Approvals retorna cópia das aprovações pendentes.
func (c *Client) Approvals() []Approval {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Approval, len(c.approvals))
	copy(out, c.approvals)
	return out
}

// ResolveDecision converte string externa em ApprovalDecision normalizada.
func ResolveDecision(decision string) (ApprovalDecision, error) {
	switch decision {
	case "accept", "approve", "approved":
		return DecisionAccept, nil
	case "decline", "deny", "denied", "cancel":
		return DecisionDecline, nil
	default:
		return "", fmt.Errorf("decisão inválida: %q", decision)
	}
}

// DecideApproval responde uma aprovação pendente.
func (c *Client) DecideApproval(ctx context.Context, id string, decision ApprovalDecision) error {
	c.mu.Lock()
	var approval *Approval
	for i := range c.approvals {
		if c.approvals[i].ID == id {
			approval = &c.approvals[i]
			c.approvals = append(c.approvals[:i], c.approvals[i+1:]...)
			break
		}
	}
	c.mu.Unlock()
	if approval == nil {
		return errors.New("aprovação não encontrada")
	}
	return c.respondServerRequest(ctx, approval.ID, CommandExecutionRequestApprovalResponse{Decision: decision})
}

// respondServerRequest envia uma resposta para um request do app-server.
func (c *Client) respondServerRequest(ctx context.Context, requestID string, result any) error {
	id, err := parseRequestID(requestID)
	if err != nil {
		// requests vêm com id serializado no JSON; para nosso fake e normalização usamos string.
		id = 0
	}
	data, err := newResponse(id, result)
	if err != nil {
		return err
	}
	return c.transport.Send(ctx, data)
}

func parseRequestID(s string) (uint64, error) {
	var id uint64
	_, err := fmt.Sscanf(s, "%d", &id)
	return id, err
}

func (c *Client) request(ctx context.Context, method string, params any) (*jsonRPCMessage, error) {
	id := nextID()
	data, err := newRequest(id, method, params)
	if err != nil {
		return nil, err
	}
	ch := make(chan *jsonRPCMessage, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if err := c.transport.Send(ctx, data); err != nil {
		return nil, err
	}
	select {
	case resp := <-ch:
		if resp == nil {
			return nil, errors.New("resposta nula")
		}
		if resp.Error != nil {
			return resp, fmt.Errorf("json-rpc error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		return nil, errors.New("timeout aguardando resposta json-rpc")
	}
}

func (c *Client) notify(ctx context.Context, method string, params any) error {
	data, err := newNotification(method, params)
	if err != nil {
		return err
	}
	return c.transport.Send(ctx, data)
}

func (c *Client) handleMessage(raw []byte) {
	var msg jsonRPCMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		c.emit(Event{
			Kind:      EventKindError,
			Text:      fmt.Sprintf("parse: %v", err),
			CreatedAt: time.Now(),
		})
		return
	}
	if msg.Method != "" {
		// request ou notificação do servidor
		c.handleServerMessage(msg, raw)
		return
	}
	c.mu.Lock()
	ch, ok := c.pending[msg.ID]
	c.mu.Unlock()
	if ok {
		ch <- &msg
		return
	}
	// resposta sem request pendente: normaliza como evento
	c.normalizeAndEmit(msg, raw)
}

func (c *Client) handleServerMessage(msg jsonRPCMessage, raw []byte) {
	switch msg.Method {
	case "item/commandExecution/requestApproval":
		var params CommandExecutionRequestApprovalParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			_ = c.respondServerRequest(context.Background(), formatRequestID(msg.ID), CommandExecutionRequestApprovalResponse{Decision: DecisionDecline})
			c.emit(Event{
				Kind:      EventKindError,
				Text:      fmt.Sprintf("approval parse: %v", err),
				CreatedAt: time.Now(),
			})
			return
		}
		approval := Approval{
			ID:          formatRequestID(msg.ID),
			ThreadID:    params.ThreadID,
			TurnID:      params.TurnID,
			ItemID:      params.ItemID,
			ApprovalID:  params.ApprovalID,
			Command:     params.Command,
			Cwd:         params.Cwd,
			Reason:      params.Reason,
			CreatedAt:   time.Now(),
			StartedAtMs: params.StartedAtMs,
		}
		c.mu.Lock()
		c.approvals = append(c.approvals, approval)
		c.mu.Unlock()
		c.emit(Event{
			ID:        approval.ID,
			ThreadID:  approval.ThreadID,
			TurnID:    approval.TurnID,
			Kind:      EventKindApproval,
			Method:    msg.Method,
			Payload:   msg.Params,
			Text:      params.Command,
			CreatedAt: approval.CreatedAt,
		})
		if !c.asyncApprovals && c.handler != nil {
			go func() {
				result, err := c.handler.HandleCommandExecutionRequestApproval(context.Background(), params)
				if err != nil {
					result = CommandExecutionRequestApprovalResponse{Decision: DecisionDecline}
				}
				if err := c.respondServerRequest(context.Background(), approval.ID, result); err != nil {
					log.Printf("codex approval response error: %v", err)
				}
				c.mu.Lock()
				for i, a := range c.approvals {
					if a.ID == approval.ID {
						c.approvals = append(c.approvals[:i], c.approvals[i+1:]...)
						break
					}
				}
				c.mu.Unlock()
			}()
		}
	default:
		c.normalizeAndEmit(msg, raw)
	}
}

func (c *Client) normalizeAndEmit(msg jsonRPCMessage, raw []byte) {
	e := Event{
		ID:        formatRequestID(msg.ID),
		Method:    msg.Method,
		Payload:   raw,
		CreatedAt: time.Now(),
	}
	switch {
	case msg.Method == "turn/started", msg.Method == "turn/completed", msg.Method == "turn/interrupted":
		e.Kind = EventKindStatus
		var p struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		e.ThreadID = p.ThreadID
		e.TurnID = p.TurnID
		// Normalização: se resumir uma thread busy, passa a waiting_local.
		c.mu.Lock()
		info := c.threads[p.ThreadID]
		if info != nil && info.Status == "busy" && msg.Method == "thread/resume" {
			info.Status = "waiting_local"
			e.Status = "waiting_local"
		}
		c.mu.Unlock()
		if e.Status == "" {
			switch msg.Method {
			case "turn/started":
				e.Status = "busy"
			case "turn/completed":
				e.Status = "completed"
			case "turn/interrupted":
				e.Status = "interrupted"
			}
		}
	case containsAny(msg.Method, "item/", "agent/"):
		e.Kind = EventKindTimeline
	case msg.Error != nil:
		e.Kind = EventKindError
		e.Text = msg.Error.Message
	default:
		e.Kind = EventKindTimeline
	}
	c.emit(e)
}

func (c *Client) emit(e Event) {
	c.mu.Lock()
	c.events = append(c.events, e)
	onEvent := c.onEvent
	c.mu.Unlock()
	if onEvent != nil {
		go onEvent(e)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if contains(s, sub) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && s[:len(sub)] == sub || len(s) > len(sub) && findSub(s, sub)
}

func findSub(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func formatRequestID(id uint64) string {
	return fmt.Sprintf("%d", id)
}

// NewDefaultClient cria um client usando transporte real a partir de RELAY_CODEX_TRANSPORT.
func NewDefaultClient(ctx context.Context, handler ServerRequestHandler) (*Client, error) {
	transport, err := TransportFactory(ctx)
	if err != nil {
		return nil, err
	}
	c := NewClient(transport, handler)
	return c, nil
}

func init() {
	// Evita log padrão poluindo; descomente se quiser debug.
	log.SetOutput(os.Stderr)
}
