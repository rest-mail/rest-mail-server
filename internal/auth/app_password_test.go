package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
)

// appPasswordShape is the whole format: letters and digits joined by
// underscores, so every mail client takes it.
var appPasswordShape = regexp.MustCompile(`^rm_[A-Za-z0-9]{12}_[A-Za-z0-9]{43}$`)

func TestNewAppPassword_Format(t *testing.T) {
	password, publicID, secretHash, err := NewAppPassword()
	if err != nil {
		t.Fatalf("NewAppPassword: %v", err)
	}
	if !appPasswordShape.MatchString(password) {
		t.Fatalf("app password %q is not in the format", password)
	}
	if len(password) > 64 || len(password) != AppPasswordLength {
		t.Errorf("length %d, want %d and at most 64", len(password), AppPasswordLength)
	}
	if !strings.HasPrefix(password, "rm_"+publicID+"_") {
		t.Errorf("app password %q does not carry its public id %q", password, publicID)
	}
	gotID, secret, ok := ParseAppPassword(password)
	if !ok || gotID != publicID {
		t.Fatalf("ParseAppPassword(%q) = %q, ok=%v; want %q", password, gotID, ok, publicID)
	}
	// The stored hash is the SHA-256 of the secret, and nothing of the secret
	// itself.
	sum := sha256.Sum256([]byte(secret))
	if secretHash != hex.EncodeToString(sum[:]) {
		t.Errorf("secret hash %q is not the SHA-256 of the secret", secretHash)
	}
	if strings.Contains(secretHash, secret) || strings.Contains(password, secretHash) {
		t.Error("the hash and the secret overlap")
	}
}

func TestNewAppPassword_Random(t *testing.T) {
	seen := map[string]bool{}
	ids := map[string]bool{}
	for i := 0; i < 200; i++ {
		password, publicID, _, err := NewAppPassword()
		if err != nil {
			t.Fatalf("NewAppPassword: %v", err)
		}
		if seen[password] || ids[publicID] {
			t.Fatalf("a repeat after %d app passwords", i)
		}
		seen[password], ids[publicID] = true, true
	}
}

func TestParseAppPassword_RefusesAnythingElse(t *testing.T) {
	good, _, _, err := NewAppPassword()
	if err != nil {
		t.Fatalf("NewAppPassword: %v", err)
	}
	for name, s := range map[string]string{
		"empty":            "",
		"a password":       "correct horse battery staple",
		"no prefix":        "xx" + good[2:],
		"upper prefix":     "RM" + good[2:],
		"one short":        good[:len(good)-1],
		"one long":         good + "a",
		"separator moved":  good[:14] + "_a" + good[16:],
		"no separator":     good[:15] + "a" + good[16:],
		"punctuation":      good[:20] + "-" + good[21:],
		"underscore in it": good[:20] + "_" + good[21:],
		"unicode":          good[:20] + "é" + good[22:],
		"space":            good[:20] + " " + good[21:],
	} {
		if _, _, ok := ParseAppPassword(s); ok {
			t.Errorf("%s: ParseAppPassword(%q) accepted it", name, s)
		}
		if IsAppPassword(s) {
			t.Errorf("%s: IsAppPassword(%q) = true", name, s)
		}
	}
	if !IsAppPassword(good) {
		t.Errorf("IsAppPassword(%q) = false", good)
	}
}

func TestAppPasswordSecretMatches(t *testing.T) {
	password, _, hash, err := NewAppPassword()
	if err != nil {
		t.Fatalf("NewAppPassword: %v", err)
	}
	_, secret, _ := ParseAppPassword(password)
	if !AppPasswordSecretMatches(secret, hash) {
		t.Error("the secret does not match its own hash")
	}
	last := "A"
	if strings.HasSuffix(secret, "A") {
		last = "B"
	}
	other := secret[:len(secret)-1] + last
	if AppPasswordSecretMatches(other, hash) {
		t.Error("a different secret matched")
	}
	// No credential found: compared against a dummy, and never a match, not
	// even for the secret the dummy is made from.
	if AppPasswordSecretMatches(secret, "") || AppPasswordSecretMatches(strings.Repeat("0", 43), "") {
		t.Error("an empty hash matched")
	}
	if AppPasswordSecretMatches(secret, hash[:63]) {
		t.Error("a truncated hash matched")
	}
}
