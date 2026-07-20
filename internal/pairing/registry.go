package pairing

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/bortolidiego/relay/internal/crypto"
	"github.com/bortolidiego/relay/internal/keychain"
	"github.com/bortolidiego/relay/shared/contracts"
)

const (
	MaxDevices    = 3
	OfferTTL      = 2 * time.Minute
	LeaseDuration = 30 * time.Minute
	NonceExpiry   = 2 * time.Minute
)

// NonceRecord guarda nonces vistos.
type NonceRecord struct {
	Nonce     string
	ExpiresAt time.Time
}

// Lease associa token a um device_id e expiração.
type Lease struct {
	Token     string
	DeviceID  string
	ExpiresAt time.Time
}

// Registry gerencia dispositivos emparelhados, ofertas e leases.
type Registry struct {
	mu        sync.RWMutex
	identity  *crypto.IdentityPair
	store     keychain.Store
	hostName  string
	endpoint  string
	sessionID string
	basePath  string
	meta      contracts.SessionMetadata

	hostKey     []byte
	hostECDH    []byte
	offer       *contracts.ShareOfferPayload
	offerExpiry time.Time

	devices map[string]contracts.DeviceInfo
	nonces  map[string]NonceRecord

	lease       Lease
	leaseValid  bool
	sessions    map[string]*DeviceSession // sessões em memória por device_id (não persistidas)
}

func NewRegistry(identity *crypto.IdentityPair, store keychain.Store, sessionID, hostName, endpoint, basePath string) (*Registry, error) {
	pub, err := identity.PublicSigningKeyBytes()
	if err != nil {
		return nil, err
	}
	ecdhPub, err := identity.PublicECDHKeyBytes()
	if err != nil {
		return nil, err
	}
	r := &Registry{
		identity:  identity,
		store:     store,
		hostName:  hostName,
		endpoint:  endpoint,
		sessionID: sessionID,
		basePath:  basePath,
		meta:      contracts.SessionMetadata{Harness: contracts.HarnessNative, NativeSessionID: sessionID},
		hostKey:   pub,
		hostECDH:  ecdhPub,
		devices:   map[string]contracts.DeviceInfo{},
		nonces:    map[string]NonceRecord{},
		sessions:  map[string]*DeviceSession{},
	}
	if err := r.load(); err != nil {
		return nil, fmt.Errorf("carregar registry: %w", err)
	}
	r.cleanupLoop()
	return r, nil
}

func (r *Registry) HostName() string { return r.hostName }

func (r *Registry) BasePath() string { return r.basePath }

// LANEndpoint transforma "host:port" em URL acessível na LAN.
func LANEndpoint(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		port = "24109"
	}
	ip := localIP()
	if ip == "" {
		ip = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%s", ip, port)
}

func localIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if ipNet, ok := a.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			if ipNet.IP.To4() != nil {
				return ipNet.IP.String()
			}
		}
	}
	return ""
}

func (r *Registry) SetSessionMetadata(meta contracts.SessionMetadata) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if meta.Harness == "" {
		meta.Harness = contracts.HarnessNative
	}
	if meta.NativeSessionID == "" {
		meta.NativeSessionID = r.sessionID
	}
	r.meta = meta
}

func (r *Registry) HasDevices() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.devices) > 0
}

func (r *Registry) HostID() string {
	return crypto.FingerprintBytes(r.hostKey)
}

// StartOffer gera QR one-time com nonce aleatório e TTL de 2 min.
func (r *Registry) StartOffer() (*contracts.ShareOfferPayload, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	offer := &contracts.ShareOfferPayload{
		SessionID: r.sessionID,
		HostID:    r.HostID(),
		HostName:  r.hostName,
		HostKey:   r.hostKey,
		HostECDH:  r.hostECDH,
		Nonce:     base64.RawURLEncoding.EncodeToString(nonce),
		ExpiresAt: time.Now().Add(OfferTTL),
		Endpoint:  r.endpoint,
		Version:   "relay-m2",
	}
	r.offer = offer
	r.offerExpiry = offer.ExpiresAt
	return offer, nil
}

// Offer retorna oferta ativa se ainda válida.
func (r *Registry) Offer() (*contracts.ShareOfferPayload, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.offer == nil || time.Now().After(r.offerExpiry) {
		return nil, false
	}
	return r.offer, true
}

// Pair aceita um cliente, valida nonce, autentica chave, limita dispositivos, deriva segredo e emite lease exclusivo.
func (r *Registry) Pair(req *contracts.PairRequest) (*contracts.PairResponse, *DeviceSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.offer == nil || time.Now().After(r.offerExpiry) {
		return nil, nil, errors.New("oferta expirada ou inexistente")
	}
	if req.SessionID != r.sessionID {
		return nil, nil, errors.New("sessão incompatível")
	}
	if req.HostID != r.HostID() {
		return nil, nil, errors.New("host incompatível")
	}
	if !r.consumeNonce(req.Nonce) {
		return nil, nil, errors.New("nonce inválido, expirado ou reutilizado")
	}
	if len(r.devices) >= MaxDevices {
		return nil, nil, fmt.Errorf("limite de %d dispositivos atingido", MaxDevices)
	}
	if req.DeviceID == "" || req.Name == "" || len(req.ClientKey) == 0 || len(req.ClientECDH) == 0 || len(req.ClientSignature) == 0 {
		return nil, nil, errors.New("dados de emparelhamento incompletos")
	}
	if _, exists := r.devices[req.DeviceID]; exists {
		return nil, nil, errors.New("device_id já emparelhado")
	}
	challenge, err := contracts.BuildPairChallenge(*req)
	if err != nil {
		return nil, nil, fmt.Errorf("montar desafio: %w", err)
	}
	if err := crypto.Verify(req.ClientKey, challenge, req.ClientSignature); err != nil {
		return nil, nil, fmt.Errorf("assinatura do cliente inválida: %w", err)
	}

	sharedKey, err := crypto.DeriveSharedSecret(r.identity.ECDHKey(), req.ClientECDH)
	if err != nil {
		return nil, nil, fmt.Errorf("derivar segredo: %w", err)
	}

	dev := contracts.DeviceInfo{
		DeviceID:     req.DeviceID,
		Name:         req.Name,
		Role:         contracts.RoleClient,
		PublicKey:    req.ClientKey,
		Fingerprint:  crypto.FingerprintBytes(req.ClientKey),
		Capabilities: req.Capabilities,
		PairedAt:     time.Now(),
	}
	r.devices[req.DeviceID] = dev
	r.invalidateOffer()
	leaseToken, leaseExpiry := r.grantLease(req.DeviceID)
	if err := r.save(); err != nil {
		return nil, nil, fmt.Errorf("persistir registry: %w", err)
	}
	sess := &DeviceSession{DeviceID: dev.DeviceID, SharedKey: sharedKey, Lease: leaseToken, ExpiresAt: leaseExpiry}
	r.sessions[sess.DeviceID] = sess
	return &contracts.PairResponse{
		SessionID:   r.sessionID,
		DeviceID:    dev.DeviceID,
		HostName:    r.hostName,
		ServerKey:   r.hostKey,
		ServerECDH:  r.hostECDH,
		LeaseToken:  leaseToken,
		LeaseExpiry: leaseExpiry,
	}, sess, nil
}

// RegisterSession armazena sessão compartilhada em memória (não persistida).
func (r *Registry) RegisterSession(sess *DeviceSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[sess.DeviceID] = sess
}

// DeviceSession segredo derivado no emparelhamento.
type DeviceSession struct {
	DeviceID  string
	SharedKey []byte
	Lease     string
	ExpiresAt time.Time
}

func (r *Registry) consumeNonce(nonce string) bool {
	if nonce == "" {
		return false
	}
	if _, ok := r.nonces[nonce]; ok {
		return false
	}
	r.nonces[nonce] = NonceRecord{Nonce: nonce, ExpiresAt: time.Now().Add(NonceExpiry)}
	return true
}

func (r *Registry) invalidateOffer() {
	r.offer = nil
	r.offerExpiry = time.Time{}
}

func (r *Registry) grantLease(deviceID string) (string, time.Time) {
	tok := make([]byte, 24)
	_, _ = rand.Read(tok)
	r.lease = Lease{
		Token:     base64.RawURLEncoding.EncodeToString(tok),
		DeviceID:  deviceID,
		ExpiresAt: time.Now().Add(LeaseDuration),
	}
	r.leaseValid = true
	return r.lease.Token, r.lease.ExpiresAt
}

// ValidateLease verifica lease associado a device_id em tempo constante.
func (r *Registry) ValidateLease(token, deviceID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.leaseValid || deviceID == "" || time.Now().After(r.lease.ExpiresAt) {
		return false
	}
	return crypto.ConstantTimeCompare(token, r.lease.Token) && deviceID == r.lease.DeviceID
}

func (r *Registry) LeaseDeviceID() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.leaseValid {
		return ""
	}
	return r.lease.DeviceID
}

func (r *Registry) Devices() []contracts.DeviceInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]contracts.DeviceInfo, 0, len(r.devices))
	for _, d := range r.devices {
		out = append(out, d)
	}
	return out
}

func (r *Registry) Revoke(deviceID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.devices[deviceID]; ok {
		delete(r.devices, deviceID)
		delete(r.sessions, deviceID)
		if r.leaseValid && r.lease.DeviceID == deviceID {
			r.leaseValid = false
			r.lease = Lease{}
		}
		_ = r.save()
		return true
	}
	return false
}

func (r *Registry) Sessions() []contracts.SessionDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return []contracts.SessionDescriptor{r.buildSessionDescriptorLocked()}
}

func (r *Registry) SessionByID(id string) (contracts.SessionDescriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sess := r.buildSessionDescriptorLocked()
	if sess.ID != id && sess.SessionID != id {
		return contracts.SessionDescriptor{}, false
	}
	return sess, true
}

func (r *Registry) buildSessionDescriptorLocked() contracts.SessionDescriptor {
	caps := []contracts.Capability{}
	if r.identity != nil && r.identity.ECDHKey() != nil {
		caps = append(caps, contracts.CapabilityNativeControl)
	}
	meta := r.meta
	if meta.Harness == "" {
		meta.Harness = contracts.HarnessNative
	}
	if meta.NativeSessionID == "" {
		meta.NativeSessionID = r.sessionID
	}
	return contracts.SessionDescriptor{
		ID:                r.HostID(),
		SessionID:         r.sessionID,
		HostID:            r.HostID(),
		Harness:           meta.Harness,
		NativeSessionID:   meta.NativeSessionID,
		MaestriTerminalID: meta.MaestriTerminalID,
		CodexThreadID:     meta.CodexThreadID,
		Cwd:               r.basePath,
		PID:               meta.PID,
		WindowID:          meta.WindowID,
		Frontmost:         meta.Frontmost,
		Status:            contracts.StatusActive,
		Capabilities:      caps,
		CreatedAt:         time.Now().Add(-time.Minute),
		ExpiresAt:         time.Now().Add(24 * time.Hour),
		Devices:           r.devicesLocked(),
	}
}

func (r *Registry) devicesLocked() []contracts.DeviceInfo {
	out := make([]contracts.DeviceInfo, 0, len(r.devices))
	for _, d := range r.devices {
		out = append(out, d)
	}
	return out
}

// ReleaseLease invalida lease de um device sem apagar o device.
func (r *Registry) ReleaseLease(deviceID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.devices[deviceID]; !ok {
		return false
	}
	if r.leaseValid && r.lease.DeviceID == deviceID {
		r.leaseValid = false
		r.lease = Lease{}
	}
	return true
}

func (r *Registry) GetDevice(deviceID string) (contracts.DeviceInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	dev, ok := r.devices[deviceID]
	return dev, ok
}

// DeviceSession retorna a sessão compartilhada de um device emparelhado (volátil).
func (r *Registry) DeviceSession(deviceID string) (*DeviceSession, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sess, ok := r.sessions[deviceID]
	if !ok {
		return nil, false
	}
	if time.Now().After(sess.ExpiresAt) {
		return nil, false
	}
	return sess, true
}

func (r *Registry) cleanupLoop() {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			r.mu.Lock()
			if r.offer != nil && time.Now().After(r.offerExpiry) {
				r.invalidateOffer()
			}
			if r.leaseValid && time.Now().After(r.lease.ExpiresAt) {
				r.leaseValid = false
				r.lease = Lease{}
			}
			for k, v := range r.nonces {
				if time.Now().After(v.ExpiresAt) {
					delete(r.nonces, k)
				}
			}
			r.mu.Unlock()
		}
	}()
}

// SignOffer assina a oferta serializada com a chave do host.
func (r *Registry) SignOffer(offer *contracts.ShareOfferPayload) ([]byte, error) {
	b, err := json.Marshal(offer)
	if err != nil {
		return nil, err
	}
	return crypto.Sign(r.identity.SigningKey(), b)
}

// VerifyIdentitySignature verifica se a assinatura vem de um dispositivo emparelhado.
func (r *Registry) VerifyIdentitySignature(deviceID string, payload, signature []byte) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	dev, ok := r.devices[deviceID]
	if !ok {
		return errors.New("dispositivo não encontrado")
	}
	return crypto.Verify(dev.PublicKey, payload, signature)
}

func (r *Registry) HostECDH() []byte { return r.hostECDH }

func (r *Registry) save() error {
	if r.store == nil {
		return nil
	}
	b, err := json.Marshal(r.devices)
	if err != nil {
		return err
	}
	return r.store.SaveRegistry("relay-devices", "host-"+r.sessionID, b)
}

func (r *Registry) load() error {
	if r.store == nil {
		return nil
	}
	b, err := r.store.LoadRegistry("relay-devices", "host-"+r.sessionID)
	if err != nil {
		return nil // não persistido ainda
	}
	return json.Unmarshal(b, &r.devices)
}
