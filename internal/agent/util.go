package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/bortolidiego/relay/internal/crypto"
	"github.com/bortolidiego/relay/internal/keychain"
)

// resolveStore usa cfg.Store se fornecido, senão DefaultStore.
func init() {
	_ = keychain.NewFakeStore()
	_ = crypto.GenerateIdentity
}

// CreatePWAHandler cria um handler estático para a PWA embutida no apps/web/dist.
func CreatePWAHandler(distPath string) http.Handler {
	return http.FileServer(http.Dir(distPath))
}

// LoadIdentityFromStore carrega identidade do Keychain.
func LoadIdentityFromStore(store keychain.Store, sessionID string) (*ecdsa.PrivateKey, error) {
	if store == nil {
		return nil, errors.New("store ausente")
	}
	return store.LoadIdentity("relay-identity", "host-"+sessionID)
}

// ValidateHostName normaliza e valida nome do host.
func ValidateHostName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("host_name não pode ser vazio")
	}
	if len(name) > 64 {
		return "", errors.New("host_name muito longo")
	}
	return name, nil
}

// DeriveHostFingerprint de uma chave pública.
func DeriveHostFingerprint(pub []byte) string {
	return crypto.FingerprintBytes(pub)
}

// IsCurveP256 garante que a chave é compatível.
func IsCurveP256(key *ecdsa.PrivateKey) error {
	if key == nil {
		return errors.New("chave ausente")
	}
	if key.Curve != elliptic.P256() {
		return fmt.Errorf("curva inesperada: %v", key.Curve.Params().Name)
	}
	return nil
}
