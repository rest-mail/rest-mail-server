package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	dkim "github.com/rest-mail/go-dkim"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/outbound"
	"gorm.io/gorm"
)

// openRotationTestDB is the authz test database with the rotation key slots the
// settings read reports from.
func openRotationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := openAuthzTestDB(t)
	if err := db.AutoMigrate(&models.DKIMRotationKey{}); err != nil {
		t.Skipf("DKIM rotation DB test skipped: migrate failed (%v)", err)
	}
	return db
}

// A domain's rotation reads where it is (#728): stable with no key in rotation, then
// awaiting publication of the staged key's record, then waiting one TTL once it resolved,
// then retiring the previous key. Each key comes with the TXT record to publish, under its
// selector and the domain's A-label name, and the time the rotation moves on.
func TestDKIMRotationStatus(t *testing.T) {
	db := openRotationTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	const master = "rotation-status-master-key-0123456789"
	private, public, err := dkim.GenerateKey(outbound.MinDKIMRSABits)
	if err != nil {
		t.Fatal(err)
	}
	want, err := dkim.RecordValue(public)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := models.EncryptDKIMPrivateKey(private, master)
	if err != nil {
		t.Fatal(err)
	}
	domain := models.Domain{Name: fmt.Sprintf("bücher-%d.test", time.Now().UnixNano()), DKIMRotationIntervalDays: 90}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	read := func(t *testing.T) dkimRotationSettings {
		t.Helper()
		rr := httptest.NewRecorder()
		dkimRoutes(NewDKIMHandler(tx, master)).ServeHTTP(rr, rotationRequest(http.MethodGet, fmt.Sprint(domain.ID), ""))
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body)
		}
		var response struct {
			Data dkimRotationSettings `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response.Data
	}

	if got := read(t); got.State != "stable" || got.Staged != nil || got.Previous != nil {
		t.Fatalf("no key in rotation: %+v", got)
	}

	staged := models.DKIMRotationKey{DomainID: domain.ID, Slot: models.DKIMRotationStaged, Selector: "restmail-2", PrivateKey: stored, TTLSeconds: 3600}
	if err := tx.Create(&staged).Error; err != nil {
		t.Fatal(err)
	}
	got := read(t)
	if got.State != "awaiting_publication" || got.Staged == nil || got.Staged.PublishedAt != nil || got.Staged.ActivatesAt != nil {
		t.Fatalf("a staged key not yet published: %+v", got)
	}
	ascii := "restmail-2._domainkey.xn--bcher-" // the rest depends on the generated name
	if got.Staged.Selector != "restmail-2" || len(got.Staged.RecordName) < len(ascii) || got.Staged.RecordName[:len(ascii)] != ascii || got.Staged.RecordValue != want {
		t.Fatalf("the staged key's record: %+v, want %s… = %s", got.Staged, ascii, want)
	}

	published := time.Now().UTC().Truncate(time.Second)
	if err := tx.Model(&staged).Update("published_at", published).Error; err != nil {
		t.Fatal(err)
	}
	got = read(t)
	if got.State != "waiting_ttl" || got.Staged.PublishedAt == nil || got.Staged.ActivatesAt == nil ||
		!got.Staged.ActivatesAt.Equal(published.Add(time.Hour)) {
		t.Fatalf("a published staged key: %+v", got.Staged)
	}

	retire := published.Add(7 * 24 * time.Hour)
	if err := tx.Delete(&staged).Error; err != nil {
		t.Fatal(err)
	}
	previous := models.DKIMRotationKey{DomainID: domain.ID, Slot: models.DKIMRotationPrevious, Selector: "default", PrivateKey: stored,
		TTLSeconds: 3600, InstalledAt: &published, PublishedAt: &published, RetireAfter: &retire}
	if err := tx.Create(&previous).Error; err != nil {
		t.Fatal(err)
	}
	got = read(t)
	if got.State != "retiring" || got.Staged != nil || got.Previous == nil || got.Previous.Selector != "default" ||
		got.Previous.RetireAfter == nil || !got.Previous.RetireAfter.Equal(retire) || got.Previous.RecordValue != want {
		t.Fatalf("a previous key retiring: %+v", got)
	}
}
