package handlers

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"gorm.io/gorm"
)

type admissionExplainProbe struct {
	gorm.Dialector
	calls atomic.Int64
}

func (probe *admissionExplainProbe) Explain(query string, values ...any) string {
	probe.calls.Add(1)
	return probe.Dialector.Explain(query, values...)
}

func TestMessageAdmissionDoesNotFormatSilentSQL(t *testing.T) {
	db := importProofDB(t)
	fixture := seedAccountsFixture(t, db)
	if err := grants.EnsureImport(db, &fixture.user); err != nil {
		t.Fatal(err)
	}
	probe := &admissionExplainProbe{Dialector: db.Dialector}
	quiet := db.Session(&gorm.Session{NewDB: true})
	quiet.Dialector = probe
	held, err := grants.Load(quiet, fixture.user.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !held.Allows(models.CapMailImport, grants.Mailbox(fixture.user.Address)) {
		t.Fatal("the projected grant query lost the import allowance")
	}
	chain, err := repositories.StorageQuotaChain(quiet, fixture.user.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	remaining, err := repositories.StorageRemaining(quiet, chain)
	if err != nil || remaining <= 0 {
		t.Fatalf("remaining quota: %d, %v", remaining, err)
	}
	if calls := probe.calls.Load(); calls != 0 {
		t.Fatalf("silent admission queries formatted SQL %d times", calls)
	}
}
