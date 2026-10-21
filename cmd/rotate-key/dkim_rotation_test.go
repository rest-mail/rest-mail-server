package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

func rotationFixture(t *testing.T, database *gorm.DB, slot, selector string, domainID uint) models.DKIMRotationKey {
	t.Helper()
	installed := time.Now().UTC().Add(-90 * 24 * time.Hour).Truncate(time.Second)
	published := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	key := models.DKIMRotationKey{
		DomainID: domainID, Slot: slot, Selector: selector,
		PublishedAt: &published, TTLSeconds: 3600,
	}
	if slot == models.DKIMRotationPrevious {
		retire := published.Add(2 * time.Hour)
		key.InstalledAt, key.RetireAfter = &installed, &retire
	}
	if err := key.SetPrivateKey("private-"+selector, "old-master"); err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.First(&key, key.ID).Error; err != nil {
		t.Fatal(err)
	}
	return key
}

func TestRotate_DKIMRotationKeys(t *testing.T) {
	database := openRotateTestDB(t)
	domain := models.Domain{Name: "re-encrypt.test"}
	if err := database.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	staged := rotationFixture(t, database, models.DKIMRotationStaged, "restmail-2", domain.ID)
	previous := rotationFixture(t, database, models.DKIMRotationPrevious, "restmail-1", domain.ID)
	summary, err := rotate(database, "old-master", "new-master")
	if err != nil || summary.RotationDKIM != 2 || summary.Failures != 0 {
		t.Fatalf("rotation summary: %+v, %v", summary, err)
	}
	for _, want := range []models.DKIMRotationKey{staged, previous} {
		var got models.DKIMRotationKey
		if err := database.First(&got, want.ID).Error; err != nil {
			t.Fatal(err)
		}
		if plain, err := models.LoadDKIMPrivateKey(got.PrivateKey, "new-master"); err != nil || plain != "private-"+want.Selector {
			t.Fatal("rotation key did not decrypt with new master key")
		}
		if _, err := models.LoadDKIMPrivateKey(got.PrivateKey, "old-master"); err == nil {
			t.Fatal("rotation key still decrypts with retired master key")
		}
		// The normal UpdatedAt changes; lifecycle timestamps must not.
		want.PrivateKey, want.UpdatedAt = got.PrivateKey, got.UpdatedAt
		if !reflect.DeepEqual(got, want) {
			t.Fatal("master-key rotation changed lifecycle metadata")
		}
	}
	before := staged.PrivateKey
	if err := database.Model(&models.DKIMRotationKey{}).Where("id = ?", staged.ID).Update("private_key", before).Error; err != nil {
		t.Fatal(err)
	}
	// A key encrypted under another master remains byte-for-byte intact.
	processed, failures, err := rotateRotationDKIM(database, "new-master", "third-master")
	if err != nil || processed != 1 || failures != 1 {
		t.Fatalf("mixed-master result: %d, %d, %v", processed, failures, err)
	}
	var unchanged models.DKIMRotationKey
	if err := database.First(&unchanged, staged.ID).Error; err != nil || unchanged.PrivateKey != before {
		t.Fatal("undecryptable key was changed")
	}
}
