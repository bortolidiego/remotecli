package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/bortolidiego/relay/internal/codex"
	"github.com/bortolidiego/relay/internal/keychain"
	"github.com/bortolidiego/relay/shared/contracts"
	"github.com/stretchr/testify/require"
)

type fakeCodexTransportFactory struct {
	transport *codex.FakeTransport
}

func newFakeCodexTransportFactory(buffered int) *fakeCodexTransportFactory {
	return &fakeCodexTransportFactory{transport: codex.NewFakeTransport(buffered)}
}

func (f *fakeCodexTransportFactory) Factory(ctx context.Context) (codex.Transport, error) {
	return f.transport, nil
}

func (f *fakeCodexTransportFactory) RespondInitialize() {
	f.transport.OnSend(func(data []byte) {
		var req jsonRPCMessageRaw
		if err := json.Unmarshal(data, &req); err != nil {
			return
		}
		if req.Method == "initialize" {
			f.transport.InjectMessage(mustJSONRaw(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"ok": true}}))
		}
	})
}

func (f *fakeCodexTransportFactory) RespondThreadResume() {
	f.transport.OnSend(func(data []byte) {
		var req jsonRPCMessageRaw
		if err := json.Unmarshal(data, &req); err != nil {
			return
		}
		if req.Method == "thread/resume" {
			f.transport.InjectMessage(mustJSONRaw(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"ok": true}}))
		}
	})
}

func (f *fakeCodexTransportFactory) RespondTurnStart(turnID string) {
	f.transport.OnSend(func(data []byte) {
		var req jsonRPCMessageRaw
		if err := json.Unmarshal(data, &req); err != nil {
			return
		}
		if req.Method == "turn/start" {
			f.transport.InjectMessage(mustJSONRaw(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"turnId": turnID}}))
		}
	})
}

func (f *fakeCodexTransportFactory) RespondInterrupt() {
	f.transport.OnSend(func(data []byte) {
		var req jsonRPCMessageRaw
		if err := json.Unmarshal(data, &req); err != nil {
			return
		}
		if req.Method == "turn/interrupt" {
			f.transport.InjectMessage(mustJSONRaw(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"ok": true}}))
		}
	})
}

// jsonRPCMessageRaw expõe campos com id genérico para o fake.
type jsonRPCMessageRaw struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
}

func mustJSONRaw(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func TestCodexTurnEndpoint(t *testing.T) {
	store := keychain.NewFakeStore()
	factory := newFakeCodexTransportFactory(10)
	factory.RespondInitialize()
	factory.RespondThreadResume()
	factory.RespondTurnStart("turn-xyz")

	codexThreadID := "thread-xyz"
	ag, err := New(Config{
		Addr:      "127.0.0.1:0",
		DisableTLS: true,
		SessionID: "codex-turn-test",
		HostName:  "test-host",
		BasePath:  t.TempDir(),
		Store:     store,
		Metadata: contracts.SessionMetadata{
			Harness:         contracts.HarnessCodex,
			NativeSessionID: "native-xyz",
			CodexThreadID:   &codexThreadID,
		},
	})
	require.NoError(t, err)
	ag.codexMu.Lock()
	ag.codexManager = codex.NewManager(factory.Factory, nil)
	ag.codexMu.Unlock()
	require.NoError(t, ag.Start())
	defer func() { _ = ag.Stop(context.Background()) }()
	time.Sleep(50 * time.Millisecond)

	sessionID := ag.registry.Sessions()[0].ID
	body, _ := json.Marshal(map[string]string{"text": "diga olá"})
	req, _ := http.NewRequest("POST", "http://"+ag.ListenAddr()+"/api/sessions/"+sessionID+"/turn", bytes.NewReader(body))
	req.Header = localHeader(ag)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var result map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	require.Equal(t, "turn-xyz", result["turn_id"])
}

func TestCodexInterruptEndpoint(t *testing.T) {
	store := keychain.NewFakeStore()
	factory := newFakeCodexTransportFactory(10)
	factory.RespondInitialize()
	factory.RespondThreadResume()
	factory.RespondTurnStart("turn-abc")
	factory.RespondInterrupt()

	codexThreadID := "thread-interrupt"
	ag, err := New(Config{
		Addr:      "127.0.0.1:0",
		DisableTLS: true,
		SessionID: "codex-interrupt-test",
		HostName:  "test-host",
		BasePath:  t.TempDir(),
		Store:     store,
		Metadata: contracts.SessionMetadata{
			Harness:         contracts.HarnessCodex,
			NativeSessionID: "native-interrupt",
			CodexThreadID:   &codexThreadID,
		},
	})
	require.NoError(t, err)
	ag.codexMu.Lock()
	ag.codexManager = codex.NewManager(factory.Factory, nil)
	ag.codexMu.Unlock()
	require.NoError(t, ag.Start())
	defer func() { _ = ag.Stop(context.Background()) }()
	time.Sleep(50 * time.Millisecond)

	sessionID := ag.registry.Sessions()[0].ID

	// primeiro starta um turno para ter turnId
	body, _ := json.Marshal(map[string]string{"text": "diga olá"})
	reqStart, _ := http.NewRequest("POST", "http://"+ag.ListenAddr()+"/api/sessions/"+sessionID+"/turn", bytes.NewReader(body))
	reqStart.Header = localHeader(ag)
	reqStart.Header.Set("Content-Type", "application/json")
	respStart, err := http.DefaultClient.Do(reqStart)
	require.NoError(t, err)
	respStart.Body.Close()
	require.Equal(t, http.StatusOK, respStart.StatusCode)

	req, _ := http.NewRequest("POST", "http://"+ag.ListenAddr()+"/api/sessions/"+sessionID+"/interrupt", nil)
	req.Header = localHeader(ag)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var result map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	require.Equal(t, "interrupted", result["status"])
}

func TestCodexEventsAndApprovalsEndpoints(t *testing.T) {
	store := keychain.NewFakeStore()
	factory := newFakeCodexTransportFactory(10)
	factory.RespondInitialize()
	factory.RespondThreadResume()

	codexThreadID := "thread-events"
	ag, err := New(Config{
		Addr:      "127.0.0.1:0",
		DisableTLS: true,
		SessionID: "codex-events-test",
		HostName:  "test-host",
		BasePath:  t.TempDir(),
		Store:     store,
		Metadata: contracts.SessionMetadata{
			Harness:         contracts.HarnessCodex,
			NativeSessionID: "native-events",
			CodexThreadID:   &codexThreadID,
		},
	})
	require.NoError(t, err)
	ag.codexMu.Lock()
	ag.codexManager = codex.NewManager(factory.Factory, nil)
	ag.codexMu.Unlock()
	require.NoError(t, ag.Start())
	defer func() { _ = ag.Stop(context.Background()) }()
	time.Sleep(50 * time.Millisecond)

	// conecta a thread (resume)
	_, err = ag.ensureCodexSession(codexThreadID)
	require.NoError(t, err)

	// injeta uma aprovação
	factory.transport.InjectMessage(mustJSONRaw(map[string]any{
		"jsonrpc": "2.0",
		"id":      7,
		"method":  "item/commandExecution/requestApproval",
		"params": map[string]any{
			"threadId":    codexThreadID,
			"turnId":      "turn-1",
			"itemId":      "item-1",
			"command":     "cat package.json",
			"cwd":         ag.BasePath(),
			"reason":      "leitura",
			"startedAtMs": time.Now().UnixMilli(),
		},
	}))
	time.Sleep(100 * time.Millisecond)

	sessionID := ag.registry.Sessions()[0].ID

	req, _ := http.NewRequest("GET", "http://"+ag.ListenAddr()+"/api/sessions/"+sessionID+"/approvals", nil)
	req.Header = localHeader(ag)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	require.Contains(t, string(body), "cat package.json")

	// decide aprovação
	decBody, _ := json.Marshal(map[string]string{"decision": "accept"})
	reqDec, _ := http.NewRequest("POST", "http://"+ag.ListenAddr()+"/api/sessions/"+sessionID+"/approvals/7", bytes.NewReader(decBody))
	reqDec.Header = localHeader(ag)
	reqDec.Header.Set("Content-Type", "application/json")
	respDec, err := http.DefaultClient.Do(reqDec)
	require.NoError(t, err)
	defer respDec.Body.Close()
	require.Equal(t, http.StatusOK, respDec.StatusCode)
}
