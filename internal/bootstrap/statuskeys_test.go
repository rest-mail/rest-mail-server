package bootstrap

import (
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"golang.org/x/crypto/ssh"
)

// askStatus answers a fresh challenge with signer, signed for purpose, as
// `restmail status --key` does for "status".
func askStatus(t *testing.T, s ssh.Signer, purpose, username string) StatusRequest {
	t.Helper()
	req := signed(t, s, purpose, username)
	return StatusRequest{Challenge: req.Challenge, PublicKey: req.PublicKey, Signature: req.Signature}
}

func TestParseStatusKeys(t *testing.T) {
	a := newSigner(t)
	keys, err := ParseStatusKeys("# the deployer\n" + authorized(a) + " deployer@ci\n")
	if err != nil || keys.Len() != 1 {
		t.Fatalf("keys=%d err=%v, want 1 and no error", keys.Len(), err)
	}
	if _, err := ParseStatusKeys("ssh-ed25519 not-base64!!\n"); err == nil || !strings.Contains(err.Error(), "RESTMAIL_STATUS_KEYS line 1") {
		t.Errorf("a malformed line: err = %v, want one naming RESTMAIL_STATUS_KEYS line 1", err)
	}
}

// The message follows the owner scheme, with "status" for the purpose and no
// username: what `restmail status` signs, and what the API documents.
func TestStatusMessage(t *testing.T) {
	if got, want := string(StatusMessage("CH")), "restmail-owner-v1\nstatus\n\nCH"; got != want {
		t.Errorf("StatusMessage = %q, want %q", got, want)
	}
}

func TestStatus_StatusAndOwnerKeysMayRead(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	migrateTokens(t, gdb)
	deployer, owner := newSigner(t), newSigner(t)
	statusKeys, _ := ParseStatusKeys(authorized(deployer))
	ownerKeys, _ := ParseOwnerKeys(authorized(owner))

	if err := CheckStatus(gdb, statusKeys, ownerKeys, challengeKey, askStatus(t, deployer, StatusPurpose, ""), time.Now()); err != nil {
		t.Errorf("a status key: %v", err)
	}
	if err := CheckStatus(gdb, statusKeys, ownerKeys, challengeKey, askStatus(t, owner, StatusPurpose, ""), time.Now()); err != nil {
		t.Errorf("an owner key: %v", err)
	}
	noStatusKeys, _ := ParseStatusKeys("")
	if err := CheckStatus(gdb, noStatusKeys, ownerKeys, challengeKey, askStatus(t, owner, StatusPurpose, ""), time.Now()); err != nil {
		t.Errorf("an owner key, no status keys listed: %v", err)
	}
	noOwnerKeys, _ := ParseOwnerKeys("")
	if err := CheckStatus(gdb, statusKeys, noOwnerKeys, challengeKey, askStatus(t, deployer, StatusPurpose, ""), time.Now()); err != nil {
		t.Errorf("a status key, no owner keys listed: %v", err)
	}
}

func TestStatus_EverythingElseIsRefused(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	migrateTokens(t, gdb)
	deployer, owner, stranger := newSigner(t), newSigner(t), newSigner(t)
	statusKeys, _ := ParseStatusKeys(authorized(deployer))
	ownerKeys, _ := ParseOwnerKeys(authorized(owner))
	now := time.Now()
	check := func(req StatusRequest, at time.Time) error {
		return CheckStatus(gdb, statusKeys, ownerKeys, challengeKey, req, at)
	}

	t.Run("a key on neither list", func(t *testing.T) {
		if err := check(askStatus(t, stranger, StatusPurpose, ""), now); !errors.Is(err, ErrNotStatusKey) {
			t.Errorf("err = %v, want ErrNotStatusKey", err)
		}
	})
	t.Run("a stranger presenting the deployer's public key", func(t *testing.T) {
		req := askStatus(t, stranger, StatusPurpose, "")
		req.PublicKey = authorized(deployer)
		if err := check(req, now); !errors.Is(err, ErrNotStatusKey) {
			t.Errorf("err = %v, want ErrNotStatusKey", err)
		}
	})
	// Purpose separation: a signature made to reset an admin does not read
	// the status, though the owner's key may read it.
	t.Run("an owner's reset-admin signature", func(t *testing.T) {
		if err := check(askStatus(t, owner, "reset-admin", "admin"), now); !errors.Is(err, ErrNotStatusKey) {
			t.Errorf("err = %v, want ErrNotStatusKey", err)
		}
	})
	t.Run("a status signature with a username", func(t *testing.T) {
		if err := check(askStatus(t, deployer, StatusPurpose, "admin"), now); !errors.Is(err, ErrNotStatusKey) {
			t.Errorf("err = %v, want ErrNotStatusKey", err)
		}
	})
	t.Run("a forged challenge", func(t *testing.T) {
		forged, _, _ := NewChallenge(ChallengeKey("another server's secret"), now)
		sig, _ := deployer.Sign(rand.Reader, StatusMessage(forged))
		req := StatusRequest{Challenge: forged, PublicKey: authorized(deployer), Signature: ssh.Marshal(sig)}
		if err := check(req, now); !errors.Is(err, ErrChallengeInvalid) {
			t.Errorf("err = %v, want ErrChallengeInvalid", err)
		}
	})
	t.Run("an expired challenge", func(t *testing.T) {
		if err := check(askStatus(t, deployer, StatusPurpose, ""), now.Add(ChallengeLifetime+time.Minute)); !errors.Is(err, ErrChallengeInvalid) {
			t.Errorf("err = %v, want ErrChallengeInvalid", err)
		}
	})
	t.Run("no keys listed at all", func(t *testing.T) {
		noStatus, _ := ParseStatusKeys("")
		noOwners, _ := ParseOwnerKeys("")
		if err := CheckStatus(gdb, noStatus, noOwners, challengeKey, askStatus(t, deployer, StatusPurpose, ""), now); !errors.Is(err, ErrNoStatusKeys) {
			t.Errorf("err = %v, want ErrNoStatusKeys", err)
		}
	})
	var spent int64
	gdb.Model(&models.OneTimeToken{}).Count(&spent)
	if spent != 0 {
		t.Errorf("%d rows written by refused requests, want none: only a key holder makes the server write", spent)
	}
}

// A status key reads the status, and nothing else: it cannot let an admin back
// in, whatever it signs.
func TestStatus_AStatusKeyCannotResetAnAdmin(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	migrateTokens(t, gdb)
	mustRBAC(t, gdb)
	newAdmin(t, gdb, "admin", "the first admin's password")
	deployer, owner := newSigner(t), newSigner(t)
	ownerKeys, _ := ParseOwnerKeys(authorized(owner))

	if _, _, err := MintForOwner(gdb, ownerKeys, challengeKey, signed(t, deployer, "reset-admin", "admin"), time.Now()); !errors.Is(err, ErrNotOwner) {
		t.Errorf("a status key signing reset-admin: err = %v, want ErrNotOwner", err)
	}
	// Nor can an owner's status signature be presented as a reset.
	req := signed(t, owner, StatusPurpose, "")
	req.Purpose, req.Username = "reset-admin", "admin"
	if _, _, err := MintForOwner(gdb, ownerKeys, challengeKey, req, time.Now()); !errors.Is(err, ErrNotOwner) {
		t.Errorf("a status signature presented as reset-admin: err = %v, want ErrNotOwner", err)
	}
	var minted int64
	gdb.Model(&models.OneTimeToken{}).Where("purpose <> ?", ownerChallengePurpose).Count(&minted)
	if minted != 0 {
		t.Errorf("%d tokens minted, want none", minted)
	}
}

// A signed answer, captured somehow, works once. (The refusal aborts the test's
// transaction, so it is the last thing each test asks.)
func TestStatus_AnAnswerCannotBeReplayed(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	migrateTokens(t, gdb)
	deployer := newSigner(t)
	statusKeys, _ := ParseStatusKeys(authorized(deployer))
	req := askStatus(t, deployer, StatusPurpose, "")

	if err := CheckStatus(gdb, statusKeys, nil, challengeKey, req, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := CheckStatus(gdb, statusKeys, nil, challengeKey, req, time.Now()); !errors.Is(err, ErrChallengeInvalid) {
		t.Errorf("replayed answer: err = %v, want ErrChallengeInvalid", err)
	}
}

// A challenge answered for the status cannot then be answered for a reset.
func TestStatus_AChallengeIsAnsweredOnce(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	migrateTokens(t, gdb)
	mustRBAC(t, gdb)
	newAdmin(t, gdb, "admin", "the first admin's password")
	owner := newSigner(t)
	ownerKeys, _ := ParseOwnerKeys(authorized(owner))
	req := askStatus(t, owner, StatusPurpose, "")

	if err := CheckStatus(gdb, nil, ownerKeys, challengeKey, req, time.Now()); err != nil {
		t.Fatal(err)
	}
	sig, _ := owner.Sign(rand.Reader, OwnerMessage(req.Challenge, "reset-admin", "admin"))
	reset := OwnerRequest{Challenge: req.Challenge, PublicKey: req.PublicKey, Signature: ssh.Marshal(sig),
		Purpose: "reset-admin", Username: "admin"}
	if _, _, err := MintForOwner(gdb, ownerKeys, challengeKey, reset, time.Now()); !errors.Is(err, ErrChallengeInvalid) {
		t.Errorf("the same challenge answered again for a reset: err = %v, want ErrChallengeInvalid", err)
	}
}

// Reading the status after every deploy must not grow the token table for
// ever: a spent challenge is forgotten once it has expired.
func TestStatus_ExpiredChallengesAreForgotten(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	migrateTokens(t, gdb)
	deployer := newSigner(t)
	statusKeys, _ := ParseStatusKeys(authorized(deployer))
	long := time.Now().Add(-time.Hour)
	if err := gdb.Create(&models.OneTimeToken{Purpose: ownerChallengePurpose, TokenHash: hashToken("long spent"),
		ExpiresAt: long, UsedAt: &long}).Error; err != nil {
		t.Fatal(err)
	}
	keep := time.Now().Add(time.Hour)
	if err := gdb.Create(&models.OneTimeToken{Purpose: models.OneTimeTokenResetAdmin, TokenHash: hashToken("a reset token"),
		ExpiresAt: long}).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Create(&models.OneTimeToken{Purpose: ownerChallengePurpose, TokenHash: hashToken("just spent"),
		ExpiresAt: keep, UsedAt: &long}).Error; err != nil {
		t.Fatal(err)
	}

	if err := CheckStatus(gdb, statusKeys, nil, challengeKey, askStatus(t, deployer, StatusPurpose, ""), time.Now()); err != nil {
		t.Fatal(err)
	}
	var hashes []string
	gdb.Model(&models.OneTimeToken{}).Order("id").Pluck("token_hash", &hashes)
	if len(hashes) != 3 || hashes[0] != hashToken("a reset token") || hashes[1] != hashToken("just spent") {
		t.Errorf("left %d rows, want the reset token, the unexpired challenge and the new one", len(hashes))
	}
}
