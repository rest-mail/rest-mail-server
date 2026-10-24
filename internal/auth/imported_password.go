package auth

import (
	"errors"
	"regexp"
	"strings"

	"github.com/sergeymakinen/go-crypt/sha256"
	"github.com/sergeymakinen/go-crypt/sha512"
	"golang.org/x/crypto/bcrypt"
)

// Bound imported work factors before doing any password work. A hash comes
// from an administrator, but its verification is exposed to unauthenticated
// clients. Larger installations can lower their source work factor first.
const maxImportedSHAcryptRounds = 1_000_000

var (
	errImportedPasswordHash = errors.New("use a valid BLF-CRYPT (cost 4–14), SHA256-CRYPT or SHA512-CRYPT (1000–1000000 rounds) hash")
	bcryptImportPattern     = regexp.MustCompile(`^\$2[aby]\$[0-9]{2}\$[./A-Za-z0-9]{53}$`)
	sha256ImportPattern     = regexp.MustCompile(`^\$5\$(rounds=[1-9][0-9]*\$)?[./A-Za-z0-9]{1,16}\$[./A-Za-z0-9]{43}$`)
	sha512ImportPattern     = regexp.MustCompile(`^\$6\$(rounds=[1-9][0-9]*\$)?[./A-Za-z0-9]{1,16}\$[./A-Za-z0-9]{86}$`)
)

// NormalizeImportedPasswordHash validates a Dovecot or raw crypt(3) hash
// without needing the password, returning its explicit Dovecot scheme.
// Weak schemes (including MD5-CRYPT and PLAIN) are never accepted.
func NormalizeImportedPasswordHash(hash string) (string, error) {
	if len(hash) > 256 {
		return "", errImportedPasswordHash
	}
	raw, scheme := hash, ""
	if strings.HasPrefix(hash, "{") {
		var ok bool
		scheme, raw, ok = strings.Cut(hash, "}")
		if !ok {
			return "", errImportedPasswordHash
		}
		scheme += "}"
	}
	switch {
	case bcryptImportPattern.MatchString(raw) && (scheme == "" || scheme == "{BLF-CRYPT}"):
		cost, err := bcrypt.Cost([]byte(raw))
		if err == nil && cost >= bcrypt.MinCost && cost <= 14 {
			return "{BLF-CRYPT}" + raw, nil
		}
	case sha256ImportPattern.MatchString(raw) && (scheme == "" || scheme == "{SHA256-CRYPT}"):
		_, rounds, err := sha256.Params(raw)
		if err == nil && rounds >= sha256.MinRounds && rounds <= maxImportedSHAcryptRounds {
			return "{SHA256-CRYPT}" + raw, nil
		}
	case sha512ImportPattern.MatchString(raw) && (scheme == "" || scheme == "{SHA512-CRYPT}"):
		_, rounds, err := sha512.Params(raw)
		if err == nil && rounds >= sha512.MinRounds && rounds <= maxImportedSHAcryptRounds {
			return "{SHA512-CRYPT}" + raw, nil
		}
	}
	return "", errImportedPasswordHash
}

func checkImportedPassword(password, hash string) error {
	normalized, err := NormalizeImportedPasswordHash(hash)
	// SHA-crypt includes a password-length-dependent expansion. Bound it as
	// well as the rounds; never truncate a password for verification.
	if err != nil || len(password) > 4096 {
		return ErrInvalidCredentials
	}
	switch {
	case strings.HasPrefix(normalized, "{SHA256-CRYPT}"):
		err = sha256.Check(strings.TrimPrefix(normalized, "{SHA256-CRYPT}"), password)
	case strings.HasPrefix(normalized, "{SHA512-CRYPT}"):
		err = sha512.Check(strings.TrimPrefix(normalized, "{SHA512-CRYPT}"), password)
	default:
		return ErrInvalidCredentials
	}
	if err != nil {
		return ErrInvalidCredentials
	}
	return nil
}

// PasswordNeedsRehash reports whether a proven password can be upgraded to
// our bcrypt format. bcrypt cannot represent passwords longer than 72 bytes:
// keep those SHA-crypt hashes until the user chooses a shorter password.
func PasswordNeedsRehash(password, hash string) bool {
	return len(password) <= 72 && (strings.HasPrefix(hash, "{SHA256-CRYPT}") || strings.HasPrefix(hash, "{SHA512-CRYPT}") || strings.HasPrefix(hash, "$5$") || strings.HasPrefix(hash, "$6$"))
}
