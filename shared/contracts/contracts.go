package contracts

import (
	"encoding/base64"
	"encoding/json"
	"time"
)

// Role define o lado de uma sessão Relay.
type Role string

const (
	RoleHost   Role = "host"
	RoleClient Role = "client"
)

// Capability lista recursos que um dispositivo pode anunciar para futuros marcos.
type Capability string

const (
	CapabilityCloudflare    Capability = "cloudflare"
	CapabilityWebRTC        Capability = "webrtc"
	CapabilityScreenCapture Capability = "screencapture"
	CapabilityNativeControl Capability = "native_control"
)

// Harness identifica a origem do controle.
type Harness string

const (
	HarnessNative  Harness = "native"
	HarnessMaestri Harness = "maestri"
	HarnessCodex   Harness = "codex"
)

// Status define o estado da sessão.
type Status string

const (
	StatusActive    Status = "active"
	StatusPaused    Status = "paused"
	StatusOffline   Status = "offline"
	StatusReleasing Status = "releasing"
)

// SessionMetadata carrega contexto local detectado pelo CLI/helper.
type SessionMetadata struct {
	Harness           Harness `json:"harness"`
	NativeSessionID   string  `json:"nativeSessionId"`
	MaestriTerminalID *string `json:"maestriTerminalId,omitempty"`
	CodexThreadID     *string `json:"codexThreadId,omitempty"`
	PID               *int    `json:"pid,omitempty"`
	WindowID          *string `json:"windowId,omitempty"`
	Frontmost         bool    `json:"frontmost"`
}

// DeviceInfo descreve um dispositivo emparelhado.
type DeviceInfo struct {
	DeviceID     string       `json:"device_id"`
	Name         string       `json:"name"`
	Role         Role         `json:"role"`
	PublicKey    []byte       `json:"public_key"`
	Fingerprint  string       `json:"fingerprint"`
	Capabilities []Capability `json:"capabilities"`
	PairedAt     time.Time    `json:"paired_at"`
}

// SessionDescriptor é o contrato canônico de uma sessão segura.
type SessionDescriptor struct {
	ID                string       `json:"id"`
	Harness           Harness      `json:"harness"`
	NativeSessionID   string       `json:"nativeSessionId"`
	MaestriTerminalID *string      `json:"maestriTerminalId,omitempty"`
	CodexThreadID     *string      `json:"codexThreadId,omitempty"`
	Cwd               string       `json:"cwd"`
	PID               *int         `json:"pid,omitempty"`
	WindowID          *string      `json:"windowId,omitempty"`
	Frontmost         bool         `json:"frontmost"`
	Status            Status       `json:"status"`
	Capabilities      []Capability `json:"capabilities"`
	// campos legados de compatibilidade com Marco 1
	SessionID string       `json:"session_id"`
	HostID    string       `json:"host_id"`
	Devices   []DeviceInfo `json:"devices"`
	CreatedAt time.Time    `json:"created_at"`
	ExpiresAt time.Time    `json:"expires_at"`
}

// ShareOfferPayload é exibido pelo host no QR one-time.
type ShareOfferPayload struct {
	SessionID string    `json:"session_id"`
	HostID    string    `json:"host_id"`
	HostName  string    `json:"host_name"`
	HostKey   []byte    `json:"host_key"`
	HostECDH  []byte    `json:"host_ecdh"`
	Nonce     string    `json:"nonce"`
	ExpiresAt time.Time `json:"expires_at"`
	Endpoint  string    `json:"endpoint"`
	Version   string    `json:"version"`
}

// PairRequest é enviado pelo cliente após ler o QR.
type PairRequest struct {
	SessionID       string       `json:"session_id"`
	HostID          string       `json:"host_id"`
	DeviceID        string       `json:"device_id"`
	Name            string       `json:"name"`
	ClientKey       []byte       `json:"client_key"`
	ClientECDH      []byte       `json:"client_ecdh"`
	ClientSignature []byte       `json:"client_signature"`
	Nonce           string       `json:"nonce"`
	Capabilities    []Capability `json:"capabilities"`
}

// PairChallengeVersion identifica o payload assinado pelo cliente no pairing.
const PairChallengeVersion = "relay-pair-v1"

// PairChallenge é a forma canônica assinada por ECDSA P-256 no cliente.
type PairChallenge struct {
	Version      string       `json:"version"`
	SessionID    string       `json:"session_id"`
	HostID       string       `json:"host_id"`
	DeviceID     string       `json:"device_id"`
	Name         string       `json:"name"`
	Nonce        string       `json:"nonce"`
	ClientKey    string       `json:"client_key"`
	ClientECDH   string       `json:"client_ecdh"`
	Capabilities []Capability `json:"capabilities"`
}

// BuildPairChallenge serializa o desafio de forma estável para Go e WebCrypto.
func BuildPairChallenge(req PairRequest) ([]byte, error) {
	return json.Marshal(PairChallenge{
		Version:      PairChallengeVersion,
		SessionID:    req.SessionID,
		HostID:       req.HostID,
		DeviceID:     req.DeviceID,
		Name:         req.Name,
		Nonce:        req.Nonce,
		ClientKey:    base64.StdEncoding.EncodeToString(req.ClientKey),
		ClientECDH:   base64.StdEncoding.EncodeToString(req.ClientECDH),
		Capabilities: req.Capabilities,
	})
}

// PairResponse é devolvido pelo agente ao cliente.
type PairResponse struct {
	SessionID   string    `json:"session_id"`
	DeviceID    string    `json:"device_id"`
	HostName    string    `json:"host_name"`
	ServerKey   []byte    `json:"server_key"`
	ServerECDH  []byte    `json:"server_ecdh"`
	LeaseToken  string    `json:"lease_token"`
	LeaseExpiry time.Time `json:"lease_expiry"`
}

// AgentStatus resumo de runtime — mínimo para visitantes.
type AgentStatus struct {
	Listening bool   `json:"listening"`
	Address   string `json:"address"`
	Version   string `json:"version"`
	Paired    bool   `json:"paired,omitempty"`
}

// AuthenticatedStatus resumo completo, apenas para dispositivos autenticados.
type AuthenticatedStatus struct {
	AgentStatus
	SessionID   string              `json:"session_id"`
	SessionPath string              `json:"session_path"`
	Devices     []DeviceInfo        `json:"devices"`
	Sessions    []SessionDescriptor `json:"sessions"`
}

// SignedEnvelope transporta payloads assinados.
type SignedEnvelope struct {
	Payload   []byte `json:"payload"`
	Signature []byte `json:"signature"`
	SignerKey []byte `json:"signer_key"`
}

// EncryptedEnvelope é AES-256-GCM + nonce público.
type EncryptedEnvelope struct {
	Ciphertext []byte `json:"ciphertext"`
	Nonce      []byte `json:"nonce"`
}
