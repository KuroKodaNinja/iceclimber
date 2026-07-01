// Package proxy is the egress-proxy mode (decision forthcoming): an alternative to the
// relay tier where the sandbox's own package managers reach real registries through a
// controller-run MITM proxy, exposed to the sandbox over an `ssh -R` reverse tunnel (so
// the sandbox keeps zero direct network). The proxy terminates TLS with a controller-held
// CA the sandbox trusts (installed no-root via per-tool cert env/keystores), giving it
// full-URL visibility for policy + audit while the native tools do resolution.
package proxy

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CA is the controller-held certificate authority the MITM proxy signs per-host leaf
// certificates with. The sandbox trusts the CA's public cert (never the private key,
// which never leaves the controller). Leaves are minted lazily per SNI and cached.
type CA struct {
	cert    *x509.Certificate
	key     *rsa.PrivateKey
	certPEM []byte

	mu     sync.Mutex
	leaves map[string]*tls.Certificate
}

// NewCA generates a fresh in-memory CA (used by tests and when no persistence path is
// given). The CA is valid for a year — long enough for a session, re-minted on restart.
func NewCA() (*CA, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "iceclimber egress CA", Organization: []string{"iceclimber"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{
		cert:    cert,
		key:     key,
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		leaves:  map[string]*tls.Certificate{},
	}, nil
}

// LoadOrCreateCA reuses the CA persisted at certPath/keyPath (so the sandbox's installed
// trust survives controller restarts within the CA's validity), generating and persisting
// a new one if either file is absent. The key file is written 0600.
func LoadOrCreateCA(certPath, keyPath string) (*CA, error) {
	certPEM, cerr := os.ReadFile(certPath)
	keyPEM, kerr := os.ReadFile(keyPath)
	if errors.Is(cerr, fs.ErrNotExist) || errors.Is(kerr, fs.ErrNotExist) {
		ca, err := NewCA()
		if err != nil {
			return nil, err
		}
		if err := ca.persist(certPath, keyPath); err != nil {
			return nil, err
		}
		return ca, nil
	}
	if cerr != nil {
		return nil, cerr
	}
	if kerr != nil {
		return nil, kerr
	}
	certBlock, _ := pem.Decode(certPEM)
	keyBlock, _ := pem.Decode(keyPEM)
	if certBlock == nil || keyBlock == nil {
		return nil, fmt.Errorf("egress CA files are not valid PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, err
	}
	return &CA{cert: cert, key: key, certPEM: certPEM, leaves: map[string]*tls.Certificate{}}, nil
}

func (c *CA) persist(certPath, keyPath string) error {
	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(certPath, c.certPEM, 0o644); err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(c.key)})
	return os.WriteFile(keyPath, keyPEM, 0o600)
}

// CertPEM returns the CA's public certificate (what the sandbox trusts). Safe to share;
// the private key stays on the controller.
func (c *CA) CertPEM() []byte { return c.certPEM }

// leafFor returns a server certificate for host, minted and cached on first use.
func (c *CA) leafFor(host string) (*tls.Certificate, error) {
	if host == "" {
		host = "localhost"
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if leaf, ok := c.leaves[host]; ok {
		return leaf, nil
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(0, 0, 90),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	// A host reached by IP needs an IP SAN, not a DNS SAN (clients reject otherwise).
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, err
	}
	leaf := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	c.leaves[host] = leaf
	return leaf, nil
}
