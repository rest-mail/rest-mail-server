package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const CredentialKindClientSecret = "client_secret"
const MachineTokenLifetime = 300 * time.Second

// NewClientSecret returns the public client id, one-time secret and its hash.
func NewClientSecret() (publicID, secret, secretHash string, err error) {
	publicID, err = randomBase62(appPasswordIDLength)
	if err != nil {
		return
	}
	secret, err = randomBase62(appPasswordSecretLength)
	if err != nil {
		return
	}
	secretHash = HashAppPasswordSecret(secret)
	return
}

// GenerateMachineToken issues only an access token, with no human authentication
// methods, two-factor flag, refresh token or saved permissions.
func (s *JWTService) GenerateMachineToken(accountID uint, address string, credentialID uint) (string, error) {
	if accountID == 0 || credentialID == 0 {
		return "", ErrInvalidCredentials
	}
	now := time.Now()
	sid, err := newSessionID()
	if err != nil {
		return "", err
	}
	jti, err := newJTI()
	if err != nil {
		return "", err
	}
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{ID: jti, Subject: fmt.Sprintf("account:%d", accountID), Issuer: s.issuer, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(MachineTokenLifetime))},
		AccountID:        accountID, Address: address, SessionID: sid, TokenType: "access", MachineCredentialID: credentialID,
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
}

// ClientSecretPermissionAllowed is the client-secret tier. Unknown capabilities
// fail closed. Account/grant/credential management, evidence and server controls
// cannot be delegated through this kind, even with an administrative account.
func ClientSecretPermissionAllowed(p ScopedPermission) bool {
	switch p.Capability {
	case "mail:read", "mail:write", "mail:send", "mail:receive":
		return true
	case "domains:read", "mailboxes:read", "pipelines:read", "pipelines:write", "pipelines:delete":
		return len(p.Scope) > len("domain:") && p.Scope[:len("domain:")] == "domain:"
	}
	return false
}
