// Package srs implements the Sender Rewriting Scheme for mail REST Mail forwards
// (#775). A forward's envelope sender becomes an address under the forwarding
// domain, so SPF at the next hop is checked against that domain, and a bounce
// comes back here, where the address is checked and reversed to find whom to tell:
//
//	SRS0=HHHH=TT=orig.example=alice@forward.example
//
// TT is the day the address was made, as the day count mod 1024 in two base 32
// characters. HHHH is the start of an HMAC-SHA256 over TT, the sender's domain
// and the sender's local part, in four base 32 characters. Only this server ever
// checks an address it made, so the format need not interoperate with another
// implementation's hash.
//
// Mail systems can change the case of a local part. So the hash is taken over the
// fields in lower case and compared in any case, as postsrsd does. Base 32 has
// one letter case, so ignoring case costs the hash no strength.
package srs

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"strings"
	"time"
)

// Lifetime is how many days an address stays valid: long enough for the slowest
// bounce, as the queue retries for five days (#750).
const Lifetime = 21

const (
	prefix       = "SRS0="
	hashLength   = 4
	timeAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	timeSlots    = 1024 // two base 32 characters
	secondsInDay = 86400
)

var (
	// ErrForged is an address this server did not sign with a secret it still
	// accepts, or one that does not parse.
	ErrForged = errors.New("srs: address is forged")
	// ErrExpired is an address older than Lifetime.
	ErrExpired = errors.New("srs: address has expired")
	// ErrNotRewritable is a sender the address format cannot carry, or a forward
	// with no domain to rewrite it under.
	ErrNotRewritable = errors.New("srs: sender cannot be rewritten")
)

// Signer makes and checks addresses. A nil Signer means no secret is configured.
type Signer struct {
	current  []byte
	previous []byte
}

// New returns a Signer that signs with current and checks with current or
// previous. Previous may be empty. Without a current secret it returns nil.
func New(current, previous string) *Signer {
	if current == "" {
		return nil
	}
	s := &Signer{current: []byte(current)}
	if previous != "" {
		s.previous = []byte(previous)
	}
	return s
}

// IsAddress reports whether address claims to be an SRS0 address, whatever its
// case, and whether or not it is valid.
func IsAddress(address string) bool {
	return len(address) >= len(prefix) && strings.EqualFold(address[:len(prefix)], prefix)
}

// Rewrite returns the address that carries sender under forwardingDomain. A null
// sender stays null (RFC 5321 §6.1).
func (s *Signer) Rewrite(sender, forwardingDomain string, now time.Time) (string, error) {
	if sender == "" {
		return "", nil
	}
	at := strings.LastIndexByte(sender, '@')
	if at <= 0 || at == len(sender)-1 || forwardingDomain == "" {
		return "", ErrNotRewritable
	}
	local, domain := sender[:at], sender[at+1:]
	// The domain is a field between separators, and an address literal is not
	// allowed in a local part.
	if strings.ContainsAny(domain, "=[]") {
		return "", ErrNotRewritable
	}
	stamp := timestamp(now)
	return prefix + hash(s.current, stamp, domain, local) + "=" + stamp + "=" + domain + "=" + local + "@" + forwardingDomain, nil
}

// Reverse checks an address and returns the sender it carries. It returns
// ErrForged or ErrExpired for one that must be refused.
func (s *Signer) Reverse(address string, now time.Time) (string, error) {
	at := strings.LastIndexByte(address, '@')
	if at < 0 || !IsAddress(address) {
		return "", ErrForged
	}
	// The sender's local part may hold separators of its own, so it is the rest.
	fields := strings.SplitN(address[len(prefix):at], "=", 4)
	if len(fields) != 4 {
		return "", ErrForged
	}
	tag, stamp, domain, local := fields[0], fields[1], fields[2], fields[3]
	if len(tag) != hashLength || domain == "" || local == "" {
		return "", ErrForged
	}
	slot, ok := parseTimestamp(stamp)
	if !ok || !s.signed(tag, stamp, domain, local) {
		return "", ErrForged
	}
	if age := (daySlot(now) - slot + timeSlots) % timeSlots; age > Lifetime {
		return "", ErrExpired
	}
	return local + "@" + domain, nil
}

// signed reports whether tag is the hash of the fields under either secret. It
// always takes both, so the time it takes does not say which one matched.
func (s *Signer) signed(tag, stamp, domain, local string) bool {
	tag = strings.ToUpper(tag)
	match := hmac.Equal([]byte(tag), []byte(hash(s.current, stamp, domain, local)))
	if s.previous != nil && hmac.Equal([]byte(tag), []byte(hash(s.previous, stamp, domain, local))) {
		match = true
	}
	return match
}

// hash is the truncated HMAC of the fields, in lower case and each ended by a
// byte no address holds, so that moving a character between fields changes it.
func hash(secret []byte, stamp, domain, local string) string {
	mac := hmac.New(sha256.New, secret)
	for _, field := range []string{stamp, domain, local} {
		mac.Write([]byte(strings.ToLower(field)))
		mac.Write([]byte{0})
	}
	return base32.StdEncoding.EncodeToString(mac.Sum(nil))[:hashLength]
}

func daySlot(now time.Time) int {
	return int((now.Unix() / secondsInDay) % timeSlots)
}

func timestamp(now time.Time) string {
	slot := daySlot(now)
	return string([]byte{timeAlphabet[slot>>5], timeAlphabet[slot&31]})
}

func parseTimestamp(stamp string) (int, bool) {
	if len(stamp) != 2 {
		return 0, false
	}
	stamp = strings.ToUpper(stamp)
	high, low := strings.IndexByte(timeAlphabet, stamp[0]), strings.IndexByte(timeAlphabet, stamp[1])
	if high < 0 || low < 0 {
		return 0, false
	}
	return high<<5 | low, true
}
