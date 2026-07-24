package filters

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

// openAllowlistAuthTestDB connects to the unit-test Postgres, skipping (never
// failing) when none is reachable, per the repo's DB-test convention.
func openAllowlistAuthTestDB(t *testing.T) *gorm.DB {
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
		t.Skipf("allowlist auth DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(
		&models.Domain{}, &models.Mailbox{}, &models.Contact{}, &models.DomainSenderRule{},
	); err != nil {
		t.Skipf("allowlist auth DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

// TestSenderAuthenticated exercises the DB-free authentication gate directly:
// only a locally-written, aligned SPF or DKIM pass authenticates a sender's
// domain. A spoofer controls the raw envelope but cannot forge these local
// verdicts (inbound Authentication-Results is stripped at ingress, #162).
func TestSenderAuthenticated(t *testing.T) {
	mk := func(ar string) *pipeline.EmailJSON {
		return &pipeline.EmailJSON{
			Headers: pipeline.Headers{Extra: map[string]string{"Authentication-Results": ar}},
		}
	}
	cases := []struct {
		name   string
		email  *pipeline.EmailJSON
		domain string
		want   bool
	}{
		{"no auth results", &pipeline.EmailJSON{}, "partner.example", false},
		{"spf pass aligned", mk("spf=pass smtp.mailfrom=alice@partner.example"), "partner.example", true},
		{"spf pass subdomain aligned", mk("spf=pass smtp.mailfrom=alice@mail.partner.example"), "partner.example", true},
		{"spf pass unaligned domain", mk("spf=pass smtp.mailfrom=alice@attacker.example"), "partner.example", false},
		{"spf fail", mk("spf=fail smtp.mailfrom=alice@partner.example"), "partner.example", false},
		{"dkim pass aligned", mk("dkim=pass header.d=partner.example"), "partner.example", true},
		{"dkim pass unaligned", mk("dkim=pass header.d=attacker.example"), "partner.example", false},
		{"spf pass no mailfrom domain", mk("spf=pass"), "partner.example", false},
		{"empty domain", mk("spf=pass smtp.mailfrom=alice@partner.example"), "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := senderAuthenticated(tc.email, tc.domain); got != tc.want {
				t.Errorf("senderAuthenticated(%q) = %v, want %v", tc.domain, got, tc.want)
			}
		})
	}
}

func containsFilter(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// alignedAuthResults builds the local Authentication-Results a passing SPF check
// would have written for sender: an aligned spf=pass. senderAuthenticated must
// treat this as a verified identity.
func alignedAuthResults(sender string) map[string]string {
	return map[string]string{
		"Authentication-Results": "spf=pass (matched ip4) smtp.mailfrom=" + sender,
	}
}

// TestDomainAllowlist_SkipRequiresAuthenticatedSender proves #177 for the
// domain allowlist: a spoofed sender (no passing/aligned SPF or DKIM) whose
// address matches an allow rule must NOT have spam/greylist scanning skipped,
// while a genuinely authenticated + aligned sender that matches the same rule
// still has scanning skipped.
func TestDomainAllowlist_SkipRequiresAuthenticatedSender(t *testing.T) {
	db := openAllowlistAuthTestDB(t)

	rcptDomainName := fmt.Sprintf("rcpt-allow-%d.test", time.Now().UnixNano())
	dom := models.Domain{Name: rcptDomainName}
	if err := db.Create(&dom).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}

	// Allow rule keyed on the sender's *domain* — exactly the entry a legitimate
	// correspondent would earn and an attacker would try to reuse by spoofing.
	senderDomain := "partner.example"
	sender := "alice@" + senderDomain
	rule := models.DomainSenderRule{
		DomainID: dom.ID,
		Pattern:  "@" + senderDomain,
		ListType: "allow",
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatalf("create allow rule: %v", err)
	}

	rcpt := "user@" + rcptDomainName
	run := func(extra map[string]string) *pipeline.FilterResult {
		t.Helper()
		f, err := NewDomainAllowlist(db)(nil)
		if err != nil {
			t.Fatalf("build domain_allowlist: %v", err)
		}
		email := &pipeline.EmailJSON{
			Envelope: pipeline.Envelope{MailFrom: sender, RcptTo: []string{rcpt}},
			Headers:  pipeline.Headers{Extra: extra},
		}
		res, err := f.Execute(context.Background(), email)
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		return res
	}

	t.Run("spoofed sender does not skip scanning", func(t *testing.T) {
		res := run(nil) // no local Authentication-Results => unauthenticated
		if res.Action != pipeline.ActionContinue {
			t.Fatalf("action = %v, want continue (detail=%q)", res.Action, res.Log.Detail)
		}
		for _, scan := range []string{"rspamd", "spamassassin", "greylist"} {
			if containsFilter(res.SkipFilters, scan) {
				t.Errorf("spoofed allowlist match skipped %q scanning (SkipFilters=%v, detail=%q)",
					scan, res.SkipFilters, res.Log.Detail)
			}
		}
	})

	t.Run("authenticated aligned sender still skips scanning", func(t *testing.T) {
		res := run(alignedAuthResults(sender))
		if res.Action != pipeline.ActionContinue {
			t.Fatalf("action = %v, want continue (detail=%q)", res.Action, res.Log.Detail)
		}
		for _, scan := range []string{"rspamd", "spamassassin", "greylist"} {
			if !containsFilter(res.SkipFilters, scan) {
				t.Errorf("authenticated allowlist match did NOT skip %q (SkipFilters=%v, detail=%q)",
					scan, res.SkipFilters, res.Log.Detail)
			}
		}
	})
}

// TestContactWhitelist_SkipRequiresAuthenticatedSender proves #177 for the
// contact whitelist: a spoofed sender matching a trusted contact must NOT have
// scanning skipped, while an authenticated + aligned sender still does.
func TestContactWhitelist_SkipRequiresAuthenticatedSender(t *testing.T) {
	db := openAllowlistAuthTestDB(t)

	rcptDomainName := fmt.Sprintf("rcpt-contact-%d.test", time.Now().UnixNano())
	dom := models.Domain{Name: rcptDomainName}
	if err := db.Create(&dom).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}
	mb := models.Mailbox{
		DomainID:  dom.ID,
		LocalPart: "bob",
		Address:   "bob@" + rcptDomainName,
		Password:  "x",
		Active:    true,
	}
	if err := db.Create(&mb).Error; err != nil {
		t.Fatalf("create mailbox: %v", err)
	}

	sender := "alice@partner.example"
	if err := db.Create(&models.Contact{
		MailboxID:  mb.ID,
		Email:      sender,
		TrustLevel: "trusted",
	}).Error; err != nil {
		t.Fatalf("create contact: %v", err)
	}

	rcpt := "bob@" + rcptDomainName
	run := func(extra map[string]string) *pipeline.FilterResult {
		t.Helper()
		f, err := NewContactWhitelist(db)(nil)
		if err != nil {
			t.Fatalf("build contact_whitelist: %v", err)
		}
		email := &pipeline.EmailJSON{
			Envelope: pipeline.Envelope{MailFrom: sender, RcptTo: []string{rcpt}},
			Headers:  pipeline.Headers{Extra: extra},
		}
		res, err := f.Execute(context.Background(), email)
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		return res
	}

	t.Run("spoofed sender does not skip scanning", func(t *testing.T) {
		res := run(nil)
		if res.Action != pipeline.ActionContinue {
			t.Fatalf("action = %v, want continue (detail=%q)", res.Action, res.Log.Detail)
		}
		for _, scan := range []string{"rspamd", "spamassassin", "greylist"} {
			if containsFilter(res.SkipFilters, scan) {
				t.Errorf("spoofed trusted-contact match skipped %q scanning (SkipFilters=%v, detail=%q)",
					scan, res.SkipFilters, res.Log.Detail)
			}
		}
	})

	t.Run("authenticated aligned sender still skips scanning", func(t *testing.T) {
		res := run(alignedAuthResults(sender))
		if res.Action != pipeline.ActionContinue {
			t.Fatalf("action = %v, want continue (detail=%q)", res.Action, res.Log.Detail)
		}
		for _, scan := range []string{"rspamd", "spamassassin", "greylist"} {
			if !containsFilter(res.SkipFilters, scan) {
				t.Errorf("authenticated trusted-contact match did NOT skip %q (SkipFilters=%v, detail=%q)",
					scan, res.SkipFilters, res.Log.Detail)
			}
		}
	})
}
