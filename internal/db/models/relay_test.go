package models

import (
	"errors"
	"strings"
	"testing"
)

func TestRelayPasswordAtRest(t *testing.T) {
	const key, secret = "relay-master-key", "relay-password"
	stored, err := EncryptRelayPassword(secret, key)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, RelayPasswordCipherPrefix) || strings.Contains(stored, secret) {
		t.Fatalf("not encrypted at rest: %q", stored)
	}
	if got, err := LoadRelayPassword(stored, key); err != nil || got != secret {
		t.Fatalf("round trip: %q, %v", got, err)
	}
	for name, tc := range map[string]struct{ stored, key string }{
		"wrong key":     {stored, "another-key"},
		"no key":        {stored, ""},
		"plaintext":     {secret, key},
		"corrupt bytes": {RelayPasswordCipherPrefix + "AAAA", key},
	} {
		if _, err := LoadRelayPassword(tc.stored, tc.key); !errors.Is(err, ErrRelayPasswordUndecryptable) {
			t.Errorf("%s: %v, want ErrRelayPasswordUndecryptable", name, err)
		}
	}
	if _, err := EncryptRelayPassword(secret, ""); err == nil {
		t.Fatal("stored a relay password without a MASTER_KEY")
	}
}

func TestValidRelayHost(t *testing.T) {
	for _, host := range []string{"smtp.example.net", "10.0.0.5", "::1", "relay"} {
		if !ValidRelayHost(host) {
			t.Errorf("%q refused", host)
		}
	}
	for _, host := range []string{"", "smtp://x", "x/y", "x:25", "[::1]", "a..b", "-a.b", "a b"} {
		if ValidRelayHost(host) {
			t.Errorf("%q accepted", host)
		}
	}
}
