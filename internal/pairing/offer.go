package pairing

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/bortolidiego/relay/internal/crypto"
	"github.com/bortolidiego/relay/shared/contracts"
)

// VerifySignedOffer valida envelope, assinatura, fingerprint e expiração da oferta.
func VerifySignedOffer(env contracts.SignedEnvelope, now time.Time) (*contracts.ShareOfferPayload, error) {
	if len(env.Payload) == 0 || len(env.Signature) == 0 || len(env.SignerKey) == 0 {
		return nil, errors.New("envelope assinado incompleto")
	}
	var offer contracts.ShareOfferPayload
	if err := json.Unmarshal(env.Payload, &offer); err != nil {
		return nil, err
	}
	if offer.SessionID == "" || offer.HostID == "" || len(offer.HostKey) == 0 || len(offer.HostECDH) == 0 || offer.Nonce == "" || offer.Endpoint == "" {
		return nil, errors.New("oferta incompleta")
	}
	if now.After(offer.ExpiresAt) {
		return nil, errors.New("oferta expirada")
	}
	if !bytesEqual(env.SignerKey, offer.HostKey) {
		return nil, errors.New("assinante difere da chave do host")
	}
	if crypto.FingerprintBytes(offer.HostKey) != offer.HostID {
		return nil, errors.New("fingerprint da oferta não confere")
	}
	if err := crypto.Verify(offer.HostKey, env.Payload, env.Signature); err != nil {
		return nil, err
	}
	return &offer, nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var out byte
	for i := range a {
		out |= a[i] ^ b[i]
	}
	return out == 0
}
