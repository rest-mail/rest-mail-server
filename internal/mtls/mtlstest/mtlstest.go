// Package mtlstest mints internal-mTLS certificate material for tests. It is a
// normal (importable) package, like net/http/httptest, so mTLS tests across
// packages share one trustworthy cert factory instead of re-deriving x509
// boilerplate. It has no dependency on the testing package.
package mtlstest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// CA is a generated certificate authority that can issue server and client
// leaf certificates for tests.
type CA struct {
	Cert    *x509.Certificate
	Key     *ecdsa.PrivateKey
	CertPEM []byte
}

func serial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

func encodeKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

// NewCA creates a new self-signed CA with the given common name.
func NewCA(cn string) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	sn, err := serial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          sn,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
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
		Cert:    cert,
		Key:     key,
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}, nil
}

func (ca *CA) issue(tmpl *x509.Certificate) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	sn, err := serial()
	if err != nil {
		return nil, nil, err
	}
	tmpl.SerialNumber = sn
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err = encodeKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), keyPEM, nil
}

// IssueServer issues a ServerAuth leaf with the given DNS/IP SANs and validity
// window.
func (ca *CA) IssueServer(cn string, dnsNames []string, ips []net.IP, notBefore, notAfter time.Time) (certPEM, keyPEM []byte, err error) {
	return ca.issue(&x509.Certificate{
		Subject:     pkix.Name{CommonName: cn},
		DNSNames:    dnsNames,
		IPAddresses: ips,
		NotBefore:   notBefore,
		NotAfter:    notAfter,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
}

// IssueClient issues a ClientAuth leaf (a gateway machine identity) with the
// given validity window. Pass a past notAfter to mint an expired cert.
func (ca *CA) IssueClient(cn string, notBefore, notAfter time.Time) (certPEM, keyPEM []byte, err error) {
	return ca.issue(&x509.Certificate{
		Subject:     pkix.Name{CommonName: cn},
		NotBefore:   notBefore,
		NotAfter:    notAfter,
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
}

// Paths are the on-disk paths of a written trust domain.
type Paths struct {
	CACert     string
	ServerCert string
	ServerKey  string
	ClientCert string
	ClientKey  string
}

// Write mints a CA + valid server cert (SANs = serverHosts, either DNS names or
// IPs, auto-detected) + valid client cert into dir and returns their paths. It
// is the common-case fixture for tests that just need a working trust domain.
func Write(dir string, serverHosts ...string) (*Paths, error) {
	ca, err := NewCA("mtlstest CA")
	if err != nil {
		return nil, err
	}
	var dns []string
	var ips []net.IP
	for _, h := range serverHosts {
		if ip := net.ParseIP(h); ip != nil {
			ips = append(ips, ip)
		} else {
			dns = append(dns, h)
		}
	}
	now := time.Now()
	srvCert, srvKey, err := ca.IssueServer("mtlstest server", dns, ips, now.Add(-time.Hour), now.Add(24*time.Hour))
	if err != nil {
		return nil, err
	}
	cliCert, cliKey, err := ca.IssueClient("mtlstest client", now.Add(-time.Hour), now.Add(24*time.Hour))
	if err != nil {
		return nil, err
	}
	p := &Paths{
		CACert:     filepath.Join(dir, "ca.crt"),
		ServerCert: filepath.Join(dir, "server.crt"),
		ServerKey:  filepath.Join(dir, "server.key"),
		ClientCert: filepath.Join(dir, "client.crt"),
		ClientKey:  filepath.Join(dir, "client.key"),
	}
	for path, data := range map[string][]byte{
		p.CACert:     ca.CertPEM,
		p.ServerCert: srvCert,
		p.ServerKey:  srvKey,
		p.ClientCert: cliCert,
		p.ClientKey:  cliKey,
	} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, err
		}
	}
	return p, nil
}
