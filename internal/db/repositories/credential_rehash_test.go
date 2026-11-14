package repositories

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const rehashPassword = "correct-horse-battery"

func costOf(t *testing.T, hash string) int {
	t.Helper()
	cost, err := bcrypt.Cost([]byte(strings.TrimPrefix(hash, "{BLF-CRYPT}")))
	if err != nil {
		t.Fatalf("%q: %v", hash, err)
	}
	return cost
}

// seedCheapPassword gives a mailbox of the test's own a password at bcrypt's
// lowest cost, as a hash from before #1642 would be: cheaper than the server's.
func seedCheapPassword(t *testing.T, db *gorm.DB) (*CredentialRepository, *models.Mailbox, *models.Credential) {
	t.Helper()
	mb := seedCredentialMailbox(t, db)
	raw, err := bcrypt.GenerateFromPassword([]byte(rehashPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewCredentialRepository(db)
	cred, err := repo.SetPassword(mb.ID, "{BLF-CRYPT}"+string(raw), nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return repo, mb, cred
}

func storedHash(t *testing.T, db *gorm.DB, id uint) string {
	t.Helper()
	var stored models.Credential
	if err := db.First(&stored, id).Error; err != nil {
		t.Fatal(err)
	}
	return stored.SecretHash
}

// A sign-in with a cheaper hash stores it at the server's cost without the
// person doing anything, and the same credential keeps working. A wrong
// password changes nothing (#1642).
func TestMatchPasswordUpgradesACheaperHash(t *testing.T) {
	db := openCredentialTestDB(t)
	repo, mb, cred := seedCheapPassword(t, db)
	old := cred.SecretHash

	if got, err := repo.MatchPassword(mb.ID, "wrong", time.Now()); err != nil || got != nil {
		t.Fatalf("wrong password: %v, %v", got, err)
	}
	if storedHash(t, db, cred.ID) != old {
		t.Fatal("a wrong password changed the hash")
	}

	got, err := repo.MatchPassword(mb.ID, rehashPassword, time.Now())
	if err != nil || got == nil || got.ID != cred.ID {
		t.Fatalf("correct password: %+v, %v", got, err)
	}
	upgraded := storedHash(t, db, cred.ID)
	if costOf(t, upgraded) != testPasswordCost || got.SecretHash != upgraded {
		t.Fatalf("stored at cost %d, returned %q", costOf(t, upgraded), got.SecretHash)
	}

	again, err := repo.MatchPassword(mb.ID, rehashPassword, time.Now())
	if err != nil || again == nil || again.ID != cred.ID {
		t.Fatalf("the upgraded password no longer signs in: %+v, %v", again, err)
	}
	if storedHash(t, db, cred.ID) != upgraded {
		t.Error("a second sign-in rewrote a hash already at the server's cost")
	}
}

// A failed rewrite does not fail the sign-in, and the old hash goes on working.
func TestMatchPasswordSurvivesAFailedRewrite(t *testing.T) {
	db := openCredentialTestDB(t)
	repo, mb, cred := seedCheapPassword(t, db)

	const hook = "credential-rehash-refused"
	if err := db.Callback().Update().Before("gorm:update").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "credentials" {
			_ = tx.AddError(errors.New("the database refused the rewrite"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.MatchPassword(mb.ID, rehashPassword, time.Now())
	if rmErr := db.Callback().Update().Remove(hook); rmErr != nil {
		t.Fatal(rmErr)
	}
	if err != nil || got == nil || got.ID != cred.ID {
		t.Fatalf("the sign-in failed with the rewrite: %+v, %v", got, err)
	}
	if storedHash(t, db, cred.ID) != cred.SecretHash {
		t.Fatal("the failed rewrite changed the hash")
	}
	if err := auth.CheckPassword(rehashPassword, got.SecretHash); err != nil {
		t.Errorf("the credential returned cannot sign in: %v", err)
	}
}

// Sign-ins at the same moment all succeed and leave one working hash: the one
// that loses the update must not read as a wrong password.
func TestConcurrentSignInsWithACheaperHashAllSucceed(t *testing.T) {
	db := openCredentialTestDB(t)
	repo, mb, cred := seedCheapPassword(t, db)

	const signIns = 4
	var wg sync.WaitGroup
	results := make([]*models.Credential, signIns)
	errs := make([]error, signIns)
	start := make(chan struct{})
	for i := range signIns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i], errs[i] = repo.MatchPassword(mb.ID, rehashPassword, time.Now())
		}()
	}
	close(start)
	wg.Wait()
	for i := range signIns {
		if errs[i] != nil || results[i] == nil || results[i].ID != cred.ID {
			t.Errorf("sign-in %d: %+v, %v", i, results[i], errs[i])
		}
	}
	stored := storedHash(t, db, cred.ID)
	if costOf(t, stored) != testPasswordCost {
		t.Errorf("stored at cost %d", costOf(t, stored))
	}
	if err := auth.CheckPassword(rehashPassword, stored); err != nil {
		t.Errorf("the hash left behind does not sign in: %v", err)
	}
}

// Whichever sign-in loses the update, the row as it is now decides. Each case
// starts from a credential read before someone else changed the row, which is
// the moment no scheduling of goroutines can be relied on to reach.
func TestAnUpgradeThatLosesTheUpdate(t *testing.T) {
	db := openCredentialTestDB(t)

	t.Run("to an upgrade of the same password, still signs in", func(t *testing.T) {
		repo, _, cred := seedCheapPassword(t, db)
		stale := *cred
		winner, err := auth.HashPassword(rehashPassword)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&models.Credential{}).Where("id = ?", cred.ID).Update("secret_hash", winner).Error; err != nil {
			t.Fatal(err)
		}
		current, err := repo.upgradePasswordHash(&stale, rehashPassword, time.Now())
		if err != nil || !current {
			t.Fatalf("refused a correct password: %v, %v", current, err)
		}
		if stale.SecretHash != winner || storedHash(t, db, cred.ID) != winner {
			t.Error("the winner's hash was not kept")
		}
	})

	t.Run("to a revocation, does not sign in", func(t *testing.T) {
		repo, mb, cred := seedCheapPassword(t, db)
		stale := *cred
		replacement, err := auth.HashPassword("a-new-password-entirely")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repo.SetPassword(mb.ID, replacement, nil, time.Now()); err != nil {
			t.Fatal(err)
		}
		current, err := repo.upgradePasswordHash(&stale, rehashPassword, time.Now())
		if err != nil || current {
			t.Fatalf("the replaced password still signs in: %v, %v", current, err)
		}
		if storedHash(t, db, cred.ID) != cred.SecretHash {
			t.Error("the proof of the old password overwrote the revoked row")
		}
	})

	t.Run("to another password in the same row, does not sign in", func(t *testing.T) {
		repo, _, cred := seedCheapPassword(t, db)
		stale := *cred
		other, err := auth.HashPassword("a-new-password-entirely")
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&models.Credential{}).Where("id = ?", cred.ID).Update("secret_hash", other).Error; err != nil {
			t.Fatal(err)
		}
		current, err := repo.upgradePasswordHash(&stale, rehashPassword, time.Now())
		if err != nil || current {
			t.Fatalf("the replaced password still signs in: %v, %v", current, err)
		}
		if storedHash(t, db, cred.ID) != other {
			t.Error("the proof of the old password overwrote the new one")
		}
	})
}

// A dearer hash than the server's is kept, not weakened.
func TestMatchPasswordKeepsADearerHash(t *testing.T) {
	db := openCredentialTestDB(t)
	mb := seedCredentialMailbox(t, db)
	raw, err := bcrypt.GenerateFromPassword([]byte(rehashPassword), testPasswordCost+1)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewCredentialRepository(db)
	cred, err := repo.SetPassword(mb.ID, "{BLF-CRYPT}"+string(raw), nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := repo.MatchPassword(mb.ID, rehashPassword, time.Now()); err != nil || got == nil {
		t.Fatalf("correct password: %+v, %v", got, err)
	}
	if storedHash(t, db, cred.ID) != cred.SecretHash {
		t.Error("a dearer hash was rewritten")
	}
}

// An imported hash of a password bcrypt can hold becomes the server's cost too.
func TestImportedHashesAndTheCost(t *testing.T) {
	db := openCredentialTestDB(t)
	repo := NewCredentialRepository(db)
	mb := seedCredentialMailbox(t, db)
	const imported = "{SHA256-CRYPT}$5$importsalt$bHBgUR.cLP5CqE2HvtcibUnobsIEkJ3fMmuinleEQt/"
	if _, err := repo.SetPassword(mb.ID, imported, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := repo.MatchPassword(mb.ID, "old-password", time.Now())
	if err != nil || got == nil {
		t.Fatalf("imported password: %+v, %v", got, err)
	}
	if costOf(t, got.SecretHash) != testPasswordCost {
		t.Errorf("imported hash became cost %d", costOf(t, got.SecretHash))
	}
}
