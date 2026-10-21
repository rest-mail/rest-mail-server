package acme

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"github.com/restmail/restmail/internal/config"
	rmcrypto "github.com/restmail/restmail/internal/crypto"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

const testMasterKey = "acme-store-test-master-key"

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// openStoreTestDB connects to the test database the CI job provides, and skips
// when there is none, the way the other database-backed tests do.
func openStoreTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	port, err := strconv.Atoi(envOrDefault("DB_PORT", "5432"))
	if err != nil {
		t.Skipf("acme store test skipped: bad DB_PORT (%v)", err)
	}
	cfg := &config.Config{
		DBHost: envOrDefault("DB_HOST", "localhost"),
		DBPort: port,
		DBName: envOrDefault("DB_NAME", "restmail"),
		DBUser: envOrDefault("DB_USER", "restmail"),
		DBPass: envOrDefault("DB_PASS", "restmail"),
	}
	gdb, err := rmdb.Connect(cfg)
	if err != nil {
		t.Skipf("acme store test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.Domain{}, &models.Certificate{}, &models.ACMEAccount{}); err != nil {
		t.Skipf("acme store test skipped: migrate failed (%v)", err)
	}
	return gdb
}

func newECKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ECDSA key: %v", err)
	}
	return key
}

func newRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	return key
}

// certFor self-signs a certificate for key with the given common name and SANs,
// valid until notAfter, and returns it as PEM.
func certFor(t *testing.T, key crypto.Signer, cn string, sans []string, notAfter time.Time) string {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     sans,
		NotBefore:    notAfter.Add(-90 * 24 * time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func keyPEMFor(t *testing.T, key crypto.Signer) string {
	t.Helper()
	switch k := key.(type) {
	case *ecdsa.PrivateKey:
		der, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			t.Fatalf("marshal ECDSA key: %v", err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
	case *rsa.PrivateKey:
		return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
	case ed25519.PrivateKey:
		der, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			t.Fatalf("marshal Ed25519 key: %v", err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	}
	t.Fatalf("unsupported key type %T", key)
	return ""
}

// certRow is the part of a certificate row a refused store must leave alone.
type certRow struct {
	ID        uint
	CertPEM   string
	KeyPEM    string
	NotAfter  time.Time
	UpdatedAt time.Time
}

func certRows(t *testing.T, tx *gorm.DB) []certRow {
	t.Helper()
	var rows []certRow
	if err := tx.Model(&models.Certificate{}).Order("id").
		Select("id, cert_pem, key_pem, not_after, updated_at").Scan(&rows).Error; err != nil {
		t.Fatalf("read certificate rows: %v", err)
	}
	return rows
}

// An issuance or renewal that hands back a certificate the server could not
// serve for the domain (issue #123) must be refused before anything is written,
// so it never supplants the working certificate, and its error must not quote
// the private key.
func TestStoreCertificate_RefusesUnusablePairsWithoutWriting(t *testing.T) {
	gdb := openStoreTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })

	const domain = "acme-store.test"
	if err := tx.Create(&models.Domain{Name: domain, ServerType: "traditional", Active: true}).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}
	if err := tx.Create(&models.Domain{Name: "acme-elsewhere.test", ServerType: "traditional", Active: true}).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}

	c := NewClient(ClientConfig{DB: tx, MasterKey: testMasterKey, Email: "admin@acme-store.test"})
	cache := &dbCache{client: c}

	// The working certificate the refusals must not disturb.
	workingKey := newECKey(t)
	workingCert := certFor(t, workingKey, domain, []string{domain}, time.Now().Add(30*24*time.Hour))
	workingKeyPEM := keyPEMFor(t, workingKey)
	if _, err := c.StoreCertificate(domain, workingCert, workingKeyPEM); err != nil {
		t.Fatalf("store working certificate: %v", err)
	}
	before := certRows(t, tx)
	if len(before) != 1 {
		t.Fatalf("got %d certificate rows after the first store, want 1", len(before))
	}

	// Every refused certificate outlives the working one, so a store that slipped
	// through would be the one the cache serves.
	later := time.Now().Add(90 * 24 * time.Hour)
	ecKey, otherECKey, rsaKey := newECKey(t), newECKey(t), newRSAKey(t)
	ecCert := certFor(t, ecKey, domain, []string{domain}, later)
	ecKeyPEM := keyPEMFor(t, ecKey)
	rsaCert := certFor(t, rsaKey, domain, []string{domain}, later)
	garbled := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not a certificate")}))
	garbledKey := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte("not a key")}))

	cases := []struct {
		name    string
		certPEM string
		keyPEM  string
	}{
		{"empty input", "", ""},
		{"malformed certificate", garbled, ecKeyPEM},
		{"malformed private key", ecCert, garbledKey},
		{"certificate PEM that is not PEM", "-----BEGIN CERTIFICATE-----\nzz\n", ecKeyPEM},
		{"ECDSA key from another pair", ecCert, keyPEMFor(t, otherECKey)},
		{"RSA certificate with an ECDSA key", rsaCert, ecKeyPEM},
		{"ECDSA certificate with an RSA key", ecCert, keyPEMFor(t, rsaKey)},
		{"certificate for another domain", certFor(t, ecKey, "acme-elsewhere.test", []string{"acme-elsewhere.test"}, later), ecKeyPEM},
		{"name only in the common name", certFor(t, ecKey, domain, nil, later), ecKeyPEM},
		{"wildcard that does not cover the apex", certFor(t, ecKey, "*."+domain, []string{"*." + domain}, later), ecKeyPEM},
		{"expired certificate", certFor(t, ecKey, domain, []string{domain}, time.Now().Add(-time.Hour)), ecKeyPEM},
		{"private key inside the certificate PEM", ecCert + ecKeyPEM, ecKeyPEM},
		{"two private keys", ecCert, ecKeyPEM + keyPEMFor(t, otherECKey)},
		{"certificate in place of the key", ecCert, ecCert},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.StoreCertificate(domain, tc.certPEM, tc.keyPEM)
			if err == nil {
				t.Fatal("StoreCertificate accepted the certificate, want a refusal")
			}
			assertNoKeyMaterial(t, err, tc.keyPEM)

			// The autocert cache path must refuse the same input.
			putErr := cache.Put(context.Background(), domain, []byte(tc.keyPEM+tc.certPEM))
			if putErr == nil {
				t.Fatal("dbCache.Put accepted the certificate, want a refusal")
			}
			assertNoKeyMaterial(t, putErr, tc.keyPEM)

			assertRowsUnchanged(t, before, certRows(t, tx))
		})
	}

	// Text around the blocks is refused when stored directly. (Put never passes
	// it on: it re-encodes the blocks it splits out.)
	t.Run("text around the certificate", func(t *testing.T) {
		if _, err := c.StoreCertificate(domain, "junk\n"+ecCert, ecKeyPEM); err == nil {
			t.Fatal("StoreCertificate accepted text around the certificate")
		}
		assertRowsUnchanged(t, before, certRows(t, tx))
	})

	// A valid pair stored under a domain it does not name is refused too.
	t.Run("valid pair stored under another domain", func(t *testing.T) {
		if _, err := c.StoreCertificate("acme-elsewhere.test", ecCert, ecKeyPEM); err == nil {
			t.Fatal("StoreCertificate accepted a certificate for another domain")
		}
		assertRowsUnchanged(t, before, certRows(t, tx))
	})

	// The cache still serves the working certificate and its key.
	data, err := cache.Get(context.Background(), domain)
	if err != nil {
		t.Fatalf("cache get after refusals: %v", err)
	}
	if !strings.Contains(string(data), workingCert) || !strings.Contains(string(data), workingKeyPEM) {
		t.Fatal("cache no longer serves the working certificate after the refusals")
	}
}

// A matching certificate and key for the domain store with the key encrypted,
// and come back out of the autocert cache as a usable pair. RSA and ECDSA keys
// both work, and the domain may be any SAN, not just the first.
func TestStoreCertificate_StoresAndRoundTripsValidPairs(t *testing.T) {
	gdb := openStoreTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })

	c := NewClient(ClientConfig{DB: tx, MasterKey: testMasterKey, Email: "admin@acme-roundtrip.test"})
	cache := &dbCache{client: c}

	cases := []struct {
		name   string
		domain string
		key    crypto.Signer
		sans   []string
	}{
		{"ECDSA", "acme-ec.test", newECKey(t), []string{"acme-ec.test"}},
		{"RSA, domain as a later SAN", "acme-rsa.test", newRSAKey(t), []string{"mail.acme-rsa.test", "acme-rsa.test"}},
		{"wildcard covering a subdomain", "mx.acme-wild.test", newECKey(t), []string{"*.acme-wild.test"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tx.Create(&models.Domain{Name: tc.domain, ServerType: "traditional", Active: true}).Error; err != nil {
				t.Fatalf("create domain: %v", err)
			}
			certPEM := certFor(t, tc.key, tc.sans[0], tc.sans, time.Now().Add(60*24*time.Hour))
			keyPEM := keyPEMFor(t, tc.key)

			cacheKey := tc.domain
			cipherSuite := uint16(tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256)
			if _, ok := tc.key.(*rsa.PrivateKey); ok {
				cacheKey += "+rsa"
				cipherSuite = tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256
			}
			// autocert hands the cache the key first, then the chain.
			if err := cache.Put(context.Background(), cacheKey, []byte(keyPEM+certPEM)); err != nil {
				t.Fatalf("dbCache.Put: %v", err)
			}

			var stored models.Certificate
			if err := tx.Joins("JOIN domains ON domains.id = certificates.domain_id").
				Where("domains.name = ?", tc.domain).First(&stored).Error; err != nil {
				t.Fatalf("read stored certificate: %v", err)
			}
			if stored.CertPEM != certPEM {
				t.Error("stored certificate PEM differs from the one put")
			}
			if strings.Contains(stored.KeyPEM, "PRIVATE KEY") {
				t.Fatal("private key stored in plaintext despite MASTER_KEY")
			}
			if plain, err := rmcrypto.DecryptString(stored.KeyPEM, testMasterKey); err != nil || plain != keyPEM {
				t.Fatalf("stored key does not decrypt to the key put (err %v)", err)
			}

			data, err := cache.Get(context.Background(), cacheKey)
			if err != nil {
				t.Fatalf("dbCache.Get: %v", err)
			}
			gotCert, gotKey, err := splitCertKey(data)
			if err != nil {
				t.Fatalf("split cached data: %v", err)
			}
			if gotCert != certPEM || gotKey != keyPEM {
				t.Fatal("cache returned a different certificate or key than was put")
			}
			if first, _ := pem.Decode(data); first == nil || !strings.HasSuffix(first.Type, "PRIVATE KEY") {
				t.Fatal("cached data does not start with the private key, as autocert requires")
			}

			// autocert itself must serve the cached pair. The directory is an
			// unreachable loopback address, so a cache miss fails instead of
			// reaching an ACME server.
			m := &autocert.Manager{
				Prompt:     autocert.AcceptTOS,
				HostPolicy: autocert.HostWhitelist(tc.domain),
				Client:     &acme.Client{DirectoryURL: "http://127.0.0.1:1/directory"},
				Cache:      cache,
			}
			served, err := m.GetCertificate(&tls.ClientHelloInfo{
				ServerName:   tc.domain,
				CipherSuites: []uint16{cipherSuite},
			})
			if err != nil {
				t.Fatalf("autocert could not serve the cached certificate: %v", err)
			}
			if !served.Leaf.Equal(storedLeaf(t, certPEM)) {
				t.Fatal("autocert served a different certificate than was put")
			}
		})
	}
}

func storedLeaf(t *testing.T, certPEM string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(certPEM))
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return leaf
}

// assertNoKeyMaterial fails if err quotes any of keyPEM: its armour, or any
// line of its base64 body.
func assertNoKeyMaterial(t *testing.T, err error, keyPEM string) {
	t.Helper()
	msg := err.Error()
	if strings.Contains(msg, "-----") || strings.Contains(msg, "PRIVATE KEY") {
		t.Fatalf("error quotes PEM armour: %q", msg)
	}
	rest := []byte(keyPEM)
	for {
		block, next := pem.Decode(rest)
		if block == nil {
			return
		}
		body := base64.StdEncoding.EncodeToString(block.Bytes)
		for i := 0; i+16 <= len(body); i += 16 {
			if strings.Contains(msg, body[i:i+16]) {
				t.Fatalf("error quotes private key material: %q", msg)
			}
		}
		rest = next
	}
}

func assertRowsUnchanged(t *testing.T, before, after []certRow) {
	t.Helper()
	if len(after) != len(before) {
		t.Fatalf("got %d certificate rows, want %d: a refused store wrote a row", len(after), len(before))
	}
	for i := range before {
		b, a := before[i], after[i]
		if a.ID != b.ID || a.CertPEM != b.CertPEM || a.KeyPEM != b.KeyPEM ||
			!a.NotAfter.Equal(b.NotAfter) || !a.UpdatedAt.Equal(b.UpdatedAt) {
			t.Fatalf("certificate row %d changed after a refused store", b.ID)
		}
	}
}

// Existing direct stores and autocert writes share rows, but the RSA and ECDSA
// cache entries must remain independent even when the other algorithm is newer.
func TestDBCache_AlgorithmIsolation(t *testing.T) {
	gdb := openStoreTestDB(t)
	ecKey, rsaKey := newECKey(t), newRSAKey(t)
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate Ed25519 key: %v", err)
	}
	const domain, other = "acme-isolation.test", "acme-other.test"
	expiry := time.Now().Add(60 * 24 * time.Hour)
	ecCert := certFor(t, ecKey, domain, []string{domain}, expiry)
	rsaCert := certFor(t, rsaKey, domain, []string{domain}, expiry.Add(24*time.Hour))
	ecPEM, rsaPEM := keyPEMFor(t, ecKey), keyPEMFor(t, rsaKey)
	edCert := certFor(t, edKey, domain, []string{domain}, expiry.Add(96*time.Hour))
	edPEM := keyPEMFor(t, edKey)

	for _, deleteRSA := range []bool{false, true} {
		name := "delete ECDSA"
		if deleteRSA {
			name = "delete RSA"
		}
		t.Run(name, func(t *testing.T) {
			tx := gdb.Begin()
			t.Cleanup(func() { tx.Rollback() })
			for _, d := range []string{domain, other} {
				if err := tx.Create(&models.Domain{Name: d, ServerType: "traditional", Active: true}).Error; err != nil {
					t.Fatalf("create domain: %v", err)
				}
			}
			client := NewClient(ClientConfig{DB: tx, MasterKey: testMasterKey})
			cache := &dbCache{client: client}
			ctx := context.Background()
			// Legacy direct StoreCertificate rows are selected by their leaf's
			// algorithm, without migrating or changing their domain identity.
			for _, pair := range []struct{ cert, key string }{{ecCert, ecPEM}, {rsaCert, rsaPEM}, {edCert, edPEM}} {
				if _, err := client.StoreCertificate(domain, pair.cert, pair.key); err != nil {
					t.Fatalf("direct store: %v", err)
				}
			}
			assertCachedPair(t, cache, domain+"+rsa", rsaCert, rsaPEM)
			assertCachedPair(t, cache, domain, ecCert, ecPEM)

			// Put both variants while they coexist, first making ECDSA newest,
			// then RSA newest. Each Get must still select its own latest row.
			ecNew := certFor(t, ecKey, domain, []string{domain}, expiry.Add(48*time.Hour))
			if err := cache.Put(ctx, domain, []byte(ecPEM+ecNew)); err != nil {
				t.Fatalf("put ECDSA: %v", err)
			}
			assertCachedPair(t, cache, domain+"+rsa", rsaCert, rsaPEM)
			rsaNew := certFor(t, rsaKey, domain, []string{domain}, expiry.Add(72*time.Hour))
			if err := cache.Put(ctx, domain+"+rsa", []byte(rsaPEM+rsaNew)); err != nil {
				t.Fatalf("put RSA: %v", err)
			}
			assertCachedPair(t, cache, domain, ecNew, ecPEM)
			assertCachedPair(t, cache, domain+"+rsa", rsaNew, rsaPEM)

			for _, key := range []crypto.Signer{ecKey, rsaKey} {
				cert := certFor(t, key, other, []string{other}, expiry)
				if _, err := client.StoreCertificate(other, cert, keyPEMFor(t, key)); err != nil {
					t.Fatalf("store other domain: %v", err)
				}
			}
			manual, err := client.StoreCertificate(domain, ecNew, ecPEM)
			if err != nil {
				t.Fatalf("store manual fixture: %v", err)
			}
			if err := tx.Model(manual).Update("issuer", "manual").Error; err != nil {
				t.Fatalf("set fixture issuer: %v", err)
			}

			before := certRows(t, tx)
			for _, bad := range []struct{ key, data string }{
				{domain, rsaPEM + rsaNew},
				{domain + "+rsa", ecPEM + ecNew},
				{domain, edPEM + edCert},
				{domain + "+rsa", edPEM + edCert},
				{other + "+rsa", rsaPEM + rsaNew}, // actual hostname must match
			} {
				if err := cache.Put(ctx, bad.key, []byte(bad.data)); err == nil {
					t.Fatalf("accepted wrong algorithm or hostname under %q", bad.key)
				} else {
					assertNoKeyMaterial(t, err, rsaPEM)
					assertNoKeyMaterial(t, err, ecPEM)
				}
				assertRowsUnchanged(t, before, certRows(t, tx))
			}
			for _, key := range []string{"acme_account+key", "acme_account.key", domain + "+token", domain + "+http-01", domain + "+unknown", domain + "+rsa+rsa", "+rsa", ""} {
				if _, err := cache.Get(ctx, key); err != autocert.ErrCacheMiss {
					t.Fatalf("unsupported key %q: Get error = %v", key, err)
				}
				if err := cache.Put(ctx, key, []byte(rsaPEM+rsaNew)); err == nil {
					t.Fatalf("unsupported key %q: Put succeeded", key)
				}
				if err := cache.Delete(ctx, key); err != nil {
					t.Fatalf("unsupported key %q: Delete: %v", key, err)
				}
				assertRowsUnchanged(t, before, certRows(t, tx))
			}

			deleteKey, keepKey, keepCert, keepPEM := domain, domain+"+rsa", rsaNew, rsaPEM
			if deleteRSA {
				deleteKey, keepKey, keepCert, keepPEM = domain+"+rsa", domain, ecNew, ecPEM
			}
			if err := cache.Delete(ctx, deleteKey); err != nil {
				t.Fatalf("delete: %v", err)
			}
			if _, err := cache.Get(ctx, deleteKey); err != autocert.ErrCacheMiss {
				t.Fatalf("deleted variant Get error = %v, want cache miss", err)
			}
			assertCachedPair(t, cache, keepKey, keepCert, keepPEM)
			var kept []certRow
			for _, row := range before {
				deleted := row.CertPEM == ecCert || row.CertPEM == ecNew
				if deleteRSA {
					deleted = row.CertPEM == rsaCert || row.CertPEM == rsaNew
				}
				if row.ID != manual.ID && deleted {
					continue
				}
				kept = append(kept, row)
			}
			assertRowsUnchanged(t, kept, certRows(t, tx))
		})
	}
}

func assertCachedPair(t *testing.T, cache *dbCache, key, certPEM, keyPEM string) {
	t.Helper()
	data, err := cache.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("cache Get(%q): %v", key, err)
	}
	cert, private, err := splitCertKey(data)
	if err != nil || cert != certPEM || private != keyPEM {
		t.Fatalf("cache Get(%q) returned a different pair (err %v)", key, err)
	}
}

func TestCertificateCacheKey(t *testing.T) {
	for _, tc := range []struct {
		key       string
		domain    string
		algorithm x509.PublicKeyAlgorithm
	}{
		{"example.test", "example.test", x509.ECDSA},
		{"example.test+rsa", "example.test", x509.RSA},
		{"xn--bcher-kva.example+rsa", "xn--bcher-kva.example", x509.RSA},
	} {
		domain, algorithm, err := certificateCacheKey(tc.key)
		if err != nil || domain != tc.domain || algorithm != tc.algorithm {
			t.Errorf("certificateCacheKey(%q) = (%q, %v, %v)", tc.key, domain, algorithm, err)
		}
	}
	for _, key := range []string{"", "acme_account+key", "acme_account.key", "example.test+token", "example.test+http-01", "example.test+ecdsa", "example.test+rsa+rsa", "+rsa", "example.test.", "EXAMPLE.test", "-bad.test", "bad-.test", "a..test", "*.example.test", "a/other.test", strings.Repeat("a", 64) + ".test"} {
		if _, _, err := certificateCacheKey(key); err == nil {
			t.Errorf("certificateCacheKey(%q) accepted unsupported key", key)
		}
	}
}
