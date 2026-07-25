package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

// redirectEmail builds a pipeline EmailJSON carrying the redirect_to metadata the
// sieve filter records, so the consumer (buildRedirectForwards) can be exercised
// without running the whole pipeline.
func redirectEmail(targets []string, suppressKeep bool) *pipeline.EmailJSON {
	meta := map[string]string{}
	if len(targets) > 0 {
		b, _ := json.Marshal(targets)
		meta["redirect_to"] = string(b)
	}
	if suppressKeep {
		meta["redirect_suppress_keep"] = "true"
	}
	return &pipeline.EmailJSON{Metadata: meta}
}

// TestBuildRedirectForwards_ForwardsOriginalMessage is the core RED->GREEN check:
// a recorded redirect target produces an outbound-queue row that forwards the
// ORIGINAL message to that address, preserving the original envelope sender and
// stamping a Delivered-To header. Before this consumer existed the redirect
// metadata dead-ended and nothing was ever queued.
func TestBuildRedirectForwards_ForwardsOriginalMessage(t *testing.T) {
	originalRaw := "From: alice@remote.test\r\n" +
		"To: user@local.test\r\n" +
		"Subject: Hi\r\n" +
		"\r\n" +
		"hello body\r\n"

	forwards := buildRedirectForwards(
		redirectEmail([]string{"fwd@example.com"}, true),
		originalRaw,
		"alice@remote.test",
		[]string{"user@local.test"},
	)

	if len(forwards) != 1 {
		t.Fatalf("got %d forwards, want 1", len(forwards))
	}
	f := forwards[0]
	if f.Recipient != "fwd@example.com" {
		t.Errorf("Recipient = %q, want fwd@example.com", f.Recipient)
	}
	if f.Sender != "alice@remote.test" {
		t.Errorf("Sender = %q, want the preserved original sender alice@remote.test", f.Sender)
	}
	if f.Domain != "example.com" {
		t.Errorf("Domain = %q, want example.com", f.Domain)
	}
	if f.Status != "pending" {
		t.Errorf("Status = %q, want pending", f.Status)
	}
	if !strings.HasPrefix(f.RawMessage, "Delivered-To: fwd@example.com\r\n") {
		t.Errorf("forwarded message must start with a Delivered-To header for the target, got:\n%s", f.RawMessage)
	}
	if !strings.Contains(f.RawMessage, "hello body") || !strings.Contains(f.RawMessage, "From: alice@remote.test") {
		t.Errorf("forwarded message must carry the original content, got:\n%s", f.RawMessage)
	}
}

// TestBuildRedirectForwards_LoopSuppressed_DeliveredToChain: a target already in
// the message's Delivered-To chain is not forwarded again.
func TestBuildRedirectForwards_LoopSuppressed_DeliveredToChain(t *testing.T) {
	originalRaw := "Delivered-To: fwd@example.com\r\n" +
		"From: alice@remote.test\r\n" +
		"To: user@local.test\r\n" +
		"\r\n" +
		"body\r\n"

	forwards := buildRedirectForwards(
		redirectEmail([]string{"fwd@example.com"}, true),
		originalRaw,
		"alice@remote.test",
		[]string{"user@local.test"},
	)
	if len(forwards) != 0 {
		t.Fatalf("got %d forwards, want 0 (loop suppressed)", len(forwards))
	}
}

// TestBuildRedirectForwards_LoopSuppressed_SelfRecipient: forwarding to one of
// the current recipients (a self-redirect) is suppressed.
func TestBuildRedirectForwards_LoopSuppressed_SelfRecipient(t *testing.T) {
	forwards := buildRedirectForwards(
		redirectEmail([]string{"user@local.test"}, true),
		"From: a@b.test\r\n\r\nbody\r\n",
		"a@b.test",
		[]string{"user@local.test"},
	)
	if len(forwards) != 0 {
		t.Fatalf("got %d forwards, want 0 (self-redirect suppressed)", len(forwards))
	}
}

// TestBuildRedirectForwards_NoMetadata: no redirect metadata yields no forwards.
func TestBuildRedirectForwards_NoMetadata(t *testing.T) {
	if f := buildRedirectForwards(&pipeline.EmailJSON{}, "raw", "a@b.test", nil); len(f) != 0 {
		t.Errorf("expected no forwards without redirect metadata, got %d", len(f))
	}
	if f := buildRedirectForwards(nil, "raw", "a@b.test", nil); len(f) != 0 {
		t.Errorf("expected no forwards for nil email, got %d", len(f))
	}
	// No original bytes to forward: fall back to keeping locally (no forward), so
	// a redirect that cannot be honoured never loses the message.
	if f := buildRedirectForwards(redirectEmail([]string{"x@y.test"}, true), "", "a@b.test", nil); len(f) != 0 {
		t.Errorf("expected no forwards when there is no original message, got %d", len(f))
	}
}

func TestRedirectSuppressesKeep(t *testing.T) {
	if !redirectSuppressesKeep(redirectEmail([]string{"x@y.test"}, true)) {
		t.Error("expected suppress=true when redirect_suppress_keep is set")
	}
	if redirectSuppressesKeep(redirectEmail([]string{"x@y.test"}, false)) {
		t.Error("expected suppress=false when redirect_suppress_keep is unset (e.g. :copy)")
	}
}

func TestDeliveredToChain(t *testing.T) {
	raw := "Delivered-To: a@x.test\r\nReceived: by mx\r\nDelivered-To: <b@y.test>\r\n\r\nDelivered-To: not-a-header@z.test\r\n"
	got := deliveredToChain(raw)
	if len(got) != 2 || got[0] != "a@x.test" || got[1] != "<b@y.test>" {
		t.Fatalf("deliveredToChain = %v, want [a@x.test <b@y.test>] (body line ignored)", got)
	}
}

// ── End-to-end: RESTMAIL delivery enqueues the redirect (needs Postgres) ──

func openRedirectTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := &config.Config{
		DBHost: envOr("DB_HOST", "localhost"),
		DBPort: envIntOr("DB_PORT", 5432),
		DBName: envOr("DB_NAME", "restmail"),
		DBUser: envOr("DB_USER", "restmail"),
		DBPass: envOr("DB_PASS", "restmail"),
	}
	gdb, err := rmdb.Connect(cfg)
	if err != nil {
		t.Skipf("restmail redirect DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.Domain{}, &models.Mailbox{}, &models.Alias{}, &models.Message{},
		&models.QuotaUsage{}, &models.Pipeline{}, &models.OutboundQueue{}); err != nil {
		t.Skipf("restmail redirect DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

// TestRESTMAILDeliver_SieveRedirect proves the full inbound path: a mailbox whose
// inbound pipeline has a Sieve `redirect` script forwards arriving mail onto the
// outbound queue. A bare redirect forwards WITHOUT storing a local copy; a
// `redirect :copy` forwards AND stores the local copy.
func TestRESTMAILDeliver_SieveRedirect(t *testing.T) {
	db := openRedirectTestDB(t)
	engine := pipeline.NewEngine(pipeline.DefaultRegistry, slog.Default())

	// setup provisions a fresh domain + mailbox + inbound pipeline whose sieve
	// script is built from the concrete generated domain via scriptFn.
	setup := func(t *testing.T, localPart string, scriptFn func(domain string) string) (*RestmailHandler, models.Mailbox, string) {
		t.Helper()
		suffix := time.Now().UnixNano()
		domainName := fmt.Sprintf("redir-%d.test", suffix)
		dom := models.Domain{Name: domainName, Active: true}
		if err := db.Create(&dom).Error; err != nil {
			t.Fatalf("create domain: %v", err)
		}
		addr := localPart + "@" + domainName
		mb := models.Mailbox{DomainID: dom.ID, LocalPart: localPart, Address: addr, Password: "x", Active: true, QuotaBytes: 1 << 30}
		if err := db.Create(&mb).Error; err != nil {
			t.Fatalf("create mailbox: %v", err)
		}
		sieveCfg, _ := json.Marshal(map[string]string{"script": scriptFn(domainName)})
		filters, _ := json.Marshal([]pipeline.FilterConfig{
			{Name: "sieve", Type: pipeline.FilterTypeTransform, Enabled: true, Config: sieveCfg},
		})
		pl := models.Pipeline{DomainID: dom.ID, Direction: "inbound", Filters: filters, Active: true}
		if err := db.Create(&pl).Error; err != nil {
			t.Fatalf("create pipeline: %v", err)
		}
		t.Cleanup(func() {
			db.Where("domain = ?", domainName).Delete(&models.OutboundQueue{})
			db.Where("mailbox_id = ?", mb.ID).Delete(&models.Message{})
			db.Where("id = ?", pl.ID).Delete(&models.Pipeline{})
			db.Where("id = ?", mb.ID).Delete(&models.Mailbox{})
			db.Where("id = ?", dom.ID).Delete(&models.Domain{})
		})
		return NewRestmailHandler(db, engine, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{}), mb, domainName
	}

	countForwards := func(t *testing.T, domain, target string) int64 {
		t.Helper()
		var n int64
		db.Model(&models.OutboundQueue{}).Where("recipient = ? AND domain = ?", target, domain).Count(&n)
		return n
	}
	countStored := func(t *testing.T, mailboxID uint) int64 {
		t.Helper()
		var n int64
		db.Model(&models.Message{}).Where("mailbox_id = ?", mailboxID).Count(&n)
		return n
	}

	t.Run("bare redirect forwards and does not keep", func(t *testing.T) {
		h, mb, domain := setup(t, "user", func(d string) string {
			return `redirect "fwd@` + d + `";`
		})
		target := "fwd@" + domain

		raw := "From: alice@remote.test\r\nTo: " + mb.Address + "\r\nSubject: Hi\r\n\r\nhello body\r\n"
		delivered, _ := deliverRESTMAIL(t, h, "alice@remote.test", raw, mb.Address)
		if len(delivered) != 1 {
			t.Fatalf("delivered = %v, want the recipient accepted (forwarded)", delivered)
		}
		if got := countForwards(t, domain, target); got != 1 {
			t.Errorf("outbound_queue rows for %s = %d, want 1 (redirect forwarded)", target, got)
		}
		if got := countStored(t, mb.ID); got != 0 {
			t.Errorf("stored messages = %d, want 0 (bare redirect must not also keep)", got)
		}
	})

	t.Run("redirect :copy forwards and keeps", func(t *testing.T) {
		h, mb, domain := setup(t, "user2", func(d string) string {
			return `require ["copy"]; redirect :copy "fwd@` + d + `";`
		})
		target := "fwd@" + domain

		raw := "From: alice@remote.test\r\nTo: " + mb.Address + "\r\nSubject: Hi\r\n\r\nhello body\r\n"
		delivered, _ := deliverRESTMAIL(t, h, "alice@remote.test", raw, mb.Address)
		if len(delivered) != 1 {
			t.Fatalf("delivered = %v, want the recipient delivered", delivered)
		}
		if got := countForwards(t, domain, target); got != 1 {
			t.Errorf("outbound_queue rows for %s = %d, want 1 (:copy forwarded)", target, got)
		}
		if got := countStored(t, mb.ID); got != 1 {
			t.Errorf("stored messages = %d, want 1 (:copy also keeps a local copy)", got)
		}
	})
}
