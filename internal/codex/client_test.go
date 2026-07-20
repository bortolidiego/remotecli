package codex

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClientInitialize(t *testing.T) {
	fake := NewFakeTransport(10)
	c := NewClient(fake, nil)

	fake.OnSend(func(data []byte) {
		var req jsonRPCMessage
		require.NoError(t, json.Unmarshal(data, &req))
		if req.Method == "initialize" {
			fake.InjectMessage(mustJSON(jsonRPCMessage{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(`{"serverInfo":{"name":"codex","version":"1.0"}}`),
			}))
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, c.Initialize(ctx))
	require.True(t, c.Running())

	sent := fake.Sent()
	require.Len(t, sent, 2) // initialize + initialized notification
	var init jsonRPCMessage
	require.NoError(t, json.Unmarshal(sent[0], &init))
	require.Equal(t, "initialize", init.Method)
	require.NoError(t, c.Close(ctx))
}

func TestThreadResumeAndTurnStart(t *testing.T) {
	fake := NewFakeTransport(10)
	c := NewClient(fake, nil)

	fake.OnSend(func(data []byte) {
		var req jsonRPCMessage
		require.NoError(t, json.Unmarshal(data, &req))
		if req.Method == "initialize" {
			fake.InjectMessage(mustJSON(jsonRPCMessage{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"ok":true}`)}))
		} else if req.Method == "turn/start" {
			fake.InjectMessage(mustJSON(jsonRPCMessage{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(`{"turnId":"turn-123"}`),
			}))
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, c.Initialize(ctx))

	turnID, err := c.TurnStart(ctx, "thread-1", "diga olá")
	require.NoError(t, err)
	require.Equal(t, "turn-123", turnID)

	require.NoError(t, c.Close(ctx))
}

func TestTurnInterrupt(t *testing.T) {
	fake := NewFakeTransport(10)
	c := NewClient(fake, nil)

	fake.OnSend(func(data []byte) {
		var req jsonRPCMessage
		require.NoError(t, json.Unmarshal(data, &req))
		fake.InjectMessage(mustJSON(jsonRPCMessage{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"ok":true}`)}))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, c.Initialize(ctx))

	require.NoError(t, c.TurnInterrupt(ctx, "thread-1", "turn-123"))
	require.NoError(t, c.Close(ctx))
}

func TestApprovalRequestAndDecisionAsync(t *testing.T) {
	fake := NewFakeTransport(10)
	c := NewClient(fake, nil)

	fake.OnSend(func(data []byte) {
		var req jsonRPCMessage
		require.NoError(t, json.Unmarshal(data, &req))
		if req.Method == "initialize" {
			fake.InjectMessage(mustJSON(jsonRPCMessage{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"ok":true}`)}))
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, c.Initialize(ctx))

	params := CommandExecutionRequestApprovalParams{
		ThreadID:    "thread-1",
		TurnID:      "turn-1",
		ItemID:      "item-1",
		Command:     "cat secret.txt",
		Cwd:         "/tmp",
		Reason:      "leitura",
		StartedAtMs: time.Now().UnixMilli(),
	}
	fake.InjectMessage(mustJSON(jsonRPCMessage{
		JSONRPC: "2.0",
		ID:      42,
		Method:  "item/commandExecution/requestApproval",
		Params:  mustJSON(params),
	}))

	time.Sleep(50 * time.Millisecond)

	// Aprovação fica pendente; handler não foi passado, então não responde sozinho.
	require.Len(t, c.Approvals(), 1)
	require.Equal(t, "cat secret.txt", c.Approvals()[0].Command)

	// Decide accept explicitamente.
	require.NoError(t, c.DecideApproval(ctx, "42", DecisionAccept))

	// Após decisão, aprovação sai do pending.
	require.Len(t, c.Approvals(), 0)

	// Verifica response enviada ao app-server.
	sent := fake.Sent()
	var found bool
	for _, data := range sent {
		var m jsonRPCMessage
		if json.Unmarshal(data, &m) != nil || m.ID != 42 {
			continue
		}
		var resp CommandExecutionRequestApprovalResponse
		if json.Unmarshal(m.Result, &resp) == nil {
			require.Equal(t, DecisionAccept, resp.Decision)
			found = true
		}
	}
	require.True(t, found, "resposta de aprovação accept não encontrada")
	require.NoError(t, c.Close(ctx))
}

func TestApprovalHandlerCanStillAutoDecline(t *testing.T) {
	fake := NewFakeTransport(10)
	done := make(chan ApprovalDecision, 1)
	handler := &StaticApprovalHandler{
		Decider: func(_ context.Context, _ CommandExecutionRequestApprovalParams) (ApprovalDecision, error) {
			done <- DecisionDecline
			return DecisionDecline, nil
		},
	}
	c := NewClientWithOptions(fake, handler, false)

	fake.OnSend(func(data []byte) {
		var req jsonRPCMessage
		require.NoError(t, json.Unmarshal(data, &req))
		if req.Method == "initialize" {
			fake.InjectMessage(mustJSON(jsonRPCMessage{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"ok":true}`)}))
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, c.Initialize(ctx))

	params := CommandExecutionRequestApprovalParams{
		ThreadID:    "thread-1",
		TurnID:      "turn-1",
		ItemID:      "item-1",
		Command:     "cat secret.txt",
		Cwd:         "/tmp",
		Reason:      "leitura",
		StartedAtMs: time.Now().UnixMilli(),
	}
	fake.InjectMessage(mustJSON(jsonRPCMessage{
		JSONRPC: "2.0",
		ID:      7,
		Method:  "item/commandExecution/requestApproval",
		Params:  mustJSON(params),
	}))

	select {
	case d := <-done:
		require.Equal(t, DecisionDecline, d)
	case <-time.After(time.Second):
		t.Fatal("handler de aprovação não foi chamado")
	}

	time.Sleep(50 * time.Millisecond)
	// No modo sync (async=false) com handler, o handler remove a aprovação do pending.
	require.Len(t, c.Approvals(), 0)

	// Verifica que a resposta decline foi enviada.
	sent := fake.Sent()
	var found bool
	for _, data := range sent {
		var m jsonRPCMessage
		if json.Unmarshal(data, &m) != nil || m.ID != 7 {
			continue
		}
		var resp CommandExecutionRequestApprovalResponse
		if json.Unmarshal(m.Result, &resp) == nil {
			require.Equal(t, DecisionDecline, resp.Decision)
			found = true
		}
	}
	require.True(t, found, "resposta decline não encontrada")
	require.NoError(t, c.Close(ctx))
}

func TestExtractTurnIDNestedAndTopLevel(t *testing.T) {
	require.Equal(t, "turn-top", extractTurnID(json.RawMessage(`{"turnId":"turn-top"}`)))
	require.Equal(t, "turn-nested", extractTurnID(json.RawMessage(`{"turn":{"id":"turn-nested","status":"inProgress"}}`)))
	require.Equal(t, "turn-top", extractTurnID(json.RawMessage(`{"turnId":"turn-top","turn":{"id":"turn-nested"}}`)))
	require.Equal(t, "", extractTurnID(nil))
}

func TestTurnStartedEventFillsTurnIDFromTurnObject(t *testing.T) {
	fake := NewFakeTransport(10)
	c := NewClient(fake, nil)

	fake.OnSend(func(data []byte) {
		var req jsonRPCMessage
		require.NoError(t, json.Unmarshal(data, &req))
		if req.Method == "initialize" {
			fake.InjectMessage(mustJSON(jsonRPCMessage{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"ok":true}`)}))
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, c.Initialize(ctx))

	fake.InjectMessage(mustJSON(jsonRPCMessage{
		JSONRPC: "2.0",
		Method:  "turn/started",
		Params:  json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-live","status":"inProgress"}}`),
	}))
	time.Sleep(50 * time.Millisecond)

	info := c.ThreadInfo("thread-1")
	require.NotNil(t, info)
	require.Equal(t, "turn-live", info.TurnID)
	require.Equal(t, "busy", info.Status)

	events := c.Events()
	require.Len(t, events, 1)
	require.Equal(t, "turn-live", events[0].TurnID)
	require.NoError(t, c.Close(ctx))
}

func TestResolveDecision(t *testing.T) {
	d, err := ResolveDecision("approve")
	require.NoError(t, err)
	require.Equal(t, DecisionAccept, d)
	d, err = ResolveDecision("deny")
	require.NoError(t, err)
	require.Equal(t, DecisionDecline, d)
	_, err = ResolveDecision("other")
	require.Error(t, err)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
