package agent

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bortolidiego/relay/internal/codex"
	"github.com/bortolidiego/relay/internal/crypto"
	"github.com/bortolidiego/relay/internal/keychain"
	"github.com/bortolidiego/relay/internal/pairing"
	"github.com/bortolidiego/relay/internal/sandbox"
	"github.com/bortolidiego/relay/internal/tlsutil"
	"github.com/bortolidiego/relay/internal/tunnel"
	"github.com/bortolidiego/relay/internal/web"
	"github.com/bortolidiego/relay/shared/contracts"
)

const defaultAddr = "0.0.0.0:24109"

// Agent é o servidor local Relay.
type Agent struct {
	addr      string
	listener  net.Listener
	server    *http.Server
	mux       *http.ServeMux
	store     keychain.Store
	registry  *pairing.Registry
	sessionID string
	basePath  string
	mu        sync.RWMutex
	running   bool
	stopped   chan struct{}
	stopOnce  sync.Once

	pwaHandler  http.Handler
	localToken  string // token que permite acesso a endpoints administrativos vindos do CLI local
	peerManager *PeerManager
	tunnel         *tunnel.Manager
	tunnelConfig   tunnel.Config
	tunnelRunner_  tunnel.ProcessRunner

	codexManager *codex.Manager
	codexMu      sync.RWMutex

	// disableTLS força HTTP (apenas testes). Em produção o agente usa HTTPS local.
	disableTLS bool

	// claims: códigos curtos no QR do terminal → envelope completo (TTL da oferta).
	claimMu sync.Mutex
	claims  map[string]claimEntry
}

type claimEntry struct {
	envelope []byte
	expires  time.Time
}

// New cria o agente, carregando ou criando identidade no Keychain.
func New(cfg Config) (*Agent, error) {
	if cfg.Addr == "" {
		cfg.Addr = defaultAddr
	}
	if cfg.SessionID == "" {
		return nil, errors.New("session_id é obrigatório")
	}
	if cfg.BasePath == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		cfg.BasePath = cwd
	}
	s, err := resolveStore(cfg.Store)
	if err != nil {
		return nil, err
	}
	identity, err := loadOrCreateIdentity(s, cfg.SessionID)
	if err != nil {
		return nil, err
	}
	registry, err := pairing.NewRegistry(identity, s, cfg.SessionID, cfg.HostName, pairing.LANEndpoint(cfg.Addr), cfg.BasePath)
	if err != nil {
		return nil, err
	}
	if cfg.Metadata.NativeSessionID != "" || cfg.Metadata.Harness != "" || cfg.Metadata.PID != nil || cfg.Metadata.MaestriTerminalID != nil || cfg.Metadata.CodexThreadID != nil || cfg.Metadata.WindowID != nil || cfg.Metadata.Frontmost {
		registry.SetSessionMetadata(cfg.Metadata)
	}
	localToken, err := LoadOrCreateLocalToken(s, cfg.SessionID)
	if err != nil {
		return nil, err
	}

	tun := tunnel.NewManager(cfg.Tunnel, cfg.TunnelRunner)
	a := &Agent{
		addr:         cfg.Addr,
		store:        s,
		registry:     registry,
		sessionID:    cfg.SessionID,
		basePath:     cfg.BasePath,
		localToken:   localToken,
		disableTLS:    cfg.DisableTLS,
		stopped:       make(chan struct{}),
		tunnel:        tun,
		tunnelConfig:  cfg.Tunnel,
		tunnelRunner_: cfg.TunnelRunner,
		claims:        map[string]claimEntry{},
	}
	peerMgr, err := NewPeerManager(a)
	if err != nil {
		return nil, err
	}
	a.peerManager = peerMgr
	a.initCodexManager()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", a.handleHealth)
	mux.HandleFunc("/api/offer", a.requireLocal(a.handleOffer))
	// Público (pairing-only): troca código curto do QR por envelope assinado.
	mux.HandleFunc("/api/claim", a.handleClaim)
	mux.HandleFunc("/api/metadata", a.requireLocal(a.handleMetadata))
	mux.HandleFunc("/api/stop", a.requireLocal(a.handleStop))
	mux.HandleFunc("/api/tunnel/start", a.requireLocal(a.handleTunnelStart))
	mux.HandleFunc("/api/tunnel/stop", a.requireLocal(a.handleTunnelStop))
	mux.HandleFunc("/api/pair", a.handlePair)
	mux.HandleFunc("/api/status", a.requireAuth(a.handleStatus))
	mux.HandleFunc("/api/devices", a.requireAuth(a.handleDevices))
	mux.HandleFunc("/api/sessions", a.requireAuth(a.handleSessions))
	a.registerCodexRoutes(mux)
	mux.HandleFunc("/api/lease/release", a.handleLeaseRelease) // sem requireAuth: release é idempotente
	mux.HandleFunc("/api/revoke", a.handleRevoke)
	mux.HandleFunc("/api/read", a.requireLease(a.handleRead))
	mux.Handle("/", web.Handler())

		a.mux = mux
		a.RegisterWebRTCRoutes(peerMgr)
		a.server = &http.Server{
			Addr:              cfg.Addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		}
		return a, nil
}

func resolveStore(store keychain.Store) (keychain.Store, error) {
	if store != nil {
		return store, nil
	}
	return keychain.DefaultStore(), nil
}

func loadOrCreateIdentity(store keychain.Store, sessionID string) (*crypto.IdentityPair, error) {
	acc := "host-" + sessionID
	signing, err := store.LoadIdentity("relay-identity", acc)
	signingLoaded := err == nil && signing != nil

	ecdhKey, errECDH := store.LoadSecret("relay-ecdh", acc)
	ecdhLoaded := errECDH == nil && len(ecdhKey) > 0

	if signingLoaded && ecdhLoaded {
		id, err := crypto.NewIdentityFromECDSA(signing)
		if err != nil {
			return nil, err
		}
		loadedECDH, err := crypto.DecodeECDHKeyFromPEM(ecdhKey)
		if err != nil {
			return nil, fmt.Errorf("decode ecdh: %w", err)
		}
		if err := id.SetECDHKey(loadedECDH); err != nil {
			return nil, err
		}
		return id, nil
	}

	id, err := crypto.GenerateIdentity()
	if err != nil {
		return nil, err
	}
	if err := store.SaveIdentity("relay-identity", acc, id.SigningKey()); err != nil {
		return nil, fmt.Errorf("salvar identidade: %w", err)
	}
	ecdhPEM, err := crypto.EncodeECDHKeyToPEM(id.ECDHKey())
	if err != nil {
		return nil, err
	}
	if err := store.SaveSecret("relay-ecdh", acc, ecdhPEM); err != nil {
		return nil, fmt.Errorf("salvar ecdh: %w", err)
	}
	return id, nil
}

// LoadOrCreateLocalToken carrega ou cria o segredo local administrativo da sessão.
func LoadOrCreateLocalToken(store keychain.Store, sessionID string) (string, error) {
	if store == nil {
		return "", errors.New("store obrigatório")
	}
	account := "host-" + sessionID
	if token, err := LoadLocalToken(store, sessionID); err == nil {
		return token, nil
	}
	token, err := generateLocalToken()
	if err != nil {
		return "", err
	}
	if err := store.SaveSecret("relay-local-token", account, []byte(token)); err != nil {
		return "", err
	}
	return token, nil
}

// LoadLocalToken retorna o segredo local administrativo já persistido.
func LoadLocalToken(store keychain.Store, sessionID string) (string, error) {
	if store == nil {
		return "", errors.New("store obrigatório")
	}
	b, err := store.LoadSecret("relay-local-token", "host-"+sessionID)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return "", errors.New("token local vazio")
	}
	return token, nil
}

func generateLocalToken() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Config para inicialização do agente.
type Config struct {
	Addr          string
	SessionID     string
	HostName      string
	BasePath      string
	Store         keychain.Store
	Metadata      contracts.SessionMetadata
	Tunnel        tunnel.Config
	TunnelRunner  tunnel.ProcessRunner
	// DisableTLS usa HTTP puro (testes unitários). O serve de produção usa HTTPS.
	DisableTLS bool
}

func (a *Agent) ListenAddr() string {
	if a.listener != nil {
		return a.listener.Addr().String()
	}
	return a.addr
}

func (a *Agent) BasePath() string { return a.basePath }

func (a *Agent) SessionID() string { return a.sessionID }

func (a *Agent) Registry() *pairing.Registry { return a.registry }

func (a *Agent) PeerManager() *PeerManager { return a.peerManager }

func (a *Agent) LocalToken() string { return a.localToken }

func (a *Agent) Done() <-chan struct{} { return a.stopped }

func (a *Agent) TunnelManager() *tunnel.Manager { return a.tunnel }

func (a *Agent) tunnelRunner() tunnel.ProcessRunner {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.tunnelRunner_
}

// SetTunnelConfig atualiza preferências do tunnel; não reinicia um tunnel em execução.
func (a *Agent) SetTunnelConfig(cfg tunnel.Config) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tunnelConfig = cfg
	if a.tunnel != nil {
		a.tunnel.SetConfig(cfg)
	}
}

func (a *Agent) TunnelConfig() tunnel.Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.tunnelConfig
}

// Start inicia o agente em background. Se o tunnel estiver habilitado e tiver token, inicia-o também.
// Por padrão serve HTTPS com certificado local em ~/.relay/tls (necessário para WebCrypto no iPhone).
func (a *Agent) Start() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return errors.New("agente já em execução")
	}
	lis, err := net.Listen("tcp", a.addr)
	if err != nil {
		return err
	}
	if !a.disableTLS {
		cert, err := tlsutil.EnsureLocalCert("")
		if err != nil {
			_ = lis.Close()
			return fmt.Errorf("certificado TLS local: %w", err)
		}
		tlsCfg := &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
		lis = tls.NewListener(lis, tlsCfg)
		log.Printf("relay TLS ativo (cert: %s)", tlsutil.DescribePaths())
	}
	a.listener = lis
	a.running = true
	go func() {
		if err := a.server.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("agent serve error: %v", err)
		}
	}()
	if _, err := a.peerManager.StartIPC(""); err != nil {
		log.Printf("ipc start warning: %v", err)
	}
	if a.tunnel != nil && a.tunnelConfig.Enabled {
		if _, err := tunnel.ResolveToken(a.tunnelConfig); err == nil {
			if err := a.tunnel.Start(context.Background()); err != nil && !errors.Is(err, tunnel.ErrAlreadyRunning) {
				if errors.Is(err, tunnel.ErrCloudflaredMissing) {
					log.Printf("tunnel start warning: cloudflared não encontrado no PATH")
				} else if errors.Is(err, tunnel.ErrTokenMissing) {
					log.Printf("tunnel start warning: token do tunnel não configurado")
				} else {
					log.Printf("tunnel start warning: %v", err)
				}
			}
		}
	}
	return nil
}

// Stop finaliza o agente e o tunnel.
func (a *Agent) Stop(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.running {
		return nil
	}
	a.running = false
	if a.tunnel != nil {
		_ = a.tunnel.Stop(ctx)
	}
	if a.peerManager != nil {
		_ = a.peerManager.Stop()
	}
	err := a.server.Shutdown(ctx)
	a.stopOnce.Do(func() { close(a.stopped) })
	return err
}

func (a *Agent) Running() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.running
}

// SetPWAHandler permite servir a PWA embarcada (dev placeholder por padrão).
func (a *Agent) SetPWAHandler(h http.Handler) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pwaHandler = h
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return errors.New("body vazio")
	}
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(v)
}

func (a *Agent) requireLocal(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !crypto.ConstantTimeCompare(r.Header.Get("X-Relay-Local-Token"), a.localToken) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "local token required"})
			return
		}
		next(w, r)
	}
}

func (a *Agent) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// autenticação local via header OU lease+device vinculado
		if crypto.ConstantTimeCompare(r.Header.Get("X-Relay-Local-Token"), a.localToken) {
			next(w, r)
			return
		}
		deviceID := r.Header.Get("X-Relay-Device-ID")
		lease := r.Header.Get("X-Relay-Lease-Token")
		if a.registry.ValidateLease(lease, deviceID) {
			next(w, r)
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "autenticação necessária"})
	}
}

func (a *Agent) requireLease(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		deviceID := r.Header.Get("X-Relay-Device-ID")
		lease := r.Header.Get("X-Relay-Lease-Token")
		if !a.registry.ValidateLease(lease, deviceID) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "lease inválido ou expirado"})
			return
		}
		next(w, r)
	}
}

func (a *Agent) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, contracts.AgentStatus{
		Listening: a.Running(),
		Address:   a.ListenAddr(),
		Version:   "relay-m2",
	})
}

func (a *Agent) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		a.mu.RLock()
		h := a.pwaHandler
		a.mu.RUnlock()
		if h != nil {
			h.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html>
<html><head><title>Relay Agent</title></head>
<body><h1>Relay Agent (Marco 2)</h1>
<p>PWA embarcada indisponível neste build.</p>
</body></html>`))
		return
	}
	http.NotFound(w, r)
}

func (a *Agent) handleOffer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	offer, err := a.registry.StartOffer()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	sig, err := a.registry.SignOffer(offer)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	env := contracts.SignedEnvelope{
		Payload:   mustJSON(offer),
		Signature: sig,
		SignerKey: offer.HostKey,
	}
	raw, err := json.Marshal(env)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	code, err := a.putClaim(raw, offer.ExpiresAt)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"payload":    env.Payload,
		"signature":  env.Signature,
		"signer_key": env.SignerKey,
		"claim_code": code,
	})
}

// handleClaim é público: devolve o envelope assinado a partir do código curto do QR.
func (a *Agent) handleClaim(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("c"))
	if code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "código c obrigatório"})
		return
	}
	raw, ok := a.takeClaim(code)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "código inválido ou expirado"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (a *Agent) putClaim(envelope []byte, expires time.Time) (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 6)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	code := make([]byte, 6)
	for i := range b {
		code[i] = alphabet[int(b[i])%len(alphabet)]
	}
	s := string(code)
	a.claimMu.Lock()
	a.claims[s] = claimEntry{envelope: append([]byte(nil), envelope...), expires: expires}
	// limpa expirados
	now := time.Now()
	for k, v := range a.claims {
		if now.After(v.expires) {
			delete(a.claims, k)
		}
	}
	a.claimMu.Unlock()
	return s, nil
}

func (a *Agent) takeClaim(code string) ([]byte, bool) {
	a.claimMu.Lock()
	defer a.claimMu.Unlock()
	e, ok := a.claims[code]
	if !ok || time.Now().After(e.expires) {
		delete(a.claims, code)
		return nil, false
	}
	// one-time
	delete(a.claims, code)
	return e.envelope, true
}

func (a *Agent) handleMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	var meta contracts.SessionMetadata
	if err := readJSON(r, &meta); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	a.registry.SetSessionMetadata(meta)
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (a *Agent) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopping"})
	go func() {
		time.Sleep(50 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = a.Stop(ctx)
	}()
}

func (a *Agent) handleTunnelStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	if a.tunnel == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "tunnel manager indisponível"})
		return
	}
	if _, err := tunnel.ResolveToken(a.tunnel.Config()); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	err := a.tunnel.Start(context.Background())
	if err != nil {
		if errors.Is(err, tunnel.ErrAlreadyRunning) {
			writeJSON(w, http.StatusOK, map[string]string{"status": "already running"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
}

func (a *Agent) handleTunnelStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	if a.tunnel == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "tunnel manager indisponível"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := a.tunnel.Stop(ctx)
	if err != nil {
		if errors.Is(err, tunnel.ErrNotRunning) {
			writeJSON(w, http.StatusOK, map[string]string{"status": "not running"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (a *Agent) handlePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	var req contracts.PairRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	resp, sess, err := a.registry.Pair(&req)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	if sess != nil {
		a.registry.RegisterSession(sess)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *Agent) handleStatus(w http.ResponseWriter, r *http.Request) {
	status := a.currentAgentStatus()
	if crypto.ConstantTimeCompare(r.Header.Get("X-Relay-Local-Token"), a.localToken) {
		full := contracts.AuthenticatedStatus{
			AgentStatus: status,
			SessionID:   a.sessionID,
			SessionPath: a.basePath,
			Devices:     a.registry.Devices(),
			Sessions:    a.sessionList(),
		}
		writeJSON(w, http.StatusOK, full)
		return
	}
	deviceID := r.Header.Get("X-Relay-Device-ID")
	if dev, ok := a.registry.GetDevice(deviceID); ok {
		full := contracts.AuthenticatedStatus{
			AgentStatus: status,
			SessionID:   a.sessionID,
			SessionPath: a.basePath,
			Devices:     []contracts.DeviceInfo{dev},
			Sessions:    a.sessionList(),
		}
		writeJSON(w, http.StatusOK, full)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (a *Agent) currentAgentStatus() contracts.AgentStatus {
	ts := contracts.TunnelStatus{Enabled: a.tunnelConfig.Enabled}
	if a.tunnel != nil {
		st := a.tunnel.Status()
		ts.Enabled = a.tunnelConfig.Enabled
		ts.Running = st.Running
		ts.Name = st.Name
		ts.Hostname = st.Hostname
		ts.URL = st.URL
		ts.StartedAt = st.StartedAt
		ts.Error = st.Error
	}
	return contracts.AgentStatus{
		Listening: a.Running(),
		Address:   a.ListenAddr(),
		Version:   "relay-m2",
		Paired:    a.registry.HasDevices(),
		Tunnel:    ts,
	}
}

func (a *Agent) sessionList() []contracts.SessionDescriptor {
	return a.registry.Sessions()
}

func (a *Agent) handleDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, a.registry.Devices())
}

func (a *Agent) handleRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	deviceID := r.URL.Query().Get("device_id")
	if deviceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "device_id obrigatório"})
		return
	}
	// somente host local pode revogar
	if !crypto.ConstantTimeCompare(r.Header.Get("X-Relay-Local-Token"), a.localToken) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "revoke requer local token"})
		return
	}
	if a.registry.Revoke(deviceID) {
		a.peerManager.ReleasePeer(deviceID)
		writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "dispositivo não encontrado"})
}

func (a *Agent) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, a.sessionList())
}

func (a *Agent) handleSessionDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	id = strings.TrimSpace(id)
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session_id obrigatório"})
		return
	}
	sess, ok := a.registry.SessionByID(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "sessão não encontrada"})
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (a *Agent) handleLeaseRelease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	// Idempotente: se o lease já morreu (agente reiniciado / expirado), ainda responde OK
	// para o celular poder limpar o estado local sem ficar preso.
	deviceID := r.Header.Get("X-Relay-Device-ID")
	lease := r.Header.Get("X-Relay-Lease-Token")
	if a.registry.ValidateLease(lease, deviceID) {
		_ = a.registry.ReleaseLease(deviceID)
		if a.peerManager != nil {
			a.peerManager.ReleasePeer(deviceID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "released"})
}

func (a *Agent) handleRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}
	deviceID := r.Header.Get("X-Relay-Device-ID")
	rel := r.URL.Query().Get("path")
	rel, err := sandbox.NormalizeCoord(rel)
	if err != nil || rel == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path obrigatório ou inválido"})
		return
	}
	data, err := sandbox.ReadFile(a.basePath, rel)
	if err != nil {
		if strings.Contains(err.Error(), "bloqueado") || strings.Contains(err.Error(), "fora") {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "não encontrado"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Relay-Path", filepath.ToSlash(rel))
	w.Header().Set("X-Relay-Device", deviceID)
	_, _ = w.Write(data)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
