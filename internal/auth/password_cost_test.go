package auth

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func bcryptCostOf(t *testing.T, hash string) int {
	t.Helper()
	cost, err := bcrypt.Cost([]byte(strings.TrimPrefix(hash, "{BLF-CRYPT}")))
	if err != nil {
		t.Fatalf("%q: %v", hash, err)
	}
	return cost
}

func hashAt(t *testing.T, password string, cost int) string {
	t.Helper()
	hash, err := hashAtCost(password, cost)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

// A new password is at cost 12, and an unknown address costs what a wrong
// password does: the dummy comparison is at the same cost (#1642).
func TestNewPasswordsAndTheDummyHashAreAtCost12(t *testing.T) {
	if PasswordHashCost != 12 {
		t.Errorf("PasswordHashCost = %d, want 12", PasswordHashCost)
	}
	hash, err := HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if got := bcryptCostOf(t, hash); got != 12 {
		t.Errorf("new password at cost %d", got)
	}
	if got := bcryptCostOf(t, DummyPasswordHash); got != 12 {
		t.Errorf("dummy hash at cost %d", got)
	}
}

// A recovery code is random, so it does not pay for the password cost on every
// unused code a wrong code is compared with.
func TestRecoveryCodesStayAtTheirOwnCost(t *testing.T) {
	hash, err := HashRecoveryCode("abcd-efgh")
	if err != nil {
		t.Fatal(err)
	}
	if got := bcryptCostOf(t, hash); got != recoveryCodeHashCost {
		t.Errorf("recovery code at cost %d, want %d", got, recoveryCodeHashCost)
	}
	if !CheckRecoveryCode("ABCD-EFGH", hash) {
		t.Error("a recovery code does not check against its hash")
	}
}

func TestPasswordNeedsRehash(t *testing.T) {
	const password = "correct-horse-battery"
	for name, tc := range map[string]struct {
		password, hash string
		want           bool
	}{
		"cheaper bcrypt":                   {password, hashAt(t, password, bcrypt.MinCost), true},
		"cheaper bcrypt, raw":              {password, strings.TrimPrefix(hashAt(t, password, bcrypt.MinCost), "{BLF-CRYPT}"), true},
		"bcrypt at the server's cost":      {password, hashAt(t, password, PasswordHashCost), false},
		"dearer bcrypt is not weakened":    {password, hashAt(t, password, PasswordHashCost+1), false},
		"imported SHA-crypt":               {password, "{SHA512-CRYPT}$6$importsalt$x", true},
		"imported Argon2":                  {password, "{ARGON2ID}$argon2id$x", true},
		"over 72 bytes keeps cheap bcrypt": {strings.Repeat("x", 73), "{BLF-CRYPT}$2a$04$" + strings.Repeat(".", 53), false},
		"over 72 bytes keeps an import":    {strings.Repeat("x", 73), "{SHA512-CRYPT}$6$importsalt$x", false},
		"what is not a hash":               {password, "not a hash", false},
	} {
		if got := PasswordNeedsRehash(tc.password, tc.hash); got != tc.want {
			t.Errorf("%s: PasswordNeedsRehash = %v, want %v", name, got, tc.want)
		}
	}
}
