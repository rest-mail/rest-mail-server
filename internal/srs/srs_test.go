package srs

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

const (
	fakeCurrent  = "fake-srs-secret-current-for-tests"
	fakePrevious = "fake-srs-secret-previous-for-tests"
)

// 2026-11-02, day 20759 since the epoch, slot 279 of 1024: base 32 "IX".
var testNow = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

func rewrite(t *testing.T, s *Signer, sender string, now time.Time) string {
	t.Helper()
	address, err := s.Rewrite(sender, "forward.example", now)
	if err != nil {
		t.Fatalf("Rewrite(%q): %v", sender, err)
	}
	return address
}

// The address has the decided form, its timestamp is the day count mod 1024 in
// two base 32 characters, and it lives under the forwarding domain.
func TestRewriteAddressFormat(t *testing.T) {
	s := New(fakeCurrent, "")
	got := rewrite(t, s, "alice@orig.example", testNow)
	if !regexp.MustCompile(`^SRS0=[A-Z2-7]{4}=IX=orig\.example=alice@forward\.example$`).MatchString(got) {
		t.Fatalf("address = %q, want SRS0=HHHH=IX=orig.example=alice@forward.example", got)
	}
}

// The timestamp wraps at 1024 days.
func TestRewriteTimestampWraps(t *testing.T) {
	s := New(fakeCurrent, "")
	first := rewrite(t, s, "alice@orig.example", testNow)
	wrapped := rewrite(t, s, "alice@orig.example", testNow.AddDate(0, 0, 1024))
	if first != wrapped {
		t.Fatalf("1024 days apart: %q and %q, want the same address", first, wrapped)
	}
}

// A null reverse path has no address to rewrite, and stays null (RFC 5321 §6.1).
func TestRewriteNullSenderStaysNull(t *testing.T) {
	if got := rewrite(t, New(fakeCurrent, ""), "", testNow); got != "" {
		t.Fatalf("null sender became %q", got)
	}
}

// What the address cannot carry is refused, so the caller does not put a
// malformed reverse path on the wire.
func TestRewriteRefusesWhatItCannotCarry(t *testing.T) {
	s := New(fakeCurrent, "")
	for _, sender := range []string{"no-at-sign", "@orig.example", "alice@", "alice@[192.0.2.1]", "alice@or=ig.example"} {
		if got, err := s.Rewrite(sender, "forward.example", testNow); !errors.Is(err, ErrNotRewritable) {
			t.Errorf("Rewrite(%q) = %q, %v, want ErrNotRewritable", sender, got, err)
		}
	}
	if got, err := s.Rewrite("alice@orig.example", "", testNow); !errors.Is(err, ErrNotRewritable) {
		t.Errorf("no forwarding domain: %q, %v, want ErrNotRewritable", got, err)
	}
}

// The address reverses to the sender, whatever the sender's local part holds:
// another system's tag, or another forwarder's SRS address, since there is no SRS1.
func TestReverseReturnsTheSender(t *testing.T) {
	s := New(fakeCurrent, "")
	for _, sender := range []string{
		"alice@orig.example",
		"Alice.Smith+tag@Orig.Example",
		"prvs=1234=alice@orig.example",
		"SRS0=ABCD=AA=first.example=alice@second.example",
		"jörg@orig.example",
	} {
		got, err := s.Reverse(rewrite(t, s, sender, testNow), testNow)
		if err != nil {
			t.Errorf("Reverse for %q: %v", sender, err)
			continue
		}
		if got != sender {
			t.Errorf("Reverse = %q, want %q", got, sender)
		}
	}
}

// Signing uses the current secret. Checking takes the current or the previous
// one, so a rotation invalidates nothing in flight, and nothing older.
func TestReverseAcceptsCurrentAndPreviousSecret(t *testing.T) {
	before := New(fakePrevious, "")
	inFlight := rewrite(t, before, "alice@orig.example", testNow)

	after := New(fakeCurrent, fakePrevious)
	if got, err := after.Reverse(inFlight, testNow); err != nil || got != "alice@orig.example" {
		t.Fatalf("signed with the previous secret: %q, %v", got, err)
	}
	signed := rewrite(t, after, "alice@orig.example", testNow)
	if signed == inFlight {
		t.Fatal("signing used the previous secret")
	}
	if got, err := after.Reverse(signed, testNow); err != nil || got != "alice@orig.example" {
		t.Fatalf("signed with the current secret: %q, %v", got, err)
	}

	rotatedAgain := New("fake-srs-secret-third-for-tests", fakeCurrent)
	if _, err := rotatedAgain.Reverse(inFlight, testNow); !errors.Is(err, ErrForged) {
		t.Fatalf("two rotations old: %v, want ErrForged", err)
	}
}

// The lifetime is 21 days, counted in whole days like the timestamp.
func TestReverseEnforcesTheLifetime(t *testing.T) {
	s := New(fakeCurrent, "")
	address := rewrite(t, s, "alice@orig.example", testNow)
	if _, err := s.Reverse(address, testNow.AddDate(0, 0, Lifetime)); err != nil {
		t.Fatalf("on the last day: %v", err)
	}
	if _, err := s.Reverse(address, testNow.AddDate(0, 0, Lifetime+1)); !errors.Is(err, ErrExpired) {
		t.Fatalf("a day after the lifetime: %v, want ErrExpired", err)
	}
	if _, err := s.Reverse(address, testNow.AddDate(0, 0, -1)); !errors.Is(err, ErrExpired) {
		t.Fatalf("an address from tomorrow: %v, want ErrExpired", err)
	}
}

// Anything but the address as it was signed is refused: each signed field, a
// hash from another key, and addresses that do not parse.
func TestReverseRefusesForgeries(t *testing.T) {
	s := New(fakeCurrent, "")
	address := rewrite(t, s, "alice@orig.example", testNow)
	parts := strings.SplitN(address, "=", 5)
	if len(parts) != 5 {
		t.Fatalf("address = %q", address)
	}
	otherHash := "AAAA"
	if parts[1] == otherHash {
		otherHash = "BBBB"
	}
	for name, forged := range map[string]string{
		"hash":             "SRS0=" + otherHash + "=" + parts[2] + "=orig.example=alice@forward.example",
		"timestamp":        "SRS0=" + parts[1] + "=IY=orig.example=alice@forward.example",
		"domain":           "SRS0=" + parts[1] + "=IX=victim.example=alice@forward.example",
		"local part":       "SRS0=" + parts[1] + "=IX=orig.example=mallory@forward.example",
		"moved separator":  "SRS0=" + parts[1] + "=IX=orig.examplea=lice@forward.example",
		"another key":      rewrite(t, New("fake-srs-secret-another-for-tests", ""), "alice@orig.example", testNow),
		"short hash":       "SRS0=ABC=IX=orig.example=alice@forward.example",
		"bad timestamp":    "SRS0=" + parts[1] + "=I1=orig.example=alice@forward.example",
		"long timestamp":   "SRS0=" + parts[1] + "=IXX=orig.example=alice@forward.example",
		"too few fields":   "SRS0=" + parts[1] + "=IX=alice@forward.example",
		"no original host": "SRS0=" + parts[1] + "=IX==alice@forward.example",
		"no original user": "SRS0=" + parts[1] + "=IX=orig.example=@forward.example",
		"no at sign":       "SRS0=" + parts[1] + "=IX=orig.example=alice",
	} {
		if got, err := s.Reverse(forged, testNow); !errors.Is(err, ErrForged) {
			t.Errorf("%s: Reverse(%q) = %q, %v, want ErrForged", name, forged, got, err)
		}
	}
	if _, err := s.Reverse("alice@forward.example", testNow); !errors.Is(err, ErrForged) {
		t.Errorf("not an SRS address: %v, want ErrForged", err)
	}
}

// Mail systems can change the case of a local part, so the hash, the timestamp
// and the prefix are checked as postsrsd checks them, whatever their case. The
// address returned keeps the case that arrived.
func TestReverseIgnoresCase(t *testing.T) {
	s := New(fakeCurrent, "")
	address := rewrite(t, s, "Alice@Orig.Example", testNow)
	local, domain, _ := strings.Cut(address, "@")

	for name, changed := range map[string]string{
		"lower": strings.ToLower(local) + "@" + domain,
		"upper": strings.ToUpper(local) + "@" + strings.ToUpper(domain),
	} {
		got, err := s.Reverse(changed, testNow)
		if err != nil {
			t.Errorf("%s: Reverse(%q): %v", name, changed, err)
			continue
		}
		if !strings.EqualFold(got, "alice@orig.example") {
			t.Errorf("%s: Reverse = %q, want alice@orig.example in some case", name, got)
		}
	}
	// A hash signed for one spelling is valid for any other.
	lower := rewrite(t, s, "alice@orig.example", testNow)
	if address != strings.Replace(strings.Replace(lower, "alice", "Alice", 1), "orig.example", "Orig.Example", 1) {
		t.Errorf("case changed the hash: %q and %q", address, lower)
	}
}

// IsAddress is true of anything claiming to be an SRS0 address, valid or not, in
// any case, and of nothing else.
func TestIsAddress(t *testing.T) {
	for address, want := range map[string]bool{
		"SRS0=ABCD=IX=orig.example=alice@forward.example": true,
		"srs0=abcd=ix=orig.example=alice@forward.example": true,
		"SRS0=@forward.example":                           true,
		"srs0@forward.example":                            false,
		"SRS1=ABCD=forward.example@forward.example":       false,
		"alice@forward.example":                           false,
		"alice=SRS0=@forward.example":                     false,
		"":                                                false,
	} {
		if got := IsAddress(address); got != want {
			t.Errorf("IsAddress(%q) = %v, want %v", address, got, want)
		}
	}
}

// No current secret means no signer: nothing to sign or check with.
func TestNewWithoutASecret(t *testing.T) {
	if New("", fakePrevious) != nil {
		t.Fatal("a signer without a current secret")
	}
}
