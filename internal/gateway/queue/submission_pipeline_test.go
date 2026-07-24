package queue

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	dkim "github.com/rest-mail/go-dkim"
	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// captureRESTMAILServer starts an httptest TLS server that records the
// raw_message and to fields of the RESTMAIL delivery payload the worker POSTs,
// and answers 200. The recorded raw_message is exactly the bytes the worker
// would put on the wire, so a test can assert what a receiver actually sees.
func captureRESTMAILServer(t *testing.T, gotRaw *string, gotTo *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			To         []string `json:"to"`
			RawMessage string   `json:"raw_message"`
		}
		_ = json.Unmarshal(body, &payload)
		*gotRaw = payload.RawMessage
		*gotTo = payload.To
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestSubmittedMailBccStrippedBeforeRelay is a red-green guard for #171: an
// MUA-supplied Bcc header on submitted mail must be removed before the message
// is relayed, so Bcc recipients are not disclosed to every destination MX. The
// Bcc recipient is delivered via the envelope (its own queue row / the payload
// `to`), which is unaffected — only the header is withheld.
//
// Before the fix the worker relayed item.RawMessage verbatim, so the Bcc header
// reached the receiver. It needs no database: with no DKIM key for the sender
// domain the message is simply Bcc-stripped and relayed unsigned.
func TestSubmittedMailBccStrippedBeforeRelay(t *testing.T) {
	var gotRaw string
	var gotTo []string
	srv := captureRESTMAILServer(t, &gotRaw, &gotTo)

	// tlsInsecure accepts the httptest self-signed cert; allowPrivateDest permits
	// the loopback endpoint. No db => no DKIM key => unsigned relay, isolating the
	// Bcc-strip behavior.
	w := &Worker{sendDeadline: defaultSendDeadline, tlsInsecure: true, allowPrivateDest: true}

	item := models.OutboundQueue{
		Sender:    "alice@example.com",
		Recipient: "bob@example.net",
		RawMessage: "From: alice@example.com\r\n" +
			"To: bob@example.net\r\n" +
			"Bcc: secret-boss@example.org\r\n" +
			"Subject: hi\r\n" +
			"\r\n" +
			"hello\r\n",
	}

	if err := w.deliverRESTMAILHTTPS(srv.URL, item, false); err != nil {
		t.Fatalf("deliverRESTMAILHTTPS: %v", err)
	}

	if strings.Contains(strings.ToLower(gotRaw), "bcc:") {
		t.Fatalf("Bcc header relayed to receiver — Bcc recipients disclosed (#171):\n%s", gotRaw)
	}
	// The envelope recipient must be untouched: stripping the header must not drop
	// the delivery target, and the body/other headers must survive.
	if len(gotTo) != 1 || gotTo[0] != "bob@example.net" {
		t.Fatalf("envelope recipient altered by Bcc strip: got %v", gotTo)
	}
	if !strings.Contains(gotRaw, "Subject: hi") || !strings.Contains(gotRaw, "hello") {
		t.Fatalf("Bcc strip corrupted the message:\n%s", gotRaw)
	}
}

// TestSubmittedMailSignedBeforeRelay is a red-green guard for #171: submitted
// mail that arrives unsigned must be DKIM-signed with the sender domain's key
// before relay, so it passes DKIM/DMARC at receivers. Before the fix the worker
// relayed the raw bytes unsigned. Needs the unit-test Postgres to hold the
// domain's DKIM key; skips when none is reachable, per the repo convention.
func TestSubmittedMailSignedBeforeRelay(t *testing.T) {
	const masterKey = "submission-pipeline-master-key"
	db := openQueueTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	domainName := seedSignedDomain(t, tx, masterKey)

	var gotRaw string
	var gotTo []string
	srv := captureRESTMAILServer(t, &gotRaw, &gotTo)

	w := &Worker{db: tx, masterKey: masterKey, sendDeadline: defaultSendDeadline, tlsInsecure: true, allowPrivateDest: true}

	item := models.OutboundQueue{
		Sender:    "alice@" + domainName,
		Recipient: "bob@example.net",
		RawMessage: "From: alice@" + domainName + "\r\n" +
			"To: bob@example.net\r\n" +
			"Subject: hi\r\n" +
			"\r\n" +
			"hello\r\n",
	}

	if err := w.deliverRESTMAILHTTPS(srv.URL, item, false); err != nil {
		t.Fatalf("deliverRESTMAILHTTPS: %v", err)
	}
	if !strings.Contains(gotRaw, "DKIM-Signature:") {
		t.Fatalf("submitted mail relayed unsigned — no DKIM-Signature (#171):\n%s", gotRaw)
	}
	if !strings.Contains(gotRaw, "d="+domainName) {
		t.Fatalf("DKIM-Signature not for the sender domain %q:\n%s", domainName, gotRaw)
	}
}

// TestSubmittedMailRelayedUnsignedWhenNoMasterKey is the direct guard for the
// #171 E2E regression: the queue worker runs in a process (the SMTP gateway)
// that may have no MASTER_KEY, so it cannot decrypt an at-rest DKIM key. It must
// then relay the message UNSIGNED and deliver it — never fail closed and strand
// deliverable mail, which would drop every submitted outbound message for any
// domain with an encrypted key. Needs the unit-test Postgres; skips when absent.
func TestSubmittedMailRelayedUnsignedWhenNoMasterKey(t *testing.T) {
	db := openQueueTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	// Key encrypted at rest under a real master key...
	domainName := seedSignedDomain(t, tx, "the-real-master-key")

	var gotRaw string
	var gotTo []string
	srv := captureRESTMAILServer(t, &gotRaw, &gotTo)

	// ...but the worker has NO master key, so it cannot decrypt it.
	w := &Worker{db: tx, masterKey: "", sendDeadline: defaultSendDeadline, tlsInsecure: true, allowPrivateDest: true}

	item := models.OutboundQueue{
		Sender:     "alice@" + domainName,
		Recipient:  "bob@example.net",
		RawMessage: "From: alice@" + domainName + "\r\nTo: bob@example.net\r\nSubject: hi\r\n\r\nhello\r\n",
	}

	if err := w.deliverRESTMAILHTTPS(srv.URL, item, false); err != nil {
		t.Fatalf("delivery must NOT fail when the worker cannot sign (deliverable mail dropped): %v", err)
	}
	if strings.Contains(gotRaw, "DKIM-Signature:") {
		t.Fatalf("worker with no MASTER_KEY should relay unsigned, but a signature was added:\n%s", gotRaw)
	}
	if !strings.Contains(gotRaw, "hello") {
		t.Fatalf("message body not relayed:\n%s", gotRaw)
	}
}

// TestSubmittedMailFailsClosedOnWrongMasterKey proves the fail-closed guarantee
// is preserved for a GENUINE key fault: when the worker HAS a master key but it
// is the wrong one for the domain's encrypted key, signing must fail closed so
// the send temp-fails (retries) rather than going out unsigned. Needs the
// unit-test Postgres; skips when absent.
func TestSubmittedMailFailsClosedOnWrongMasterKey(t *testing.T) {
	db := openQueueTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	domainName := seedSignedDomain(t, tx, "the-real-master-key")

	var gotRaw string
	var gotTo []string
	srv := captureRESTMAILServer(t, &gotRaw, &gotTo)

	// A DIFFERENT, non-empty master key: the process can attempt decryption but
	// the key is wrong, so it is a real fault, not a missing-capability case.
	w := &Worker{db: tx, masterKey: "a-different-master-key", sendDeadline: defaultSendDeadline, tlsInsecure: true, allowPrivateDest: true}

	item := models.OutboundQueue{
		Sender:     "alice@" + domainName,
		Recipient:  "bob@example.net",
		RawMessage: "From: alice@" + domainName + "\r\nTo: bob@example.net\r\nSubject: hi\r\n\r\nhello\r\n",
	}

	if err := w.deliverRESTMAILHTTPS(srv.URL, item, false); err == nil {
		t.Fatal("expected fail-closed error on an undecryptable key, got nil (message would go out unsigned)")
	}
}

// TestAlreadySignedMailPassesThroughUnchanged proves the relay transform is
// idempotent: API-originated mail is Bcc-free and DKIM-signed at submission, so
// the worker must not disturb it (no second signature, no re-canonicalization
// that could break the existing one). Needs no database.
func TestAlreadySignedMailPassesThroughUnchanged(t *testing.T) {
	var gotRaw string
	var gotTo []string
	srv := captureRESTMAILServer(t, &gotRaw, &gotTo)

	w := &Worker{sendDeadline: defaultSendDeadline, tlsInsecure: true, allowPrivateDest: true}

	raw := "DKIM-Signature: v=1; a=rsa-sha256; d=example.com; s=sel; h=from:to:subject; b=abc\r\n" +
		"From: alice@example.com\r\n" +
		"To: bob@example.net\r\n" +
		"Subject: hi\r\n" +
		"\r\n" +
		"hello\r\n"
	item := models.OutboundQueue{Sender: "alice@example.com", Recipient: "bob@example.net", RawMessage: raw}

	if err := w.deliverRESTMAILHTTPS(srv.URL, item, false); err != nil {
		t.Fatalf("deliverRESTMAILHTTPS: %v", err)
	}
	if gotRaw != raw {
		t.Fatalf("already-signed message was modified before relay:\n got: %q\nwant: %q", gotRaw, raw)
	}
}

// ── test DB helpers (unit-test Postgres, skipped when unreachable) ──────────

func qEnvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func qEnvIntOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func openQueueTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := &config.Config{
		DBHost: qEnvOr("DB_HOST", "localhost"),
		DBPort: qEnvIntOr("DB_PORT", 5432),
		DBName: qEnvOr("DB_NAME", "restmail"),
		DBUser: qEnvOr("DB_USER", "restmail"),
		DBPass: qEnvOr("DB_PASS", "restmail"),
	}
	gdb, err := rmdb.Connect(cfg)
	if err != nil {
		t.Skipf("submission-pipeline DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.Domain{}); err != nil {
		t.Skipf("submission-pipeline DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

// seedSignedDomain creates a domain whose DKIM key is encrypted at rest under
// masterKey and returns the domain name.
func seedSignedDomain(t *testing.T, tx *gorm.DB, masterKey string) string {
	t.Helper()
	priv, _, err := dkim.GenerateKey(2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	stored, err := models.EncryptDKIMPrivateKey(priv, masterKey)
	if err != nil {
		t.Fatalf("EncryptDKIMPrivateKey: %v", err)
	}
	name := fmt.Sprintf("sub171-%d.test", time.Now().UnixNano())
	dom := models.Domain{Name: name, DKIMSelector: "sel", DKIMPrivateKey: stored}
	if err := tx.Create(&dom).Error; err != nil {
		t.Fatalf("seed domain: %v", err)
	}
	return name
}
