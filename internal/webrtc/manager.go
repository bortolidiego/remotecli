// Package webrtc gerencia PeerConnections por lease, DataChannels e sinalização.
package webrtc

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"sync"
	"time"

	"github.com/bortolidiego/relay/internal/channel"
	"github.com/bortolidiego/relay/internal/geometry"
	"github.com/pion/webrtc/v4"
)

const (
	channelControl   = "relay-control"
	channelClipboard = "relay-clipboard"
	channelFiles     = "relay-files"
)

// PeerManager gerencia uma PeerConnection autenticada por lease.
type PeerManager struct {
	mu              sync.RWMutex
	pc              *webrtc.PeerConnection
	deviceID        string
	sessionID       string
	sharedKey       []byte
	controlCh       *webrtc.DataChannel
	clipboardCh     *webrtc.DataChannel
	filesCh         *webrtc.DataChannel
	secure          *channel.SecureChannel
	iceProvider     ICEProvider
	onGeometry      func(geometry.DisplayGeometry)
	onInput         func([]byte)
	onClipboard     func([]byte)
	onStateChange   func(state webrtc.ICEConnectionState)
	onICECandidate  func(webrtc.ICECandidateInit)
	connected       bool
	disconnectedAt  *time.Time
}

// Config para criação de PeerManager.
type Config struct {
	// DeviceID é o identificador do dispositivo usado no AAD do canal seguro.
	// Host e client devem usar o mesmo DeviceID (o device_id emparelhado).
	DeviceID    string
	SessionID   string
	SharedKey   []byte
	ICEProvider ICEProvider
	Initiator   bool // true cria DataChannels; false aguarda remotos via OnDataChannel
	// PeerID é rótulo interno opcional para logs; default = DeviceID.
	PeerID      string
}

// NewPeerManager cria uma nova PeerConnection e DataChannels.
func NewPeerManager(cfg Config) (*PeerManager, error) {
	if len(cfg.SharedKey) != 32 {
		return nil, errors.New("shared_key deve ter 32 bytes")
	}
	if cfg.DeviceID == "" || cfg.SessionID == "" {
		return nil, errors.New("device_id e session_id são obrigatórios")
	}
	servers, err := cfg.ICEServers()
	if err != nil {
		return nil, err
	}
	se := webrtc.SettingEngine{}
	se.DisableMediaEngineCopy(true)
	se.SetHostAcceptanceMinWait(0)
	// Força candidatos host com IPs da LAN (iPhone na mesma Wi‑Fi).
	if ips := lanIPv4s(); len(ips) > 0 {
		se.SetNAT1To1IPs(ips, webrtc.ICECandidateTypeHost)
	}
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))
	pc, err := api.NewPeerConnection(webrtc.Configuration{
		ICEServers:         servers,
		ICETransportPolicy: webrtc.ICETransportPolicyAll,
		BundlePolicy:       webrtc.BundlePolicyMaxBundle,
		RTCPMuxPolicy:      webrtc.RTCPMuxPolicyRequire,
	})
	if err != nil {
		return nil, err
	}
	secure, err := channel.NewSecureChannel(cfg.SharedKey, cfg.DeviceID, cfg.SessionID, "webrtc")
	if err != nil {
		return nil, err
	}
	peerID := cfg.PeerID
	if peerID == "" {
		peerID = cfg.DeviceID
	}
	pm := &PeerManager{
		pc:          pc,
		deviceID:    peerID,
		sessionID:   cfg.SessionID,
		sharedKey:   append([]byte(nil), cfg.SharedKey...),
		secure:      secure,
		iceProvider: cfg.ICEProvider,
	}
	pm.pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		pm.mu.Lock()
		defer pm.mu.Unlock()
		switch dc.Label() {
		case channelControl:
			pm.controlCh = dc
			pm.bindDataChannel(dc, channel.MsgTypeControl, channel.MsgTypeGeometry, channel.MsgTypeInput)
		case channelClipboard:
			pm.clipboardCh = dc
			pm.bindDataChannel(dc, channel.MsgTypeClipboard)
		case channelFiles:
			pm.filesCh = dc
			pm.bindDataChannel(dc, channel.MsgTypeFile)
		}
	})
	pm.pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		pm.mu.RLock()
		cb := pm.onICECandidate
		pm.mu.RUnlock()
		if cb != nil && c != nil {
			cb(c.ToJSON())
		}
	})
	pm.pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		pm.mu.Lock()
		pm.connected = state == webrtc.ICEConnectionStateConnected
		if state == webrtc.ICEConnectionStateFailed || state == webrtc.ICEConnectionStateDisconnected {
			now := time.Now()
			pm.disconnectedAt = &now
		}
		cb := pm.onStateChange
		pm.mu.Unlock()
		if cb != nil {
			cb(state)
		}
	})
	if cfg.Initiator {
		if err := pm.createDataChannels(); err != nil {
			return nil, err
		}
	}
	return pm, nil
}

func (c Config) ICEServers() ([]webrtc.ICEServer, error) {
	if c.ICEProvider != nil {
		return c.ICEProvider.Servers()
	}
	return DefaultICEProvider{}.Servers()
}

func (pm *PeerManager) createDataChannels() error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	control, err := pm.pc.CreateDataChannel(channelControl, &webrtc.DataChannelInit{Ordered: boolPtr(true)})
	if err != nil {
		return err
	}
	pm.controlCh = control
	pm.bindDataChannel(control, channel.MsgTypeControl, channel.MsgTypeGeometry, channel.MsgTypeInput)

	clipboard, err := pm.pc.CreateDataChannel(channelClipboard, &webrtc.DataChannelInit{Ordered: boolPtr(true)})
	if err != nil {
		return err
	}
	pm.clipboardCh = clipboard
	pm.bindDataChannel(clipboard, channel.MsgTypeClipboard)

	files, err := pm.pc.CreateDataChannel(channelFiles, &webrtc.DataChannelInit{Ordered: boolPtr(true)})
	if err != nil {
		return err
	}
	pm.filesCh = files
	pm.bindDataChannel(files, channel.MsgTypeFile)
	return nil
}

func boolPtr(b bool) *bool { return &b }

func (pm *PeerManager) clipboardChannel() *webrtc.DataChannel {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.clipboardCh
}

func (pm *PeerManager) controlChannel() *webrtc.DataChannel {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.controlCh
}

// SendRaw envia bytes brutos no canal de clipboard (apenas para testes de rejeição de plaintext).
func (pm *PeerManager) SendRaw(payload []byte) error {
	ch := pm.clipboardChannel()
	if ch == nil {
		return errors.New("clipboard channel não inicializado")
	}
	return ch.Send(payload)
}

func (pm *PeerManager) bindDataChannel(dc *webrtc.DataChannel, acceptedTypes ...channel.MessageType) {
	dc.OnOpen(func() {
		log.Printf("datachannel %s aberto para %s", dc.Label(), pm.deviceID)
	})
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		dec, err := pm.secure.Decrypt(msg.Data)
		if err != nil {
			log.Printf("datachannel %s: mensagem rejeitada: %v", dc.Label(), err)
			return
		}
		if !isAccepted(dec.Type, acceptedTypes) {
			log.Printf("datachannel %s: tipo %d não esperado", dc.Label(), dec.Type)
			return
		}
		pm.dispatch(dec.Type, dec.Plaintext)
	})
}

func isAccepted(t channel.MessageType, list []channel.MessageType) bool {
	for _, v := range list {
		if v == t {
			return true
		}
	}
	return false
}

func (pm *PeerManager) dispatch(msgType channel.MessageType, payload []byte) {
	switch msgType {
	case channel.MsgTypeGeometry:
		var g geometry.DisplayGeometry
		if err := json.Unmarshal(payload, &g); err == nil {
			pm.mu.RLock()
			cb := pm.onGeometry
			pm.mu.RUnlock()
			if cb != nil {
				cb(g)
			}
		}
	case channel.MsgTypeInput:
		pm.mu.RLock()
		cb := pm.onInput
		pm.mu.RUnlock()
		if cb != nil {
			cb(append([]byte(nil), payload...))
		}
	case channel.MsgTypeClipboard:
		pm.mu.RLock()
		cb := pm.onClipboard
		pm.mu.RUnlock()
		if cb != nil {
			cb(append([]byte(nil), payload...))
		}
	}
}

func waitGather(pc *webrtc.PeerConnection, timeout time.Duration) {
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	select {
	case <-gatherComplete:
	case <-time.After(timeout):
	}
}

// CreateOffer inicia a negociação WebRTC e espera ICE gathering (SDP completo).
func (pm *PeerManager) CreateOffer() (*webrtc.SessionDescription, error) {
	offer, err := pm.pc.CreateOffer(nil)
	if err != nil {
		return nil, err
	}
	if err := pm.pc.SetLocalDescription(offer); err != nil {
		return nil, err
	}
	waitGather(pm.pc, 5*time.Second)
	desc := pm.pc.LocalDescription()
	if desc == nil {
		return nil, errors.New("local description vazia após gather")
	}
	return desc, nil
}

// SetRemoteAnswer aplica a resposta remota.
func (pm *PeerManager) SetRemoteAnswer(answer webrtc.SessionDescription) error {
	return pm.pc.SetRemoteDescription(answer)
}

// SetRemoteOffer aplica a oferta remota e gera answer com ICE gather completo
// (evita depender de trickle host→client, que ainda não existe).
func (pm *PeerManager) SetRemoteOffer(offer webrtc.SessionDescription) (*webrtc.SessionDescription, error) {
	if err := pm.pc.SetRemoteDescription(offer); err != nil {
		return nil, err
	}
	answer, err := pm.pc.CreateAnswer(nil)
	if err != nil {
		return nil, err
	}
	if err := pm.pc.SetLocalDescription(answer); err != nil {
		return nil, err
	}
	waitGather(pm.pc, 5*time.Second)
	desc := pm.pc.LocalDescription()
	if desc == nil {
		return nil, errors.New("local description vazia após gather")
	}
	return desc, nil
}

// AddICECandidate adiciona candidato remoto.
func (pm *PeerManager) AddICECandidate(candidate webrtc.ICECandidateInit) error {
	return pm.pc.AddICECandidate(candidate)
}

// LocalDescription retorna descrição local atual.
func (pm *PeerManager) LocalDescription() *webrtc.SessionDescription {
	return pm.pc.LocalDescription()
}

// SendGeometry envia geometria cifrada no canal de controle.
func (pm *PeerManager) SendGeometry(g geometry.DisplayGeometry) error {
	b, err := json.Marshal(g)
	if err != nil {
		return err
	}
	return pm.sendOnChannel(pm.controlCh, channel.MsgTypeGeometry, b)
}

// SendInput envia evento de input cifrado.
func (pm *PeerManager) SendInput(payload []byte) error {
	return pm.sendOnChannel(pm.controlCh, channel.MsgTypeInput, payload)
}

// SendClipboard envia clipboard cifrado.
func (pm *PeerManager) SendClipboard(payload []byte) error {
	return pm.sendOnChannel(pm.clipboardCh, channel.MsgTypeClipboard, payload)
}

func (pm *PeerManager) sendOnChannel(dc *webrtc.DataChannel, msgType channel.MessageType, payload []byte) error {
	if dc == nil {
		return errors.New("datachannel não inicializado")
	}
	enc, err := pm.secure.Encrypt(msgType, payload)
	if err != nil {
		return err
	}
	return dc.Send(enc)
}

// ICEConnectionState retorna o estado ICE.
func (pm *PeerManager) ICEConnectionState() webrtc.ICEConnectionState {
	return pm.pc.ICEConnectionState()
}

// IsConnected retorna true se conectado.
func (pm *PeerManager) IsConnected() bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.connected
}

// OnGeometry registra callback de geometria.
func (pm *PeerManager) OnGeometry(cb func(geometry.DisplayGeometry)) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.onGeometry = cb
}

// OnInput registra callback de input.
func (pm *PeerManager) OnInput(cb func([]byte)) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.onInput = cb
}

// OnClipboard registra callback de clipboard.
func (pm *PeerManager) OnClipboard(cb func([]byte)) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.onClipboard = cb
}

// OnStateChange registra callback de estado ICE.
func (pm *PeerManager) OnStateChange(cb func(webrtc.ICEConnectionState)) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.onStateChange = cb
}

// OnICECandidate registra callback de candidato ICE gerado.
func (pm *PeerManager) OnICECandidate(cb func(webrtc.ICECandidateInit)) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.onICECandidate = cb
}

// ConnectTo conecta dois PeerManagers em processo (para testes), trocando oferta/answer/candidatos.
func ConnectTo(a, b *PeerManager) error {
	var pendingA, pendingB []webrtc.ICECandidateInit
	a.OnICECandidate(func(c webrtc.ICECandidateInit) {
		if b.pc == nil {
			return
		}
		if b.pc.RemoteDescription() == nil {
			pendingA = append(pendingA, c)
			return
		}
		_ = b.AddICECandidate(c)
	})
	b.OnICECandidate(func(c webrtc.ICECandidateInit) {
		if a.pc == nil {
			return
		}
		if a.pc.RemoteDescription() == nil {
			pendingB = append(pendingB, c)
			return
		}
		_ = a.AddICECandidate(c)
	})
	offer, err := a.CreateOffer()
	if err != nil {
		return err
	}
	answer, err := b.SetRemoteOffer(*offer)
	if err != nil {
		return err
	}
	if err := a.SetRemoteAnswer(*answer); err != nil {
		return err
	}
	for _, c := range pendingB {
		_ = a.AddICECandidate(c)
	}
	for _, c := range pendingA {
		_ = b.AddICECandidate(c)
	}
	return nil
}

// Close finaliza a conexão e limpa recursos.
func (pm *PeerManager) Close() error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.pc == nil {
		return nil
	}
	return pm.pc.Close()
}

// RestartICE tenta reiniciar a conectividade ICE.
func (pm *PeerManager) RestartICE() error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.pc == nil {
		return errors.New("peerconnection fechada")
	}
	// Re-create offer with iceRestart.
	offer, err := pm.pc.CreateOffer(&webrtc.OfferOptions{ICERestart: true})
	if err != nil {
		return err
	}
	if err := pm.pc.SetLocalDescription(offer); err != nil {
		return err
	}
	return nil
}

// WaitConnected aguarda conexão até timeout.
func (pm *PeerManager) WaitConnected(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pm.IsConnected() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func lanIPv4s() []string {
	var out []string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		if v4 := ipNet.IP.To4(); v4 != nil {
			out = append(out, v4.String())
		}
	}
	return out
}

// PeerID retorna identificador do par.
func (pm *PeerManager) PeerID() string {
	return pm.deviceID
}

// AddVideoTrack adiciona track H264 à conexão (usado quando vídeo vem de source local/IPC).
func (pm *PeerManager) AddVideoTrack(track *webrtc.TrackLocalStaticSample) error {
	if track == nil {
		return errors.New("track nulo")
	}
	_, err := pm.pc.AddTrack(track)
	return err
}

// Ensure context stub para satisfazer lint.
var _ = context.Background
