package auth

import (
	"errors"
	"strings"
	"testing"
)

// Independent fixtures: openssl passwd -5/-6 -salt importsalt old-password.
const (
	importSHA256 = "$5$importsalt$bHBgUR.cLP5CqE2HvtcibUnobsIEkJ3fMmuinleEQt/"
	importSHA512 = "$6$importsalt$ObInO4qcr134i8FQT.XNC6Ma04n9aRTAUNHmm8Yb1idoDBOXKoDTmdosY1xwfageGXo4ZSSrkIPRk9zMWfsG7/"
)

func TestImportedPasswordHashes(t *testing.T) {
	for _, tc := range []struct{ raw, scheme string }{
		{importSHA256, "{SHA256-CRYPT}"},
		{importSHA512, "{SHA512-CRYPT}"},
	} {
		for _, input := range []string{tc.raw, tc.scheme + tc.raw} {
			normalized, err := NormalizeImportedPasswordHash(input)
			if err != nil || normalized != tc.scheme+tc.raw {
				t.Fatalf("normalize: %q, %v", normalized, err)
			}
			if err := CheckPassword("old-password", input); err != nil {
				t.Errorf("correct password: %v", err)
			}
			for _, wrong := range []string{"", "wrong", "old-password\x00", strings.Repeat("x", 4097)} {
				if err := CheckPassword(wrong, input); !errors.Is(err, ErrInvalidCredentials) {
					t.Errorf("wrong password: %v", err)
				}
			}
			if !PasswordNeedsRehash("old-password", input) || PasswordNeedsRehash(strings.Repeat("x", 73), input) {
				t.Error("rehash must preserve bcrypt's 72-byte boundary")
			}
		}
	}
	for _, input := range []string{DummyPasswordHash, strings.TrimPrefix(DummyPasswordHash, "{BLF-CRYPT}")} {
		got, err := NormalizeImportedPasswordHash(input)
		if err != nil || got != DummyPasswordHash || PasswordNeedsRehash("password", got) {
			t.Errorf("bcrypt normalization: %q, %v", got, err)
		}
	}
}

func TestImportedPasswordHashesRejectMalformedAndUnbounded(t *testing.T) {
	for _, hash := range []string{
		"", "{PLAIN}password", "$1$salt$checksum", "{MD5-CRYPT}$1$salt$checksum",
		"{ARGON2ID}$argon2id$v=19$m=65536,t=3,p=1$salt$sum",
		"{SHA256-CRYPT}" + importSHA512, "{BLF-CRYPT}" + importSHA256,
		importSHA256 + "extra", importSHA512[:len(importSHA512)-1],
		strings.Replace(importSHA256, "importsalt", "invalid!", 1),
		strings.Replace(importSHA512, "$6$", "$6$rounds=1000001$", 1),
		strings.Replace(importSHA256, "$5$", "$5$rounds=999$", 1),
		strings.Replace(importSHA512, "$6$", "$6$rounds=9999999999999999999$", 1),
		strings.Replace(DummyPasswordHash, "$10$", "$15$", 1),
		DummyPasswordHash + "extra", "{BLF-CRYPT}$2a$10$", strings.Repeat("x", 257),
	} {
		if _, err := NormalizeImportedPasswordHash(hash); err == nil {
			t.Errorf("accepted malformed, weak or excessive hash %q", hash)
		}
	}
}
