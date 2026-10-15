package bootstrap

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"golang.org/x/crypto/ssh"
)

var challengeKey = ChallengeKey("owner-test-secret")

func newSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func authorized(s ssh.Signer) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.PublicKey())))
}

// signed answers a fresh challenge with signer, as the CLI does.
func signed(t *testing.T, s ssh.Signer, purpose, username string) OwnerRequest {
	t.Helper()
	challenge, _, err := NewChallenge(challengeKey, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	sig, err := s.Sign(rand.Reader, OwnerMessage(challenge, purpose, username))
	if err != nil {
		t.Fatal(err)
	}
	return OwnerRequest{Challenge: challenge, PublicKey: authorized(s), Signature: ssh.Marshal(sig),
		Purpose: purpose, Username: username}
}

func TestParseOwnerKeys(t *testing.T) {
	a, b := newSigner(t), newSigner(t)
	keys, err := ParseOwnerKeys("# the owner\n" + authorized(a) + "\n\n" + authorized(b) + " agent@pi\n")
	if err != nil || keys.Len() != 2 {
		t.Fatalf("keys=%d err=%v, want 2 and no error", keys.Len(), err)
	}
	if keys, err := ParseOwnerKeys(""); err != nil || keys.Len() != 0 {
		t.Errorf("empty list: keys=%d err=%v", keys.Len(), err)
	}
	// A typo must not quietly drop a key and lock its owner out.
	if _, err := ParseOwnerKeys(authorized(a) + "\nssh-ed25519 not-base64!!\n"); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("a malformed line: err = %v, want one naming line 2", err)
	}
}

// Owner keys only let an admin back in. A request to claim the server — what
// they also did before 0.7 — is refused, even signed by an owner.
func TestOwner_NoTokenClaimsAServer(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	migrateTokens(t, gdb)
	mustRBAC(t, gdb)
	owner := newSigner(t)
	keys, _ := ParseOwnerKeys(authorized(owner))

	if _, _, err := MintForOwner(gdb, keys, challengeKey, signed(t, owner, "setup", ""), time.Now()); err == nil {
		t.Fatal("an owner key minted a setup token")
	}
	var minted int64
	gdb.Model(&models.OneTimeToken{}).Where("purpose <> ?", ownerChallengePurpose).Count(&minted)
	if minted != 0 {
		t.Errorf("%d tokens minted, want none", minted)
	}
}

func TestOwner_EverythingElseIsRefused(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	migrateTokens(t, gdb)
	mustRBAC(t, gdb)
	newAdmin(t, gdb, "admin", "the first admin's password")
	owner, stranger := newSigner(t), newSigner(t)
	keys, _ := ParseOwnerKeys(authorized(owner))
	now := time.Now()

	t.Run("a key not on the list", func(t *testing.T) {
		if _, _, err := MintForOwner(gdb, keys, challengeKey, signed(t, stranger, "reset-admin", "admin"), now); !errors.Is(err, ErrNotOwner) {
			t.Errorf("err = %v, want ErrNotOwner", err)
		}
	})
	t.Run("a stranger presenting the owner's public key", func(t *testing.T) {
		req := signed(t, stranger, "reset-admin", "admin")
		req.PublicKey = authorized(owner)
		if _, _, err := MintForOwner(gdb, keys, challengeKey, req, now); !errors.Is(err, ErrNotOwner) {
			t.Errorf("err = %v, want ErrNotOwner", err)
		}
	})
	t.Run("a signature for one admin used for another", func(t *testing.T) {
		req := signed(t, owner, "reset-admin", "admin")
		req.Username = "someone-else"
		if _, _, err := MintForOwner(gdb, keys, challengeKey, req, now); !errors.Is(err, ErrNotOwner) {
			t.Errorf("err = %v, want ErrNotOwner: the signature is bound to what it asked for", err)
		}
	})
	t.Run("a forged challenge", func(t *testing.T) {
		req := signed(t, owner, "reset-admin", "admin")
		forged, _, _ := NewChallenge(ChallengeKey("another server's secret"), now)
		sig, _ := owner.Sign(rand.Reader, OwnerMessage(forged, "reset-admin", "admin"))
		req.Challenge, req.Signature = forged, ssh.Marshal(sig)
		if _, _, err := MintForOwner(gdb, keys, challengeKey, req, now); !errors.Is(err, ErrChallengeInvalid) {
			t.Errorf("err = %v, want ErrChallengeInvalid", err)
		}
	})
	t.Run("an expired challenge", func(t *testing.T) {
		req := signed(t, owner, "reset-admin", "admin")
		if _, _, err := MintForOwner(gdb, keys, challengeKey, req, now.Add(ChallengeLifetime+time.Minute)); !errors.Is(err, ErrChallengeInvalid) {
			t.Errorf("err = %v, want ErrChallengeInvalid", err)
		}
	})
	t.Run("no keys configured", func(t *testing.T) {
		none, _ := ParseOwnerKeys("")
		if _, _, err := MintForOwner(gdb, none, challengeKey, signed(t, owner, "reset-admin", "admin"), now); !errors.Is(err, ErrNoOwnerKeys) {
			t.Errorf("err = %v, want ErrNoOwnerKeys", err)
		}
	})
}

// A signed answer, captured somehow, works once.
func TestOwner_AnAnswerCannotBeReplayed(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	migrateTokens(t, gdb)
	mustRBAC(t, gdb)
	newAdmin(t, gdb, "admin", "the first admin's password")
	owner := newSigner(t)
	keys, _ := ParseOwnerKeys(authorized(owner))
	req := signed(t, owner, "reset-admin", "admin")

	if _, _, err := MintForOwner(gdb, keys, challengeKey, req, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := MintForOwner(gdb, keys, challengeKey, req, time.Now()); !errors.Is(err, ErrChallengeInvalid) {
		t.Errorf("replayed answer: err = %v, want ErrChallengeInvalid", err)
	}
}

func TestOwner_ResetNamesItsAdmin(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	migrateTokens(t, gdb)
	mustRBAC(t, gdb)
	owner := newSigner(t)
	keys, _ := ParseOwnerKeys(authorized(owner))
	newAdmin(t, gdb, "admin", "the first admin's password")

	if _, _, err := MintForOwner(gdb, keys, challengeKey, signed(t, owner, "reset-admin", "nobody"), time.Now()); !errors.Is(err, ErrNoSuchAdmin) {
		t.Errorf("reset for nobody: err = %v, want ErrNoSuchAdmin", err)
	}
	token, _, err := MintForOwner(gdb, keys, challengeKey, signed(t, owner, "reset-admin", "admin"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResetAdmin(gdb, token, chosenPassword); err != nil {
		t.Errorf("reset with the owner's token: %v", err)
	}
}

// RSA keys are fine; RSA signatures over SHA-1 are not.
func TestOwner_RSAOnlyWithSHA2(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	migrateTokens(t, gdb)
	mustRBAC(t, gdb)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	newAdmin(t, gdb, "admin", "the first admin's password")
	s, _ := ssh.NewSignerFromKey(rsaKey)
	as := s.(ssh.AlgorithmSigner)
	keys, _ := ParseOwnerKeys(authorized(s))

	sign := func(algo string) OwnerRequest {
		challenge, _, _ := NewChallenge(challengeKey, time.Now())
		sig, err := as.SignWithAlgorithm(rand.Reader, OwnerMessage(challenge, "reset-admin", "admin"), algo)
		if err != nil {
			t.Fatal(err)
		}
		return OwnerRequest{Challenge: challenge, PublicKey: authorized(s), Signature: ssh.Marshal(sig),
			Purpose: "reset-admin", Username: "admin"}
	}
	if _, _, err := MintForOwner(gdb, keys, challengeKey, sign(ssh.KeyAlgoRSA), time.Now()); !errors.Is(err, ErrNotOwner) {
		t.Errorf("ssh-rsa (SHA-1) signature: err = %v, want ErrNotOwner", err)
	}
	if _, _, err := MintForOwner(gdb, keys, challengeKey, sign(ssh.KeyAlgoRSASHA256), time.Now()); err != nil {
		t.Errorf("rsa-sha2-256 signature: %v", err)
	}
}
