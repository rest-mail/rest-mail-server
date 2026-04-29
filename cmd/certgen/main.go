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
	"os"
	"path/filepath"
	"time"
)

func main() {
	logHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(logHandler))

	var (
		outputDir string
		genCA     bool
		genDKIM   bool
		domain    string
		domains   string
	)

	flag.StringVar(&outputDir, "out", "projects/certs/output", "Output directory for certificates")
	flag.BoolVar(&genCA, "ca", false, "Generate CA certificate")
	flag.BoolVar(&genDKIM, "dkim", false, "Generate DKIM key pair")
	flag.StringVar(&domain, "domain", "", "Domain name for certificate or DKIM key")
	flag.StringVar(&domains, "domains", "mail1.test,mail2.test,mail3.test", "Comma-separated list of domains to generate certs for")
	flag.Parse()

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		slog.Error("failed to create output directory", "error", err)
		os.Exit(1)
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
	os.Chmod(keyPath, 0600)

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
	os.Chmod(keyPath, 0600)

	slog.Info("server certificate generated", "domain", domain, "cert", certPath, "key", keyPath)
	return nil
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
	os.Chmod(privPath, 0600)

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
