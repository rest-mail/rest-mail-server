package filters

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"gorm.io/gorm"
)

// installationRuleFixture makes its own recipient domains and a sender domain no
// other test uses, so the installation rules it adds touch no other test, and
// removes all of it afterwards.
type installationRuleFixture struct {
	t       *testing.T
	db      *gorm.DB
	sender  string
	domains []models.Domain
}

func newInstallationRuleFixture(t *testing.T, domains int) *installationRuleFixture {
	t.Helper()
	db := openAllowlistAuthTestDB(t)
	if err := db.AutoMigrate(&models.InstallationSenderRule{}); err != nil {
		t.Skipf("installation sender rule test skipped: migrate failed (%v)", err)
	}
	stamp := time.Now().UnixNano()
	f := &installationRuleFixture{t: t, db: db, sender: fmt.Sprintf("spam@sender-%d.example", stamp)}
	for i := 0; i < domains; i++ {
		d := models.Domain{Name: fmt.Sprintf("rcpt-%d-%d.test", i, stamp)}
		if err := db.Create(&d).Error; err != nil {
			t.Fatalf("create domain: %v", err)
		}
		f.domains = append(f.domains, d)
	}
	t.Cleanup(func() {
		senderDomain := f.sender[len("spam"):]
		db.Where("pattern IN ?", []string{f.sender, senderDomain}).Delete(&models.InstallationSenderRule{})
		for _, d := range f.domains {
			db.Where("domain_id = ?", d.ID).Delete(&models.DomainSenderRule{})
			db.Delete(&d)
		}
	})
	return f
}

func (f *installationRuleFixture) installationRule(pattern, listType string) {
	f.t.Helper()
	if err := f.db.Create(&models.InstallationSenderRule{Pattern: pattern, ListType: listType}).Error; err != nil {
		f.t.Fatalf("create installation %s rule: %v", listType, err)
	}
}

func (f *installationRuleFixture) domainRule(d models.Domain, pattern, listType string) {
	f.t.Helper()
	if err := f.db.Create(&models.DomainSenderRule{DomainID: d.ID, Pattern: pattern, ListType: listType}).Error; err != nil {
		f.t.Fatalf("create domain %s rule: %v", listType, err)
	}
}

func (f *installationRuleFixture) run(rcpts ...string) *pipeline.FilterResult {
	f.t.Helper()
	filter, err := NewDomainAllowlist(f.db)(nil)
	if err != nil {
		f.t.Fatalf("build domain_allowlist: %v", err)
	}
	res, err := filter.Execute(context.Background(), &pipeline.EmailJSON{
		Envelope: pipeline.Envelope{MailFrom: f.sender, RcptTo: rcpts},
	})
	if err != nil {
		f.t.Fatalf("execute: %v", err)
	}
	return res
}

func rcptAt(d models.Domain) string { return "user@" + d.Name }

// #1576's "Done when": one installation-wide block refuses the sender's mail to
// every domain, here two that have no rules of their own.
func TestInstallationBlock_RefusesMailForEveryDomain(t *testing.T) {
	f := newInstallationRuleFixture(t, 2)
	f.installationRule(f.sender, "block")

	for _, d := range f.domains {
		res := f.run(rcptAt(d))
		if res.Action != pipeline.ActionReject || res.Log.Result != "blocked" {
			t.Fatalf("mail to %s: action = %v result %q, want reject/blocked (detail %q)", d.Name, res.Action, res.Log.Result, res.Log.Detail)
		}
	}
}

// An installation block on the sender's @domain refuses its mail too.
func TestInstallationBlock_MatchesSenderDomainPattern(t *testing.T) {
	f := newInstallationRuleFixture(t, 1)
	f.installationRule(f.sender[len("spam"):], "block")

	if res := f.run(rcptAt(f.domains[0])); res.Action != pipeline.ActionReject {
		t.Fatalf("action = %v, want reject (detail %q)", res.Action, res.Log.Detail)
	}
}

// A domain's allow cannot lift the installation's block: the operator's block
// is not the customer's to bypass.
func TestInstallationBlock_WinsOverDomainAllow(t *testing.T) {
	f := newInstallationRuleFixture(t, 1)
	f.installationRule(f.sender, "block")
	f.domainRule(f.domains[0], f.sender, "allow")

	if res := f.run(rcptAt(f.domains[0])); res.Action != pipeline.ActionReject {
		t.Fatalf("action = %v, want reject: a domain allow overrode an installation block (detail %q)", res.Action, res.Log.Detail)
	}
}

// A domain block still refuses mail, even where the installation allows the
// sender, and only for that domain.
func TestDomainBlock_StillRefusesUnderInstallationAllow(t *testing.T) {
	f := newInstallationRuleFixture(t, 2)
	f.installationRule(f.sender, "allow")
	f.domainRule(f.domains[0], f.sender, "block")

	if res := f.run(rcptAt(f.domains[0])); res.Action != pipeline.ActionReject {
		t.Fatalf("blocking domain: action = %v, want reject (detail %q)", res.Action, res.Log.Detail)
	}
	res := f.run(rcptAt(f.domains[1]))
	if res.Action != pipeline.ActionContinue || res.Log.Result != "allowed" {
		t.Fatalf("other domain: action = %v result %q, want continue/allowed by the installation (detail %q)", res.Action, res.Log.Result, res.Log.Detail)
	}
}

// One recipient's allow does not carry the message past another recipient's
// block: every block is checked before any allow.
func TestDomainBlock_WinsOverAnotherRecipientsAllow(t *testing.T) {
	f := newInstallationRuleFixture(t, 2)
	f.domainRule(f.domains[0], f.sender, "allow")
	f.domainRule(f.domains[1], f.sender, "block")

	if res := f.run(rcptAt(f.domains[0]), rcptAt(f.domains[1])); res.Action != pipeline.ActionReject {
		t.Fatalf("action = %v, want reject (detail %q)", res.Action, res.Log.Detail)
	}
}
