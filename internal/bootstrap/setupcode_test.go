package bootstrap

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestSetupCode_ClaimsTheServer(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	migrateTokens(t, gdb)
	mustRBAC(t, gdb)

	code, expires, err := EnsureSetupCode(gdb, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^([0-9A-HJKMNP-TV-Z]{4}-){5}[0-9A-HJKMNP-TV-Z]{4}$`).MatchString(code) {
		t.Errorf("code %q: want six groups of four Crockford base32 characters", code)
	}
	if d := time.Until(expires); d < SetupCodeLifetime-time.Minute || d > SetupCodeLifetime {
		t.Errorf("expires in %v, want %v", d, SetupCodeLifetime)
	}
	if _, err := ClaimServer(gdb, code, "owner", chosenPassword, true); err != nil {
		t.Fatalf("claim with the printed code: %v", err)
	}
	if _, err := ClaimServer(gdb, code, "thief", chosenPassword, true); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("the code again: err = %v, want ErrTokenInvalid", err)
	}
	// Claimed: the next start prints nothing.
	if code, _, err := EnsureSetupCode(gdb, time.Now()); err != nil || code != "" {
		t.Errorf("after the claim: code %q err %v, want none", code, err)
	}
}

// A code read off a screen and typed into a phone still works.
func TestSetupCode_AsTyped(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	migrateTokens(t, gdb)
	mustRBAC(t, gdb)
	code, _, err := EnsureSetupCode(gdb, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	typed := strings.ToLower(strings.ReplaceAll(code, "-", " "))
	typed = strings.ReplaceAll(strings.ReplaceAll(typed, "0", "o"), "1", "l")
	if _, err := ClaimServer(gdb, typed, "owner", chosenPassword, true); err != nil {
		t.Errorf("claim with %q (printed %q): %v", typed, code, err)
	}
}

// A restart replaces the code: one that leaked stops working.
func TestSetupCode_ARestartReplacesIt(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	migrateTokens(t, gdb)
	mustRBAC(t, gdb)
	first, _, _ := EnsureSetupCode(gdb, time.Now())
	second, _, err := EnsureSetupCode(gdb, time.Now())
	if err != nil || second == "" || second == first {
		t.Fatalf("second start: %q, %v", second, err)
	}
	if _, err := ClaimServer(gdb, first, "thief", chosenPassword, true); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("the replaced code: err = %v, want ErrTokenInvalid", err)
	}
	if _, err := ClaimServer(gdb, second, "owner", chosenPassword, true); err != nil {
		t.Errorf("the current code: %v", err)
	}
}

func TestSetupCode_Expires(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	migrateTokens(t, gdb)
	mustRBAC(t, gdb)
	code, _, _ := EnsureSetupCode(gdb, time.Now().Add(-SetupCodeLifetime-time.Minute))
	if _, err := ClaimServer(gdb, code, "owner", chosenPassword, true); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("an expired code: err = %v, want ErrTokenInvalid", err)
	}
}

// Claiming any way kills every other way, the printed code included.
func TestSetupCode_DeadOnceClaimedOtherwise(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	migrateTokens(t, gdb)
	mustRBAC(t, gdb)
	code, _, _ := EnsureSetupCode(gdb, time.Now())
	minted, _, _ := MintSetupToken(gdb, 0)
	if _, err := ClaimServer(gdb, minted, "owner", chosenPassword, true); err != nil {
		t.Fatal(err)
	}
	if _, err := ClaimServer(gdb, code, "thief", chosenPassword, true); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("printed code after a claim with a minted token: err = %v, want ErrTokenInvalid", err)
	}
}

// The code is a separate purpose: it cannot reset an admin.
func TestSetupCode_IsNotAResetToken(t *testing.T) {
	gdb := openBootstrapTestDB(t)
	migrateTokens(t, gdb)
	mustRBAC(t, gdb)
	code, _, _ := EnsureSetupCode(gdb, time.Now())
	if _, err := ResetAdmin(gdb, code, chosenPassword); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("reset with the setup code: err = %v, want ErrTokenInvalid", err)
	}
}
