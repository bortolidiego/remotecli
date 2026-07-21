package tunnel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	// Defaults genéricos (self-host). Sem hostname de um único dono.
	DefaultName = "remotecli"
	DefaultURL  = "http://127.0.0.1:24109"

	// Modos de acesso (produto).
	ModeLAN    = "lan"    // só Wi‑Fi local (padrão)
	ModeTunnel = "tunnel" // Cloudflare Tunnel do usuário
	ModeHosted = "hosted" // serviço remoto (roadmap)

	tokenEnv         = "RELAY_TUNNEL_TOKEN"
	tokenEnvAlias    = "REMOTECLI_TUNNEL_TOKEN"
	hostedEnv        = "REMOTECLI_HOSTED_URL"
)

// ProcessRunner abstrai o exec.CommandContext para permitir fake em testes.
type ProcessRunner interface {
	Start(ctx context.Context, name string, args ...string) (ProcessHandle, error)
	LookPath(file string) (string, error)
}

// ProcessHandle abstrai *os/exec.Cmd para permitir fake em testes.
type ProcessHandle interface {
	Wait() error
	ProcessState() *os.ProcessState
	Signal(sig os.Signal) error
}

// osProcessHandle adapta *exec.Cmd para ProcessHandle.
type osProcessHandle struct {
	cmd *exec.Cmd
}

func (p *osProcessHandle) Wait() error              { return p.cmd.Wait() }
func (p *osProcessHandle) ProcessState() *os.ProcessState {
	if p.cmd == nil {
		return nil
	}
	return p.cmd.ProcessState
}
func (p *osProcessHandle) Signal(sig os.Signal) error {
	if p.cmd == nil || p.cmd.Process == nil {
		return errors.New("processo não iniciado")
	}
	return p.cmd.Process.Signal(sig)
}

// OSRunner é o ProcessRunner real que delega a exec.CommandContext.
type OSRunner struct{}

func (OSRunner) Start(ctx context.Context, name string, args ...string) (ProcessHandle, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &osProcessHandle{cmd: cmd}, nil
}

func (OSRunner) LookPath(file string) (string, error) { return exec.LookPath(file) }

// Config guarda preferências de acesso remoto (LAN / tunnel / hosted).
type Config struct {
	// Mode: lan | tunnel | hosted. Vazio = lan se !Enabled, tunnel se Enabled (compat).
	Mode     string `json:"mode,omitempty"`
	Enabled  bool   `json:"enabled"` // true quando Mode==tunnel e há token (compat com código antigo)
	Name     string `json:"name"`
	Hostname string `json:"hostname,omitempty"` // opcional; só exibição/docs (token cloudflared não exige)
	URL      string `json:"url"`               // URL local que o tunnel expõe
	Token    string `json:"token,omitempty"`
	// HostedURL: URL do serviço central (modo hosted — roadmap).
	HostedURL string `json:"hosted_url,omitempty"`
}

// Normalize preenche valores padrão e deriva Mode/Enabled.
//
// Regras:
//   - Modo vazio + Enabled=false + sem HostedURL → LAN (padrão seguro).
//   - Modo vazio + Enabled=true → tunnel.
//   - Modo vazio + HostedURL → hosted (mesmo sem Enabled).
//   - Mode==tunnel → Enabled = (Token != "").
//   - Mode==lan|hosted → Enabled=false.
//   - Enabled:true + Token → tunnel sobe.
func (c *Config) Normalize() {
	if c.Name == "" {
		c.Name = DefaultName
	}
	// Hostname NÃO é mais forçado (cada usuário define o próprio, se quiser).
	if c.URL == "" {
		c.URL = DefaultURL
	}

	// 1) Decidir o modo efetivo.
	switch strings.ToLower(strings.TrimSpace(c.Mode)) {
	case ModeLAN, ModeTunnel, ModeHosted:
		c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	case "":
		if c.HostedURL != "" {
			c.Mode = ModeHosted
		} else if c.Enabled {
			c.Mode = ModeTunnel
		} else {
			c.Mode = ModeLAN
		}
	default:
		c.Mode = ModeLAN
	}

	// 2) Normalizar Enabled conforme o modo.
	switch c.Mode {
	case ModeTunnel:
		c.Enabled = c.Token != ""
	case ModeLAN, ModeHosted:
		c.Enabled = false
	}
}

// AccessMode retorna o modo efetivo.
func (c Config) AccessMode() string {
	c.Normalize()
	return c.Mode
}

// DefaultConfig retorna LAN-only (zero config).
func DefaultConfig() Config {
	return Config{
		Mode:    ModeLAN,
		Enabled: false,
		Name:    DefaultName,
		URL:     DefaultURL,
	}
}

// Manager gerencia o processo cloudflared.
type Manager struct {
	cfg    Config
	runner ProcessRunner

	mu        sync.RWMutex
	running   bool
	cancel    context.CancelFunc
	process   ProcessHandle
	startTime time.Time
	lastError string
	startOnce sync.Once
}

// NewManager cria um novo manager. token: cfg.Token, REMOTECLI_TUNNEL_TOKEN ou RELAY_TUNNEL_TOKEN.
func NewManager(cfg Config, runner ProcessRunner) *Manager {
	if runner == nil {
		runner = OSRunner{}
	}
	if cfg.Token == "" {
		cfg.Token = os.Getenv(tokenEnvAlias)
	}
	if cfg.Token == "" {
		cfg.Token = os.Getenv(tokenEnv)
	}
	if cfg.HostedURL == "" {
		cfg.HostedURL = os.Getenv(hostedEnv)
	}
	cfg.Normalize()
	return &Manager{cfg: cfg, runner: runner}
}

// Config expõe a configuração atual.
func (m *Manager) Config() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// Status descreve o estado atual do tunnel.
type Status struct {
	Running   bool      `json:"running"`
	Name      string    `json:"name"`
	Hostname  string    `json:"hostname"`
	URL       string    `json:"url"`
	StartedAt time.Time `json:"started_at,omitempty"`
	Error     string    `json:"error,omitempty"`
	Version   string    `json:"version,omitempty"`
}

// Status retorna o estado atual do tunnel.
func (m *Manager) Status() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := Status{
		Running:  m.running,
		Name:     m.cfg.Name,
		Hostname: m.cfg.Hostname,
		URL:      m.cfg.URL,
	}
	if m.running {
		s.StartedAt = m.startTime
	}
	if m.lastError != "" {
		s.Error = m.lastError
	}
	return s
}

var (
	ErrTokenMissing       = errors.New("token do Cloudflare Tunnel não configurado (REMOTECLI_TUNNEL_TOKEN / RELAY_TUNNEL_TOKEN ou: remotecli access tunnel --token …)")
	ErrCloudflaredMissing = errors.New("cloudflared não encontrado no PATH; instale com 'brew install cloudflared'")
	ErrTunnelDisabled     = errors.New("tunnel desabilitado (modo LAN ou hosted). Use: remotecli access tunnel --token …")
	ErrAlreadyRunning     = errors.New("tunnel já está em execução")
	ErrNotRunning         = errors.New("tunnel não está em execução")
	ErrHostedNotReady     = errors.New("modo hosted ainda não está disponível; use LAN ou tunnel do usuário")
)

// Start sobe o tunnel cloudflared apontando para a URL local.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()

	if !m.cfg.Enabled {
		m.mu.Unlock()
		return ErrTunnelDisabled
	}
	if m.cfg.Token == "" {
		m.mu.Unlock()
		return ErrTokenMissing
	}
	if m.running {
		m.mu.Unlock()
		return ErrAlreadyRunning
	}

	bin, err := m.runner.LookPath("cloudflared")
	if err != nil {
		m.mu.Unlock()
		return ErrCloudflaredMissing
	}

	tunnelCtx, cancel := context.WithCancel(ctx)
	args := []string{
		"tunnel", "run",
		"--token", m.cfg.Token,
		"--url", m.cfg.URL,
		"--metrics", "localhost:0",
	}
	proc, err := m.runner.Start(tunnelCtx, bin, args...)
	if err != nil {
		cancel()
		m.mu.Unlock()
		if strings.Contains(err.Error(), "file does not exist") || strings.Contains(err.Error(), "not found") {
			return ErrCloudflaredMissing
		}
		return fmt.Errorf("falha ao iniciar cloudflared: %w", err)
	}

	m.running = true
	m.cancel = cancel
	m.process = proc
	m.startTime = time.Now()
	m.lastError = ""
	m.startOnce = sync.Once{}

	m.mu.Unlock()
	go m.monitor(proc)
	return nil
}

// Stop encerra o processo cloudflared.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.running {
		return ErrNotRunning
	}
	if m.cancel != nil {
		m.cancel()
	}
	if m.process != nil {
		_ = m.process.Signal(os.Interrupt)
		// dê um prazo curto para o processo morrer graciosamente
		done := make(chan struct{})
		go func() {
			_ = m.process.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-ctx.Done():
			_ = m.process.Signal(os.Kill)
		}
	}
	m.running = false
	m.cancel = nil
	m.process = nil
	return nil
}

// Running retorna true se o tunnel está ativo.
func (m *Manager) Running() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.running
}

func (m *Manager) monitor(proc ProcessHandle) {
	err := proc.Wait()
	m.mu.Lock()
	m.running = false
	if err != nil && m.lastError == "" {
		m.lastError = err.Error()
	}
	m.process = nil
	m.cancel = nil
	m.mu.Unlock()
}

// SetConfig atualiza a configuração; não reinicia um tunnel em execução.
func (m *Manager) SetConfig(cfg Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg.Normalize()
	m.cfg = cfg
}

// ResolveToken tenta obter o token de cfg, env ou retorna erro.
func ResolveToken(cfg Config) (string, error) {
	if cfg.Token != "" {
		return cfg.Token, nil
	}
	if v := os.Getenv(tokenEnv); v != "" {
		return v, nil
	}
	return "", ErrTokenMissing
}
