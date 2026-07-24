package filters

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	dkim "github.com/rest-mail/go-dkim"
	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

func fcEnvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func fcEnvIntOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// openDKIMSignTestDB connects to the unit-test Postgres, skipping (never
// failing) when none is reachable, per the repo's DB-test convention.
func openDKIMSignTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := &config.Config{
		DBHost: fcEnvOr("DB_HOST", "localhost"),
		DBPort: fcEnvIntOr("DB_PORT", 5432),
		DBName: fcEnvOr("DB_NAME", "restmail"),
		DBUser: fcEnvOr("DB_USER", "restmail"),
		DBPass: fcEnvOr("DB_PASS", "restmail"),
	}
	gdb, err := rmdb.Connect(cfg)
	if err != nil {
		t.Skipf("dkim_sign DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.Domain{}); err != nil {
		t.Skipf("dkim_sign DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

// seedSignDomain creates a domain whose DKIM key is encrypted at rest under
// keyForStorage, returning the domain name.
func seedSignDomain(t *testing.T, tx *gorm.DB, keyForStorage string) string {
	t.Helper()
	priv, _, err := dkim.GenerateKey(2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	stored, err := models.EncryptDKIMPrivateKey(priv, keyForStorage)
	if err != nil {
		t.Fatalf("EncryptDKIMPrivateKey: %v", err)
	}
	name := fmt.Sprintf("osi8-sign-%d.test", time.Now().UnixNano())
	dom := models.Domain{Name: name, DKIMSelector: "sel", DKIMPrivateKey: stored}
	if err := tx.Create(&dom).Error; err != nil {
		t.Fatalf("seed domain: %v", err)
	}
	return name
}

func outboundEmail(fromDomain string) *pipeline.EmailJSON {
	return &pipeline.EmailJSON{
		Envelope: pipeline.Envelope{MailFrom: "alice@" + fromDomain, Direction: "outbound"},
		Headers: pipeline.Headers{
			From:      []pipeline.Address{{Address: "alice@" + fromDomain}},
			To:        []pipeline.Address{{Address: "bob@example.net"}},
			Subject:   "hi",
			Date:      "Mon, 01 Jan 2024 00:00:00 +0000",
			MessageID: "<abc@" + fromDomain + ">",
		},
		Body: pipeline.Body{ContentType: "text/plain", Content: "hello world\r\n"},
	}
}

func newSigner(t *testing.T, db *gorm.DB, masterKey string) pipeline.Filter {
	t.Helper()
	filt, err := NewDKIMSign(db, masterKey)(nil)
	if err != nil {
		t.Fatalf("NewDKIMSign: %v", err)
	}
	return filt
}

// TestDKIMSignFailsClosedOnUndecryptableKey: a key stored under one MASTER_KEY
// signed by a filter holding a DIFFERENT MASTER_KEY must FAIL (Execute returns
// an error → the engine defers) instead of silently emitting an unsigned
// message.
func TestDKIMSignFailsClosedOnUndecryptableKey(t *testing.T) {
	gdb := openDKIMSignTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })

	name := seedSignDomain(t, tx, "storage-master-key")
	signer := newSigner(t, tx, "a-different-master-key")

	res, err := signer.Execute(context.Background(), outboundEmail(name))
	if err == nil {
		t.Fatalf("expected fail-closed error, got nil (res=%+v)", res)
	}
	if !errors.Is(err, models.ErrDKIMKeyUndecryptable) {
		t.Fatalf("expected ErrDKIMKeyUndecryptable, got %v", err)
	}
}

// TestDKIMSignSucceedsOnGoodRoundTrip: a key stored and loaded under the SAME
// MASTER_KEY signs normally, emitting a DKIM-Signature header — proving the
// encrypt/decrypt round-trip yields a usable signing key.
func TestDKIMSignSucceedsOnGoodRoundTrip(t *testing.T) {
	gdb := openDKIMSignTestDB(t)
	tx := gdb.Begin()
	t.Cleanup(func() { tx.Rollback() })

	const masterKey = "matching-master-key"
	name := seedSignDomain(t, tx, masterKey)
	signer := newSigner(t, tx, masterKey)

	res, err := signer.Execute(context.Background(), outboundEmail(name))
	if err != nil {
		t.Fatalf("expected successful sign, got error: %v", err)
	}
	if res.Log.Result != "signed" {
		t.Fatalf("expected result=signed, got %q (detail=%q)", res.Log.Result, res.Log.Detail)
	}
	if res.Message == nil || res.Message.Headers.Extra["DKIM-Signature"] == "" {
		t.Fatal("expected a DKIM-Signature header on the signed message")
	}
}
