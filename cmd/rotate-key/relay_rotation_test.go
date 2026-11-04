package main

import (
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

func relayFixture(t *testing.T, database *gorm.DB, name, stored string) models.DomainRelay {
	t.Helper()
	domain := models.Domain{Name: name}
	if err := database.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	relay := models.DomainRelay{
		DomainID: domain.ID, Transport: models.OutboundTransportRelay,
		Host: "smtp." + name, Port: 587, TLSMode: models.RelayTLSStartTLS,
		Username: "relay-user", PasswordEnc: stored,
	}
	if err := database.Create(&relay).Error; err != nil {
		t.Fatal(err)
	}
	return relay
}

func storedRelayPassword(t *testing.T, password, masterKey string) string {
	t.Helper()
	stored, err := models.EncryptRelayPassword(password, masterKey)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

// A relay password left under the old key stops the domain's mail while the
// rotation reports success (#1911).
func TestRotate_ReEncryptsRelayPasswords(t *testing.T) {
	database := openRotateTestDB(t)

	const (
		oldKey   = "rotate-test-old-master-key"
		newKey   = "rotate-test-new-master-key"
		otherKey = "rotate-test-other-master-key"
		password = "relay-password-under-test"
	)
	rotated := relayFixture(t, database, "rotated.test", storedRelayPassword(t, password, oldKey))
	noAuth := relayFixture(t, database, "no-auth.test", "")
	unreadableStored := storedRelayPassword(t, password, otherKey)
	unreadable := relayFixture(t, database, "unreadable.test", unreadableStored)

	summary, err := rotate(database, oldKey, newKey)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if summary.RelayPasswords != 1 || summary.Failures != 1 {
		t.Fatalf("summary = %+v, want one relay password and one failure", summary)
	}

	reload := func(id uint) models.DomainRelay {
		t.Helper()
		var relay models.DomainRelay
		if err := database.First(&relay, id).Error; err != nil {
			t.Fatal(err)
		}
		return relay
	}

	got := reload(rotated.ID)
	if plain, err := models.LoadRelayPassword(got.PasswordEnc, newKey); err != nil || plain != password {
		t.Fatalf("relay password does not decrypt with the new key: %v", err)
	}
	if _, err := models.LoadRelayPassword(got.PasswordEnc, oldKey); err == nil {
		t.Fatal("relay password still decrypts with the old key")
	}
	// The password's own UpdatedAt changes, as it does on every other column;
	// the relay's settings must not.
	if got.Host != rotated.Host || got.Port != rotated.Port || got.TLSMode != rotated.TLSMode ||
		got.Username != rotated.Username || got.Transport != rotated.Transport {
		t.Fatalf("rotation changed the relay's settings: %+v", got)
	}

	if reload(noAuth.ID).PasswordEnc != "" {
		t.Fatal("a relay with no password was given one")
	}

	if reload(unreadable.ID).PasswordEnc != unreadableStored {
		t.Fatal("a relay password under another key was changed")
	}
}
