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
	DefaultName     = "relay-diego"
	DefaultHostname = "relay.kbtech.com.br"
	DefaultURL      = "http://127.0.0.1:24109"

	tokenEnv = "RELAY_TUNNEL_TOKEN"
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

// Config guarda preferências do tunnel.
type Config struct {
	Enabled  bool   `json:"enabled"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	URL      string `json:"url"`
	Token    string `json:"token,omitempty"`
}

// Normalize preenche valores padrão.
func (c *Config) Normalize() {
	if c.Name == "" {
		c.Name = DefaultName
	}
	if c.Hostname == "" {
		c.Hostname = DefaultHostname
	}
	if c.URL == "" {
		c.URL = DefaultURL
	}
}

// DefaultConfig retorna a configuração padrão do tunnel.
func DefaultConfig() Config {
	return Config{
		Enabled:  false,
		Name:     DefaultName,
		Hostname: DefaultHostname,
		URL:      DefaultURL,
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

// NewManager cria um novo manager. token pode vir de cfg.Token, env RELAY_TUNNEL_TOKEN ou keychain futuramente.
func NewManager(cfg Config, runner ProcessRunner) *Manager {
	if runner == nil {
		runner = OSRunner{}
	}
	cfg.Normalize()
	if cfg.Token == "" {
		cfg.Token = os.Getenv(tokenEnv)
	}
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
	ErrTokenMissing       = errors.New("token do Cloudflare Tunnel não configurado (RELAY_TUNNEL_TOKEN, keychain ou setup --tunnel-token)")
	ErrCloudflaredMissing = errors.New("cloudflared não encontrado no PATH; instale com 'brew install cloudflared'")
	ErrTunnelDisabled     = errors.New("tunnel desabilitado nas preferências")
	ErrAlreadyRunning     = errors.New("tunnel já está em execução")
	ErrNotRunning         = errors.New("tunnel não está em execução")
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
