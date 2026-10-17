package filters

import (
	"fmt"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/gorm"
)

// fromAddress is a message From addr.
func fromAddress(addr string) *pipeline.EmailJSON {
	return &pipeline.EmailJSON{Headers: pipeline.Headers{From: []pipeline.Address{{Address: addr}}}}
}

// A subdomain that publishes no record of its own gets its Organizational
// Domain's subdomain policy (RFC 7489 §6.6.3), so spoofing it is no way round
// the organization's p=reject.
func TestDMARC_SubdomainUsesOrgDomainPolicy(t *testing.T) {
	stubDMARC(t, func(domain string) string {
		if domain == "example.test" {
			return "v=DMARC1; p=none; sp=reject"
		}
		return ""
	})
	f := &dmarcCheckFilter{}

	res, err := f.Execute(authCtx(spfVerdict("pass", "attacker.test")), fromAddress("ceo@mail.example.test"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != pipeline.ActionReject {
		t.Errorf("action: got %s want reject (detail: %s)", res.Action, res.Log.Detail)
	}

	// The organizational domain itself gets p=.
	res, err = f.Execute(authCtx(spfVerdict("pass", "attacker.test")), fromAddress("ceo@example.test"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != pipeline.ActionContinue {
		t.Errorf("org domain action: got %s want continue under p=none", res.Action)
	}
}

// Organizational Domains come from the Public Suffix List: two registrants
// under co.uk are two organizations, not one.
func TestDMARC_MultiLabelSuffixAlignment(t *testing.T) {
	stubDMARC(t, func(string) string { return "v=DMARC1; p=reject" })
	f := &dmarcCheckFilter{}

	cases := []struct {
		name       string
		verdict    pipeline.AuthResult
		wantAction pipeline.Action
	}{
		{"another registrant's dkim", dkimVerdict("pass", "b.other.co.uk"), pipeline.ActionReject},
		{"another registrant's spf", spfVerdict("pass", "b.other.co.uk"), pipeline.ActionReject},
		{"the suffix itself", dkimVerdict("pass", "co.uk"), pipeline.ActionReject},
		{"same organization's dkim", dkimVerdict("pass", "mail.example.co.uk"), pipeline.ActionContinue},
		{"same organization's spf", spfVerdict("pass", "bounces.example.co.uk"), pipeline.ActionContinue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := f.Execute(authCtx(tc.verdict), fromAddress("user@a.example.co.uk"))
			if err != nil {
				t.Fatal(err)
			}
			if res.Action != tc.wantAction {
				t.Errorf("action: got %s want %s (detail: %s)", res.Action, tc.wantAction, res.Log.Detail)
			}
		})
	}
}

// Strict alignment (adkim=s) is the record's to ask for: a subdomain's
// signature then does not align.
func TestDMARC_StrictAlignmentFromRecord(t *testing.T) {
	stubDMARC(t, func(string) string { return "v=DMARC1; p=reject; adkim=s" })
	f := &dmarcCheckFilter{}

	res, err := f.Execute(authCtx(dkimVerdict("pass", "mail.example.test")), fromExampleTest())
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != pipeline.ActionReject {
		t.Errorf("action: got %s want reject under adkim=s", res.Action)
	}
}

// openDMARCCaptureDB connects to the unit-test Postgres, skipping (never
// failing) when none is reachable, and returns a domain of the test's own
// whose captured rows are removed afterwards.
func openDMARCCaptureDB(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	cfg := &config.Config{
		DBHost: fcEnvOr("DB_HOST", "localhost"),
		DBPort: fcEnvIntOr("DB_PORT", 5432),
		DBName: fcEnvOr("DB_NAME", "restmail"),
		DBUser: fcEnvOr("DB_USER", "restmail"),
		DBPass: fcEnvOr("DB_PASS", "restmail"),
	}
	db, err := rmdb.Connect(cfg)
	if err != nil {
		t.Skipf("DMARC capture DB test skipped: no database reachable (%v)", err)
	}
	if err := db.AutoMigrate(&models.DMARCAggregateRecord{}); err != nil {
		t.Skipf("DMARC capture DB test skipped: migrate failed (%v)", err)
	}
	domain := fmt.Sprintf("capture-%d.test", time.Now().UnixNano())
	t.Cleanup(func() { db.Where("domain = ?", domain).Delete(&models.DMARCAggregateRecord{}) })
	return db, domain
}

// Mail with no client IP (a submission the SMTP gateway delivered locally) is
// not captured for aggregate reports, which must name the IP mail came from;
// mail from the same domain that has one still is.
func TestDMARC_CaptureSkipsMailWithoutClientIP(t *testing.T) {
	db, domain := openDMARCCaptureDB(t)
	stubDMARC(t, func(string) string { return "v=DMARC1; p=none; rua=mailto:agg@" + domain })
	f := &dmarcCheckFilter{}
	for _, ip := range []string{"192.0.2.10", ""} {
		email := fromAddress("user@" + domain)
		email.Envelope.Direction = "inbound"
		email.Envelope.ClientIP = ip
		if _, err := f.Execute(pipeline.WithDB(authCtx(spfVerdict("pass", domain)), db), email); err != nil {
			t.Fatal(err)
		}
	}

	var rows []models.DMARCAggregateRecord
	if err := db.Where("domain = ?", domain).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SourceIP != "192.0.2.10" {
		t.Fatalf("captured %+v, want one row from 192.0.2.10", rows)
	}
}

// A trusted sealer's ARC pass that overrides p=reject is captured with the
// reason the disposition differs from the policy, for the aggregate report.
func TestDMARC_CaptureRecordsTrustedForwarder(t *testing.T) {
	db, domain := openDMARCCaptureDB(t)
	stubDMARC(t, func(string) string { return "v=DMARC1; p=reject; rua=mailto:agg@" + domain })
	filt, err := NewDMARCCheckWithSealers([]string{"forwarder.example"})(nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, email := arcEmail(domain, "pass", "forwarder.example")
	email.Envelope.Direction = "inbound"
	email.Envelope.ClientIP = "192.0.2.20"
	if _, err := filt.Execute(pipeline.WithDB(ctx, db), email); err != nil {
		t.Fatal(err)
	}

	var row models.DMARCAggregateRecord
	if err := db.Where("domain = ?", domain).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Disposition != "none" || row.OverrideReason != "trusted_forwarder" || row.OverrideComment != "arc=pass from forwarder.example" {
		t.Errorf("captured disposition=%q reason=%q comment=%q, want none, trusted_forwarder, arc=pass from forwarder.example",
			row.Disposition, row.OverrideReason, row.OverrideComment)
	}
}
