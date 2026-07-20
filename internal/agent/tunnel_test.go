package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/bortolidiego/relay/internal/keychain"
	"github.com/bortolidiego/relay/internal/tunnel"
	"github.com/stretchr/testify/require"
)

// fakeProcessHandle simula um processo filho para testes.
type fakeProcessHandle struct {
	mu       sync.Mutex
	waited   chan struct{}
	signaled []os.Signal
	waitErr  error
}

func newFakeProcessHandle() *fakeProcessHandle { return &fakeProcessHandle{waited: make(chan struct{})} }

func (p *fakeProcessHandle) Wait() error              { <-p.waited; return p.waitErr }
func (p *fakeProcessHandle) ProcessState() *os.ProcessState { return nil }
func (p *fakeProcessHandle) Signal(sig os.Signal) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.signaled = append(p.signaled, sig)
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

// fakeTunnelRunner captura comandos para testes.
type fakeTunnelRunner struct {
	mu       sync.Mutex
	lookErr  error
	startErr error
	started  []struct{ name string; args []string }
	handle   *fakeProcessHandle
}

func (f *fakeTunnelRunner) Start(ctx context.Context, name string, args ...string) (tunnel.ProcessHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return nil, f.startErr
	}
	f.started = append(f.started, struct{ name string; args []string }{name: name, args: args})
	if f.handle == nil {
		f.handle = newFakeProcessHandle()
	}
	return f.handle, nil
}

func (f *fakeTunnelRunner) LookPath(file string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lookErr != nil {
		return "", f.lookErr
	}
	return "/opt/homebrew/bin/" + file, nil
}

func (f *fakeTunnelRunner) calls() []struct{ name string; args []string } {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]struct{ name string; args []string }, len(f.started))
	copy(out, f.started)
	return out
}

func TestAgentStartsTunnelWhenEnabled(t *testing.T) {
	cfg := tunnel.Config{Enabled: true, Name: "relay-diego", Token: "tok"}
	ag := startTestAgentWithTunnel(t, cfg)
	runner := ag.tunnelRunner().(*fakeTunnelRunner)
	require.Eventually(t, func() bool {
		return ag.TunnelManager().Running()
	}, time.Second, 10*time.Millisecond)
	require.Len(t, runner.calls(), 1)
	require.Equal(t, "/opt/homebrew/bin/cloudflared", runner.calls()[0].name)
	require.Contains(t, runner.calls()[0].args, "tok")

	// /api/status reflete tunnel ativo
	req, _ := http.NewRequest("GET", "http://"+ag.ListenAddr()+"/api/status", nil)
	req.Header = localHeader(ag)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body struct {
		Tunnel struct {
			Running bool `json:"running"`
		} `json:"tunnel"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.True(t, body.Tunnel.Running)
}

func TestAgentStopsTunnelOnShutdown(t *testing.T) {
	cfg := tunnel.Config{Enabled: true, Name: "relay-diego", Token: "tok"}
	ag := startTestAgentWithTunnel(t, cfg)
	require.Eventually(t, func() bool { return ag.TunnelManager().Running() }, time.Second, 10*time.Millisecond)

	runner := ag.tunnelRunner().(*fakeTunnelRunner)
	require.NoError(t, ag.Stop(context.Background()))
	require.Eventually(t, func() bool { return !ag.TunnelManager().Running() }, time.Second, 10*time.Millisecond)
	require.Len(t, runner.handle.signaled, 1)
	require.Equal(t, os.Interrupt, runner.handle.signaled[0])
}

func TestTunnelStartStopEndpoints(t *testing.T) {
	ag := startTestAgentWithTunnel(t, tunnel.Config{})
	time.Sleep(50 * time.Millisecond)

	// start sem token deve falhar
	reqStart, _ := http.NewRequest("POST", "http://"+ag.ListenAddr()+"/api/tunnel/start", nil)
	reqStart.Header = localHeader(ag)
	resp, err := http.DefaultClient.Do(reqStart)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// configura token e habilita
	cfg := tunnel.Config{Enabled: true, Name: "relay-diego", Token: "tok"}
	ag.SetTunnelConfig(cfg)

	reqStart2, _ := http.NewRequest("POST", "http://"+ag.ListenAddr()+"/api/tunnel/start", nil)
	reqStart2.Header = localHeader(ag)
	resp2, err := http.DefaultClient.Do(reqStart2)
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	require.Eventually(t, func() bool { return ag.TunnelManager().Running() }, time.Second, 10*time.Millisecond)

	// start de novo = already running
	resp3, err := http.DefaultClient.Do(reqStart2)
	require.NoError(t, err)
	defer resp3.Body.Close()
	require.Equal(t, http.StatusOK, resp3.StatusCode)

	// stop
	reqStop, _ := http.NewRequest("POST", "http://"+ag.ListenAddr()+"/api/tunnel/stop", nil)
	reqStop.Header = localHeader(ag)
	resp4, err := http.DefaultClient.Do(reqStop)
	require.NoError(t, err)
	defer resp4.Body.Close()
	require.Equal(t, http.StatusOK, resp4.StatusCode)
	require.Eventually(t, func() bool { return !ag.TunnelManager().Running() }, time.Second, 10*time.Millisecond)
}

func TestAgentStartWarnsWithoutCloudflared(t *testing.T) {
	runner := &fakeTunnelRunner{lookErr: fmt.Errorf("not found")}
	cfg := tunnel.Config{Enabled: true, Token: "tok"}
	ag, err := New(Config{
		Addr:         "127.0.0.1:0",
		DisableTLS: true,
		SessionID:    "test-sess-cloudflared-missing",
		HostName:     "test-host",
		BasePath:     t.TempDir(),
		Store:        keychain.NewFakeStore(),
		Tunnel:       cfg,
		TunnelRunner: runner,
	})
	require.NoError(t, err)
	require.NoError(t, ag.Start())
	t.Cleanup(func() { _ = ag.Stop(context.Background()) })
	require.False(t, ag.TunnelManager().Running())
}
