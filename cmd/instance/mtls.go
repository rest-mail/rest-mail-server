package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// mtlsIssueCmd is the ca_source: testbed-certgen branch of `chore
// instance:mtls:issue`: it mints the gateway→API internal mTLS material into
// -out. It used to be `certgen --internal-mtls`; it lives here so cmd/certgen
// could go and the instance tooling is the one cert path (#149).
func mtlsIssueCmd(args []string) {
	fs := flag.NewFlagSet("mtls-issue", flag.ExitOnError)
	out := fs.String("out", "", "directory the internal mTLS material is written to")
	serverDNS := fs.String("server-dns", "api,localhost", "comma-separated DNS SANs for the API server cert")
	serverIPs := fs.String("server-ip", "127.0.0.1", "comma-separated IP SANs for the API server cert")
	clientCN := fs.String("client-cn", "rest-mail-gateway", "Common Name (machine identity) for the gateway client cert")
	_ = fs.Parse(args)

	if *out == "" || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "instance mtls-issue: -out is required, and no arguments")
		os.Exit(2)
	}
	if err := os.MkdirAll(*out, 0755); err != nil {
		fatal("mtls-issue: %v", err)
	}
	if err := generateInternalMTLS(*out, *serverDNS, *serverIPs, *clientCN); err != nil {
		fatal("mtls-issue: %v", err)
	}
}

// splitList splits a comma-separated flag value, trimming blanks and dropping
// empty entries.
func splitList(s string) []string {
	var result []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			result = append(result, v)
		}
	}
	return result
}

// A DEDICATED CA, distinct from the public/testbed CA, so only certificates
// this CA signs can authenticate as a gateway on the API's internal listener.

const (
	internalCACertFile     = "internal-ca.crt"
	internalCAKeyFile      = "internal-ca.key"
	internalServerCertFile = "internal-server.crt"
	internalServerKeyFile  = "internal-server.key"
	internalClientCertFile = "internal-client.crt"
	internalClientKeyFile  = "internal-client.key"
)

// generateInternalMTLS mints the material for the gateway→API internal mTLS
// channel under outputDir: a dedicated internal CA, an API server cert for the
// internal listener (ServerAuth, with the given DNS/IP SANs), and a gateway
// client cert (ClientAuth, machine identity clientCN).
//
// It is idempotent and CA-preserving: an existing internal CA is reused so
// client certs already deployed to gateways keep verifying, and existing leaf
// certs are left untouched. This mirrors the reference-certgen behavior the
// testbed relies on, so the provisioning task is safe to re-run.
func generateInternalMTLS(outputDir, serverDNS, serverIPs, clientCN string) error {
	caCert, caKey, err := loadOrCreateInternalCA(outputDir)
	if err != nil {
		return err
	}
	if err := ensureInternalServerCert(outputDir, caCert, caKey, serverDNS, serverIPs); err != nil {
		return err
	}
	return ensureInternalClientCert(outputDir, caCert, caKey, clientCN)
}

func loadOrCreateInternalCA(outputDir string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPath := filepath.Join(outputDir, internalCACertFile)
	keyPath := filepath.Join(outputDir, internalCAKeyFile)
	if fileExists(certPath) && fileExists(keyPath) {
		slog.Info("reusing existing internal mTLS CA", "cert", certPath)
		return loadCAFrom(certPath, keyPath)
	}

	slog.Info("generating internal mTLS CA")
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate internal CA key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"RestMail Internal"},
			CommonName:   "RestMail Internal mTLS CA",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create internal CA certificate: %w", err)
	}
	if err := writePEM(certPath, "CERTIFICATE", der); err != nil {
		return nil, nil, err
	}
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal internal CA key: %w", err)
	}
	if err := writeKeyPEM(keyPath, "EC PRIVATE KEY", keyBytes); err != nil {
		return nil, nil, err
	}
	caCert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	slog.Info("internal mTLS CA generated", "cert", certPath, "key", keyPath)
	return caCert, key, nil
}

func ensureInternalServerCert(outputDir string, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, serverDNS, serverIPs string) error {
	certPath := filepath.Join(outputDir, internalServerCertFile)
	keyPath := filepath.Join(outputDir, internalServerKeyFile)

	dnsNames := splitList(serverDNS)
	var ips []net.IP
	for _, s := range splitList(serverIPs) {
		ip := net.ParseIP(s)
		if ip == nil {
			return fmt.Errorf("invalid -server-ip %q", s)
		}
		ips = append(ips, ip)
	}
	if len(dnsNames) == 0 && len(ips) == 0 {
		return fmt.Errorf("internal mTLS server cert needs at least one -server-dns or -server-ip SAN")
	}

	// Skip ONLY when an existing server cert already covers the requested SAN
	// set exactly. If the SANs drifted — e.g. the api container IP changed on a
	// persistent certs volume — re-mint the leaf (the CA is untouched, so
	// already-issued client certs keep verifying) to avoid a stale SAN causing a
	// later TLS name mismatch.
	if fileExists(certPath) && fileExists(keyPath) {
		if serverCertSANsMatch(certPath, dnsNames, ips) {
			slog.Info("internal mTLS server cert already present with matching SANs, skipping", "cert", certPath)
			return nil
		}
		slog.Info("internal mTLS server cert SANs changed, re-minting leaf", "cert", certPath, "dns", dnsNames, "ips", serverIPs)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("failed to generate internal server key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	cn := "rest-mail-internal-api"
	if len(dnsNames) > 0 {
		cn = dnsNames[0]
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{Organization: []string{"RestMail Internal"}, CommonName: cn},
		DNSNames:     dnsNames,
		IPAddresses:  ips,
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(825 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if err := signAndWriteLeaf(certPath, keyPath, template, caCert, caKey, key); err != nil {
		return err
	}
	slog.Info("internal mTLS server certificate generated", "cert", certPath, "dns", dnsNames, "ips", serverIPs)
	return nil
}

func ensureInternalClientCert(outputDir string, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, clientCN string) error {
	certPath := filepath.Join(outputDir, internalClientCertFile)
	keyPath := filepath.Join(outputDir, internalClientKeyFile)
	if fileExists(certPath) && fileExists(keyPath) {
		slog.Info("internal mTLS client cert already present, skipping", "cert", certPath)
		return nil
	}
	if clientCN == "" {
		return fmt.Errorf("internal mTLS client cert needs a non-empty -client-cn")
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("failed to generate internal client key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{Organization: []string{"RestMail Internal"}, CommonName: clientCN},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(825 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if err := signAndWriteLeaf(certPath, keyPath, template, caCert, caKey, key); err != nil {
		return err
	}
	slog.Info("internal mTLS gateway client certificate generated", "cert", certPath, "cn", clientCN)
	return nil
}

// signAndWriteLeaf signs template with the CA and writes the resulting cert +
// its EC private key (0600) to disk.
func signAndWriteLeaf(certPath, keyPath string, template, caCert *x509.Certificate, caKey, leafKey *ecdsa.PrivateKey) error {
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("failed to create certificate: %w", err)
	}
	if err := writePEM(certPath, "CERTIFICATE", der); err != nil {
		return err
	}
	keyBytes, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return fmt.Errorf("failed to marshal key: %w", err)
	}
	if err := writeKeyPEM(keyPath, "EC PRIVATE KEY", keyBytes); err != nil {
		return err
	}
	return nil
}

// loadCAFrom parses a CA cert + EC key pair from explicit paths (nil-checked,
// unlike the fixed-path loadCA used by the public server-cert path).
func loadCAFrom(certPath, keyPath string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, nil, fmt.Errorf("no PEM certificate in %s", certPath)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}
	block, _ = pem.Decode(keyPEM)
	if block == nil {
		return nil, nil, fmt.Errorf("no PEM key in %s", keyPath)
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// serverCertSANsMatch reports whether the cert at certPath has EXACTLY the
// requested DNS + IP SAN sets. A parse failure counts as "no match" so a
// corrupt/unreadable existing cert is safely re-minted.
func serverCertSANsMatch(certPath string, dnsNames []string, ips []net.IP) bool {
	data, err := os.ReadFile(certPath)
	if err != nil {
		return false
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	if !sameStringSet(cert.DNSNames, dnsNames) {
		return false
	}
	haveIPs := make([]string, len(cert.IPAddresses))
	for i, ip := range cert.IPAddresses {
		haveIPs[i] = ip.String()
	}
	wantIPs := make([]string, len(ips))
	for i, ip := range ips {
		wantIPs[i] = ip.String()
	}
	return sameStringSet(haveIPs, wantIPs)
}

// sameStringSet reports whether a and b contain the same elements (ignoring
// order and multiplicity).
func sameStringSet(a, b []string) bool {
	set := make(map[string]struct{}, len(a))
	for _, s := range a {
		set[s] = struct{}{}
	}
	other := make(map[string]struct{}, len(b))
	for _, s := range b {
		other[s] = struct{}{}
	}
	if len(set) != len(other) {
		return false
	}
	for s := range set {
		if _, ok := other[s]; !ok {
			return false
		}
	}
	return true
}

func writePEM(path, pemType string, data []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", path, err)
	}
	defer f.Close()

	return pem.Encode(f, &pem.Block{Type: pemType, Bytes: data})
}

// writeKeyPEM writes a PRIVATE-key PEM block to path. Unlike writePEM (used for
// public certs/keys), the file is created with 0600 permissions at open time
// via O_CREATE|O_TRUNC, so the key material is never briefly world-readable in
// the window between an os.Create (umask-derived, typically 0644) and a
// follow-up os.Chmod. If the path pre-existed with wider permissions it is
// tightened to 0600 BEFORE any key bytes are written. Every error is returned,
// never discarded.
func writeKeyPEM(path, pemType string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", path, err)
	}
	defer f.Close()

	if err := f.Chmod(0600); err != nil {
		return fmt.Errorf("failed to secure %s: %w", path, err)
	}
	if err := pem.Encode(f, &pem.Block{Type: pemType, Bytes: data}); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}
