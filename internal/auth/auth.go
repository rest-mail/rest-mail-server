package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrTokenExpired       = errors.New("token expired")
	ErrInvalidToken       = errors.New("invalid token")
	ErrWrongTokenType     = errors.New("wrong token type")
)

// Claims represents the JWT claims for access and refresh tokens.
type Claims struct {
	jwt.RegisteredClaims
	Email            string `json:"email"`
	WebmailAccountID uint   `json:"webmail_account_id"`
	MailboxID        uint   `json:"mailbox_id"`
	IsAdmin          bool   `json:"is_admin,omitempty"`
	TokenType        string `json:"token_type"`
}

// TokenPair contains both access and refresh tokens.
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"` // seconds until access token expires
}

// JWTService handles JWT token creation and validation.
type JWTService struct {
	secret       []byte
	accessExpiry time.Duration
	refreshExpiry time.Duration
}

// NewJWTService creates a new JWT service.
func NewJWTService(secret string, accessExpiry, refreshExpiry time.Duration) *JWTService {
	return &JWTService{
		secret:        []byte(secret),
		accessExpiry:  accessExpiry,
		refreshExpiry: refreshExpiry,
	}
}

// GenerateTokenPair creates both access and refresh tokens for a user.
func (s *JWTService) GenerateTokenPair(mailboxID uint, email string, webmailAccountID uint, isAdmin bool) (*TokenPair, error) {
	now := time.Now()

	// Access token
	accessClaims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   fmt.Sprintf("%d", mailboxID),
			Issuer:    "restmail",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.accessExpiry)),
		},
		Email:            email,
		WebmailAccountID: webmailAccountID,
		MailboxID:        mailboxID,
		IsAdmin:          isAdmin,
		TokenType:        "access",
	}

	accessToken := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessStr, err := accessToken.SignedString(s.secret)
	if err != nil {
		return nil, fmt.Errorf("failed to sign access token: %w", err)
	}

	// Refresh token
	refreshClaims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   fmt.Sprintf("%d", mailboxID),
			Issuer:    "restmail",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.refreshExpiry)),
		},
		Email:            email,
		WebmailAccountID: webmailAccountID,
		MailboxID:        mailboxID,
		IsAdmin:          isAdmin,
		TokenType:        "refresh",
	}

	refreshToken := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims)
	refreshStr, err := refreshToken.SignedString(s.secret)
	if err != nil {
		return nil, fmt.Errorf("failed to sign refresh token: %w", err)
	}

	return &TokenPair{
		AccessToken:  accessStr,
		RefreshToken: refreshStr,
		ExpiresIn:    int(s.accessExpiry.Seconds()),
	}, nil
}

// ValidateToken parses and validates a JWT token string.
func (s *JWTService) ValidateToken(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return s.secret, nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, ErrInvalidToken
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, ErrInvalidToken
	}

	return claims, nil
}

// ValidateAccessToken parses a JWT and verifies it is an access token.
func (s *JWTService) ValidateAccessToken(tokenStr string) (*Claims, error) {
	claims, err := s.ValidateToken(tokenStr)
	if err != nil {
		return nil, err
	}
	if claims.TokenType != "access" {
		return nil, ErrWrongTokenType
	}
	return claims, nil
}

// ValidateRefreshToken parses a JWT and verifies it is a refresh token.
func (s *JWTService) ValidateRefreshToken(tokenStr string) (*Claims, error) {
	claims, err := s.ValidateToken(tokenStr)
	if err != nil {
		return nil, err
	}
	if claims.TokenType != "refresh" {
		return nil, ErrWrongTokenType
	}
	return claims, nil
}

// HashPassword hashes a password using bcrypt with cost 10, compatible with Dovecot's {BLF-CRYPT}.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 10)
	if err != nil {
		return "", fmt.Errorf("failed to hash password: %w", err)
	}
	// Prefix with {BLF-CRYPT} for Dovecot compatibility
	return "{BLF-CRYPT}" + string(hash), nil
}

// CheckPassword verifies a password against a {BLF-CRYPT} bcrypt hash.
func CheckPassword(password, hash string) error {
	// Strip the {BLF-CRYPT} prefix if present
	bcryptHash := hash
	if len(hash) > 11 && hash[:11] == "{BLF-CRYPT}" {
		bcryptHash = hash[11:]
	}
	err := bcrypt.CompareHashAndPassword([]byte(bcryptHash), []byte(password))
	if err != nil {
		return ErrInvalidCredentials
	}
	return nil
}
