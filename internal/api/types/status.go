// Package apitypes holds wire types shared by API registrations and their clients.
package apitypes

import "time"

// ServerDescription is the public body of GET /api/v1/server.
type ServerDescription struct {
	Product string `json:"product" enums:"rest-mail"`
	Version string `json:"version" description:"e.g. 0.7.0; dev for a build with none stamped in"`
}

// OwnerChallenge is a challenge for an owner or status key to sign.
type OwnerChallenge struct {
	Challenge string    `json:"challenge"`
	ExpiresAt time.Time `json:"expires_at"`
}

// OwnerStatusRequest proves possession of a status or owner key. The endpoint
// supplies the purpose, "status", which is part of the signed message.
type OwnerStatusRequest struct {
	Challenge string `json:"challenge,omitempty"`
	PublicKey string `json:"public_key,omitempty"`
	Signature string `json:"signature,omitempty"`
}

// ServerStatus is what a server runs, and since when. No secrets or configuration.
type ServerStatus struct {
	Version   string    `json:"version"`
	Commit    string    `json:"commit"`
	BuiltAt   string    `json:"built_at"`
	StartedAt time.Time `json:"started_at"`
}
