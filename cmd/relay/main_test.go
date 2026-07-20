package main

import (
	"testing"

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
