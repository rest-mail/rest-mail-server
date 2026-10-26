package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"gorm.io/gorm"
)

func TestInvitationEligibility(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name        string
		account     models.Mailbox
		credentials int64
		want        bool
	}{
		{"new", models.Mailbox{Active: true}, 0, true},
		{"disabled", models.Mailbox{}, 0, false},
		{"password history", models.Mailbox{Active: true, LastPasswordChange: &now}, 0, false},
		{"revoked sessions", models.Mailbox{Active: true, CredentialEpoch: 1}, 0, false},
		{"existing credential", models.Mailbox{Active: true}, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := invitationEligible(tc.account, tc.credentials); got != tc.want {
				t.Fatalf("invitable = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestInvitationRefusesBadInputBeforeStorage(t *testing.T) {
	for _, password := range []string{"", strings.Repeat("x", MinChosenPasswordLength-1)} {
		if _, err := AcceptInvitation(context.Background(), nil, "token", password); !errors.Is(err, ErrPasswordTooShort) {
			t.Fatalf("short password: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := AcceptInvitation(ctx, nil, "token", chosenPassword); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request reached storage: %v", err)
	}
	if _, _, err := MintInvitation(ctx, nil, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled issuance reached storage: %v", err)
	}
}

// These cases use the existing isolated PostgreSQL helper. They compile and
// skip on machines without PostgreSQL; the normal database CI runs them.
func TestInvitationPersistence(t *testing.T) {
	gdb := openScratchDatabase(t)
	mustRBAC(t, gdb)
	domain, err := ensureDomain(gdb, "invitation.example")
	if err != nil {
		t.Fatal(err)
	}
	newAccount := func(name string) models.Mailbox {
		a := models.Mailbox{DomainID: domain.ID, LocalPart: name, Address: name + "@invitation.example", Active: true}
		if err := gdb.Create(&a).Error; err != nil {
			t.Fatal(err)
		}
		return a
	}
	ctx := context.Background()
	t.Run("hash-only, replacement, first password and replay", func(t *testing.T) {
		a := newAccount("first")
		old, _, err := MintInvitation(ctx, gdb, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		token, expiry, err := MintInvitation(ctx, gdb, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if token == old || len(token) != 43 || time.Until(expiry) < 14*time.Minute || time.Until(expiry) > DefaultTokenLifetime {
			t.Fatal("invitation entropy or lifetime is wrong")
		}
		var row models.OneTimeToken
		if err := gdb.Where("token_hash = ?", hashToken(token)).First(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.TokenHash == token || row.Purpose != models.OneTimeTokenInvitation || row.AccountID == nil || *row.AccountID != a.ID {
			t.Fatal("unsafe persisted invitation")
		}
		if _, err := AcceptInvitation(ctx, gdb, old, chosenPassword); !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("superseded invitation accepted: %v", err)
		}
		if _, err := ResetAdmin(gdb, token, chosenPassword); !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("invitation accepted as owner reset: %v", err)
		}
		accepted, err := AcceptInvitation(ctx, gdb, token, chosenPassword)
		if err != nil {
			t.Fatal(err)
		}
		if accepted.ID != a.ID || !accepted.TwoFactorRequired || accepted.PasswordChangeRequired || accepted.CredentialEpoch != 1 || accepted.LastPasswordChange == nil {
			t.Fatal("invited account setup state is wrong")
		}
		creds, err := repositories.NewCredentialRepository(gdb).Passwords(a.ID, time.Now())
		if err != nil || len(creds) != 1 {
			t.Fatalf("password credential count: %d, error %v", len(creds), err)
		}
		if err := auth.CheckPassword(chosenPassword, creds[0].SecretHash); err != nil {
			t.Fatal("recipient password did not persist")
		}
		if _, err := AcceptInvitation(ctx, gdb, token, chosenPassword); !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("replayed invitation accepted: %v", err)
		}
		if _, _, err := MintInvitation(ctx, gdb, a.ID); !errors.Is(err, ErrAccountNotInvitable) {
			t.Fatalf("established account invited: %v", err)
		}
	})
	t.Run("expiry and disabled accounts", func(t *testing.T) {
		for _, kind := range []string{"expired", "disabled", "password set"} {
			a := newAccount(strings.ReplaceAll(kind, " ", "-"))
			token, _, err := MintInvitation(ctx, gdb, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "expired":
				err = gdb.Model(&models.OneTimeToken{}).Where("token_hash = ?", hashToken(token)).Update("expires_at", time.Now().Add(-time.Second)).Error
			case "disabled":
				err = gdb.Model(&a).Update("active", false).Error
			case "password set":
				var hash string
				hash, err = auth.HashPassword("an administrator supplied password")
				if err == nil {
					_, err = repositories.NewCredentialRepository(gdb).SetPassword(a.ID, hash, nil, time.Now())
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := AcceptInvitation(ctx, gdb, token, chosenPassword); !errors.Is(err, ErrTokenInvalid) {
				t.Fatalf("%s invitation accepted: %v", kind, err)
			}
		}
	})
	t.Run("credential failure rolls back token and account", func(t *testing.T) {
		a := newAccount("rollback")
		token, _, err := MintInvitation(ctx, gdb, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		const callback = "test:refuse-invitation-password"
		if err := gdb.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "credentials" {
				_ = tx.AddError(errors.New("injected credential failure"))
			}
		}); err != nil {
			t.Fatal(err)
		}
		_, failed := AcceptInvitation(ctx, gdb, token, chosenPassword)
		if err := gdb.Callback().Create().Remove(callback); err != nil {
			t.Fatal(err)
		}
		if failed == nil {
			t.Fatal("credential failure was ignored")
		}
		var row models.OneTimeToken
		if err := gdb.Where("token_hash = ?", hashToken(token)).First(&row).Error; err != nil {
			t.Fatal(err)
		}
		if err := gdb.First(&a, a.ID).Error; err != nil {
			t.Fatal(err)
		}
		if row.UsedAt != nil || a.CredentialEpoch != 0 || a.LastPasswordChange != nil || a.TwoFactorRequired {
			t.Fatal("failed invitation changed account or spent its token")
		}
		if _, err := AcceptInvitation(ctx, gdb, token, chosenPassword); err != nil {
			t.Fatalf("rolled-back invitation cannot be retried: %v", err)
		}
	})
	t.Run("competing recipients have one winner", func(t *testing.T) {
		a := newAccount("race")
		token, _, err := MintInvitation(ctx, gdb, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		results := make(chan error, 4)
		for i := range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := AcceptInvitation(ctx, gdb, token, fmt.Sprintf("recipient %d chooses this password", i))
				results <- err
			}()
		}
		wg.Wait()
		close(results)
		wins := 0
		for err := range results {
			if err == nil {
				wins++
			} else if !errors.Is(err, ErrTokenInvalid) {
				t.Fatal(err)
			}
		}
		if wins != 1 {
			t.Fatalf("%d recipients accepted one invitation", wins)
		}
	})
}
