package crypto

import (
	"crypto/ecdh"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateIdentity(t *testing.T) {
	id, err := GenerateIdentity()
	require.NoError(t, err)
	require.NotNil(t, id.SigningKey())
	require.NotNil(t, id.ECDHKey())
	require.Equal(t, ecdh.P256(), id.ECDHKey().Curve())

	pub, err := id.PublicSigningKeyBytes()
	require.NoError(t, err)
	require.NotEmpty(t, pub)

	ecdhPub, err := id.PublicECDHKeyBytes()
	require.NoError(t, err)
	require.NotEmpty(t, ecdhPub)

	fp := FingerprintBytes(pub)
	require.Len(t, fp, 16)
}

func TestSignVerify(t *testing.T) {
	id, err := GenerateIdentity()
	require.NoError(t, err)
	payload := []byte("relay-test-payload")

	sig, err := Sign(id.SigningKey(), payload)
	require.NoError(t, err)

	pub, err := id.PublicSigningKeyBytes()
	require.NoError(t, err)
	require.NoError(t, Verify(pub, payload, sig))

	// assinatura inválida
	require.Error(t, Verify(pub, []byte("outro"), sig))
}

func TestDeriveAndEncrypt(t *testing.T) {
	alice, err := GenerateIdentity()
	require.NoError(t, err)
	bob, err := GenerateIdentity()
	require.NoError(t, err)

	bobECDH, err := bob.PublicECDHKeyBytes()
	require.NoError(t, err)
	aliceECDH, err := alice.PublicECDHKeyBytes()
	require.NoError(t, err)
	sharedA, err := DeriveSharedSecret(alice.ECDHKey(), bobECDH)
	require.NoError(t, err)
	sharedB, err := DeriveSharedSecret(bob.ECDHKey(), aliceECDH)
	require.NoError(t, err)
	require.Equal(t, sharedA, sharedB)
	require.Len(t, sharedA, 32)

	ct, nonce, err := EncryptAESGCM(sharedA, []byte("segredo"))
	require.NoError(t, err)

	pt, err := DecryptAESGCM(sharedB, nonce, ct)
	require.NoError(t, err)
	require.Equal(t, "segredo", string(pt))
}

func TestDeriveFailsWithWrongCurve(t *testing.T) {
	alice, err := GenerateIdentity()
	require.NoError(t, err)
	require.Error(t, alice.SetECDHKey(nil))
}

func TestConstantTimeCompare(t *testing.T) {
	require.True(t, ConstantTimeCompare("abc", "abc"))
	require.False(t, ConstantTimeCompare("abc", "abd"))
	require.False(t, ConstantTimeCompare("abc", "abcd"))
}

func TestPEMRoundTrip(t *testing.T) {
	id, err := GenerateIdentity()
	require.NoError(t, err)
	pem, err := EncodePrivateKeyToPEM(id.SigningKey())
	require.NoError(t, err)
	loaded, err := DecodePrivateKeyFromPEM(pem)
	require.NoError(t, err)
	require.Equal(t, id.SigningKey().X, loaded.X)
}

func TestECDHPEMRoundTrip(t *testing.T) {
	id, err := GenerateIdentity()
	require.NoError(t, err)
	pem, err := EncodeECDHKeyToPEM(id.ECDHKey())
	require.NoError(t, err)
	loaded, err := DecodeECDHKeyFromPEM(pem)
	require.NoError(t, err)
	require.Equal(t, id.ECDHKey().PublicKey().Bytes(), loaded.PublicKey().Bytes())
}
