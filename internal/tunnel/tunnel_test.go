package tunnel

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProcessHandle simula um processo filho.
type fakeProcessHandle struct {
	mu        sync.Mutex
	waited    chan struct{}
	signaled  []os.Signal
	state     *os.ProcessState
	waitErr   error
}

func newFakeProcessHandle() *fakeProcessHandle {
	return &fakeProcessHandle{waited: make(chan struct{})}
}

func (p *fakeProcessHandle) Wait() error {
	<-p.waited
	return p.waitErr
}

func (p *fakeProcessHandle) ProcessState() *os.ProcessState { return p.state }
func (p *fakeProcessHandle) Signal(sig os.Signal) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.signaled = append(p.signaled, sig)
	// se sinalizou interrupção, completa o wait
	if sig == os.Interrupt || sig == os.Kill {
		close(p.waited)
	}
	return nil
}

func (p *fakeProcessHandle) finish(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waitErr == nil {
		p.waitErr = err
	}
	select {
	case <-p.waited:
	default:
		close(p.waited)
	}
}

// fakeRunner captura comandos e devolve handles configuráveis.
type fakeRunner struct {
	mu       sync.Mutex
	lookErr  error
	startErr error
	started  []struct {
		name string
		args []string
	}
	handle *fakeProcessHandle
}

func (f *fakeRunner) Start(ctx context.Context, name string, args ...string) (ProcessHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return nil, f.startErr
	}
	f.started = append(f.started, struct {
		name string
		args []string
	}{name: name, args: args})
	if f.handle == nil {
		f.handle = newFakeProcessHandle()
	}
	return f.handle, nil
}

func (f *fakeRunner) LookPath(file string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lookErr != nil {
		return "", f.lookErr
	}
	return "/opt/homebrew/bin/" + file, nil
}

func (f *fakeRunner) calls() []struct{ name string; args []string } {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]struct{ name string; args []string }, len(f.started))
	copy(out, f.started)
	return out
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	assert.False(t, cfg.Enabled)
	assert.Equal(t, DefaultName, cfg.Name)
	assert.Equal(t, "", cfg.Hostname)
	assert.Equal(t, DefaultURL, cfg.URL)
}

func TestNewManagerInjectsDefaults(t *testing.T) {
	m := NewManager(Config{}, nil)
	assert.Equal(t, DefaultName, m.Config().Name)
	assert.Equal(t, "", m.Config().Hostname)
	assert.Equal(t, DefaultURL, m.Config().URL)
}

func TestStartRequiresToken(t *testing.T) {
	t.Setenv(tokenEnv, "")
	m := NewManager(Config{Mode: ModeTunnel, Token: ""}, &fakeRunner{})
	err := m.Start(context.Background())
	assert.ErrorIs(t, err, ErrTunnelDisabled)
}

func TestStartRequiresCloudflared(t *testing.T) {
	runner := &fakeRunner{lookErr: fmt.Errorf("not found")}
	m := NewManager(Config{Enabled: true, Token: "tok"}, runner)
	err := m.Start(context.Background())
	assert.ErrorIs(t, err, ErrCloudflaredMissing)
}

func TestStartDisabled(t *testing.T) {
	m := NewManager(Config{Token: "tok"}, &fakeRunner{})
	err := m.Start(context.Background())
	assert.ErrorIs(t, err, ErrTunnelDisabled)
}

func TestAccessModeLANByDefault(t *testing.T) {
	cfg := Config{}
	assert.Equal(t, ModeLAN, cfg.AccessMode())
	assert.False(t, cfg.Enabled)
}

func TestAccessModeTunnelWhenEnabledWithToken(t *testing.T) {
	cfg := Config{Enabled: true, Token: "tok"}
	assert.Equal(t, ModeTunnel, cfg.AccessMode())
	assert.True(t, cfg.Enabled)
}

func TestAccessModeHostedFromURL(t *testing.T) {
	cfg := Config{HostedURL: "https://relay.example.com"}
	assert.Equal(t, ModeHosted, cfg.AccessMode())
	assert.False(t, cfg.Enabled)
}

func TestAccessModeTokenOnlyDoesNotForceTunnel(t *testing.T) {
	cfg := Config{Token: "tok"}
	assert.Equal(t, ModeLAN, cfg.AccessMode())
	assert.False(t, cfg.Enabled)
}

func TestStartSuccess(t *testing.T) {
	runner := &fakeRunner{}
	m := NewManager(Config{Enabled: true, Token: "tok", Name: "relay-diego", URL: DefaultURL}, runner)
	err := m.Start(context.Background())
	require.NoError(t, err)
	assert.True(t, m.Running())

	calls := runner.calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "/opt/homebrew/bin/cloudflared", calls[0].name)
	assert.Equal(t, []string{"tunnel", "run", "--token", "tok", "--url", DefaultURL, "--metrics", "localhost:0"}, calls[0].args)

	st := m.Status()
	assert.True(t, st.Running)
	assert.Equal(t, "relay-diego", st.Name)

	runner.handle.finish(nil)
	require.Eventually(t, func() bool { return !m.Running() }, time.Second, 10*time.Millisecond)
}

func TestStartAlreadyRunning(t *testing.T) {
	runner := &fakeRunner{}
	m := NewManager(Config{Enabled: true, Token: "tok"}, runner)
	require.NoError(t, m.Start(context.Background()))
	err := m.Start(context.Background())
	assert.ErrorIs(t, err, ErrAlreadyRunning)
}

func TestStop(t *testing.T) {
	runner := &fakeRunner{}
	m := NewManager(Config{Enabled: true, Token: "tok"}, runner)
	require.NoError(t, m.Start(context.Background()))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, m.Stop(ctx))
	assert.False(t, m.Running())

	handle := runner.handle
	require.Len(t, handle.signaled, 1)
	assert.Equal(t, os.Interrupt, handle.signaled[0])
}

func TestStopNotRunning(t *testing.T) {
	m := NewManager(Config{Enabled: true, Token: "tok"}, &fakeRunner{})
	err := m.Stop(context.Background())
	assert.ErrorIs(t, err, ErrNotRunning)
}

func TestResolveToken(t *testing.T) {
	_, err := ResolveToken(DefaultConfig())
	assert.ErrorIs(t, err, ErrTokenMissing)

	t.Setenv(tokenEnv, "envtok")
	tok, err := ResolveToken(DefaultConfig())
	require.NoError(t, err)
	assert.Equal(t, "envtok", tok)

	tok, err = ResolveToken(Config{Token: "cfgtok"})
	require.NoError(t, err)
	assert.Equal(t, "cfgtok", tok)
}

func TestSetConfig(t *testing.T) {
	m := NewManager(DefaultConfig(), nil)
	m.SetConfig(Config{Name: "x", Hostname: "y", URL: "z"})
	cfg := m.Config()
	assert.Equal(t, "x", cfg.Name)
	assert.Equal(t, "y", cfg.Hostname)
	assert.Equal(t, "z", cfg.URL)
}
