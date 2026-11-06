package handlers

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	dkim "github.com/rest-mail/go-dkim/pkg/dkim"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/dkimrotate"
	"github.com/rest-mail/rest-mail-server/internal/dns"
	"github.com/rest-mail/rest-mail-server/internal/outbound"
)

const dnsCheckTestMasterKey = "dns-check-dkim-test-master-key"

// dnsCheckTestKey is a fresh Ed25519 signing key, PKCS#8 PEM, with the record
// its public half publishes.
func dnsCheckTestKey(t *testing.T) (keyPEM, record string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"v=DKIM1; k=ed25519; p=" + base64.StdEncoding.EncodeToString(pub)
}

// privateMaterial is every form of keyPEM's secret an answer must not carry: the
// PEM, its base64 body, and the stored (encrypted) value.
func privateMaterial(t *testing.T, keyPEM, stored string) []string {
	t.Helper()
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		t.Fatal("not PEM")
	}
	body := base64.StdEncoding.EncodeToString(block.Bytes)
	return []string{"PRIVATE KEY", keyPEM, body, body[len(body)/2:], stored}
}

func assertNoPrivateMaterial(t *testing.T, answer []byte, secrets []string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(string(answer), secret) {
			t.Fatalf("the answer carries private key material %.40q: %s", secret, answer)
		}
	}
}

// The DNS check says which TXT record to publish for the key the domain signs
// with (#1830): mail the server signs with the stored key verifies against
// exactly the value it names, and once that value is published the check is ok.
// No private key material is answered with.
func TestDNSCheck_DKIMExpectedIsTheKeyTheServerSignsWith(t *testing.T) {
	rsaKey, _, err := dkim.GenerateKey(2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	edKey, _ := dnsCheckTestKey(t)
	for _, tc := range []struct{ name, key, k string }{{"RSA 2048", rsaKey, "rsa"}, {"Ed25519", edKey, "ed25519"}} {
		t.Run(tc.name, func(t *testing.T) {
			stored, err := models.EncryptDKIMPrivateKey(tc.key, dnsCheckTestMasterKey)
			if err != nil {
				t.Fatal(err)
			}
			tx, domain, h := dnsCheckFixture(t, func(d *models.Domain) { d.DKIMPrivateKey = stored })
			h.SetMailServer(checkMailHost, dnsCheckTestMasterKey, dns.ClientPorts{Submission: 465, IMAP: 993, POP3: 995})
			recordName := "rm2026._domainkey." + domain.Name
			zone := aCorrectZone(domain.Name)
			delete(zone.txt, recordName)
			zone.install(h)
			secrets := privateMaterial(t, tc.key, stored)

			before, raw := dkimRecordOf(t, h, domain.ID)
			assertNoPrivateMaterial(t, raw, secrets)
			if before.Name != recordName || before.Status != "missing" || before.Problem != "missing" {
				t.Fatalf("before publishing: %+v, want %s missing", before, recordName)
			}
			if !strings.HasPrefix(before.Expected, "v=DKIM1; k="+tc.k+"; p=") {
				t.Fatalf("expected = %q, want a %s DKIM record", before.Expected, tc.k)
			}

			// Sign as the server does, with the key it stored, and verify against
			// the record the check says to publish.
			msg := "From: alice@" + domain.Name + "\r\nTo: bob@example.net\r\nSubject: hi\r\n" +
				"Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n\r\nHello.\r\n"
			signed, err := outbound.SignDKIM(context.Background(), tx, dnsCheckTestMasterKey, domain.Name, msg)
			if err != nil {
				t.Fatalf("sign: %v", err)
			}
			results := dkim.Verify(context.Background(), []byte(signed), func(_ context.Context, name string) ([]string, error) {
				if name == recordName {
					return []string{before.Expected}, nil
				}
				return nil, fmt.Errorf("no TXT record for %s", name)
			})
			if len(results) != 1 || results[0].Result != dkim.ResultPass {
				t.Fatalf("verify against the expected record = %+v, want one pass", results)
			}

			zone.txt[recordName] = []string{before.Expected}
			after, raw := dkimRecordOf(t, h, domain.ID)
			assertNoPrivateMaterial(t, raw, secrets)
			if after.Status != "ok" || after.Problem != "" || after.Value != before.Expected || after.Expected != before.Expected {
				t.Fatalf("after publishing: %+v, want ok", after)
			}
		})
	}
}

type dkimRecordCheck struct {
	Type, Name, Status, Value, Message, Expected, Problem string
}

// dkimRecordOf runs the DNS check through the route and gives its DKIM record
// and the whole answer.
func dkimRecordOf(t *testing.T, h *DomainHandler, id uint) (dkimRecordCheck, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/admin/domains/%d/dns", id), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", fmt.Sprint(id))
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	domainRoutes(h).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Data struct {
			Records []dkimRecordCheck `json:"records"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, rec := range resp.Data.Records {
		if rec.Type == "DKIM" {
			return rec, rr.Body.Bytes()
		}
	}
	t.Fatalf("no DKIM record in %s", rr.Body)
	return dkimRecordCheck{}, nil
}

// The check compares what is published at the selector with the expected record
// and says which part differs (#1830).
func TestDNSCheck_DKIMSaysWhatDiffers(t *testing.T) {
	keyPEM, want := dnsCheckTestKey(t)
	_, other := dnsCheckTestKey(t)
	stored, err := models.EncryptDKIMPrivateKey(keyPEM, dnsCheckTestMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	wantKey := strings.TrimPrefix(want, "v=DKIM1; k=ed25519; p=")
	rsaShaped := "v=DKIM1; k=rsa; p=" + wantKey

	cases := []struct {
		name, masterKey      string
		published            []string
		lookupFails          bool
		status, problem, val string
	}{
		{name: "published", masterKey: dnsCheckTestMasterKey, published: []string{want}, status: "ok", val: want},
		{name: "published with other tags and spacing", masterKey: dnsCheckTestMasterKey,
			published: []string{"v=DKIM1;k=ed25519; t=s ; p=" + wantKey[:10] + " " + wantKey[10:]}, status: "ok",
			val: "v=DKIM1;k=ed25519; t=s ; p=" + wantKey[:10] + " " + wantKey[10:]},
		{name: "among other records", masterKey: dnsCheckTestMasterKey, published: []string{"hello", other, want},
			status: "different", problem: "more_than_one", val: "hello, " + other + ", " + want},
		{name: "missing", masterKey: dnsCheckTestMasterKey, status: "missing", problem: "missing"},
		{name: "not a DKIM record", masterKey: dnsCheckTestMasterKey, published: []string{"google-site-verification=abc"},
			status: "different", problem: "malformed", val: "google-site-verification=abc"},
		{name: "another version", masterKey: dnsCheckTestMasterKey, published: []string{"v=DKIM2; k=ed25519; p=" + wantKey},
			status: "different", problem: "malformed", val: "v=DKIM2; k=ed25519; p=" + wantKey},
		{name: "p= not base64", masterKey: dnsCheckTestMasterKey, published: []string{"v=DKIM1; k=ed25519; p=!!!"},
			status: "different", problem: "malformed", val: "v=DKIM1; k=ed25519; p=!!!"},
		{name: "duplicated tag", masterKey: dnsCheckTestMasterKey, published: []string{want + "; p=" + wantKey},
			status: "different", problem: "malformed", val: want + "; p=" + wantKey},
		{name: "another key type", masterKey: dnsCheckTestMasterKey, published: []string{rsaShaped},
			status: "different", problem: "different_key_type", val: rsaShaped},
		{name: "another key", masterKey: dnsCheckTestMasterKey, published: []string{other},
			status: "different", problem: "different_key", val: other},
		{name: "the key revoked", masterKey: dnsCheckTestMasterKey, published: []string{"v=DKIM1; k=ed25519; p="},
			status: "different", problem: "different_key", val: "v=DKIM1; k=ed25519; p="},
		{name: "another key beats a malformed record", masterKey: dnsCheckTestMasterKey, published: []string{"junk", rsaShaped, other},
			status: "different", problem: "different_key", val: other},
		{name: "lookup failed", masterKey: dnsCheckTestMasterKey, lookupFails: true, status: "error"},
		{name: "key unreadable", masterKey: "the wrong master key", published: []string{want},
			status: "error", problem: "key_unreadable", val: want},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, domain, h := dnsCheckFixture(t, func(d *models.Domain) { d.DKIMPrivateKey = stored })
			h.SetMailServer(checkMailHost, tc.masterKey, dns.ClientPorts{Submission: 465, IMAP: 993, POP3: 995})
			recordName := "rm2026._domainkey." + domain.Name
			zone := aCorrectZone(domain.Name)
			delete(zone.txt, recordName)
			if tc.published != nil {
				zone.txt[recordName] = tc.published
			}
			if tc.lookupFails {
				zone.failing = map[string]bool{recordName: true}
			}
			zone.install(h)

			got, raw := dkimRecordOf(t, h, domain.ID)
			if got.Name != recordName {
				t.Fatalf("named %q, want %q", got.Name, recordName)
			}
			if got.Status != tc.status || got.Problem != tc.problem || got.Value != tc.val {
				t.Fatalf("got %s/%s value %q (%s), want %s/%s value %q", got.Status, got.Problem, got.Value, got.Message, tc.status, tc.problem, tc.val)
			}
			wantExpected := want
			if tc.problem == "key_unreadable" {
				wantExpected = ""
			}
			if got.Expected != wantExpected {
				t.Fatalf("expected = %q, want %q", got.Expected, wantExpected)
			}
			if (got.Status == "ok") == (got.Message != "") {
				t.Fatalf("message %q on a %s record", got.Message, got.Status)
			}
			assertNoPrivateMaterial(t, raw, privateMaterial(t, keyPEM, stored))
		})
	}

	t.Run("no key", func(t *testing.T) {
		_, domain, h := dnsCheckFixture(t, func(d *models.Domain) { d.DKIMSelector, d.DKIMPrivateKey = "", "" })
		aCorrectZone(domain.Name).install(h)
		got, _ := dkimRecordOf(t, h, domain.ID)
		if got.Status != "missing" || got.Expected != "" || got.Problem != "" || got.Name != domain.Name {
			t.Fatalf("got %+v", got)
		}
	})
}

// A rotation's retired key is published revoked, whichever type of key it was, so an empty p=
// is what the check wants there, and a key still published there is what differs.
func TestDNSCheck_DKIMRevokedRotationKey(t *testing.T) {
	_, other := dnsCheckTestKey(t)
	cases := []struct {
		name, published, status, problem string
	}{
		{"revoked as asked", dkimrotate.RevokedRecord, "ok", ""},
		{"revoked, naming its key type", "v=DKIM1; k=ed25519; p=", "ok", ""},
		{"the key still published", other, "different", "different_key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx, domain, h := dnsCheckFixture(t)
			revoked := time.Now()
			key := models.DKIMRotationKey{DomainID: domain.ID, Slot: models.DKIMRotationPrevious, Selector: "rm2025",
				PrivateKey: dnsCheckKeyPEM(t), TTLSeconds: 3600, RetireAfter: &revoked, RevokedAt: &revoked}
			if err := tx.Create(&key).Error; err != nil {
				t.Fatalf("create rotation key: %v", err)
			}
			name := "rm2025._domainkey." + domain.Name
			zone := aCorrectZone(domain.Name)
			zone.txt[name] = []string{tc.published}
			zone.install(h)

			r := checkDomainDNS(t, h, domain.ID).record(t, "DKIM", name)
			if r.Status != tc.status || r.Problem != tc.problem || r.Expected != dkimrotate.RevokedRecord {
				t.Fatalf("got %+v, want %s/%s", r, tc.status, tc.problem)
			}
		})
	}
}
