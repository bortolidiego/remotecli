package codex

import (
	"encoding/json"
	"time"
)

// HarnessSource usado no contrato Relay para sessões Codex.
const HarnessSource = "codex"

// AsyncApprovals é uma opção de Client para enfileirar aprovações sem auto-responder.
const AsyncApprovals = true

// ClientInfo identifica este adapter no handshake initialize.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeCapabilities são as capabilities declaradas ao app-server.
type InitializeCapabilities struct {
	ExperimentalAPI bool `json:"experimentalApi,omitempty"`
}

// InitializeParams é o payload do método initialize (cliente → servidor).
type InitializeParams struct {
	ClientInfo   ClientInfo             `json:"clientInfo"`
	Capabilities InitializeCapabilities `json:"capabilities,omitempty"`
}

// ThreadResumeParams é o payload do método thread/resume.
type ThreadResumeParams struct {
	ThreadID string `json:"threadId"`
}

// UserInput representa uma entrada de texto para turn/start.
type UserInput struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// TurnStartParams é o payload do método turn/start.
type TurnStartParams struct {
	ThreadID string      `json:"threadId"`
	Input    []UserInput `json:"input"`
}

// TurnInterruptParams é o payload do método turn/interrupt.
type TurnInterruptParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

// ApprovalDecision define as decisões de aprovação implementadas nesta fatia.
type ApprovalDecision string

const (
	DecisionAccept  ApprovalDecision = "accept"
	DecisionDecline ApprovalDecision = "decline"
)

// CommandExecutionRequestApprovalParams representa a solicitação de aprovação do app-server.
type CommandExecutionRequestApprovalParams struct {
	ThreadID    string `json:"threadId"`
	TurnID      string `json:"turnId"`
	ItemID      string `json:"itemId"`
	ApprovalID  string `json:"approvalId,omitempty"`
	Command     string `json:"command,omitempty"`
	Cwd         string `json:"cwd,omitempty"`
	Reason      string `json:"reason,omitempty"`
	StartedAtMs int64  `json:"startedAtMs"`
}

// CommandExecutionRequestApprovalResponse é a resposta à solicitação de aprovação.
type CommandExecutionRequestApprovalResponse struct {
	Decision ApprovalDecision `json:"decision"`
}

// EventKind classifica eventos normalizados do app-server.
type EventKind string

const (
	EventKindStatus   EventKind = "status"
	EventKindTimeline EventKind = "timeline"
	EventKindError    EventKind = "error"
	EventKindApproval EventKind = "approval"
)

// Event é um evento normalizado consumido pela PWA/agente.
type Event struct {
	ID        string          `json:"id"`
	ThreadID  string          `json:"thread_id"`
	TurnID    string          `json:"turn_id,omitempty"`
	Kind      EventKind       `json:"kind"`
	Method    string          `json:"method,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Text      string          `json:"text,omitempty"`
	Status    string          `json:"status,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// Approval é uma solicitação de aprovação normalizada pendente.
type Approval struct {
	ID          string    `json:"id"`
	ThreadID    string    `json:"thread_id"`
	TurnID      string    `json:"turn_id"`
	ItemID      string    `json:"item_id"`
	ApprovalID  string    `json:"approval_id,omitempty"`
	Command     string    `json:"command,omitempty"`
	Cwd         string    `json:"cwd,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	StartedAtMs int64     `json:"started_at_ms"`
}

// ThreadInfo mantém o estado de uma thread Codex mapeada para uma sessão Relay.
type ThreadInfo struct {
	ThreadID string
	TurnID   string
	Status   string
}
