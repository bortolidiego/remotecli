package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/bortolidiego/relay/internal/crypto"
	"github.com/bortolidiego/relay/internal/keychain"
	"github.com/bortolidiego/relay/shared/contracts"
	"github.com/stretchr/testify/require"
)

func TestWebRTCSignalingRequiresLease(t *testing.T) {
	ag := startTestAgent(t)
	time.Sleep(50 * time.Millisecond)

	body, _ := json.Marshal(map[string]any{
		"device_id":   "dev",
		"lease_token": "bad",
	})
	req, _ := http.NewRequest("POST", "http://"+ag.ListenAddr()+"/api/webrtc/offer", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestWebRTCOfferAnswerFlow(t *testing.T) {
	ag := startTestAgent(t)
	time.Sleep(50 * time.Millisecond)

	// Pair a device to obtain a valid lease.
	o, err := ag.Registry().StartOffer()
	require.NoError(t, err)
	cid, err := crypto.GenerateIdentity()
	require.NoError(t, err)
	pub, err := cid.PublicSigningKeyBytes()
	require.NoError(t, err)
	ecdh, err := cid.PublicECDHKeyBytes()
	require.NoError(t, err)
	challenge, err := contracts.BuildPairChallenge(contracts.PairRequest{
		SessionID:  o.SessionID,
		HostID:     o.HostID,
		DeviceID:   "dev-wrtc",
		Name:       "WebRTC",
		Nonce:      o.Nonce,
		ClientKey:  pub,
		ClientECDH: ecdh,
	})
	require.NoError(t, err)
	sig, err := crypto.Sign(cid.SigningKey(), challenge)
	require.NoError(t, err)
	resp, _, err := ag.Registry().Pair(&contracts.PairRequest{
		SessionID:       o.SessionID,
		HostID:          o.HostID,
		DeviceID:        "dev-wrtc",
		Name:            "WebRTC",
		Nonce:           o.Nonce,
		ClientKey:       pub,
		ClientECDH:      ecdh,
		ClientSignature: sig,
	})
	require.NoError(t, err)

	// status endpoint
	statusReq, _ := http.NewRequest("GET", "http://"+ag.ListenAddr()+"/api/webrtc/status", nil)
	statusReq.Header.Set("X-Relay-Device-ID", resp.DeviceID)
	statusReq.Header.Set("X-Relay-Lease-Token", resp.LeaseToken)
	statusResp, err := http.DefaultClient.Do(statusReq)
	require.NoError(t, err)
	defer statusResp.Body.Close()
	require.Equal(t, http.StatusOK, statusResp.StatusCode)
	var status struct {
		Connected bool   `json:"connected"`
		IPCPath  string `json:"ipc_path"`
	}
	require.NoError(t, json.NewDecoder(statusResp.Body).Decode(&status))
	require.False(t, status.Connected)
	require.NotEmpty(t, status.IPCPath, "IPC path deve estar disponível")
}

func TestWebRTCStatusWithoutPeer(t *testing.T) {
	ag := startTestAgent(t)
	time.Sleep(50 * time.Millisecond)

	o, err := ag.Registry().StartOffer()
	require.NoError(t, err)
	cid, err := crypto.GenerateIdentity()
	require.NoError(t, err)
	pub, err := cid.PublicSigningKeyBytes()
	require.NoError(t, err)
	ecdh, err := cid.PublicECDHKeyBytes()
	require.NoError(t, err)
	req := signPairRequest(t, cid, contracts.PairRequest{
		SessionID:  o.SessionID,
		HostID:     o.HostID,
		DeviceID:   "dev-status",
		Name:       "Status",
		Nonce:      o.Nonce,
		ClientKey:  pub,
		ClientECDH: ecdh,
	})
	resp, _, err := ag.Registry().Pair(&req)
	require.NoError(t, err)

	statusReq, _ := http.NewRequest("GET", "http://"+ag.ListenAddr()+"/api/webrtc/status", nil)
	statusReq.Header.Set("X-Relay-Device-ID", resp.DeviceID)
	statusReq.Header.Set("X-Relay-Lease-Token", resp.LeaseToken)
	statusResp, err := http.DefaultClient.Do(statusReq)
	require.NoError(t, err)
	defer statusResp.Body.Close()
	require.Equal(t, http.StatusOK, statusResp.StatusCode)
	var status struct {
		Connected bool `json:"connected"`
	}
	require.NoError(t, json.NewDecoder(statusResp.Body).Decode(&status))
	require.False(t, status.Connected)
}

func readBody(resp *http.Response) []byte {
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	resp.Body.Close()
	return buf.Bytes()
}

func TestIPCStartedOnAgentStart(t *testing.T) {
	ag, err := New(Config{
		Addr:      "127.0.0.1:0",
		DisableTLS: true,
		SessionID: "test-ipc-" + t.Name(),
		HostName:  "test-host",
		BasePath:  t.TempDir(),
		Store:     keychain.NewFakeStore(),
	})
	require.NoError(t, err)
	require.NoError(t, ag.Start())
	defer ag.Stop(context.Background())
	time.Sleep(50 * time.Millisecond)
	require.NotEmpty(t, ag.PeerManager().IPCPath())
}
