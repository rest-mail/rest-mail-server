package bootstrap

import (
	"errors"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/auth"
	"golang.org/x/crypto/bcrypt"
)

const installerPassword = "kX9-mQ2_rT7vL4wN8pZ1aB3cD5eF7gH9"

func hashOf(t *testing.T, password string, cost int) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), cost)
	if err != nil {
		t.Fatal(err)
	}
	return string(h)
}

func TestBootstrapFromConfig(t *testing.T) {
	hash := hashOf(t, installerPassword, 10)

	if c, err := BootstrapFromConfig("", ""); err != nil || !c.IsZero() {
		t.Errorf("neither: %+v %v, want nothing", c, err)
	}
	if _, err := BootstrapFromConfig(installerPassword, hash); !errors.Is(err, ErrBothBootstrapCredentials) {
		t.Errorf("both: err = %v, want ErrBothBootstrapCredentials", err)
	}
	// The server's own form, and bare bcrypt, with or without stray whitespace.
	for _, given := range []string{hash, "{BLF-CRYPT}" + hash, " " + hash + "\n"} {
		c, err := BootstrapFromConfig("", given)
		if err != nil {
			t.Fatalf("%q: %v", given, err)
		}
		stored, _ := c.stored()
		if stored != "{BLF-CRYPT}"+hash {
			t.Errorf("%q is stored as %q", given, stored)
		}
		// What the account holds signs in with the password, as any account's does.
		if auth.CheckPassword(installerPassword, stored) != nil {
			t.Errorf("%q: the password does not sign in", given)
		}
	}
}

func TestBootstrapPasswordHash_RefusesWhatIsNotOne(t *testing.T) {
	for name, given := range map[string]string{
		"the password itself":  installerPassword,
		"another kind of hash": "$6$salt$IxDD3jeSOb5eB1CX5LBsqZFVkJdido3OUILO5Ifz5iwMuTS4XMS130MTSuDDl3aCI6WouIL9AjRbLCelDCy.g.",
		"a weaker bcrypt cost": hashOf(t, installerPassword, 4),
		"nothing but a prefix": "{BLF-CRYPT}",
	} {
		if _, err := BootstrapPasswordHash(given); err == nil {
			t.Errorf("%s was taken for a bcrypt hash", name)
		}
	}
}

// A restart giving the same credential changes nothing; a different one is a
// new password. For a hash, that is a different hash.
func TestBootstrapCredential_Matches(t *testing.T) {
	hash := hashOf(t, installerPassword, 10)
	c, _ := BootstrapPasswordHash(hash)
	if !c.matches("{BLF-CRYPT}"+hash) || !c.matches(hash) {
		t.Error("the same hash does not match itself")
	}
	if c.matches("{BLF-CRYPT}" + hashOf(t, installerPassword, 10)) {
		t.Error("another hash of the same password matched: a new deployment would not re-arm")
	}
	p := BootstrapPassword(installerPassword)
	if !p.matches("{BLF-CRYPT}" + hash) {
		t.Error("the password does not match its own hash")
	}
}

// A hash cannot say how long its password was; a password can.
func TestBootstrapCredential_Validate(t *testing.T) {
	if err := BootstrapPassword("short").validate(); err == nil || !strings.Contains(err.Error(), "at least") {
		t.Errorf("a short password: %v", err)
	}
	c, _ := BootstrapPasswordHash(hashOf(t, "short", 10))
	if err := c.validate(); err != nil {
		t.Errorf("a hash: %v", err)
	}
}
