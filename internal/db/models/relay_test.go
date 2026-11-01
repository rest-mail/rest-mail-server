package models

import (
	"errors"
	"regexp"
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

// The API describes the relay host by RelayHostPattern and a maxLength of 253
// (#1165): the description must accept exactly what ValidRelayHost does, or a
// host the schema permits is refused, or one it refuses would have been taken.
func TestRelayHostPatternIsValidRelayHost(t *testing.T) {
	pattern := regexp.MustCompile(RelayHostPattern)
	label63 := strings.Repeat("a", 63)
	name253 := strings.Repeat(label63+".", 3) + strings.Repeat("b", 61)
	for _, host := range []string{
		"smtp.example.net", "relay", "10.0.0.5", "999.1.1.1", "01.02.03.04", "a-b.c", "A1.b2",
		label63, label63 + "a", "a-", "-a", "a..b", ".a", "a.", "", " ", "a b", "a_b", "bücher.de",
		name253, name253 + "b",
		"::", "::1", "1::", "fe80::1", "FE80::A:b", "1:2:3:4:5:6:7:8", "1:2:3:4:5:6:7::",
		"1::2:3:4:5:6:7", "::2:3:4:5:6:7:8", "::ffff:1.2.3.4", "1:2:3:4:5:6:1.2.3.4",
		"1:2:3:4:5::1.2.3.4", "::1.2.3.4", "::0.0.0.0", "::255.255.255.255",
		"1:2:3:4:5:6:7:8::", "::1:2:3:4:5:6:7:8", "1:2:3:4:5:6::1.2.3.4", "1:2:3:4:5:6:7:1.2.3.4",
		"::01.2.3.4", "::256.1.1.1", "::1.2.3", "12345::", "1:::2", "1::2::3", ":1::", "1:",
		"[::1]", "fe80::1%eth0", "x:25", "smtp://x", "x/y", "::g", "1:2:3:4:5:6:7",
	} {
		described := len(host) <= 253 && pattern.MatchString(host)
		if described != ValidRelayHost(host) {
			t.Errorf("%q: described %v, ValidRelayHost %v", host, described, ValidRelayHost(host))
		}
	}
}
