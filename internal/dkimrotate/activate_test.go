package dkimrotate_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	dkim "github.com/rest-mail/go-dkim"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/dkimrotate"
	"github.com/restmail/restmail/internal/outbound"
	"gorm.io/gorm"
)

func activationMaterial(t *testing.T) (string, string) {
	t.Helper()
	private, public, err := dkim.GenerateKey(outbound.MinDKIMRSABits)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := models.EncryptDKIMPrivateKey(private, stageMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	record, err := dkim.RecordValue(public)
	if err != nil {
		t.Fatal(err)
	}
	return stored, record
}

func activationFixture(t *testing.T, database *gorm.DB, name, current, staged string, now time.Time) (models.Domain, models.DKIMRotationKey) {
	t.Helper()
	installed := now.Add(-91 * 24 * time.Hour)
	domain := models.Domain{Name: name, DKIMSelector: "restmail-1", DKIMPrivateKey: current,
		DKIMCreatedAt: &installed, DKIMRotationIntervalDays: 90}
	if err := database.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	published := now.Add(-time.Hour)
	key := models.DKIMRotationKey{DomainID: domain.ID, Slot: models.DKIMRotationStaged,
		Selector: "restmail-2", PrivateKey: staged, PublishedAt: &published, TTLSeconds: 3600}
	if err := database.Omit("Domain").Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	return domain, key
}

func TestActivate_SwitchesAndRetainsRollbackKey(t *testing.T) {
	database := openStageTestDB(t)
	current, _ := activationMaterial(t)
	staged, record := activationMaterial(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%v", legacy), func(t *testing.T) {
			domain, key := activationFixture(t, database, fmt.Sprintf("activate-%v.test", legacy), current, staged, now)
			if legacy {
				if err := database.Model(&domain).Update("dkim_created_at", nil).Error; err != nil {
					t.Fatal(err)
				}
				domain.DKIMCreatedAt = nil
			}
			calls := 0
			clockTime := now
			switchedAt := now.Add(2 * time.Minute)
			lookup := func(_ context.Context, name string) ([]string, error) {
				calls++
				if name != dkim.RecordName(key.Selector, domain.Name) {
					t.Fatalf("lookup name = %s", name)
				}
				clockTime = switchedAt
				return []string{record}, nil
			}
			activated, err := dkimrotate.Activate(context.Background(), database, domain.ID, stageMasterKey, lookup, func() time.Time { return clockTime })
			if err != nil || !activated || calls != 1 {
				t.Fatalf("Activate = %v, %v, DNS calls %d", activated, err, calls)
			}
			var after models.Domain
			if err := database.First(&after, domain.ID).Error; err != nil {
				t.Fatal(err)
			}
			if after.DKIMSelector != key.Selector || after.DKIMPrivateKey != staged || after.DKIMCreatedAt == nil || !after.DKIMCreatedAt.Equal(switchedAt) || after.DKIMRotationIntervalDays != 90 {
				t.Fatal("current key or rotation policy not preserved/updated as expected")
			}
			var slots []models.DKIMRotationKey
			if err := database.Where("domain_id = ?", domain.ID).Find(&slots).Error; err != nil {
				t.Fatal(err)
			}
			if len(slots) != 1 {
				t.Fatalf("remaining slots = %d, want only previous", len(slots))
			}
			previous := slots[0]
			if previous.Slot != models.DKIMRotationPrevious || previous.Selector != domain.DKIMSelector || previous.PrivateKey != current ||
				previous.TTLSeconds != 3600 || previous.RetireAfter == nil || !previous.RetireAfter.Equal(switchedAt.Add(time.Hour)) {
				t.Fatal("rollback key, ciphertext or overlap not preserved")
			}
			if legacy {
				if previous.InstalledAt != nil {
					t.Fatal("legacy installation time was invented")
				}
			} else if previous.InstalledAt == nil || !previous.InstalledAt.Equal(*domain.DKIMCreatedAt) {
				t.Fatal("old installation time not preserved")
			}
			activated, err = dkimrotate.Activate(context.Background(), database, domain.ID, stageMasterKey, lookup, func() time.Time { return switchedAt.Add(time.Minute) })
			if err != nil || activated || calls != 1 {
				t.Fatalf("repeat Activate = %v, %v, calls %d", activated, err, calls)
			}
		})
	}
}

func TestActivate_RefusesUnsafeSwitches(t *testing.T) {
	database := openStageTestDB(t)
	current, _ := activationMaterial(t)
	staged, record := activationMaterial(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	ctx := context.Background()
	cases := []struct {
		name   string
		change func(*models.Domain, *models.DKIMRotationKey)
		txt    string
		want   error
		error  bool
	}{
		{name: "unpublished", change: func(_ *models.Domain, k *models.DKIMRotationKey) { k.PublishedAt = nil }},
		{name: "TTL not elapsed", change: func(_ *models.Domain, k *models.DKIMRotationKey) {
			v := now.Add(-time.Hour + time.Microsecond)
			k.PublishedAt = &v
		}},
		{name: "future publication", change: func(_ *models.Domain, k *models.DKIMRotationKey) { v := now.Add(time.Hour); k.PublishedAt = &v }},
		{name: "disabled", change: func(d *models.Domain, _ *models.DKIMRotationKey) { d.DKIMRotationIntervalDays = 0 }},
		{name: "current replaced recently", change: func(d *models.Domain, _ *models.DKIMRotationKey) { d.DKIMCreatedAt = &now }},
		{name: "current removed", change: func(d *models.Domain, _ *models.DKIMRotationKey) { d.DKIMPrivateKey = "" }},
		{name: "selector conflict", change: func(d *models.Domain, _ *models.DKIMRotationKey) { d.DKIMSelector = "RESTMAIL-2" }, want: dkimrotate.ErrSelectorConflict},
		{name: "wrong DNS key", txt: "v=DKIM1; p=different", want: dkimrotate.ErrNotPublished},
		{name: "missing DNS key", want: dkimrotate.ErrNotPublished},
		{name: "corrupt staged key", change: func(_ *models.Domain, k *models.DKIMRotationKey) { k.PrivateKey = "dkim:v1:corrupt" }, error: true},
		{name: "invalid staged key", change: func(_ *models.Domain, k *models.DKIMRotationKey) {
			var err error
			k.PrivateKey, err = models.EncryptDKIMPrivateKey("not a signing key", stageMasterKey)
			if err != nil {
				t.Fatal(err)
			}
		}, error: true},
		{name: "corrupt current key", change: func(d *models.Domain, _ *models.DKIMRotationKey) { d.DKIMPrivateKey = "dkim:v1:corrupt" }, error: true},
		{name: "previous retained", want: dkimrotate.ErrPreviousKeyRetained},
		{name: "no resolver", error: true},
		{name: "resolver error", want: dkimrotate.ErrNotPublished},
		{name: "missing staged"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			domain, key := activationFixture(t, database, fmt.Sprintf("refuse-%d.test", i), current, staged, now)
			if tc.change != nil {
				tc.change(&domain, &key)
			}
			if err := database.Save(&domain).Error; err != nil {
				t.Fatal(err)
			}
			if err := database.Omit("Domain").Save(&key).Error; err != nil {
				t.Fatal(err)
			}
			if tc.name == "previous retained" {
				// Even an elapsed retirement deadline must not silently discard a rollback key.
				retire := now.Add(-time.Hour)
				previous := models.DKIMRotationKey{DomainID: domain.ID, Slot: models.DKIMRotationPrevious,
					Selector: "old", PrivateKey: current, TTLSeconds: 300, RetireAfter: &retire}
				if err := database.Omit("Domain").Create(&previous).Error; err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "missing staged" {
				if err := database.Delete(&key).Error; err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			var lookup dkimrotate.LookupTXT = func(_ context.Context, _ string) ([]string, error) {
				calls++
				if tc.name == "resolver error" {
					return nil, errors.New("resolver unavailable")
				}
				return []string{tc.txt}, nil
			}
			if tc.name == "no resolver" {
				lookup = nil
			}
			activated, err := dkimrotate.Activate(ctx, database, domain.ID, stageMasterKey, lookup, func() time.Time { return now })
			if activated || (tc.want != nil && !errors.Is(err, tc.want)) || (tc.error && err == nil) || (tc.want == nil && !tc.error && err != nil) {
				t.Fatalf("Activate = %v, %v; want error %v (any=%v)", activated, err, tc.want, tc.error)
			}
			if tc.want == nil && !tc.error && calls != 0 {
				t.Fatal("DNS queried before activation was eligible")
			}
			var after models.Domain
			if err := database.First(&after, domain.ID).Error; err != nil {
				t.Fatal(err)
			}
			if after.DKIMSelector != domain.DKIMSelector || after.DKIMPrivateKey != domain.DKIMPrivateKey {
				t.Fatal("refusal changed the current key")
			}
			rows := stagedRows(t, database, domain.ID)
			if tc.name != "missing staged" && (len(rows) != 1 || rows[0].ID != key.ID || rows[0].PrivateKey != key.PrivateKey) {
				t.Fatal("refusal changed the staged key")
			}
		})
	}
	// A successful DNS response alone cannot replace a deleted domain.
	lookup := func(context.Context, string) ([]string, error) { return []string{record}, nil }
	if activated, err := dkimrotate.Activate(ctx, database, 999999, stageMasterKey, lookup, func() time.Time { return now }); activated || !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("missing domain: Activate = %v, %v", activated, err)
	}
}

func TestActivate_DatabaseFailureRollsBackAllSlots(t *testing.T) {
	database := openStageTestDB(t)
	current, _ := activationMaterial(t)
	staged, record := activationMaterial(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	domain, key := activationFixture(t, database, "rollback.test", current, staged, now)
	// Fail the final write, after both the previous slot and domain were updated.
	if err := database.Exec(`CREATE FUNCTION refuse_rotation_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'injected deletion failure'; END $$`).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`CREATE TRIGGER refuse_rotation_delete BEFORE DELETE ON dkim_rotation_keys
FOR EACH ROW EXECUTE FUNCTION refuse_rotation_delete()`).Error; err != nil {
		t.Fatal(err)
	}
	lookup := func(context.Context, string) ([]string, error) { return []string{record}, nil }
	activated, err := dkimrotate.Activate(context.Background(), database, domain.ID, stageMasterKey, lookup, func() time.Time { return now })
	if activated || err == nil {
		t.Fatalf("Activate = %v, %v; want rollback", activated, err)
	}
	var after models.Domain
	if err := database.First(&after, domain.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.DKIMSelector != domain.DKIMSelector || after.DKIMPrivateKey != current || after.DKIMCreatedAt == nil || !after.DKIMCreatedAt.Equal(*domain.DKIMCreatedAt) {
		t.Fatal("failed activation changed the current key")
	}
	var slots []models.DKIMRotationKey
	if err := database.Where("domain_id = ?", domain.ID).Find(&slots).Error; err != nil {
		t.Fatal(err)
	}
	if len(slots) != 1 || slots[0].ID != key.ID || slots[0].Slot != models.DKIMRotationStaged || slots[0].PrivateKey != staged {
		t.Fatal("failed activation changed the rotation slots")
	}
}
