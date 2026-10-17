package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
)

// App passwords (#344) are random, named, revocable passwords a mailbox gives a
// mail client or an automation. One looks like
//
//	rm_<public id>_<secret>
//
// in letters and digits, joined by underscores, so every mail client takes it:
// 3 + 12 + 1 + 43 = 59 characters, within the 64 the format allows. The public
// id finds the credential; the secret proves it. The secret is 43 characters
// drawn uniformly from 62, about 256 bits: as much as 32 random bytes. A secret
// that long cannot be guessed, so the server keeps a plain SHA-256 of it; bcrypt
// is for passwords people choose.
const (
	AppPasswordPrefix       = "rm_"
	appPasswordIDLength     = 12
	appPasswordSecretLength = 43
	// AppPasswordLength is the length of every app password.
	AppPasswordLength = len(AppPasswordPrefix) + appPasswordIDLength + 1 + appPasswordSecretLength
)

const base62Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// dummyAppPasswordHash is what a sign-in compares against when no credential
// has the public id presented, so an unknown id costs the same as a wrong
// secret.
var dummyAppPasswordHash = HashAppPasswordSecret(strings.Repeat("0", appPasswordSecretLength))

// NewAppPassword makes an app password. It returns the password, to show its
// owner once; its public id; and the hash of its secret, which is all the
// server keeps.
func NewAppPassword() (password, publicID, secretHash string, err error) {
	publicID, err = randomBase62(appPasswordIDLength)
	if err != nil {
		return "", "", "", err
	}
	secret, err := randomBase62(appPasswordSecretLength)
	if err != nil {
		return "", "", "", err
	}
	return AppPasswordPrefix + publicID + "_" + secret, publicID, HashAppPasswordSecret(secret), nil
}

// ParseAppPassword splits an app password into its public id and its secret.
// ok is false for anything that is not exactly in the format.
func ParseAppPassword(s string) (publicID, secret string, ok bool) {
	if len(s) != AppPasswordLength || !strings.HasPrefix(s, AppPasswordPrefix) {
		return "", "", false
	}
	rest := s[len(AppPasswordPrefix):]
	if rest[appPasswordIDLength] != '_' {
		return "", "", false
	}
	publicID, secret = rest[:appPasswordIDLength], rest[appPasswordIDLength+1:]
	if !isBase62(publicID) || !isBase62(secret) {
		return "", "", false
	}
	return publicID, secret, true
}

// IsAppPassword reports whether s has the form of an app password. The
// protocol gateways use it to choose how to sign a client in.
func IsAppPassword(s string) bool {
	_, _, ok := ParseAppPassword(s)
	return ok
}

// HashAppPasswordSecret is the hex SHA-256 of an app password's secret: what
// the credentials table stores.
func HashAppPasswordSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// AppPasswordSecretMatches reports, in constant time, whether secret hashes to
// hash. An empty hash (no credential found) is compared against a dummy, so
// the work done is the same either way, and never matches.
func AppPasswordSecretMatches(secret, hash string) bool {
	want := hash
	if want == "" {
		want = dummyAppPasswordHash
	}
	match := subtle.ConstantTimeCompare([]byte(HashAppPasswordSecret(secret)), []byte(want)) == 1
	return match && hash != ""
}

// randomBase62 returns n characters drawn uniformly from base62Alphabet,
// rejecting the bytes that would bias the draw.
func randomBase62(n int) (string, error) {
	const limit = 256 - 256%len(base62Alphabet) // 248: the largest multiple of 62 a byte holds
	out := make([]byte, 0, n)
	buf := make([]byte, n+n/4)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("failed to generate an app password: %w", err)
		}
		for _, b := range buf {
			if int(b) < limit && len(out) < n {
				out = append(out, base62Alphabet[int(b)%len(base62Alphabet)])
			}
		}
	}
	return string(out), nil
}

func isBase62(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9') {
			return false
		}
	}
	return true
}

// Why an app password was refused. The audit log records which; whoever
// presented it is told only that the address or the app password is wrong.
const (
	AppPasswordMalformed       = "malformed"     // not in the app password format
	AppPasswordUnknown         = "unknown"       // no credential has its public id
	AppPasswordWrongSecret     = "wrong_secret"  // the secret does not match
	AppPasswordWrongAddress    = "wrong_address" // it belongs to another mailbox
	AppPasswordRevoked         = "revoked"
	AppPasswordExpired         = "expired"
	AppPasswordMailboxInactive = "mailbox_inactive"
)

// AppPasswordRefusal is the error for an app password that was checked and
// refused.
type AppPasswordRefusal struct {
	// Reason is one of the AppPassword* reasons above.
	Reason string
	// PublicID is the public id presented, when the password had the format.
	PublicID string
	// CredentialID is set once the secret has matched, so the credential was
	// really presented: refused as revoked, expired, for another address, or
	// for an inactive mailbox.
	CredentialID *uint
}

func (e *AppPasswordRefusal) Error() string { return "app password refused: " + e.Reason }
