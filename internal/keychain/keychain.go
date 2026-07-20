package keychain

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/bortolidiego/relay/internal/crypto"
)

// Store é a interface usada por agente e CLI. MacOS usa security CLI; fake para testes.
type Store interface {
	SaveIdentity(service, account string, key *ecdsa.PrivateKey) error
	LoadIdentity(service, account string) (*ecdsa.PrivateKey, error)
	DeleteIdentity(service, account string) error

	SaveSecret(service, account string, secret []byte) error
	LoadSecret(service, account string) ([]byte, error)
	DeleteSecret(service, account string) error

	SaveRegistry(service, account string, data []byte) error
	LoadRegistry(service, account string) ([]byte, error)
	DeleteRegistry(service, account string) error
}

// MacOSKeychain implementa Store via `/usr/bin/security`.
type MacOSKeychain struct{}

func NewMacOSKeychain() Store { return &MacOSKeychain{} }

func keychainArg(service, account string) string {
	return fmt.Sprintf("relay:%s:%s", service, account)
}

func (m *MacOSKeychain) SaveIdentity(service, account string, key *ecdsa.PrivateKey) error {
	if key == nil {
		return errors.New("chave ausente")
	}
	pem, err := crypto.EncodePrivateKeyToPEM(key)
	if err != nil {
		return err
	}
	return m.SaveSecret(service, account, pem)
}

func (m *MacOSKeychain) LoadIdentity(service, account string) (*ecdsa.PrivateKey, error) {
	pem, err := m.LoadSecret(service, account)
	if err != nil {
		return nil, err
	}
	key, err := crypto.DecodePrivateKeyFromPEM(pem)
	if err != nil {
		return nil, fmt.Errorf("decode key: %w", err)
	}
	return key, nil
}

func (m *MacOSKeychain) DeleteIdentity(service, account string) error {
	return m.DeleteSecret(service, account)
}

func (m *MacOSKeychain) SaveSecret(service, account string, secret []byte) error {
	cmd := exec.Command("/usr/bin/security", "add-generic-password",
		"-s", keychainArg(service, account),
		"-a", account,
		"-w", string(secret),
		"-U",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("keychain save secret: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (m *MacOSKeychain) LoadSecret(service, account string) ([]byte, error) {
	cmd := exec.Command("/usr/bin/security", "find-generic-password",
		"-s", keychainArg(service, account),
		"-a", account,
		"-w",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("keychain load secret: %w", err)
	}
	return out, nil
}

func (m *MacOSKeychain) DeleteSecret(service, account string) error {
	cmd := exec.Command("/usr/bin/security", "delete-generic-password",
		"-s", keychainArg(service, account),
		"-a", account,
	)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keychain delete secret: %w", err)
	}
	return nil
}

func (m *MacOSKeychain) SaveRegistry(service, account string, data []byte) error {
	return m.SaveSecret(service, account, data)
}

func (m *MacOSKeychain) LoadRegistry(service, account string) ([]byte, error) {
	return m.LoadSecret(service, account)
}

func (m *MacOSKeychain) DeleteRegistry(service, account string) error {
	return m.DeleteSecret(service, account)
}

// FakeStore mantém segredos em memória para testes.
type FakeStore struct {
	data map[string][]byte
}

func NewFakeStore() *FakeStore { return &FakeStore{data: map[string][]byte{}} }

func (f *FakeStore) SaveIdentity(service, account string, key *ecdsa.PrivateKey) error {
	if key == nil {
		return errors.New("chave ausente")
	}
	pem, err := crypto.EncodePrivateKeyToPEM(key)
	if err != nil {
		return err
	}
	return f.SaveSecret(service, account, pem)
}

func (f *FakeStore) LoadIdentity(service, account string) (*ecdsa.PrivateKey, error) {
	pem, err := f.LoadSecret(service, account)
	if err != nil {
		return nil, err
	}
	return crypto.DecodePrivateKeyFromPEM(pem)
}

func (f *FakeStore) DeleteIdentity(service, account string) error {
	return f.DeleteSecret(service, account)
}

func (f *FakeStore) SaveSecret(service, account string, secret []byte) error {
	f.data[keychainArg(service, account)] = secret
	return nil
}

func (f *FakeStore) LoadSecret(service, account string) ([]byte, error) {
	v, ok := f.data[keychainArg(service, account)]
	if !ok {
		return nil, errors.New("segredo não encontrado")
	}
	return v, nil
}

func (f *FakeStore) DeleteSecret(service, account string) error {
	delete(f.data, keychainArg(service, account))
	return nil
}

func (f *FakeStore) SaveECDH(service, account string, key *ecdh.PrivateKey) error {
	if key == nil || key.Curve() != ecdh.P256() {
		return errors.New("esperada chave ECDH P-256")
	}
	pem, err := crypto.EncodeECDHKeyToPEM(key)
	if err != nil {
		return err
	}
	return f.SaveSecret(service, account, pem)
}

func (f *FakeStore) LoadECDH(service, account string) (*ecdh.PrivateKey, error) {
	pem, err := f.LoadSecret(service, account)
	if err != nil {
		return nil, err
	}
	return crypto.DecodeECDHKeyFromPEM(pem)
}

func (f *FakeStore) SaveRegistry(service, account string, data []byte) error {
	return f.SaveSecret(service, account, data)
}

func (f *FakeStore) LoadRegistry(service, account string) ([]byte, error) {
	return f.LoadSecret(service, account)
}

func (f *FakeStore) DeleteRegistry(service, account string) error {
	return f.DeleteSecret(service, account)
}

// DefaultStore escolhe MacOSKeychain se `/usr/bin/security` existir, senão fake.
func DefaultStore() Store {
	if _, err := exec.LookPath("/usr/bin/security"); err == nil {
		return NewMacOSKeychain()
	}
	return NewFakeStore()
}
