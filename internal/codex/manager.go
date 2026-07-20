package codex

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Manager gerencia clients Codex por threadId.
type Manager struct {
	mu      sync.RWMutex
	clients map[string]*Client
	factory TransportFactoryFunc
	handler ServerRequestHandler
	async   bool
}

// TransportFactoryFunc cria um transporte para uma thread.
type TransportFactoryFunc func(ctx context.Context) (Transport, error)

// NewManager cria gerenciador com factory e handler injetáveis (async aprovações por padrão).
func NewManager(factory TransportFactoryFunc, handler ServerRequestHandler) *Manager {
	return NewManagerWithOptions(factory, handler, true)
}

// NewManagerWithOptions permite controlar se aprovações são async.
func NewManagerWithOptions(factory TransportFactoryFunc, handler ServerRequestHandler, asyncApprovals bool) *Manager {
	return &Manager{
		clients: make(map[string]*Client),
		factory: factory,
		handler: handler,
		async:   asyncApprovals,
	}
}

// Connect inicializa o client para uma thread.
func (m *Manager) Connect(ctx context.Context, threadID string) (*Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.clients[threadID]; ok && c.Running() {
		return c, nil
	}
	transport, err := m.factory(ctx)
	if err != nil {
		return nil, fmt.Errorf("codex transport: %w", err)
	}
	c := NewClientWithOptions(transport, m.handler, m.async)
	if err := c.Initialize(ctx); err != nil {
		return nil, err
	}
	if err := c.ThreadResume(ctx, threadID); err != nil {
		return nil, err
	}
	m.clients[threadID] = c
	c.SetEventHandler(func(e Event) {
		// eventos já estão no client; callback vazio para evitar nil
	})
	return c, nil
}

// Get retorna client existente.
func (m *Manager) Get(threadID string) (*Client, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.clients[threadID]
	return c, ok
}

// StartTurn inicia um turno em uma thread.
func (m *Manager) StartTurn(ctx context.Context, threadID, text string) (string, error) {
	c, err := m.Connect(ctx, threadID)
	if err != nil {
		return "", err
	}
	return c.TurnStart(ctx, threadID, text)
}

// Interrupt interrompe o turno atual.
func (m *Manager) Interrupt(ctx context.Context, threadID string) error {
	c, ok := m.Get(threadID)
	if !ok {
		return errors.New("thread não conectada")
	}
	info := c.ThreadInfo(threadID)
	if info == nil {
		return c.TurnInterrupt(ctx, threadID, "")
	}
	return c.TurnInterrupt(ctx, threadID, info.TurnID)
}

// Events retorna eventos de uma thread.
func (m *Manager) Events(threadID string) []Event {
	c, ok := m.Get(threadID)
	if !ok {
		return nil
	}
	var out []Event
	for _, e := range c.Events() {
		if e.ThreadID == threadID {
			out = append(out, e)
		}
	}
	return out
}

// Approvals retorna aprovações pendentes de uma thread.
func (m *Manager) Approvals(threadID string) []Approval {
	c, ok := m.Get(threadID)
	if !ok {
		return nil
	}
	var out []Approval
	for _, a := range c.Approvals() {
		if a.ThreadID == threadID {
			out = append(out, a)
		}
	}
	return out
}

// DecideApproval decide uma aprovação de uma thread.
func (m *Manager) DecideApproval(ctx context.Context, threadID, approvalID string, decision ApprovalDecision) error {
	c, ok := m.Get(threadID)
	if !ok {
		return errors.New("thread não conectada")
	}
	return c.DecideApproval(ctx, approvalID, decision)
}

// Close encerra todos os clients.
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var firstErr error
	for _, c := range m.clients {
		if err := c.Close(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	m.clients = make(map[string]*Client)
	return firstErr
}

// ThreadInfo expõe estado da thread no client.
func (c *Client) ThreadInfo(threadID string) *ThreadInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.threads[threadID]
}
