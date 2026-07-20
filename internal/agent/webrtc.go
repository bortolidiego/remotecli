// Package agent gerencia sessões, pareamento, WebRTC e IPC.
package agent

import (
	"crypto/rand"
	"errors"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/bortolidiego/relay/internal/geometry"
	"github.com/bortolidiego/relay/internal/ipc"
	"github.com/bortolidiego/relay/internal/webrtc"
	pion "github.com/pion/webrtc/v4"
)



// peerRecord guarda PeerConnection e sessão por lease.
type peerRecord struct {
	pm        *webrtc.PeerManager
	deviceID  string
	lease     string
	createdAt time.Time
}

// PeerManager agrupa peers ativos e integração IPC.
type PeerManager struct {
	mu       sync.RWMutex
	agent    *Agent
	peers    map[string]*peerRecord // key = deviceID
	ipc      *ipc.Server
	ipcPath  string
}

// NewPeerManager cria gerenciador de peers para o agente.
func NewPeerManager(a *Agent) (*PeerManager, error) {
	pm := &PeerManager{agent: a, peers: make(map[string]*peerRecord)}
	return pm, nil
}

// StartIPC inicia servidor Unix socket para helper Swift.
func (pm *PeerManager) StartIPC(dir string) (string, error) {
	secret, err := pm.agent.loadOrCreateIPCSecret()
	if err != nil {
		return "", err
	}
	server := ipc.NewServer(secret)
	server.OnGeometry(func(g geometry.DisplayGeometry) {
		pm.broadcastGeometry(g)
	})
	server.OnInput(func(b []byte) {
		// Input vem do helper? No protocolo, Go envia input para helper; helper não envia input.
		// Mantido para logging.
		log.Printf("ipc: input inesperado do helper: %s", string(b))
	})
	server.OnClipboard(func(b []byte) {
		pm.broadcastClipboard(b)
	})
	path, err := server.Listen(dir)
	if err != nil {
		return "", err
	}
	pm.ipc = server
	pm.ipcPath = path
	return path, nil
}

// Stop finaliza IPC e peers.
func (pm *PeerManager) Stop() error {
	pm.mu.Lock()
	peers := make([]*peerRecord, 0, len(pm.peers))
	for _, p := range pm.peers {
		peers = append(peers, p)
	}
	pm.mu.Unlock()
	for _, p := range peers {
		_ = p.pm.Close()
	}
	if pm.ipc != nil {
		_ = pm.ipc.Close()
	}
	return nil
}

// CreatePeerForLease inicia PeerConnection para um device autenticado.
func (pm *PeerManager) CreatePeerForLease(deviceID, lease string, sharedKey []byte) (*webrtc.PeerManager, error) {
	if len(sharedKey) != 32 {
		return nil, errors.New("shared_key inválida")
	}
	cfg := webrtc.Config{
		DeviceID:    deviceID,
		SessionID:   pm.agent.sessionID,
		SharedKey:   sharedKey,
		ICEProvider: webrtc.DefaultICEProvider{},
		Initiator:   true,
		PeerID:      "host",
	}
	mgr, err := webrtc.NewPeerManager(cfg)
	if err != nil {
		return nil, err
	}
	mgr.OnInput(func(b []byte) {
		if pm.ipc != nil {
			_ = pm.ipc.BroadcastInput(b)
		}
	})
	mgr.OnClipboard(func(b []byte) {
		if pm.ipc != nil {
			_ = pm.ipc.BroadcastClipboard(b)
		}
	})
	mgr.OnGeometry(func(g geometry.DisplayGeometry) {
		// geometria do cliente (canvas remoto) não usada no host
	})
	pm.mu.Lock()
	pm.peers[deviceID] = &peerRecord{pm: mgr, deviceID: deviceID, lease: lease, createdAt: time.Now()}
	pm.mu.Unlock()
	return mgr, nil
}

// GetPeer retorna PeerManager de um device.
func (pm *PeerManager) GetPeer(deviceID string) (*webrtc.PeerManager, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	rec, ok := pm.peers[deviceID]
	if !ok {
		return nil, false
	}
	return rec.pm, true
}

// ReleasePeer fecha conexão de um device.
func (pm *PeerManager) ReleasePeer(deviceID string) bool {
	pm.mu.Lock()
	rec, ok := pm.peers[deviceID]
	if ok {
		delete(pm.peers, deviceID)
	}
	pm.mu.Unlock()
	if ok {
		_ = rec.pm.Close()
	}
	return ok
}

func (pm *PeerManager) broadcastGeometry(g geometry.DisplayGeometry) {
	pm.mu.RLock()
	peers := make([]*webrtc.PeerManager, 0, len(pm.peers))
	for _, rec := range pm.peers {
		peers = append(peers, rec.pm)
	}
	pm.mu.RUnlock()
	for _, p := range peers {
		_ = p.SendGeometry(g)
	}
}

func (pm *PeerManager) broadcastClipboard(b []byte) {
	pm.mu.RLock()
	peers := make([]*webrtc.PeerManager, 0, len(pm.peers))
	for _, rec := range pm.peers {
		peers = append(peers, rec.pm)
	}
	pm.mu.RUnlock()
	for _, p := range peers {
		_ = p.SendClipboard(b)
	}
}

// IPCPath retorna o caminho do socket.
func (pm *PeerManager) IPCPath() string { return pm.ipcPath }

// WebRTCSignalingPayload é usado pelos endpoints offer/answer/ice.
type WebRTCSignalingPayload struct {
	DeviceID  string                   `json:"device_id"`
	Lease     string                   `json:"lease_token"`
	SDP       *pion.SessionDescription `json:"sdp,omitempty"`
	Candidate *pion.ICECandidateInit   `json:"candidate,omitempty"`
}

func (a *Agent) loadOrCreateIPCSecret() ([]byte, error) {
	acc := "host-" + a.sessionID
	if b, err := a.store.LoadSecret("relay-ipc-secret", acc); err == nil && len(b) == 32 {
		return b, nil
	}
	b := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, err
	}
	if err := a.store.SaveSecret("relay-ipc-secret", acc, b); err != nil {
		return nil, err
	}
	return b, nil
}

// RegisterWebRTCRoutes adiciona rotas de signaling WebRTC.
func (a *Agent) RegisterWebRTCRoutes(pm *PeerManager) {
	a.mux.HandleFunc("/api/webrtc/offer", a.requireLease(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
			return
		}
		var req WebRTCSignalingPayload
		if err := readJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if !a.registry.ValidateLease(req.Lease, req.DeviceID) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "lease inválido"})
			return
		}
		dev, ok := a.registry.GetDevice(req.DeviceID)
		if !ok {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "device não encontrado"})
			return
		}
		sess, ok := a.registry.DeviceSession(dev.DeviceID)
		if !ok {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "sessão compartilhada não encontrada; reconecte o lease"})
			return
		}
		mgr, err := pm.CreatePeerForLease(req.DeviceID, req.Lease, sess.SharedKey)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if req.SDP != nil {
			answer, err := mgr.SetRemoteOffer(*req.SDP)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"sdp": answer})
			return
		}
		offer, err := mgr.CreateOffer()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sdp": offer})
	}))

	a.mux.HandleFunc("/api/webrtc/answer", a.requireLease(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
			return
		}
		var req WebRTCSignalingPayload
		if err := readJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if !a.registry.ValidateLease(req.Lease, req.DeviceID) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "lease inválido"})
			return
		}
		mgr, ok := pm.GetPeer(req.DeviceID)
		if !ok || req.SDP == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "peer ou SDP ausente"})
			return
		}
		if err := mgr.SetRemoteAnswer(*req.SDP); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))

	a.mux.HandleFunc("/api/webrtc/ice", a.requireLease(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
			return
		}
		var req WebRTCSignalingPayload
		if err := readJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if !a.registry.ValidateLease(req.Lease, req.DeviceID) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "lease inválido"})
			return
		}
		mgr, ok := pm.GetPeer(req.DeviceID)
		if !ok || req.Candidate == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "peer ou candidate ausente"})
			return
		}
		if err := mgr.AddICECandidate(*req.Candidate); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))

	a.mux.HandleFunc("/api/webrtc/status", a.requireLease(func(w http.ResponseWriter, r *http.Request) {
		deviceID := r.Header.Get("X-Relay-Device-ID")
		mgr, ok := pm.GetPeer(deviceID)
		if !ok {
			writeJSON(w, http.StatusOK, map[string]any{"connected": false, "ipc_path": pm.IPCPath()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"connected": mgr.IsConnected(),
			"state":     mgr.ICEConnectionState().String(),
			"ipc_path":  pm.IPCPath(),
		})
	}))
}

// SendInput envia evento de input para o helper via IPC.
func (pm *PeerManager) SendInput(payload []byte) error {
	if pm.ipc == nil {
		return errors.New("IPC não inicializado")
	}
	return pm.ipc.BroadcastInput(payload)
}

// SendClipboardToHelper envia clipboard para helper via IPC.
func (pm *PeerManager) SendClipboardToHelper(payload []byte) error {
	if pm.ipc == nil {
		return errors.New("IPC não inicializado")
	}
	return pm.ipc.BroadcastClipboard(payload)
}


