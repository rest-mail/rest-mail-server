package auth

import (
	"testing"
	"time"
)

func TestMachineTokenLifetimeAndNoTwoFactor(t *testing.T) {
	s := NewJWTService("test-secret", time.Hour, 24*time.Hour)
	token, err := s.GenerateMachineToken(12, "robot@example.test", 34)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.ValidateAccessToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if c.AccountID != 12 || c.MachineCredentialID != 34 || c.TwoFactor || len(c.AMR) != 0 {
		t.Fatalf("machine claims: %+v", c)
	}
	if got := c.ExpiresAt.Sub(c.IssuedAt.Time); got != 300*time.Second {
		t.Fatalf("lifetime %v", got)
	}
	if _, err := s.ValidateRefreshToken(token); err == nil {
		t.Fatal("access token accepted as refresh")
	}
}

func TestNewClientSecret(t *testing.T) {
	id, secret, hash, err := NewClientSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 12 || len(secret) != 43 || !AppPasswordSecretMatches(secret, hash) || AppPasswordSecretMatches("wrong", hash) {
		t.Fatal("bad client secret")
	}
}
