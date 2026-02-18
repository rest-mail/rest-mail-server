package auth

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

const (
	testSecret    = "test-secret-key-for-unit-tests"
	testEmail     = "alice@mail1.test"
	testMailboxID = uint(42)
	testAccountID = uint(7)
)

func newTestService() *JWTService {
	return NewJWTService(testSecret, 1*time.Second, 10*time.Second)
}

// ---------- JWT token tests ----------

func TestGenerateTokenPair(t *testing.T) {
	svc := newTestService()

	pair, err := svc.GenerateTokenPair(testMailboxID, testEmail, testAccountID, false)
	if err != nil {
		t.Fatalf("GenerateTokenPair() unexpected error: %v", err)
	}

	if pair.AccessToken == "" {
		t.Error("AccessToken is empty")
	}
	if pair.RefreshToken == "" {
		t.Error("RefreshToken is empty")
	}
	if pair.AccessToken == pair.RefreshToken {
		t.Error("AccessToken and RefreshToken should differ (different expiries)")
	}
	if pair.ExpiresIn != 1 {
		t.Errorf("ExpiresIn = %d; want 1", pair.ExpiresIn)
	}

	// Validate the access token and check claims.
	claims, err := svc.ValidateToken(pair.AccessToken)
	if err != nil {
		t.Fatalf("ValidateToken(access) unexpected error: %v", err)
	}
	if claims.Email != testEmail {
		t.Errorf("Email = %q; want %q", claims.Email, testEmail)
	}
	if claims.MailboxID != testMailboxID {
		t.Errorf("MailboxID = %d; want %d", claims.MailboxID, testMailboxID)
	}
	if claims.WebmailAccountID != testAccountID {
		t.Errorf("WebmailAccountID = %d; want %d", claims.WebmailAccountID, testAccountID)
	}
	if claims.Issuer != "restmail" {
		t.Errorf("Issuer = %q; want %q", claims.Issuer, "restmail")
	}
	wantSubject := fmt.Sprintf("%d", testMailboxID)
	if claims.Subject != wantSubject {
		t.Errorf("Subject = %q; want %q", claims.Subject, wantSubject)
	}
	if claims.TokenType != "access" {
		t.Errorf("TokenType = %q; want %q", claims.TokenType, "access")
	}

	// Validate the refresh token has correct type.
	refreshClaims, err := svc.ValidateToken(pair.RefreshToken)
	if err != nil {
		t.Fatalf("ValidateToken(refresh) unexpected error: %v", err)
	}
	if refreshClaims.TokenType != "refresh" {
		t.Errorf("RefreshToken TokenType = %q; want %q", refreshClaims.TokenType, "refresh")
	}
}

func TestValidateToken(t *testing.T) {
	svc := newTestService()

	pair, err := svc.GenerateTokenPair(testMailboxID, testEmail, testAccountID, false)
	if err != nil {
		t.Fatalf("GenerateTokenPair() unexpected error: %v", err)
	}

	// Both tokens should be valid.
	for _, tc := range []struct {
		name  string
		token string
	}{
		{"access_token", pair.AccessToken},
		{"refresh_token", pair.RefreshToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims, err := svc.ValidateToken(tc.token)
			if err != nil {
				t.Fatalf("ValidateToken() unexpected error: %v", err)
			}
			if claims.Email != testEmail {
				t.Errorf("Email = %q; want %q", claims.Email, testEmail)
			}
			if claims.MailboxID != testMailboxID {
				t.Errorf("MailboxID = %d; want %d", claims.MailboxID, testMailboxID)
			}
			if claims.WebmailAccountID != testAccountID {
				t.Errorf("WebmailAccountID = %d; want %d", claims.WebmailAccountID, testAccountID)
			}
		})
	}
}

func TestValidateAccessToken(t *testing.T) {
	svc := newTestService()
	pair, err := svc.GenerateTokenPair(testMailboxID, testEmail, testAccountID, false)
	if err != nil {
		t.Fatalf("GenerateTokenPair() unexpected error: %v", err)
	}

	// Access token should pass ValidateAccessToken.
	claims, err := svc.ValidateAccessToken(pair.AccessToken)
	if err != nil {
		t.Fatalf("ValidateAccessToken(access) unexpected error: %v", err)
	}
	if claims.TokenType != "access" {
		t.Errorf("TokenType = %q; want %q", claims.TokenType, "access")
	}

	// Refresh token should be rejected by ValidateAccessToken.
	_, err = svc.ValidateAccessToken(pair.RefreshToken)
	if !errors.Is(err, ErrWrongTokenType) {
		t.Errorf("ValidateAccessToken(refresh) error = %v; want %v", err, ErrWrongTokenType)
	}
}

func TestValidateRefreshToken(t *testing.T) {
	svc := newTestService()
	pair, err := svc.GenerateTokenPair(testMailboxID, testEmail, testAccountID, false)
	if err != nil {
		t.Fatalf("GenerateTokenPair() unexpected error: %v", err)
	}

	// Refresh token should pass ValidateRefreshToken.
	claims, err := svc.ValidateRefreshToken(pair.RefreshToken)
	if err != nil {
		t.Fatalf("ValidateRefreshToken(refresh) unexpected error: %v", err)
	}
	if claims.TokenType != "refresh" {
		t.Errorf("TokenType = %q; want %q", claims.TokenType, "refresh")
	}

	// Access token should be rejected by ValidateRefreshToken.
	_, err = svc.ValidateRefreshToken(pair.AccessToken)
	if !errors.Is(err, ErrWrongTokenType) {
		t.Errorf("ValidateRefreshToken(access) error = %v; want %v", err, ErrWrongTokenType)
	}
}

func TestValidateToken_Expired(t *testing.T) {
	// Use negative durations so the token is already expired at creation time.
	svc := NewJWTService(testSecret, -1*time.Second, -1*time.Second)

	pair, err := svc.GenerateTokenPair(testMailboxID, testEmail, testAccountID, false)
	if err != nil {
		t.Fatalf("GenerateTokenPair() unexpected error: %v", err)
	}

	_, err = svc.ValidateToken(pair.AccessToken)
	if !errors.Is(err, ErrTokenExpired) {
		t.Errorf("ValidateToken(expired) error = %v; want %v", err, ErrTokenExpired)
	}
}

func TestValidateToken_WrongSecret(t *testing.T) {
	svc := newTestService()

	pair, err := svc.GenerateTokenPair(testMailboxID, testEmail, testAccountID, false)
	if err != nil {
		t.Fatalf("GenerateTokenPair() unexpected error: %v", err)
	}

	wrongSvc := NewJWTService("wrong-secret", 1*time.Second, 10*time.Second)
	_, err = wrongSvc.ValidateToken(pair.AccessToken)
	if !errors.Is(err, ErrInvalidToken) {
		t.Errorf("ValidateToken(wrong secret) error = %v; want %v", err, ErrInvalidToken)
	}
}

func TestValidateToken_Malformed(t *testing.T) {
	svc := newTestService()

	malformed := []string{
		"",
		"not-a-jwt",
		"abc.def.ghi",
		"eyJhbGciOiJIUzI1NiJ9.garbage.garbage",
	}
	for _, tok := range malformed {
		_, err := svc.ValidateToken(tok)
		if !errors.Is(err, ErrInvalidToken) {
			t.Errorf("ValidateToken(%q) error = %v; want %v", tok, err, ErrInvalidToken)
		}
	}
}

// ---------- Password hashing tests ----------

func TestHashPassword(t *testing.T) {
	hash, err := HashPassword("s3cret!")
	if err != nil {
		t.Fatalf("HashPassword() unexpected error: %v", err)
	}
	if !strings.HasPrefix(hash, "{BLF-CRYPT}") {
		t.Errorf("hash %q does not start with {BLF-CRYPT}", hash)
	}
	// The bcrypt portion (after the prefix) should start with "$2a$" or "$2b$".
	bcryptPart := hash[11:]
	if !strings.HasPrefix(bcryptPart, "$2a$") && !strings.HasPrefix(bcryptPart, "$2b$") {
		t.Errorf("bcrypt portion %q does not look like a bcrypt hash", bcryptPart)
	}
}

func TestCheckPassword(t *testing.T) {
	password := "correcthorsebatterystaple"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() unexpected error: %v", err)
	}

	if err := CheckPassword(password, hash); err != nil {
		t.Errorf("CheckPassword(correct) unexpected error: %v", err)
	}
}

func TestCheckPassword_Wrong(t *testing.T) {
	password := "correcthorsebatterystaple"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() unexpected error: %v", err)
	}

	err = CheckPassword("wrong-password", hash)
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("CheckPassword(wrong) error = %v; want %v", err, ErrInvalidCredentials)
	}
}

func TestCheckPassword_NoPrefixHash(t *testing.T) {
	password := "plainbcrypt"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() unexpected error: %v", err)
	}

	// Strip the {BLF-CRYPT} prefix to simulate a raw bcrypt hash.
	rawHash := strings.TrimPrefix(hash, "{BLF-CRYPT}")

	if err := CheckPassword(password, rawHash); err != nil {
		t.Errorf("CheckPassword(raw bcrypt) unexpected error: %v", err)
	}
}
