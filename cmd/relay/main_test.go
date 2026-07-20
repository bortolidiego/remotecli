package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/bortolidiego/relay/internal/agent"
	"github.com/bortolidiego/relay/internal/keychain"
	"github.com/bortolidiego/relay/shared/contracts"
	"github.com/stretchr/testify/require"
)

func TestResolveLocalTokenLoadsFromStore(t *testing.T) {
	store := keychain.NewFakeStore()
	token, err := agent.LoadOrCreateLocalToken(store, "sess-cli")
	require.NoError(t, err)

	got, err := resolveLocalToken("", "sess-cli", store)
	require.NoError(t, err)
	require.Equal(t, token, got)

	got, err = resolveLocalToken("override", "sess-cli", store)
	require.NoError(t, err)
	require.Equal(t, "override", got)
}

func TestResolveLocalTokenDoesNotCreateMissingToken(t *testing.T) {
	store := keychain.NewFakeStore()
	_, err := resolveLocalToken("", "missing", store)
	require.Error(t, err)
}

func TestBuildSessionMetadataUsesTargetPID(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("MAESTRI_TERMINAL_ID", "")
	meta := buildSessionMetadata("sess", "win-1", true, 1234)
	require.Equal(t, contracts.HarnessNative, meta.Harness)
	require.Equal(t, "sess", meta.NativeSessionID)
	require.NotNil(t, meta.PID)
	require.Equal(t, 1234, *meta.PID)
	require.NotNil(t, meta.WindowID)
	require.Equal(t, "win-1", *meta.WindowID)
	require.True(t, meta.Frontmost)
}

func TestPairCommandRejectsRawOffer(t *testing.T) {
	cmd := PairCmd{Offer: `{"session_id":"sess","host_id":"host","nonce":"nonce"}`}
	require.Error(t, cmd.Run(nil))
}

func TestDefaultSessionIDResolvesEnv(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("MAESTRI_TERMINAL_ID", "")
	require.Equal(t, "default", defaultSessionID())

	t.Setenv("CODEX_THREAD_ID", "abc")
	require.Equal(t, "codex-abc", defaultSessionID())

	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("MAESTRI_TERMINAL_ID", "xyz")
	require.Equal(t, "maestri-xyz", defaultSessionID())
}

func TestResolveSessionID(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("MAESTRI_TERMINAL_ID", "")
	require.Equal(t, "foo", resolveSessionID("foo"))
	t.Setenv("CODEX_THREAD_ID", "abc")
	require.Equal(t, "bar", resolveSessionID("bar"))
	t.Setenv("CODEX_THREAD_ID", "")
	require.Equal(t, "default", resolveSessionID(""))
}

func TestEnsureAgentRunningStartsOnlyIfNeeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	started := false
	oldStart := agentStartFunc
	oldHealthy := agentHealthy
	defer func() {
		agentStartFunc = oldStart
		agentHealthy = oldHealthy
	}()
	agentStartFunc = func(selfExe, sessionID string) (*os.Process, error) {
		started = true
		return nil, nil
	}
	// health checker aponta para o test server
	agentHealthy = func(addr string) bool {
		client := http.Client{Timeout: 500 * time.Millisecond}
		resp, err := client.Get(srv.URL + "/health")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}

	didStart, err := ensureAgentRunning("relay", "test", srv.Listener.Addr().String(), time.Second)
	require.NoError(t, err)
	require.False(t, didStart)
	require.False(t, started)
}

func TestDefaultQRPath(t *testing.T) {
	require.Contains(t, defaultQRPath("sess/1"), "relay-pair-sess-1")
}

func TestPrintQRTerminal(t *testing.T) {
	// só garante que gera sem erro (saída vai pro stdout do teste)
	require.NoError(t, printQRTerminal("https://192.168.1.1:24109/?offer=test"))
}
