package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bortolidiego/relay/internal/crypto"
	"github.com/bortolidiego/relay/internal/keychain"
	"github.com/bortolidiego/relay/internal/tunnel"
	"github.com/bortolidiego/relay/shared/contracts"
	"github.com/stretchr/testify/require"
)

func startTestAgent(t *testing.T) *Agent {
	return startTestAgentWithTunnel(t, tunnel.Config{})
}

func startTestAgentWithTunnel(t *testing.T, tunCfg tunnel.Config) *Agent {
	runner := &fakeTunnelRunner{}
	ag, err := New(Config{
		Addr:         "127.0.0.1:0",
		DisableTLS: true,
		SessionID:    "test-sess-" + t.Name(),
		HostName:     "test-host",
		BasePath:     t.TempDir(),
		Store:        keychain.NewFakeStore(),
		Tunnel:       tunCfg,
		TunnelRunner: runner,
	})
	require.NoError(t, err)
	require.NoError(t, ag.Start())
	t.Cleanup(func() { _ = ag.Stop(context.Background()) })
	return ag
}

func localHeader(ag *Agent) http.Header {
	h := http.Header{}
	h.Set("X-Relay-Local-Token", ag.LocalToken())
	return h
}

func signPairRequest(t *testing.T, cid *crypto.IdentityPair, req contracts.PairRequest) contracts.PairRequest {
	challenge, err := contracts.BuildPairChallenge(req)
	require.NoError(t, err)
	sig, err := crypto.Sign(cid.SigningKey(), challenge)
	require.NoError(t, err)
	req.ClientSignature = sig
	return req
}

func TestHealthMinimal(t *testing.T) {
	ag := startTestAgent(t)
	time.Sleep(50 * time.Millisecond)
	resp, err := http.Get("http://" + ag.ListenAddr() + "/health")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	require.Contains(t, string(body), "listening")
	require.NotContains(t, string(body), "session_id")
	require.NotContains(t, string(body), "host_id")
}

func TestStatusRequiresAuth(t *testing.T) {
	ag := startTestAgent(t)
	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get("http://" + ag.ListenAddr() + "/api/status")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	req, _ := http.NewRequest("GET", "http://"+ag.ListenAddr()+"/api/status", nil)
	req.Header = localHeader(ag)
	resp2, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	body2, _ := io.ReadAll(resp2.Body)
	require.Contains(t, string(body2), "session_path")
}

func TestOfferRequiresLocalToken(t *testing.T) {
	ag := startTestAgent(t)
	time.Sleep(50 * time.Millisecond)
	resp, err := http.Post("http://"+ag.ListenAddr()+"/api/offer", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestLocalTokenPersistedInStore(t *testing.T) {
	store := keychain.NewFakeStore()
	ag1, err := New(Config{
		Addr:      "127.0.0.1:0",
		DisableTLS: true,
		SessionID: "persisted-token",
		HostName:  "test-host",
		BasePath:  t.TempDir(),
		Store:     store,
	})
	require.NoError(t, err)
	require.NotEmpty(t, ag1.LocalToken())

	ag2, err := New(Config{
		Addr:      "127.0.0.1:0",
		DisableTLS: true,
		SessionID: "persisted-token",
		HostName:  "test-host",
		BasePath:  t.TempDir(),
		Store:     store,
	})
	require.NoError(t, err)
	require.Equal(t, ag1.LocalToken(), ag2.LocalToken())

	loaded, err := LoadLocalToken(store, "persisted-token")
	require.NoError(t, err)
	require.Equal(t, ag1.LocalToken(), loaded)
}

func TestOfferAndPair(t *testing.T) {
	ag := startTestAgent(t)
	time.Sleep(50 * time.Millisecond)

	reqOffer, _ := http.NewRequest("POST", "http://"+ag.ListenAddr()+"/api/offer", nil)
	reqOffer.Header = localHeader(ag)
	resp, err := http.DefaultClient.Do(reqOffer)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var env contracts.SignedEnvelope
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&env))
	resp.Body.Close()

	var offer contracts.ShareOfferPayload
	require.NoError(t, json.Unmarshal(env.Payload, &offer))
	require.NoError(t, crypto.Verify(offer.HostKey, env.Payload, env.Signature))
	require.NotEmpty(t, offer.HostECDH)

	cid, err := crypto.GenerateIdentity()
	require.NoError(t, err)
	clientPub, err := cid.PublicSigningKeyBytes()
	require.NoError(t, err)
	clientECDH, err := cid.PublicECDHKeyBytes()
	require.NoError(t, err)

	pairReq := signPairRequest(t, cid, contracts.PairRequest{
		SessionID:  offer.SessionID,
		HostID:     offer.HostID,
		DeviceID:   "dev-web",
		Name:       "Web PWA",
		Nonce:      offer.Nonce,
		ClientKey:  clientPub,
		ClientECDH: clientECDH,
	})
	reqBody, err := json.Marshal(pairReq)
	require.NoError(t, err)
	resp2, err := http.Post("http://"+ag.ListenAddr()+"/api/pair", "application/json", &reqBuffer{data: reqBody})
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	var pairResp contracts.PairResponse
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&pairResp))

	sharedClient, err := crypto.DeriveSharedSecret(cid.ECDHKey(), offer.HostECDH)
	require.NoError(t, err)
	require.Len(t, sharedClient, 32)
}

func TestMetadataEndpointUpdatesSessionDescriptor(t *testing.T) {
	ag := startTestAgent(t)
	time.Sleep(50 * time.Millisecond)

	pid := 4242
	codex := "thread-abc"
	meta := contracts.SessionMetadata{
		Harness:         contracts.HarnessCodex,
		NativeSessionID: "native-abc",
		CodexThreadID:   &codex,
		PID:             &pid,
		Frontmost:       true,
	}
	body, err := json.Marshal(meta)
	require.NoError(t, err)
	req, _ := http.NewRequest("POST", "http://"+ag.ListenAddr()+"/api/metadata", &reqBuffer{data: body})
	req.Header = localHeader(ag)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	reqSessions, _ := http.NewRequest("GET", "http://"+ag.ListenAddr()+"/api/sessions", nil)
	reqSessions.Header = localHeader(ag)
	resp2, err := http.DefaultClient.Do(reqSessions)
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	var sessions []contracts.SessionDescriptor
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&sessions))
	require.Len(t, sessions, 1)
	require.Equal(t, contracts.HarnessCodex, sessions[0].Harness)
	require.Equal(t, "native-abc", sessions[0].NativeSessionID)
	require.Equal(t, &pid, sessions[0].PID)
	require.True(t, sessions[0].Frontmost)
	require.Equal(t, &codex, sessions[0].CodexThreadID)
}

func TestStopEndpointShutsDownAfterResponse(t *testing.T) {
	ag := startTestAgent(t)
	time.Sleep(50 * time.Millisecond)

	req, _ := http.NewRequest("POST", "http://"+ag.ListenAddr()+"/api/stop", nil)
	req.Header = localHeader(ag)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	require.Contains(t, string(body), "stopping")

	require.Eventually(t, func() bool { return !ag.Running() }, 2*time.Second, 20*time.Millisecond)
	select {
	case <-ag.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("agent Done não fechou após stop")
	}
}

type reqBuffer struct {
	data []byte
	pos  int
}

func (r *reqBuffer) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func TestReadSandbox(t *testing.T) {
	ag := startTestAgent(t)
	base := ag.BasePath()
	require.NoError(t, os.WriteFile(filepath.Join(base, "hello.txt"), []byte("world"), 0644))
	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get("http://" + ag.ListenAddr() + "/api/read?path=hello.txt")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	o, err := ag.Registry().StartOffer()
	require.NoError(t, err)
	cid, err := crypto.GenerateIdentity()
	require.NoError(t, err)
	clientPub, err := cid.PublicSigningKeyBytes()
	require.NoError(t, err)
	clientECDH, err := cid.PublicECDHKeyBytes()
	require.NoError(t, err)
	pairReq := signPairRequest(t, cid, contracts.PairRequest{
		SessionID:  o.SessionID,
		HostID:     o.HostID,
		DeviceID:   "dev-read",
		Name:       "Read",
		Nonce:      o.Nonce,
		ClientKey:  clientPub,
		ClientECDH: clientECDH,
	})
	pairResp, _, err := ag.Registry().Pair(&pairReq)
	require.NoError(t, err)

	readReq, _ := http.NewRequest("GET", "http://"+ag.ListenAddr()+"/api/read?path=hello.txt", nil)
	readReq.Header.Set("X-Relay-Device-ID", pairResp.DeviceID)
	readReq.Header.Set("X-Relay-Lease-Token", pairResp.LeaseToken)
	resp2, err := http.DefaultClient.Do(readReq)
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	body, _ := io.ReadAll(resp2.Body)
	require.Equal(t, "world", string(body))

	readReq3, _ := http.NewRequest("GET", "http://"+ag.ListenAddr()+"/api/read?path=.git/config", nil)
	readReq3.Header.Set("X-Relay-Device-ID", pairResp.DeviceID)
	readReq3.Header.Set("X-Relay-Lease-Token", pairResp.LeaseToken)
	resp3, err := http.DefaultClient.Do(readReq3)
	require.NoError(t, err)
	defer resp3.Body.Close()
	require.Equal(t, http.StatusForbidden, resp3.StatusCode)

	// lease vinculado ao device: outro device_id deve falhar
	readReq4, _ := http.NewRequest("GET", "http://"+ag.ListenAddr()+"/api/read?path=hello.txt", nil)
	readReq4.Header.Set("X-Relay-Device-ID", "outro")
	readReq4.Header.Set("X-Relay-Lease-Token", pairResp.LeaseToken)
	resp4, err := http.DefaultClient.Do(readReq4)
	require.NoError(t, err)
	defer resp4.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp4.StatusCode)
}

func TestRevokeDevice(t *testing.T) {
	ag := startTestAgent(t)
	time.Sleep(50 * time.Millisecond)
	o, err := ag.Registry().StartOffer()
	require.NoError(t, err)
	cid2, err := crypto.GenerateIdentity()
	require.NoError(t, err)
	pub2, err := cid2.PublicSigningKeyBytes()
	require.NoError(t, err)
	ecdh2, err := cid2.PublicECDHKeyBytes()
	require.NoError(t, err)
	pairReq := signPairRequest(t, cid2, contracts.PairRequest{
		SessionID: o.SessionID, HostID: o.HostID, DeviceID: "dev-x", Name: "X", Nonce: o.Nonce,
		ClientKey:  pub2,
		ClientECDH: ecdh2,
	})
	pairResp, _, err := ag.Registry().Pair(&pairReq)
	require.NoError(t, err)

	// sem local token não pode revogar
	resp, err := http.Post("http://"+ag.ListenAddr()+"/api/revoke?device_id="+pairResp.DeviceID, "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	revokeReq, _ := http.NewRequest("POST", "http://"+ag.ListenAddr()+"/api/revoke?device_id="+pairResp.DeviceID, nil)
	revokeReq.Header = localHeader(ag)
	resp2, err := http.DefaultClient.Do(revokeReq)
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	require.False(t, ag.Registry().ValidateLease(pairResp.LeaseToken, pairResp.DeviceID))
}
