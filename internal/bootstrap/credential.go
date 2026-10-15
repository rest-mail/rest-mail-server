package bootstrap

import (
	"errors"
	"fmt"
	"strings"

	"github.com/restmail/restmail/internal/auth"
	"golang.org/x/crypto/bcrypt"
)

// BootstrapCredential is what a deployment gives its first admin to sign in
// with: the password itself, or a bcrypt hash of it.
//
// A hash lets the deploy tool hold it as ordinary config, committed with the
// rest of a Pulumi stack and readable by anyone who can read that, without
// anyone being able to sign in with it: the password is made by the installer,
// 32 random characters, shown once to whoever installs. The server never needs
// the password itself, only something to check a sign-in against, and a hash
// is that.
type BootstrapCredential struct {
	password string
	hash     string // as stored: {BLF-CRYPT} and the bcrypt hash
}

// minBootstrapHashCost is the weakest bcrypt cost accepted: the cost the
// server hashes passwords with itself.
const minBootstrapHashCost = 10

const blfCrypt = "{BLF-CRYPT}"

// ErrBothBootstrapCredentials: a deployment set the password and its hash.
var ErrBothBootstrapCredentials = errors.New("set the bootstrap admin password or its hash, not both")

// BootstrapFromConfig is the credential a deployment set: the password, or its
// bcrypt hash ({BLF-CRYPT} prefix optional), or neither.
func BootstrapFromConfig(password, hash string) (BootstrapCredential, error) {
	hash = strings.TrimSpace(hash)
	switch {
	case password != "" && hash != "":
		return BootstrapCredential{}, ErrBothBootstrapCredentials
	case hash != "":
		return BootstrapPasswordHash(hash)
	default:
		return BootstrapPassword(password), nil
	}
}

// BootstrapPassword is a credential given as the password itself.
func BootstrapPassword(password string) BootstrapCredential {
	return BootstrapCredential{password: password}
}

// BootstrapPasswordHash is a credential given as a bcrypt hash of the
// password, refused unless it is one, at the server's own cost or more.
func BootstrapPasswordHash(hash string) (BootstrapCredential, error) {
	raw := strings.TrimPrefix(strings.TrimSpace(hash), blfCrypt)
	cost, err := bcrypt.Cost([]byte(raw))
	if err != nil {
		return BootstrapCredential{}, fmt.Errorf("the bootstrap admin password hash is not a bcrypt hash: %w", err)
	}
	if cost < minBootstrapHashCost {
		return BootstrapCredential{}, fmt.Errorf("the bootstrap admin password hash has bcrypt cost %d; it must be at least %d", cost, minBootstrapHashCost)
	}
	return BootstrapCredential{hash: blfCrypt + raw}, nil
}

// IsZero says the deployment gave no credential at all.
func (c BootstrapCredential) IsZero() bool { return c.password == "" && c.hash == "" }

// validate refuses a password too short to be the installer's. A hash was
// checked when it was read, and how long its password is cannot be told.
func (c BootstrapCredential) validate() error {
	if c.hash == "" && len(c.password) < MinAdminPasswordLength {
		return fmt.Errorf("bootstrap admin password is %d bytes; it must be at least %d", len(c.password), MinAdminPasswordLength)
	}
	return nil
}

// stored is what goes into the account's password_hash.
func (c BootstrapCredential) stored() (string, error) {
	if c.hash != "" {
		return c.hash, nil
	}
	return auth.HashPassword(c.password)
}

// matches says the account already holds this credential, so a restart that
// gives it again changes nothing.
func (c BootstrapCredential) matches(stored string) bool {
	if c.hash != "" {
		return strings.TrimPrefix(stored, blfCrypt) == strings.TrimPrefix(c.hash, blfCrypt)
	}
	return auth.CheckPassword(c.password, stored) == nil
}
