package auth

import (
	"errors"
	"strings"
	"testing"
	"time"
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

// Independent fixtures from OpenSSL's Argon2 (openssl kdf -binary -keylen 32
// -kdfopt pass:old-password -kdfopt salt:importsalt012345 -kdfopt iter:T
// -kdfopt memcost:M -kdfopt lanes:P ARGON2ID|ARGON2I), written as the PHC
// string libsodium and Dovecot store: base64 without padding.
const (
	importArgon2ID      = "$argon2id$v=19$m=4096,t=2,p=1$aW1wb3J0c2FsdDAxMjM0NQ$JCLzuH2QfsNfwyNUzLd3IjMXXY7Rb46hqjBt5hs545I"
	importArgon2I       = "$argon2i$v=19$m=4096,t=2,p=1$aW1wb3J0c2FsdDAxMjM0NQ$EPRiJ1+NEb2FB7BCy10XiOs7h9DWzbOWtI9FhJRyFYE"
	importArgon2IDLanes = "$argon2id$v=19$m=8192,t=3,p=2$aW1wb3J0c2FsdDAxMjM0NQ$zmp1cnc9HQqh4CJOZYzGGH1ndUB7I3SpGam4ElOVSvg"
)

func TestImportedArgon2Hashes(t *testing.T) {
	for _, tc := range []struct{ raw, scheme string }{
		{importArgon2ID, "{ARGON2ID}"},
		{importArgon2I, "{ARGON2I}"},
		{importArgon2IDLanes, "{ARGON2ID}"},
	} {
		for _, input := range []string{tc.raw, tc.scheme + tc.raw} {
			normalized, err := NormalizeImportedPasswordHash(input)
			if err != nil || normalized != tc.scheme+tc.raw {
				t.Fatalf("normalize %q: %q, %v", input, normalized, err)
			}
			if err := CheckPassword("old-password", normalized); err != nil {
				t.Errorf("%s: correct password: %v", input, err)
			}
			for _, wrong := range []string{"", "wrong", "old-password\x00", "Old-password", strings.Repeat("x", 4097)} {
				if err := CheckPassword(wrong, normalized); !errors.Is(err, ErrInvalidCredentials) {
					t.Errorf("%s: wrong password %q: %v", input, wrong, err)
				}
			}
			if !PasswordNeedsRehash("old-password", normalized) || PasswordNeedsRehash(strings.Repeat("x", 73), normalized) {
				t.Errorf("%s: rehash must preserve bcrypt's 72-byte boundary", input)
			}
		}
	}
}

func TestImportedArgon2HashesRejectMalformedAndUnbounded(t *testing.T) {
	for _, hash := range []string{
		"{ARGON2I}" + importArgon2ID, "{ARGON2ID}" + importArgon2I, "{BLF-CRYPT}" + importArgon2ID,
		strings.Replace(importArgon2ID, "argon2id", "argon2d", 1),
		strings.Replace(importArgon2ID, "v=19", "v=16", 1),
		strings.Replace(importArgon2ID, "m=4096", "m=262145", 1),
		strings.Replace(importArgon2ID, "m=4096", "m=1048576", 1),
		strings.Replace(importArgon2ID, "t=2", "t=11", 1),
		strings.Replace(importArgon2ID, "t=2", "t=0", 1),
		strings.Replace(importArgon2ID, "p=1", "p=9", 1),
		strings.Replace(importArgon2ID, "p=1", "p=0", 1),
		strings.Replace(importArgon2IDLanes, "m=8192", "m=15", 1),
		strings.Replace(importArgon2ID, "aW1wb3J0c2FsdDAxMjM0NQ", "aW1wb3J0c2", 1), // a 7-byte salt
		importArgon2ID + "=", importArgon2ID[:len(importArgon2ID)-1] + "!",
		strings.Replace(importArgon2ID, "$v=19", "", 1),
	} {
		if _, err := NormalizeImportedPasswordHash(hash); err == nil {
			t.Errorf("accepted malformed or excessive Argon2 hash %q", hash)
		}
		if err := CheckPassword("old-password", hash); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("verified a refused Argon2 hash %q: %v", hash, err)
		}
	}
}

// At most two Argon2 verifications hold their memory at once: a third waits for
// a slot instead of allocating.
func TestArgon2VerificationsWaitForASlot(t *testing.T) {
	argon2Slots <- struct{}{}
	argon2Slots <- struct{}{}
	done := make(chan error, 1)
	go func() { done <- CheckPassword("old-password", "{ARGON2ID}"+importArgon2ID) }()
	select {
	case err := <-done:
		t.Fatalf("verified with every slot taken: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	<-argon2Slots
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("correct password after a slot freed: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("verification did not finish after a slot freed")
	}
	<-argon2Slots
}
