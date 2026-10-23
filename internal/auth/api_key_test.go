package auth

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNewAPIKey_Format(t *testing.T) {
	key, publicID, hash, err := NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if APIKeyLength != 60 || len(key) != APIKeyLength {
		t.Fatalf("key length = %d, want %d", len(key), APIKeyLength)
	}
	if !strings.HasPrefix(key, "rmk_"+publicID+"_") {
		t.Fatalf("key %q does not carry public id %q", key, publicID)
	}
	gotID, secret, ok := ParseAPIKey(key)
	if !ok || gotID != publicID {
		t.Fatalf("ParseAPIKey(%q) = %q, %v", key, gotID, ok)
	}
	if hash != HashAppPasswordSecret(secret) || strings.Contains(hash, secret) {
		t.Fatal("the hash must be the SHA-256 of the secret, and not the secret")
	}
	if !APIKeySecretMatches(secret, hash) || APIKeySecretMatches(secret+"x", hash) || APIKeySecretMatches(secret, "") {
		t.Fatal("APIKeySecretMatches accepts only the secret against its own hash")
	}
	if IsAppPassword(key) {
		t.Fatal("an API key must never be taken for an app password")
	}
	if !LooksLikeAPIKey(key) {
		t.Fatal("LooksLikeAPIKey refuses a key")
	}
}

func TestNewAPIKey_Random(t *testing.T) {
	a, _, _, err := NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	b, _, _, err := NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two keys are the same")
	}
}

func TestParseAPIKey_RefusesAnythingElse(t *testing.T) {
	key, _, _, err := NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	appPassword, _, _, err := NewAppPassword()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"",
		"rmk_",
		appPassword,
		key[:len(key)-1],
		key + "A",
		"rmk-" + key[4:],
		key[:16] + "-" + key[17:],
		key[:20] + "+" + key[21:],
		key[:6] + "_" + key[7:],
		strings.ToUpper(key[:4]) + key[4:],
	} {
		if _, _, ok := ParseAPIKey(s); ok {
			t.Errorf("ParseAPIKey(%q) accepted", s)
		}
	}
	if LooksLikeAPIKey(appPassword) || LooksLikeAPIKey("eyJhbGciOiJIUzI1NiJ9.e30.x") {
		t.Fatal("only the rmk_ prefix marks an API key")
	}
}

func TestClaims_APIKeyPermissionsNeverInAToken(t *testing.T) {
	c := Claims{
		AccountID:         7,
		CredentialID:      9,
		CredentialKind:    CredentialKindAPIKey,
		APIKeyPermissions: []ScopedPermission{{Capability: "mailboxes:write", Scope: "domain:a.com"}},
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"mailboxes:write", "api_key", "permissions"} {
		if strings.Contains(string(b), leak) {
			t.Fatalf("claims JSON %s carries %q", b, leak)
		}
	}
	if !c.IsAPIKey() || c.MailboxOnly() {
		t.Fatal("an API key's claims are an API key's, not an app password's")
	}
}

func TestScopedPermission_JSON(t *testing.T) {
	var p ScopedPermission
	if err := json.Unmarshal([]byte(`{"capability":"mailboxes:write","scope":"domain:a.com"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p != (ScopedPermission{Capability: "mailboxes:write", Scope: "domain:a.com"}) {
		t.Fatalf("decoded %+v", p)
	}
}
