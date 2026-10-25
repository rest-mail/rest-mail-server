package auth

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/sergeymakinen/go-crypt/sha256"
	"github.com/sergeymakinen/go-crypt/sha512"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

// Bound imported work factors before doing any password work. A hash comes
// from an administrator, but its verification is exposed to unauthenticated
// clients. Larger installations can lower their source work factor first.
const maxImportedSHAcryptRounds = 1_000_000

// Argon2's cost is memory, paid again by every sign-in attempt until the hash
// becomes bcrypt. 256 MiB covers libsodium's moderate setting; Dovecot warns
// that Argon2 "can require quite a hefty amount of virtual memory".
const (
	maxImportedArgon2MemoryKiB = 256 * 1024
	maxImportedArgon2Passes    = 10
	maxImportedArgon2Lanes     = 8
)

// argon2Slots bounds how many Argon2 verifications run at once, so a burst of
// wrong passwords for imported accounts cannot hold more than two of them in
// memory: later attempts wait their turn.
var argon2Slots = make(chan struct{}, 2)

var (
	errImportedPasswordHash = errors.New("use a valid ARGON2ID or ARGON2I (v=19, at most 256 MiB, 10 passes and 8 lanes), BLF-CRYPT (cost 4–14), SHA256-CRYPT or SHA512-CRYPT (1000–1000000 rounds) hash")
	bcryptImportPattern     = regexp.MustCompile(`^\$2[aby]\$[0-9]{2}\$[./A-Za-z0-9]{53}$`)
	sha256ImportPattern     = regexp.MustCompile(`^\$5\$(rounds=[1-9][0-9]*\$)?[./A-Za-z0-9]{1,16}\$[./A-Za-z0-9]{43}$`)
	sha512ImportPattern     = regexp.MustCompile(`^\$6\$(rounds=[1-9][0-9]*\$)?[./A-Za-z0-9]{1,16}\$[./A-Za-z0-9]{86}$`)
	// The PHC string libsodium writes and Dovecot stores: base64 without padding,
	// a salt of 8–64 bytes and a hash of 16–64.
	argon2ImportPattern = regexp.MustCompile(`^\$(argon2id|argon2i)\$v=19\$m=([0-9]{1,6}),t=([0-9]{1,2}),p=([0-9])\$([A-Za-z0-9+/]{11,86})\$([A-Za-z0-9+/]{22,86})$`)
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
	case argon2ImportPattern.MatchString(raw):
		h, ok := parseArgon2(raw)
		if want := "{" + strings.ToUpper(h.variant) + "}"; ok && (scheme == "" || scheme == want) {
			return want + raw, nil
		}
	}
	return "", errImportedPasswordHash
}

type argon2Hash struct {
	variant        string // argon2id or argon2i
	memory, passes uint32
	lanes          uint8
	salt, sum      []byte
}

// parseArgon2 reads a PHC string and holds it to the import bounds.
func parseArgon2(raw string) (argon2Hash, bool) {
	m := argon2ImportPattern.FindStringSubmatch(raw)
	if m == nil {
		return argon2Hash{}, false
	}
	memory, err1 := strconv.ParseUint(m[2], 10, 32)
	passes, err2 := strconv.ParseUint(m[3], 10, 32)
	lanes, err3 := strconv.ParseUint(m[4], 10, 8)
	salt, err4 := base64.RawStdEncoding.Strict().DecodeString(m[5])
	sum, err5 := base64.RawStdEncoding.Strict().DecodeString(m[6])
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil {
		return argon2Hash{}, false
	}
	// Argon2 needs at least 8 KiB of memory per lane (RFC 9106 §3.1).
	if lanes < 1 || lanes > maxImportedArgon2Lanes || passes < 1 || passes > maxImportedArgon2Passes ||
		memory < 8*lanes || memory > maxImportedArgon2MemoryKiB ||
		len(salt) < 8 || len(salt) > 64 || len(sum) < 16 || len(sum) > 64 {
		return argon2Hash{}, false
	}
	return argon2Hash{variant: m[1], memory: uint32(memory), passes: uint32(passes), lanes: uint8(lanes), salt: salt, sum: sum}, true
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
	case strings.HasPrefix(normalized, "{ARGON2"):
		_, raw, _ := strings.Cut(normalized, "}")
		err = checkArgon2(password, raw)
	default:
		return ErrInvalidCredentials
	}
	if err != nil {
		return ErrInvalidCredentials
	}
	return nil
}

func checkArgon2(password, raw string) error {
	h, ok := parseArgon2(raw)
	if !ok {
		return ErrInvalidCredentials
	}
	argon2Slots <- struct{}{}
	defer func() { <-argon2Slots }()
	var sum []byte
	if h.variant == "argon2id" {
		sum = argon2.IDKey([]byte(password), h.salt, h.passes, h.memory, h.lanes, uint32(len(h.sum)))
	} else {
		sum = argon2.Key([]byte(password), h.salt, h.passes, h.memory, h.lanes, uint32(len(h.sum)))
	}
	if subtle.ConstantTimeCompare(sum, h.sum) != 1 {
		return ErrInvalidCredentials
	}
	return nil
}

// importedSchemes are the stored prefixes of an imported hash: Dovecot's
// schemes, and raw crypt(3) and PHC strings stored before the scheme was
// written alongside them.
var importedSchemes = []string{"{SHA256-CRYPT}", "{SHA512-CRYPT}", "{ARGON2ID}", "{ARGON2I}", "$5$", "$6$", "$argon2"}

// isImportedHash reports whether a stored hash is an imported scheme that
// CheckPassword verifies with checkImportedPassword rather than bcrypt.
func isImportedHash(hash string) bool {
	for _, prefix := range importedSchemes {
		if strings.HasPrefix(hash, prefix) {
			return true
		}
	}
	return false
}

// PasswordNeedsRehash reports whether a proven password can be upgraded to
// our bcrypt format. bcrypt cannot represent passwords longer than 72 bytes:
// keep those imported hashes until the user chooses a shorter password.
func PasswordNeedsRehash(password, hash string) bool {
	return len(password) <= 72 && isImportedHash(hash)
}
