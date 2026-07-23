package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
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
	"time"
)

func main() {
	logHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(logHandler))

	var (
		outputDir   string
		genCA       bool
		genDKIM     bool
		genInternal bool
		domain      string
		domains     string
		serverDNS   string
		serverIPs   string
		clientCN    string
	)

	flag.StringVar(&outputDir, "out", "projects/certs/output", "Output directory for certificates")
	flag.BoolVar(&genCA, "ca", false, "Generate CA certificate")
	flag.BoolVar(&genDKIM, "dkim", false, "Generate DKIM key pair")
	flag.BoolVar(&genInternal, "internal-mtls", false, "Generate a dedicated internal-mTLS CA + API server cert + gateway client cert")
	flag.StringVar(&domain, "domain", "", "Domain name for certificate or DKIM key")
	flag.StringVar(&domains, "domains", "mail1.test,mail2.test,mail3.test", "Comma-separated list of domains to generate certs for")
	flag.StringVar(&serverDNS, "server-dns", "api,localhost", "Comma-separated DNS SANs for the internal mTLS API server cert")
	flag.StringVar(&serverIPs, "server-ip", "127.0.0.1", "Comma-separated IP SANs for the internal mTLS API server cert")
	flag.StringVar(&clientCN, "client-cn", "rest-mail-gateway", "Common Name (machine identity) for the internal mTLS gateway client cert")
	flag.Parse()

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		slog.Error("failed to create output directory", "error", err)
		os.Exit(1)
	}

	if genInternal {
		if err := generateInternalMTLS(outputDir, serverDNS, serverIPs, clientCN); err != nil {
			slog.Error("internal mTLS generation failed", "error", err)
			os.Exit(1)
		}
		return
	}

	if genDKIM {
		if domain == "" {
			slog.Error("--domain required for DKIM key generation")
			os.Exit(1)
		}
		if err := generateDKIM(outputDir, domain); err != nil {
			slog.Error("DKIM generation failed", "error", err)
			os.Exit(1)
		}
		return
	}

	if genCA {
		if err := generateCA(outputDir); err != nil {
			slog.Error("CA generation failed", "error", err)
			os.Exit(1)
		}

		// Generate certs for all domains
		for _, d := range splitDomains(domains) {
			if err := generateServerCert(outputDir, d); err != nil {
				slog.Error("server cert generation failed", "domain", d, "error", err)
				os.Exit(1)
			}
		}
		return
	}

	fmt.Println("Usage:")
	fmt.Println("  certgen --ca                        Generate CA + server certs")
	fmt.Println("  certgen --ca --domains=a.test,b.test Generate CA + specific domain certs")
	fmt.Println("  certgen --dkim --domain=mail1.test   Generate DKIM key pair")
	fmt.Println("  certgen --internal-mtls --server-ip=10.99.0.20 --out=/certs")
	fmt.Println("                                       Generate internal-mTLS CA + API server + gateway client certs")
	flag.PrintDefaults()
}

func splitDomains(s string) []string {
	var result []string
	for _, d := range splitString(s, ',') {
		d = trimSpace(d)
		if d != "" {
			result = append(result, d)
		}
	}
	return result
}

func splitString(s string, sep byte) []string {
	var result []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			result = append(result, s[start:i])
			start = i + 1
		}
	}
	result = append(result, s[start:])
	return result
}

func trimSpace(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	j := len(s)
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t') {
		j--
	}
	return s[i:j]
}

func generateCA(outputDir string) error {
	slog.Info("generating CA certificate")

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("failed to generate CA key: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"RestMail Test CA"},
			CommonName:   "RestMail Test CA",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("failed to create CA certificate: %w", err)
	}

	// Write CA cert
	certPath := filepath.Join(outputDir, "ca.crt")
	if err := writePEM(certPath, "CERTIFICATE", certBytes); err != nil {
		return err
	}

	// Write CA key
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("failed to marshal CA key: %w", err)
	}
	keyPath := filepath.Join(outputDir, "ca.key")
	if err := writePEM(keyPath, "EC PRIVATE KEY", keyBytes); err != nil {
		return err
	}
	_ = os.Chmod(keyPath, 0600)

	slog.Info("CA certificate generated", "cert", certPath, "key", keyPath)
	return nil
}

func generateServerCert(outputDir, domain string) error {
	slog.Info("generating server certificate", "domain", domain)

	// Load CA
	caCert, caKey, err := loadCA(outputDir)
	if err != nil {
		return fmt.Errorf("failed to load CA: %w", err)
	}

	// Generate server key
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("failed to generate server key: %w", err)
	}

	serialNumber, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"RestMail"},
			CommonName:   domain,
		},
		DNSNames:  []string{domain, "*." + domain},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(825 * 24 * time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("failed to create server certificate: %w", err)
	}

	// Write cert
	certPath := filepath.Join(outputDir, domain+".crt")
	if err := writePEM(certPath, "CERTIFICATE", certBytes); err != nil {
		return err
	}

	// Write key
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("failed to marshal server key: %w", err)
	}
	keyPath := filepath.Join(outputDir, domain+".key")
	if err := writePEM(keyPath, "EC PRIVATE KEY", keyBytes); err != nil {
		return err
	}
	_ = os.Chmod(keyPath, 0600)

	slog.Info("server certificate generated", "domain", domain, "cert", certPath, "key", keyPath)
	return nil
}

// ── Internal mTLS (gateway → API machine authentication) ─────────────────
//
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
	if err := writePEM(keyPath, "EC PRIVATE KEY", keyBytes); err != nil {
		return nil, nil, err
	}
	_ = os.Chmod(keyPath, 0600)
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
	if fileExists(certPath) && fileExists(keyPath) {
		slog.Info("internal mTLS server cert already present, skipping", "cert", certPath)
		return nil
	}

	dnsNames := splitDomains(serverDNS)
	var ips []net.IP
	for _, s := range splitDomains(serverIPs) {
		ip := net.ParseIP(s)
		if ip == nil {
			return fmt.Errorf("invalid --server-ip %q", s)
		}
		ips = append(ips, ip)
	}
	if len(dnsNames) == 0 && len(ips) == 0 {
		return fmt.Errorf("internal mTLS server cert needs at least one --server-dns or --server-ip SAN")
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
		return fmt.Errorf("internal mTLS client cert needs a non-empty --client-cn")
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
	if err := writePEM(keyPath, "EC PRIVATE KEY", keyBytes); err != nil {
		return err
	}
	_ = os.Chmod(keyPath, 0600)
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

func generateDKIM(outputDir, domain string) error {
	slog.Info("generating DKIM key pair", "domain", domain)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("failed to generate DKIM key: %w", err)
	}

	// Write private key
	privBytes := x509.MarshalPKCS1PrivateKey(key)
	privPath := filepath.Join(outputDir, domain+".dkim.key")
	if err := writePEM(privPath, "RSA PRIVATE KEY", privBytes); err != nil {
		return err
	}
	_ = os.Chmod(privPath, 0600)

	// Write public key
	pubBytes, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return fmt.Errorf("failed to marshal DKIM public key: %w", err)
	}
	pubPath := filepath.Join(outputDir, domain+".dkim.pub")
	if err := writePEM(pubPath, "PUBLIC KEY", pubBytes); err != nil {
		return err
	}

	slog.Info("DKIM key pair generated", "domain", domain, "private", privPath, "public", pubPath)
	return nil
}

func loadCA(outputDir string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPEM, err := os.ReadFile(filepath.Join(outputDir, "ca.crt"))
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, err
	}

	keyPEM, err := os.ReadFile(filepath.Join(outputDir, "ca.key"))
	if err != nil {
		return nil, nil, err
	}
	block, _ = pem.Decode(keyPEM)
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, err
	}

	return cert, key, nil
}

func writePEM(path, pemType string, data []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", path, err)
	}
	defer f.Close()

	return pem.Encode(f, &pem.Block{Type: pemType, Bytes: data})
}
