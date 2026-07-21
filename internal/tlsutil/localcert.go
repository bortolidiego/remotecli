// Package tlsutil gera e carrega certificado TLS local para o agente Relay na LAN.
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// EnsureLocalCert garante cert/key em dir e retorna o par carregado.
// Certificado autoassinado com SAN para localhost, 127.0.0.1 e IPs da LAN.
func EnsureLocalCert(dir string) (tls.Certificate, error) {
	if dir == "" {
		dir = CertDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, err
	}
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if fileExists(certPath) && fileExists(keyPath) {
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err == nil && certCoversCurrentIPs(cert) {
			return cert, nil
		}
		// IP da LAN mudou: regenera SAN com IPs atuais.
		_ = os.Remove(certPath)
		_ = os.Remove(keyPath)
	}
	if err := generateSelfSigned(certPath, keyPath); err != nil {
		return tls.Certificate{}, err
	}
	return tls.LoadX509KeyPair(certPath, keyPath)
}

// CertDir retorna o diretório padrão dos certificados.
func CertDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".relay", "tls")
	}
	return filepath.Join(home, ".relay", "tls")
}

// DescribePaths mensagem amigável com paths do cert.
func DescribePaths() string {
	d := CertDir()
	return fmt.Sprintf("%s/cert.pem", d)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}


func certCoversCurrentIPs(cert tls.Certificate) bool {
	if len(cert.Certificate) == 0 {
		return false
	}
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return false
	}
	have := map[string]bool{}
	for _, ip := range parsed.IPAddresses {
		have[ip.String()] = true
	}
	for _, ip := range collectIPs() {
		if ip.To4() != nil && !ip.IsLoopback() && !have[ip.String()] {
			return false
		}
	}
	return true
}

func generateSelfSigned(certPath, keyPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"Remote CliControl Local"},
			CommonName:   "relay.local",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost", "relay.local"},
		IPAddresses:           collectIPs(),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	certOut, err := os.OpenFile(certPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		_ = certOut.Close()
		return err
	}
	_ = certOut.Close()

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	keyOut, err := os.OpenFile(keyPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		_ = keyOut.Close()
		return err
	}
	return keyOut.Close()
}

func collectIPs() []net.IP {
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ips
	}
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		if v4 := ipNet.IP.To4(); v4 != nil {
			ips = append(ips, v4)
		}
	}
	return ips
}

// InsecureTLSConfig aceita o cert autoassinado do Relay (uso exclusivo do CLI local).
func InsecureTLSConfig() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}
}
