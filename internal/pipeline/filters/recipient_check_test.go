package filters

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/bootstrap"
	"github.com/rest-mail/rest-mail-server/internal/config"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/testdb"
	"gorm.io/gorm"
)

// The full migration runs once per test binary, by whichever test comes first. It
// only adds to a database, and no test in this package removes anything from the
// schema, so running it again would change nothing.
var (
	recipientSchema    sync.Once
	recipientSchemaErr error
)

// openRecipientCheckTestDB connects to the unit-test Postgres, skipping (never
// failing) when none is reachable, per the repo's DB-test convention.
func openRecipientCheckTestDB(t *testing.T) *gorm.DB {
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
		testdb.Unreachable(t, "recipient_check DB test skipped: no database reachable (%v)", err)
	}
	testdb.ReuseConnections(t, gdb)
	if err := gdb.AutoMigrate(&models.Domain{}, &models.Mailbox{}, &models.Alias{}); err != nil {
		t.Skipf("recipient_check DB test skipped: migrate failed (%v)", err)
	}
	recipientSchema.Do(func() { recipientSchemaErr = rmdb.AutoMigrate(gdb) })
	if recipientSchemaErr != nil {
		t.Fatal(recipientSchemaErr)
	}
	if err := bootstrap.EnsureRBAC(gdb); err != nil {
		t.Fatal(err)
	}
	return gdb
}

func runRecipientCheck(t *testing.T, db *gorm.DB, rcpt string) *pipeline.FilterResult {
	t.Helper()
	factory := NewRecipientCheck(db)
	f, err := factory(nil)
	if err != nil {
		t.Fatalf("build recipient_check filter: %v", err)
	}
	res, err := f.Execute(context.Background(), &pipeline.EmailJSON{
		Envelope: pipeline.Envelope{RcptTo: []string{rcpt}},
	})
	if err != nil {
		t.Fatalf("recipient_check execute: %v", err)
	}
	return res
}

// TestRecipientCheck_Subaddressing proves RFC 5233 plus-addressing in the
// recipient_check filter: an exact mailbox passes, a "+detail" recipient falls
// back to the base mailbox, an explicit alias for the full "+detail" address is
// honored, and a recipient with neither an exact/base mailbox nor an alias is
// rejected.
func TestRecipientCheck_Subaddressing(t *testing.T) {
	db := openRecipientCheckTestDB(t)

	domainName := fmt.Sprintf("subaddr-rcpt-%d.test", time.Now().UnixNano())
	// Live: a domain that is not live has no usable certificate, so recipient_check
	// refuses mail for it (issues #289, #290). This test is about subaddressing.
	dom := models.Domain{Name: domainName, Active: true}
	if err := db.Create(&dom).Error; err != nil {
		t.Fatalf("create domain: %v", err)
	}
	// Base mailbox user@domain.
	mb := models.Mailbox{
		DomainID:  dom.ID,
		LocalPart: "user",
		Address:   "user@" + domainName,
		Active:    true,
	}
	if err := db.Create(&mb).Error; err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	if err := grants.EnsureReceive(db, &mb); err != nil {
		t.Fatal(err)
	}
	// Explicit alias for a full "+detail" address that has NO base mailbox.
	al := models.Alias{
		DomainID:           dom.ID,
		SourceAddress:      "team+promo@" + domainName,
		DestinationAddress: "user@" + domainName,
		Active:             true,
	}
	if err := db.Create(&al).Error; err != nil {
		t.Fatalf("create alias: %v", err)
	}

	cases := []struct {
		name       string
		rcpt       string
		wantAction pipeline.Action
	}{
		{"exact mailbox", "user@" + domainName, pipeline.ActionContinue},
		{"plus-detail falls back to base", "user+amazon@" + domainName, pipeline.ActionContinue},
		{"explicit alias for full +detail honored", "team+promo@" + domainName, pipeline.ActionContinue},
		{"no mailbox, no alias -> reject", "ghost+tag@" + domainName, pipeline.ActionReject},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := runRecipientCheck(t, db, tc.rcpt)
			if res.Action != tc.wantAction {
				t.Errorf("recipient_check(%q) action = %v, want %v (detail=%q)",
					tc.rcpt, res.Action, tc.wantAction, res.Log.Detail)
			}
		})
	}
}
