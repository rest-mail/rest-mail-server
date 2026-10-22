package auth

import "strings"

// API keys are random, named, revocable credentials an account gives an
// automation that calls the REST API (#341). Unlike an app password, which
// reaches its own mailbox the way a mail client does, an API key holds
// explicit permissions: capabilities, each at a scope, that it may use only
// while its account still holds them. One looks like
//
//	rmk_<public id>_<secret>
//
// in letters and digits, joined by underscores: 4 + 12 + 1 + 43 = 60
// characters. It is sent as Authorization: Bearer, never with HTTP Basic, so
// it is never mistaken for an app password ("rm_") or a session token. The
// secret is as long as an app password's, about 256 bits, so the server keeps
// a plain SHA-256 of it, the same as for an app password.
const (
	APIKeyPrefix = "rmk_"
	// APIKeyLength is the length of every API key.
	APIKeyLength = len(APIKeyPrefix) + appPasswordIDLength + 1 + appPasswordSecretLength
)

// CredentialKindAPIKey is the kind claims carry for a request made with an API
// key; models.CredentialKindAPIKey is the same.
const CredentialKindAPIKey = "api_key"

// ScopedPermission is one capability an API key may use, at one scope: "*",
// "domain:<name>" or "mailbox:<address>", as grants are scoped.
type ScopedPermission struct {
	Capability string `json:"capability"`
	Scope      string `json:"scope"`
}

// NewAPIKey makes an API key. It returns the key, to show its owner once; its
// public id; and the hash of its secret, which is all the server keeps.
func NewAPIKey() (key, publicID, secretHash string, err error) {
	publicID, err = randomBase62(appPasswordIDLength)
	if err != nil {
		return "", "", "", err
	}
	secret, err := randomBase62(appPasswordSecretLength)
	if err != nil {
		return "", "", "", err
	}
	return APIKeyPrefix + publicID + "_" + secret, publicID, HashAppPasswordSecret(secret), nil
}

// ParseAPIKey splits an API key into its public id and its secret. ok is false
// for anything that is not exactly in the format.
func ParseAPIKey(s string) (publicID, secret string, ok bool) {
	if len(s) != APIKeyLength || !strings.HasPrefix(s, APIKeyPrefix) {
		return "", "", false
	}
	rest := s[len(APIKeyPrefix):]
	if rest[appPasswordIDLength] != '_' {
		return "", "", false
	}
	publicID, secret = rest[:appPasswordIDLength], rest[appPasswordIDLength+1:]
	if !isBase62(publicID) || !isBase62(secret) {
		return "", "", false
	}
	return publicID, secret, true
}

// LooksLikeAPIKey reports whether a bearer token is meant as an API key: it
// starts with APIKeyPrefix. A token that does but is malformed is still an
// API key, and refused as one; it is never tried as a session token.
func LooksLikeAPIKey(token string) bool {
	return strings.HasPrefix(token, APIKeyPrefix)
}

// APIKeySecretMatches reports, in constant time, whether secret hashes to
// hash; an empty hash (no key found) costs the same and never matches.
func APIKeySecretMatches(secret, hash string) bool {
	return AppPasswordSecretMatches(secret, hash)
}
