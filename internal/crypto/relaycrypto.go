package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"

	"golang.org/x/crypto/hkdf"
)

const (
	KeyLabelIdentity = "relay-identity-v1"
	KeyLabelSession  = "relay-session-v1"
)

// IdentityPair mantém ECDSA P-256 (assinatura) e ECDH P-256 (acordo de chave).
type IdentityPair struct {
	signingKey *ecdsa.PrivateKey
	ecdhKey    *ecdh.PrivateKey
}

// GenerateIdentity cria par de identidade com ECDSA P-256 + ECDH P-256.
func GenerateIdentity() (*IdentityPair, error) {
	signing, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("gerar chave de assinatura: %w", err)
	}
	ecdhKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("gerar chave ECDH: %w", err)
	}
	return &IdentityPair{signingKey: signing, ecdhKey: ecdhKey}, nil
}

// NewIdentityFromECDSA constroi a partir de uma chave ECDSA P-256 existente.
// A chave ECDH deve ser carregada separadamente via SetECDHKey ou LoadECDHKeyPEM.
func NewIdentityFromECDSA(signing *ecdsa.PrivateKey) (*IdentityPair, error) {
	if signing == nil || signing.Curve != elliptic.P256() {
		return nil, errors.New("esperada chave ECDSA P-256")
	}
	return &IdentityPair{signingKey: signing}, nil
}

func (id *IdentityPair) SigningKey() *ecdsa.PrivateKey { return id.signingKey }

func (id *IdentityPair) ECDHKey() *ecdh.PrivateKey { return id.ecdhKey }

// SetECDHKey permite fixar a chave ECDH carregada do Keychain.
func (id *IdentityPair) SetECDHKey(key *ecdh.PrivateKey) error {
	if key == nil || key.Curve() != ecdh.P256() {
		return errors.New("esperada chave ECDH P-256")
	}
	id.ecdhKey = key
	return nil
}

// PublicSigningKeyBytes retorna a chave pública de assinatura em formato DER PKIX.
func (id *IdentityPair) PublicSigningKeyBytes() ([]byte, error) {
	return x509.MarshalPKIXPublicKey(&id.signingKey.PublicKey)
}

// PublicECDHKeyBytes retorna a chave pública ECDH P-256 (SEC1 uncompressed).
func (id *IdentityPair) PublicECDHKeyBytes() ([]byte, error) {
	if id.ecdhKey == nil {
		return nil, errors.New("chave ECDH ausente")
	}
	return id.ecdhKey.PublicKey().Bytes(), nil
}

func FingerprintBytes(data []byte) string {
	h := sha256.Sum256(data)
	return base64.RawURLEncoding.EncodeToString(h[:12])
}

// Sign assina um payload com ECDSA P-256 + SHA-256 (DER).
func Sign(key *ecdsa.PrivateKey, payload []byte) ([]byte, error) {
	h := sha256.Sum256(payload)
	return ecdsa.SignASN1(rand.Reader, key, h[:])
}

// Verify verifica assinatura ECDSA P-256 + SHA-256.
func Verify(pubKey []byte, payload, signature []byte) error {
	pk, err := x509.ParsePKIXPublicKey(pubKey)
	if err != nil {
		return fmt.Errorf("parse public key: %w", err)
	}
	ecdsaPub, ok := pk.(*ecdsa.PublicKey)
	if !ok || ecdsaPub.Curve != elliptic.P256() {
		return errors.New("chave pública não é ECDSA P-256")
	}
	h := sha256.Sum256(payload)
	if verifyECDSASignature(ecdsaPub, h[:], signature) {
		return nil
	}
	return errors.New("assinatura inválida")
}

func verifyECDSASignature(pub *ecdsa.PublicKey, digest, signature []byte) bool {
	if ecdsa.VerifyASN1(pub, digest, signature) {
		return true
	}
	if len(signature) != 64 {
		return false
	}
	r := signature[:32]
	s := signature[32:]
	if ecdsa.Verify(pub, digest, new(big.Int).SetBytes(r), new(big.Int).SetBytes(s)) {
		return true
	}
	return false
}

// DeriveSharedSecret executa ECDH P-256 e deriva chave AES via HKDF-SHA256.
func DeriveSharedSecret(privateKey *ecdh.PrivateKey, publicKey []byte) ([]byte, error) {
	if privateKey == nil || privateKey.Curve() != ecdh.P256() {
		return nil, errors.New("chave privada ECDH P-256 necessária")
	}
	peerPub, err := ecdh.P256().NewPublicKey(publicKey)
	if err != nil {
		return nil, fmt.Errorf("parse chave pública ECDH P-256: %w", err)
	}
	secret, err := privateKey.ECDH(peerPub)
	if err != nil {
		return nil, fmt.Errorf("ecdh: %w", err)
	}
	kdf := hkdf.New(sha256.New, secret, nil, []byte(KeyLabelSession))
	key := make([]byte, 32)
	if _, err := io.ReadFull(kdf, key); err != nil {
		return nil, fmt.Errorf("hkdf: %w", err)
	}
	return key, nil
}

// EncryptAESGCM cifra com AES-256-GCM e nonce aleatório de 12 bytes.
func EncryptAESGCM(key, plaintext []byte) ([]byte, []byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nonce, nil
}

// DecryptAESGCM decifra AES-256-GCM.
func DecryptAESGCM(key, nonce, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, ciphertext, nil)
}

// EncodePrivateKeyToPEM exporta chave ECDSA privada.
func EncodePrivateKeyToPEM(key *ecdsa.PrivateKey) ([]byte, error) {
	b, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: b}), nil
}

// DecodePrivateKeyFromPEM importa chave ECDSA.
func DecodePrivateKeyFromPEM(data []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("PEM inválido")
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

// EncodeECDHKeyToPEM exporta chave ECDH P-256 privada.
func EncodeECDHKeyToPEM(key *ecdh.PrivateKey) ([]byte, error) {
	if key == nil || key.Curve() != ecdh.P256() {
		return nil, errors.New("esperada chave ECDH P-256")
	}
	b := key.Bytes()
	return pem.EncodeToMemory(&pem.Block{Type: "RELAY ECDH PRIVATE KEY", Bytes: b}), nil
}

// DecodeECDHKeyFromPEM importa chave ECDH P-256 privada.
func DecodeECDHKeyFromPEM(data []byte) (*ecdh.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("PEM inválido")
	}
	return ecdh.P256().NewPrivateKey(block.Bytes)
}

// ConstantTimeCompare compara tokens/secrets sem vazamento de timing.
func ConstantTimeCompare(a, b string) bool {
	if len(a) != len(b) {
		_ = subtle.ConstantTimeCompare([]byte(b), []byte(b))
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
