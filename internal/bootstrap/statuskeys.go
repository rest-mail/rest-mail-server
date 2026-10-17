package bootstrap

import (
	"errors"
	"time"

	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

// Status keys let a deploy be confirmed from outside: which version runs, from
// which commit, built when and started when. Only a key holder is answered.
//
// The deployment lists them as it lists the owner keys (RESTMAIL_STATUS_KEYS,
// authorized_keys format), and they are proved the same way: sign a challenge
// the server issued, bound to the purpose "status", so a signature made to
// read the status cannot be presented to reset an admin, nor the other way
// round. A status key can only ever read the status. Owner keys can read it
// too: whoever may reset the server's admins may know what it runs.
//
// Later (#341) a status key becomes a key-pair credential holding a
// server:status capability; until there are credentials, it is a list in the
// deployment, like the owner keys.

// StatusPurpose is what a request to read the status signs, in the place
// reset-admin takes. Its address is always empty.
const StatusPurpose = "status"

var (
	// ErrNoStatusKeys: the server lists neither status keys nor owner keys, so
	// nobody can read its status this way.
	ErrNoStatusKeys = errors.New("this server lists no status keys (RESTMAIL_STATUS_KEYS) and no owner keys (RESTMAIL_OWNER_KEYS)")
	// ErrNotStatusKey covers an unlisted key and a bad signature alike.
	ErrNotStatusKey = errors.New("that key is not one of this server's status or owner keys, or the signature does not match")
)

// StatusKeys are the public keys allowed to read the server's status, and
// nothing else. A type of their own, so they cannot be handed by mistake to
// anything that takes owner keys.
type StatusKeys struct {
	keys [][]byte // each key's wire encoding
}

// ParseStatusKeys reads keys as ParseOwnerKeys does: a line that does not parse
// is an error, not a skip.
func ParseStatusKeys(spec string) (*StatusKeys, error) {
	keys, err := parseKeys("RESTMAIL_STATUS_KEYS", spec)
	if err != nil {
		return nil, err
	}
	return &StatusKeys{keys: keys}, nil
}

// Len is how many status keys are listed.
func (s *StatusKeys) Len() int {
	if s == nil {
		return 0
	}
	return len(s.keys)
}

func (s *StatusKeys) has(pub ssh.PublicKey) bool {
	return s != nil && listed(s.keys, pub)
}

// StatusMessage is what a request to read the status signs.
func StatusMessage(challenge string) []byte {
	return OwnerMessage(challenge, StatusPurpose, "")
}

// StatusRequest is a signed request to read the server's status.
type StatusRequest struct {
	Challenge string
	PublicKey string // authorized_keys format
	Signature []byte // ssh wire-format signature over StatusMessage
}

// CheckStatus checks a signed request to read the status: the key must be a
// status key or an owner key, and the signature over StatusMessage must
// verify. Like an owner's request, it spends the challenge, so a signed answer
// works once.
func CheckStatus(database *gorm.DB, statusKeys *StatusKeys, ownerKeys *OwnerKeys, challengeKey []byte, req StatusRequest, now time.Time) error {
	if statusKeys.Len() == 0 && ownerKeys.Len() == 0 {
		return ErrNoStatusKeys
	}
	mayRead := func(pub ssh.PublicKey) bool { return statusKeys.has(pub) || ownerKeys.has(pub) }
	if err := verifySigned(challengeKey, req.Challenge, req.PublicKey, req.Signature,
		StatusMessage(req.Challenge), mayRead, ErrNotStatusKey, now); err != nil {
		return err
	}
	return spendChallenge(database, req.Challenge, now)
}
