package filters

import (
	"context"
	"fmt"
	"strings"
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
// only an aligned SPF (MAIL FROM) or DKIM pass that spf_check or dkim_verify
// recorded for this run authenticates a sender's domain. A spoofer controls
// the envelope and the headers but cannot record a verdict.
func TestSenderAuthenticated(t *testing.T) {
	// partner.example and attacker.example are Organizational Domains.
	stubDMARC(t, onlyAt("v=DMARC1; p=none", "partner.example", "attacker.example"))
	cases := []struct {
		name     string
		verdicts []pipeline.AuthResult
		domain   string
		want     bool
	}{
		{"no verdicts", nil, "partner.example", false},
		{"spf pass aligned", []pipeline.AuthResult{spfVerdict("pass", "partner.example")}, "partner.example", true},
		{"spf pass subdomain aligned", []pipeline.AuthResult{spfVerdict("pass", "mail.partner.example")}, "partner.example", true},
		{"spf pass unaligned domain", []pipeline.AuthResult{spfVerdict("pass", "attacker.example")}, "partner.example", false},
		{"spf fail", []pipeline.AuthResult{spfVerdict("fail", "partner.example")}, "partner.example", false},
		{"spf HELO pass", []pipeline.AuthResult{{Method: pipeline.AuthSPF, Result: "pass", Property: pipeline.PropHelo, Domain: "partner.example"}}, "partner.example", false},
		{"dkim pass aligned", []pipeline.AuthResult{dkimVerdict("pass", "partner.example")}, "partner.example", true},
		{"dkim pass unaligned", []pipeline.AuthResult{dkimVerdict("pass", "attacker.example")}, "partner.example", false},
		{"spf pass no domain", []pipeline.AuthResult{spfVerdict("pass", "")}, "partner.example", false},
		{"dmarc pass alone", []pipeline.AuthResult{{Method: pipeline.AuthDMARC, Result: "pass", Property: pipeline.PropHeaderFrom, Domain: "partner.example"}}, "partner.example", false},
		{"empty domain", []pipeline.AuthResult{spfVerdict("pass", "partner.example")}, "", false},
		{"bare @domain", []pipeline.AuthResult{spfVerdict("pass", "partner.example")}, "@partner.example", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := senderAuthenticated(authCtx(tc.verdicts...), tc.domain); got != tc.want {
				t.Errorf("senderAuthenticated(%q) = %v, want %v", tc.domain, got, tc.want)
			}
		})
	}
	if senderAuthenticated(context.Background(), "partner.example") {
		t.Error("authenticated a sender outside a pipeline run")
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

// alignedVerdict is the verdict a passing SPF check of sender records: an
// aligned spf=pass. senderAuthenticated must treat it as a verified identity.
func alignedVerdict(sender string) pipeline.AuthResult {
	return spfVerdict("pass", sender[strings.LastIndexByte(sender, '@')+1:])
}

// forgedAuthResults is what a spoofer of sender can put in front of a filter:
// Authentication-Results text, with our authserv-id, claiming every pass.
func forgedAuthResults(sender string) map[string][]string {
	domain := sender[strings.LastIndexByte(sender, '@')+1:]
	return map[string][]string{"Authentication-Results": {
		"restmail; spf=pass smtp.mailfrom=" + domain + "; dkim=pass header.d=" + domain + "; dmarc=pass header.from=" + domain,
	}}
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
	run := func(raw map[string][]string, verdicts ...pipeline.AuthResult) *pipeline.FilterResult {
		t.Helper()
		f, err := NewDomainAllowlist(db)(nil)
		if err != nil {
			t.Fatalf("build domain_allowlist: %v", err)
		}
		email := &pipeline.EmailJSON{
			Envelope: pipeline.Envelope{MailFrom: sender, RcptTo: []string{rcpt}},
			Headers:  pipeline.Headers{Raw: raw},
		}
		res, err := f.Execute(authCtx(verdicts...), email)
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		return res
	}

	for name, raw := range map[string]map[string][]string{
		"spoofed sender does not skip scanning":                     nil, // no verdicts => unauthenticated
		"spoofed sender with forged results does not skip scanning": forgedAuthResults(sender),
	} {
		t.Run(name, func(t *testing.T) {
			res := run(raw)
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
	}

	t.Run("authenticated aligned sender still skips scanning", func(t *testing.T) {
		res := run(nil, alignedVerdict(sender))
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
	run := func(raw map[string][]string, verdicts ...pipeline.AuthResult) *pipeline.FilterResult {
		t.Helper()
		f, err := NewContactWhitelist(db)(nil)
		if err != nil {
			t.Fatalf("build contact_whitelist: %v", err)
		}
		email := &pipeline.EmailJSON{
			Envelope: pipeline.Envelope{MailFrom: sender, RcptTo: []string{rcpt}},
			Headers:  pipeline.Headers{Raw: raw},
		}
		res, err := f.Execute(authCtx(verdicts...), email)
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		return res
	}

	for name, raw := range map[string]map[string][]string{
		"spoofed sender does not skip scanning":                     nil,
		"spoofed sender with forged results does not skip scanning": forgedAuthResults(sender),
	} {
		t.Run(name, func(t *testing.T) {
			res := run(raw)
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
	}

	t.Run("authenticated aligned sender still skips scanning", func(t *testing.T) {
		res := run(nil, alignedVerdict(sender))
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
