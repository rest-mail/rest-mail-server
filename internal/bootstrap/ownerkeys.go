package bootstrap

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/db/models"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

// Owner keys prove ownership of a server over its public API, whatever runs it.
//
// The deployment lists the owner's public keys (RESTMAIL_OWNER_KEYS, the
// format of an authorized_keys file). Whoever holds a matching private key can
// ask the server for a setup or reset-admin token: it signs a challenge the
// server issued, and the server checks the signature against the list. No
// secret is configured anywhere — the key that proves ownership never leaves
// the machine holding it — and nothing about how the server is deployed is
// assumed: an environment variable is something every platform can set.

// ChallengeLifetime is how long an owner challenge can be answered.
const ChallengeLifetime = 2 * time.Minute

// ownerChallengePurpose marks a spent challenge in the one-time token table, so
// that a signed answer cannot be replayed.
const ownerChallengePurpose = "owner_challenge"

var (
	// ErrNoOwnerKeys: the server lists no owner keys, so ownership cannot be
	// proved this way.
	ErrNoOwnerKeys = errors.New("this server lists no owner keys (RESTMAIL_OWNER_KEYS)")
	// ErrNotOwner covers an unlisted key and a bad signature alike.
	ErrNotOwner = errors.New("that key is not one of this server's owner keys, or the signature does not match")
	// ErrChallengeInvalid covers forged, expired and already-answered challenges.
	ErrChallengeInvalid = errors.New("the challenge is not valid: unknown, expired, or already answered")
)

// OwnerKeys are the public keys allowed to prove ownership of this server.
type OwnerKeys struct {
	keys [][]byte // each key's wire encoding
}

// ParseOwnerKeys reads keys in authorized_keys format, one per line; blank lines
// and # comments are ignored. A line that does not parse is an error, not a
// skip: a typo here would otherwise lock the owner out without a word.
func ParseOwnerKeys(spec string) (*OwnerKeys, error) {
	o := &OwnerKeys{}
	for i, line := range strings.Split(spec, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			return nil, fmt.Errorf("RESTMAIL_OWNER_KEYS line %d: %w", i+1, err)
		}
		o.keys = append(o.keys, pub.Marshal())
	}
	return o, nil
}

// Len is how many owner keys are listed.
func (o *OwnerKeys) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

func (o *OwnerKeys) has(pub ssh.PublicKey) bool {
	wire := pub.Marshal()
	for _, k := range o.keys {
		if bytes.Equal(k, wire) {
			return true
		}
	}
	return false
}

// NewChallenge issues a challenge for an owner to sign. It is stateless — an
// expiry, randomness and an HMAC under key — so issuing one stores nothing.
func NewChallenge(key []byte, now time.Time) (string, time.Time, error) {
	expires := now.Add(ChallengeLifetime)
	body := make([]byte, 8+16)
	binary.BigEndian.PutUint64(body, uint64(expires.Unix()))
	if _, err := rand.Read(body[8:]); err != nil {
		return "", time.Time{}, err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(append(body, mac.Sum(nil)...)), expires, nil
}

func checkChallenge(key []byte, challenge string, now time.Time) error {
	raw, err := base64.RawURLEncoding.DecodeString(challenge)
	if err != nil || len(raw) != 8+16+sha256.Size {
		return ErrChallengeInvalid
	}
	body, sum := raw[:24], raw[24:]
	mac := hmac.New(sha256.New, key)
	mac.Write(body)
	if !hmac.Equal(sum, mac.Sum(nil)) {
		return ErrChallengeInvalid
	}
	if now.Unix() > int64(binary.BigEndian.Uint64(body[:8])) {
		return ErrChallengeInvalid
	}
	return nil
}

// OwnerMessage is what an owner signs: the challenge, bound to what it is asking
// for, so a signature for one request cannot be presented for another.
func OwnerMessage(challenge, purpose, username string) []byte {
	return []byte("restmail-owner-v1\n" + purpose + "\n" + username + "\n" + challenge)
}

// OwnerRequest is a signed request for a setup or reset-admin token.
type OwnerRequest struct {
	Challenge string
	PublicKey string // authorized_keys format
	Signature []byte // ssh wire-format signature over OwnerMessage
	Purpose   string // "setup" or "reset-admin"
	Username  string // the admin to reset (reset-admin)
	Lifetime  time.Duration
}

// MintForOwner checks an owner's signed request and mints the token it asks
// for. The challenge is spent only once the signature verifies, so only a key
// holder can make the server write anything.
func MintForOwner(database *gorm.DB, keys *OwnerKeys, challengeKey []byte, req OwnerRequest, now time.Time) (string, time.Time, error) {
	if keys.Len() == 0 {
		return "", time.Time{}, ErrNoOwnerKeys
	}
	if err := checkChallenge(challengeKey, req.Challenge, now); err != nil {
		return "", time.Time{}, err
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(req.PublicKey))
	if err != nil || !keys.has(pub) {
		return "", time.Time{}, ErrNotOwner
	}
	var sig ssh.Signature
	if err := ssh.Unmarshal(req.Signature, &sig); err != nil {
		return "", time.Time{}, ErrNotOwner
	}
	// RSA signatures over SHA-1 are refused; rsa-sha2-256/512, ed25519 and
	// ecdsa are fine.
	if sig.Format == ssh.KeyAlgoRSA {
		return "", time.Time{}, ErrNotOwner
	}
	if err := pub.Verify(OwnerMessage(req.Challenge, req.Purpose, req.Username), &sig); err != nil {
		return "", time.Time{}, ErrNotOwner
	}

	// Spend the challenge: the token table's unique hash makes a second
	// answer to it fail here.
	spent := models.OneTimeToken{
		Purpose:   ownerChallengePurpose,
		TokenHash: hashToken(req.Challenge),
		ExpiresAt: now.Add(ChallengeLifetime),
		UsedAt:    &now,
	}
	if err := database.Create(&spent).Error; err != nil {
		return "", time.Time{}, ErrChallengeInvalid
	}

	switch req.Purpose {
	case "setup":
		return MintSetupToken(database, req.Lifetime)
	case "reset-admin":
		return MintResetToken(database, req.Username, req.Lifetime)
	default:
		return "", time.Time{}, fmt.Errorf("purpose must be setup or reset-admin, got %q", req.Purpose)
	}
}

// ChallengeKey derives the key owner challenges are signed with from a server
// secret, so the server needs no new secret to issue them.
func ChallengeKey(serverSecret string) []byte {
	mac := hmac.New(sha256.New, []byte(serverSecret))
	mac.Write([]byte("restmail owner challenge v1"))
	return mac.Sum(nil)
}
