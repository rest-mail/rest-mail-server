package repositories

import (
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
)

func TestImportedPasswordRehashOnlyOnMatch(t *testing.T) {
	db := openCredentialTestDB(t)
	mb := seedCredentialMailbox(t, db)
	repo := NewCredentialRepository(db)
	now := time.Now()
	for _, hash := range []string{
		"{SHA256-CRYPT}$5$importsalt$bHBgUR.cLP5CqE2HvtcibUnobsIEkJ3fMmuinleEQt/",
		"{SHA512-CRYPT}$6$importsalt$ObInO4qcr134i8FQT.XNC6Ma04n9aRTAUNHmm8Yb1idoDBOXKoDTmdosY1xwfageGXo4ZSSrkIPRk9zMWfsG7/",
		"{ARGON2ID}$argon2id$v=19$m=4096,t=2,p=1$aW1wb3J0c2FsdDAxMjM0NQ$JCLzuH2QfsNfwyNUzLd3IjMXXY7Rb46hqjBt5hs545I",
		"{ARGON2I}$argon2i$v=19$m=4096,t=2,p=1$aW1wb3J0c2FsdDAxMjM0NQ$EPRiJ1+NEb2FB7BCy10XiOs7h9DWzbOWtI9FhJRyFYE",
	} {
		original, err := repo.SetPassword(mb.ID, hash, nil, now)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := repo.MatchPassword(mb.ID, "wrong", now); err != nil || got != nil {
			t.Fatalf("wrong password matched: %v", err)
		}
		var stored models.Credential
		if err := db.First(&stored, original.ID).Error; err != nil || stored.SecretHash != hash {
			t.Fatalf("a failed proof changed the hash: %v", err)
		}
		got, err := repo.MatchPassword(mb.ID, "old-password", now)
		if err != nil || got == nil || got.ID != original.ID || !strings.HasPrefix(got.SecretHash, "{BLF-CRYPT}") {
			t.Fatalf("matching password did not upgrade its credential: %v", err)
		}
		stored = models.Credential{}
		if err := db.First(&stored, original.ID).Error; err != nil || stored.SecretHash != got.SecretHash || auth.CheckPassword("old-password", stored.SecretHash) != nil {
			t.Fatalf("stored upgrade cannot sign in: %v", err)
		}
		if stored.PublicID != original.PublicID || stored.RevokedAt != nil {
			t.Fatal("rehash replaced or revoked the credential")
		}
	}
}
