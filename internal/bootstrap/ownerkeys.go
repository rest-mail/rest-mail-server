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
// PostgreSQL holds the owner's public keys, seeded once from trusted installer
// input (RESTMAIL_OWNER_KEYS, authorized_keys format). A matching private key can
// ask the server for a reset-admin token, the way back in for an admin who has
// lost every other: it signs a challenge the server issued, and the server
// checks the signature against the current registry. The private key never
// leaves its owner's machine. Recovery needs HTTPS and the database, with no
// dependency on Kubernetes, Docker or a mounted key file.

// ChallengeLifetime is how long an owner challenge can be answered.
const ChallengeLifetime = 2 * time.Minute

// ownerChallengePurpose marks a spent challenge in the one-time token table, so
// that a signed answer cannot be replayed.
const ownerChallengePurpose = "owner_challenge"

var (
	// ErrNoOwnerKeys: the server lists no owner keys, so ownership cannot be
	// proved this way.
	ErrNoOwnerKeys = errors.New("this server has no registered owner keys")
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
	keys, err := parseKeys("RESTMAIL_OWNER_KEYS", spec)
	if err != nil {
		return nil, err
	}
	return &OwnerKeys{keys: keys}, nil
}

// parseKeys reads the authorized_keys list in the variable named name.
func parseKeys(name, spec string) ([][]byte, error) {
	var keys [][]byte
	for i, line := range strings.Split(spec, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			return nil, fmt.Errorf("%s line %d: %w", name, i+1, err)
		}
		keys = append(keys, pub.Marshal())
	}
	return keys, nil
}

// Len is how many owner keys are listed.
func (o *OwnerKeys) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

func (o *OwnerKeys) has(pub ssh.PublicKey) bool {
	return o != nil && listed(o.keys, pub)
}

func listed(keys [][]byte, pub ssh.PublicKey) bool {
	wire := pub.Marshal()
	for _, k := range keys {
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
func OwnerMessage(challenge, purpose, address string) []byte {
	return []byte("restmail-owner-v1\n" + purpose + "\n" + address + "\n" + challenge)
}

// OwnerRequest is a signed request for a reset-admin token.
type OwnerRequest struct {
	Challenge string
	PublicKey string // authorized_keys format
	Signature []byte // ssh wire-format signature over OwnerMessage
	Purpose   string // "reset-admin", the only one; it is part of what is signed
	Address   string // the account to reset (reset-admin)
	Lifetime  time.Duration
}

// MintForOwner checks an owner's signed request and mints the token it asks
// for. The challenge is spent only once the signature verifies, so only a key
// holder can make the server write anything.
func MintForOwner(database *gorm.DB, keys *OwnerKeys, challengeKey []byte, req OwnerRequest, now time.Time) (string, time.Time, error) {
	if keys.Len() == 0 {
		return "", time.Time{}, ErrNoOwnerKeys
	}
	if err := verifySigned(challengeKey, req.Challenge, req.PublicKey, req.Signature,
		OwnerMessage(req.Challenge, req.Purpose, req.Address), keys.has, ErrNotOwner, now); err != nil {
		return "", time.Time{}, err
	}
	if err := spendChallenge(database, req.Challenge, now); err != nil {
		return "", time.Time{}, err
	}

	if req.Purpose != "reset-admin" {
		return "", time.Time{}, fmt.Errorf("purpose must be reset-admin, got %q", req.Purpose)
	}
	return MintResetToken(database, req.Address, req.Lifetime)
}

// verifySigned checks a signed answer to a challenge: the challenge is this
// server's and unexpired, publicKey is one of those isListed accepts, and
// signature is its signature over message. A key that is not listed and a
// signature that does not verify are both notListed, so the answer does not
// say which.
func verifySigned(challengeKey []byte, challenge, publicKey string, signature, message []byte,
	isListed func(ssh.PublicKey) bool, notListed error, now time.Time) error {
	if err := checkChallenge(challengeKey, challenge, now); err != nil {
		return err
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(publicKey))
	if err != nil || !isListed(pub) {
		return notListed
	}
	var sig ssh.Signature
	if err := ssh.Unmarshal(signature, &sig); err != nil {
		return notListed
	}
	// RSA signatures over SHA-1 are refused; rsa-sha2-256/512, ed25519 and
	// ecdsa are fine.
	if sig.Format == ssh.KeyAlgoRSA {
		return notListed
	}
	if err := pub.Verify(message, &sig); err != nil {
		return notListed
	}
	return nil
}

// spendChallenge records challenge as answered: the token table's unique hash
// makes a second answer to it fail here. Call it only once the signature has
// verified, so that only a key holder can make the server write anything.
func spendChallenge(database *gorm.DB, challenge string, now time.Time) error {
	// A spent challenge need only be remembered until it expires, after which
	// checkChallenge refuses it anyway. Forget those that have, so that reading
	// the status after every deploy does not grow the table for ever. A failure
	// here leaves rows behind, nothing worse.
	_ = database.Where("purpose = ? AND expires_at < ?", ownerChallengePurpose, now).
		Delete(&models.OneTimeToken{}).Error
	spent := models.OneTimeToken{
		Purpose:   ownerChallengePurpose,
		TokenHash: hashToken(challenge),
		ExpiresAt: now.Add(ChallengeLifetime),
		UsedAt:    &now,
	}
	if err := database.Create(&spent).Error; err != nil {
		// Only the unique hash refusing a second row means the challenge was
		// answered before. Any other failure is the database's, and reporting
		// it as an invalid challenge sent the owner to sign again.
		if isUniqueViolation(err) {
			return ErrChallengeInvalid
		}
		return fmt.Errorf("record the answered challenge: %w", err)
	}
	return nil
}

// isUniqueViolation reports a unique constraint refusing an insert, as
// repositories.IsUniqueViolation does: bootstrap stays below the repositories.
func isUniqueViolation(err error) bool {
	return errors.Is(err, gorm.ErrDuplicatedKey) || strings.Contains(err.Error(), "SQLSTATE 23505")
}

// ChallengeKey derives the key owner challenges are signed with from a server
// secret, so the server needs no new secret to issue them.
func ChallengeKey(serverSecret string) []byte {
	mac := hmac.New(sha256.New, []byte(serverSecret))
	mac.Write([]byte("restmail owner challenge v1"))
	return mac.Sum(nil)
}
