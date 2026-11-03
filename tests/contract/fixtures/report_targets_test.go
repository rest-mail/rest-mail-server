package main

import (
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/tests/contract/fixtureplan"
	"gorm.io/gorm"
)

func reportTargetAdmin(t *testing.T, db *gorm.DB) models.Mailbox {
	t.Helper()
	plan, err := fixtureplan.Default()
	if err != nil {
		t.Fatal(err)
	}
	hosted := models.Domain{Name: "restmail.test", Active: true}
	if err := db.Create(&hosted).Error; err != nil {
		t.Fatal(err)
	}
	address := plan.Principals["admin"].Address
	admin := models.Mailbox{DomainID: hosted.ID, LocalPart: strings.Split(address, "@")[0], Address: address, Active: true}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	return admin
}

func heldRows(t *testing.T, db *gorm.DB, admin models.Mailbox) (held, suppressed int64) {
	t.Helper()
	if err := db.Model(&models.Quarantine{}).Where("mailbox_id = ? AND released = ?", admin.ID, false).Count(&held).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.Suppression{}).Where("domain_id = ? AND address LIKE ?", admin.DomainID, "contract-suppressed-%").Count(&suppressed).Error; err != nil {
		t.Fatal(err)
	}
	return held, suppressed
}

func TestReportTargetsRefuseAnythingButAReportPhaseOnTheDisposableDomain(t *testing.T) {
	if err := prepareReportTargets(nil, "example.com", "authorization"); err == nil {
		t.Fatal("prepared report targets outside the disposable domain")
	}
	for _, phase := range []string{"", "gate", "coverage", "examples", "fuzz"} {
		if err := prepareReportTargets(nil, "restmail.test", phase); err == nil {
			t.Errorf("prepared report targets for %q", phase)
		}
	}
}

func TestReportTargetsTopUpOnlyWhatThePhaseConsumes(t *testing.T) {
	db := fixtureDB(t)
	admin := reportTargetAdmin(t, db)

	if err := prepareReportTargets(db, "restmail.test", "ownership"); err != nil {
		t.Fatal(err)
	}
	if held, suppressed := heldRows(t, db, admin); held != 1 || suppressed != 1 {
		t.Fatalf("ownership holds %d messages and %d suppressions, want 1 and 1", held, suppressed)
	}
	if err := prepareReportTargets(db, "restmail.test", "authorization"); err != nil {
		t.Fatal(err)
	}
	if held, suppressed := heldRows(t, db, admin); held != 2 || suppressed != 2 {
		t.Fatalf("authorization holds %d messages and %d suppressions, want 2 and 2", held, suppressed)
	}
	// A repeat, or a phase needing fewer, makes nothing more.
	for _, phase := range []string{"authorization", "ownership"} {
		if err := prepareReportTargets(db, "restmail.test", phase); err != nil {
			t.Fatal(err)
		}
		if held, suppressed := heldRows(t, db, admin); held != 2 || suppressed != 2 {
			t.Fatalf("%s grew the rows to %d messages and %d suppressions", phase, held, suppressed)
		}
	}
}

func TestReportTargetsCountOnlyRowsTheHooksWouldTake(t *testing.T) {
	db := fixtureDB(t)
	admin := reportTargetAdmin(t, db)
	now := time.Now()
	released := heldMessage(admin, "Released", now)
	released.Released = true
	if err := db.Create(released).Error; err != nil {
		t.Fatal(err)
	}
	expired := suppressedRecipient(admin, "contract-suppressed-expired@nowhere.invalid", now)
	expired.ExpiresAt = now.Add(-time.Hour)
	if err := db.Create(expired).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(suppressedRecipient(admin, "someone@else.test", now)).Error; err != nil {
		t.Fatal(err)
	}
	if err := prepareReportTargets(db, "restmail.test", "ownership"); err != nil {
		t.Fatal(err)
	}
	if held, _ := heldRows(t, db, admin); held != 1 {
		t.Errorf("%d unreleased messages, want the 1 made beside the released one", held)
	}
	var fresh int64
	if err := db.Model(&models.Suppression{}).
		Where("domain_id = ? AND address LIKE ? AND expires_at > ?", admin.DomainID, "contract-suppressed-%", now).
		Count(&fresh).Error; err != nil {
		t.Fatal(err)
	}
	if fresh != 1 {
		t.Errorf("%d live fixture suppressions, want 1", fresh)
	}
}

// The gate's own fixtures must stay one of each: coverage's skipped-probe
// ratchet depends on those rows being spent.
func TestTheGatesHeldMailStaysOneOfEach(t *testing.T) {
	db := fixtureDB(t)
	admin := reportTargetAdmin(t, db)
	if err := prepareHeldMail(db, "restmail.test"); err != nil {
		t.Fatal(err)
	}
	if held, suppressed := heldRows(t, db, admin); held != 1 || suppressed != 1 {
		t.Fatalf("the gate's fixtures made %d messages and %d suppressions, want 1 and 1", held, suppressed)
	}
}
