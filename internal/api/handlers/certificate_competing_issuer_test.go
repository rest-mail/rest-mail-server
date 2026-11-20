package handlers

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	acmeclient "github.com/rest-mail/rest-mail-server/internal/acme"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/gateway/tlsutil"
)

// competingIssuerCA is a CA whose subject reads like Let's Encrypt's, so an
// uploaded certificate looks as close to an ACME one as an upload can.
type competingIssuerCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newCompetingIssuerCA(t *testing.T) competingIssuerCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "R10", Organization: []string{"Let's Encrypt"}, Country: []string{"US"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(1, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return competingIssuerCA{cert: cert, key: key}
}

// leaf issues a certificate for name valid until notAfter; whole seconds, as a
// certificate carries them.
func (ca competingIssuerCA) leaf(t *testing.T, serial int64, name string, notAfter time.Time) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Hour).Truncate(time.Second),
		NotAfter:     notAfter.Truncate(time.Second),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}))
}

// TestUploadedCertificateIsNotTakenOverByACMERenewal proves the competing-issuer
// decision (#2104 AC01) as the server implements it:
//
//   - An uploaded certificate is recorded under its own issuer, the issuer
//     name it carries (pkix.Name.String), and the API reports that. It is never
//     "letsencrypt", even when the certificate came from Let's Encrypt.
//   - The built-in ACME renewal (CertificatesNeedingRenewal, what the API runs
//     when ACME_ENABLED=true) selects only the rows it issued itself. An
//     uploaded certificate inside the renewal window, auto_renew true, is not
//     selected, so an external manager's certificate is never re-issued by it.
//   - An upload for a domain that already has an ACME certificate adds a row;
//     it does not replace or retire the ACME row, which stays eligible for
//     renewal. The gateways serve whichever current certificate expires last.
//
// The last point is the boundary: nothing stops the built-in issuer renewing
// its own row for a domain an external manager also serves. The documented
// ownership model (docs/external-certificates.md) is to keep ACME_ENABLED=false
// there; this test does not prove that an install follows it.
func TestUploadedCertificateIsNotTakenOverByACMERenewal(t *testing.T) {
	db := openCustomFilterCreateScratchDB(t, &models.Certificate{})
	const master = "competing-issuer-test-master-key"
	ca := newCompetingIssuerCA(t)
	acme := acmeclient.NewClient(acmeclient.ClientConfig{DB: db, MasterKey: master})
	now := time.Now()

	shared := models.Domain{Name: "competing.example.org", ServerType: "traditional"}
	uploadOnly := models.Domain{Name: "uploaded-only.example.org", ServerType: "traditional"}
	for _, d := range []*models.Domain{&shared, &uploadOnly} {
		if err := db.Create(d).Error; err != nil {
			t.Fatal(err)
		}
	}

	// The built-in issuer's certificate for shared, stored the way an ACME
	// issuance stores it, ten days from expiry: inside the renewal window.
	acmePEM, acmeKey := ca.leaf(t, 10, shared.Name, now.AddDate(0, 0, 10))
	acmeRow, err := acme.StoreCertificate(shared.Name, acmePEM, acmeKey)
	if err != nil {
		t.Fatalf("store the ACME certificate: %v", err)
	}

	upload := func(domain models.Domain, serial int64, notAfter time.Time) (Certificate, string) {
		t.Helper()
		certPEM, keyPEM := ca.leaf(t, serial, domain.Name, notAfter)
		raw, err := json.Marshal(map[string]any{"domain_id": domain.ID, "cert_pem": certPEM, "key_pem": keyPEM})
		if err != nil {
			t.Fatal(err)
		}
		rr := httptest.NewRecorder()
		certificateRoutes(NewCertificateHandler(db, master)).ServeHTTP(rr,
			httptest.NewRequest(http.MethodPost, "/api/v1/admin/certificates", bytes.NewReader(raw)))
		if rr.Code != http.StatusCreated {
			t.Fatalf("upload for %s: status %d: %s", domain.Name, rr.Code, rr.Body.String())
		}
		var response struct {
			Data Certificate `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response.Data, certPEM
	}
	// Both uploads are inside the renewal window too, with auto_renew left at
	// its default (true): only the issuer keeps them from being renewed.
	sharedUpload, sharedUploadPEM := upload(shared, 20, now.AddDate(0, 0, 20))
	onlyUpload, _ := upload(uploadOnly, 30, now.AddDate(0, 0, 15))

	wantIssuer := ca.cert.Subject.String()
	if wantIssuer != "CN=R10,O=Let's Encrypt,C=US" {
		t.Fatalf("fixture issuer = %q", wantIssuer)
	}
	for _, got := range []Certificate{sharedUpload, onlyUpload} {
		if got.Issuer != wantIssuer || !got.AutoRenew {
			t.Errorf("upload %d reported issuer %q auto_renew %v, want %q and true", got.ID, got.Issuer, got.AutoRenew, wantIssuer)
		}
		// What the API reports when the certificate is read back.
		rr := httptest.NewRecorder()
		certificateRoutes(NewCertificateHandler(db, master)).ServeHTTP(rr,
			httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/admin/certificates/%d", got.ID), nil))
		var read struct {
			Data struct {
				Issuer string `json:"issuer"`
			} `json:"data"`
		}
		if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &read) != nil || read.Data.Issuer != wantIssuer {
			t.Errorf("GET certificate %d: status %d issuer %q, want 200 and %q", got.ID, rr.Code, read.Data.Issuer, wantIssuer)
		}
	}

	// The upload added a row beside the ACME one and left the ACME row as it was.
	var rows []models.Certificate
	if err := db.Where("domain_id = ?", shared.ID).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != acmeRow.ID || rows[0].Issuer != "letsencrypt" || rows[0].CertPEM != acmePEM ||
		rows[1].ID != sharedUpload.ID || rows[1].Issuer != wantIssuer {
		t.Fatalf("certificates for %s after the upload: %+v", shared.Name, summarize(rows))
	}

	// The built-in renewal selects its own row and neither upload.
	due, err := acme.CertificatesNeedingRenewal()
	if err != nil {
		t.Fatal(err)
	}
	var dueIDs []uint
	for _, c := range due {
		dueIDs = append(dueIDs, c.ID)
	}
	if len(dueIDs) != 1 || dueIDs[0] != acmeRow.ID {
		t.Fatalf("ACME renewal selects %v, want only its own certificate %d (uploads %d and %d must not be selected)",
			dueIDs, acmeRow.ID, sharedUpload.ID, onlyUpload.ID)
	}

	// Which certificate the gateways serve for the shared domain: the current
	// one that expires last, here the upload.
	served, err := tlsutil.NewDBCertLoader(db, master, nil).GetCertificate(&tls.ClientHelloInfo{ServerName: shared.Name})
	if err != nil {
		t.Fatalf("gateway certificate for %s: %v", shared.Name, err)
	}
	block, _ := pem.Decode([]byte(sharedUploadPEM))
	if len(served.Certificate) == 0 || !bytes.Equal(served.Certificate[0], block.Bytes) {
		t.Fatalf("the gateways serve a certificate other than the later-expiring upload for %s", shared.Name)
	}
}

func summarize(rows []models.Certificate) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprintf("id=%d issuer=%q not_after=%s", r.ID, r.Issuer, r.NotAfter.Format(time.RFC3339)))
	}
	return out
}
