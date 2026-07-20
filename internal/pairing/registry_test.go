package pairing

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bortolidiego/relay/internal/crypto"
	"github.com/bortolidiego/relay/internal/keychain"
	"github.com/bortolidiego/relay/shared/contracts"
	"github.com/stretchr/testify/require"
)

func newTestRegistry(t *testing.T) *Registry {
	id, err := crypto.GenerateIdentity()
	require.NoError(t, err)
	r, err := NewRegistry(id, keychain.NewFakeStore(), "sess-1", "host-test", "http://127.0.0.1:24109", t.TempDir())
	require.NoError(t, err)
	return r
}

func clientIdentity(t *testing.T) (*crypto.IdentityPair, []byte, []byte) {
	cid, err := crypto.GenerateIdentity()
	require.NoError(t, err)
	sign, err := cid.PublicSigningKeyBytes()
	require.NoError(t, err)
	ecdh, err := cid.PublicECDHKeyBytes()
	require.NoError(t, err)
	return cid, sign, ecdh
}

func signedPairRequest(t *testing.T, cid *crypto.IdentityPair, req contracts.PairRequest) *contracts.PairRequest {
	challenge, err := contracts.BuildPairChallenge(req)
	require.NoError(t, err)
	sig, err := crypto.Sign(cid.SigningKey(), challenge)
	require.NoError(t, err)
	req.ClientSignature = sig
	return &req
}

func TestOfferTTL(t *testing.T) {
	r := newTestRegistry(t)
	o, err := r.StartOffer()
	require.NoError(t, err)
	require.NotNil(t, o)
	require.True(t, time.Now().Before(o.ExpiresAt))

	got, ok := r.Offer()
	require.True(t, ok)
	require.Equal(t, o.Nonce, got.Nonce)
	require.NotEmpty(t, o.HostECDH)
}

func TestPairRequiresValidOffer(t *testing.T) {
	r := newTestRegistry(t)
	_, _, ecdh := clientIdentity(t)
	_, _, err := r.Pair(&contracts.PairRequest{SessionID: "sess-1", HostID: r.HostID(), ClientECDH: ecdh})
	require.Error(t, err)
}

func TestPairLimitsDevices(t *testing.T) {
	r := newTestRegistry(t)
	for i := 0; i < MaxDevices+1; i++ {
		o, err := r.StartOffer()
		require.NoError(t, err)
		cid, pub, ecdh := clientIdentity(t)
		req := signedPairRequest(t, cid, contracts.PairRequest{
			SessionID:  "sess-1",
			HostID:     r.HostID(),
			DeviceID:   string(rune('a' + i)),
			Name:       "Device " + string(rune('A'+i)),
			ClientKey:  pub,
			ClientECDH: ecdh,
			Nonce:      o.Nonce,
		})
		_, _, err = r.Pair(req)
		if i < MaxDevices {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
	}
}

func TestNonceReplay(t *testing.T) {
	r := newTestRegistry(t)
	o, err := r.StartOffer()
	require.NoError(t, err)
	cid, pub, ecdh := clientIdentity(t)

	req := signedPairRequest(t, cid, contracts.PairRequest{
		SessionID:  "sess-1",
		HostID:     r.HostID(),
		DeviceID:   "dev-replay",
		Name:       "Replay",
		ClientKey:  pub,
		ClientECDH: ecdh,
		Nonce:      o.Nonce,
	})
	_, _, err = r.Pair(req)
	require.NoError(t, err)

	// reutilizar nonce deve falhar
	o2, err := r.StartOffer()
	require.NoError(t, err)
	_, _, err = r.Pair(&contracts.PairRequest{SessionID: "sess-1", HostID: r.HostID(), ClientECDH: ecdh, Nonce: o.Nonce})
	require.Error(t, err)
	_ = o2
}

func TestLease(t *testing.T) {
	r := newTestRegistry(t)
	o, err := r.StartOffer()
	require.NoError(t, err)
	cid, pub, ecdh := clientIdentity(t)
	resp, sess, err := r.Pair(signedPairRequest(t, cid, contracts.PairRequest{
		SessionID: "sess-1", HostID: r.HostID(), DeviceID: "dev-l", Name: "L", ClientKey: pub, ClientECDH: ecdh, Nonce: o.Nonce,
	}))
	require.NoError(t, err)
	require.NotEmpty(t, resp.LeaseToken)
	require.NotEmpty(t, resp.ServerECDH)
	require.Len(t, sess.SharedKey, 32)
	require.True(t, r.ValidateLease(resp.LeaseToken, resp.DeviceID))
	require.False(t, r.ValidateLease("invalid", resp.DeviceID))
	require.False(t, r.ValidateLease(resp.LeaseToken, "outro-device"))
}

func TestRevokeInvalidatesLease(t *testing.T) {
	r := newTestRegistry(t)
	o, err := r.StartOffer()
	require.NoError(t, err)
	cid, pub, ecdh := clientIdentity(t)
	resp, _, err := r.Pair(signedPairRequest(t, cid, contracts.PairRequest{
		SessionID: "sess-1", HostID: r.HostID(), DeviceID: "dev-r", Name: "R", ClientKey: pub, ClientECDH: ecdh, Nonce: o.Nonce,
	}))
	require.NoError(t, err)
	require.Len(t, r.Devices(), 1)
	require.True(t, r.ValidateLease(resp.LeaseToken, resp.DeviceID))
	require.True(t, r.Revoke(resp.DeviceID))
	require.Len(t, r.Devices(), 0)
	require.False(t, r.ValidateLease(resp.LeaseToken, resp.DeviceID))
}

func TestSignOffer(t *testing.T) {
	r := newTestRegistry(t)
	o, err := r.StartOffer()
	require.NoError(t, err)
	sig, err := r.SignOffer(o)
	require.NoError(t, err)
	payload, _ := json.Marshal(o)
	require.NoError(t, crypto.Verify(o.HostKey, payload, sig))

	verified, err := VerifySignedOffer(contracts.SignedEnvelope{
		Payload:   payload,
		Signature: sig,
		SignerKey: o.HostKey,
	}, time.Now())
	require.NoError(t, err)
	require.Equal(t, o.HostID, verified.HostID)
}

func TestVerifySignedOfferRejectsInvalidEnvelope(t *testing.T) {
	r := newTestRegistry(t)
	o, err := r.StartOffer()
	require.NoError(t, err)
	sig, err := r.SignOffer(o)
	require.NoError(t, err)
	payload, _ := json.Marshal(o)

	_, err = VerifySignedOffer(contracts.SignedEnvelope{Payload: payload}, time.Now())
	require.Error(t, err)

	tampered := append([]byte(nil), payload...)
	tampered[len(tampered)-2] = 'x'
	_, err = VerifySignedOffer(contracts.SignedEnvelope{Payload: tampered, Signature: sig, SignerKey: o.HostKey}, time.Now())
	require.Error(t, err)

	o.ExpiresAt = time.Now().Add(-time.Minute)
	expiredPayload, _ := json.Marshal(o)
	expiredSig, err := r.SignOffer(o)
	require.NoError(t, err)
	_, err = VerifySignedOffer(contracts.SignedEnvelope{Payload: expiredPayload, Signature: expiredSig, SignerKey: o.HostKey}, time.Now())
	require.Error(t, err)
}

func TestSharedKey(t *testing.T) {
	r := newTestRegistry(t)
	o, err := r.StartOffer()
	require.NoError(t, err)
	cid, pub, ecdh := clientIdentity(t)
	_, sess, err := r.Pair(signedPairRequest(t, cid, contracts.PairRequest{
		SessionID: "sess-1", HostID: r.HostID(), DeviceID: "dev-shared", Name: "S", ClientKey: pub, ClientECDH: ecdh, Nonce: o.Nonce,
	}))
	require.NoError(t, err)
	clientShared, err := crypto.DeriveSharedSecret(cid.ECDHKey(), o.HostECDH)
	require.NoError(t, err)
	require.Equal(t, clientShared, sess.SharedKey)
}
