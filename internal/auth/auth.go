package auth

import (
	"crypto/rand"
	"encoding/hex"
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

// Cookie and header names for the browser (SPA) session transport. The access
// token is delivered ONLY as the httpOnly AccessCookieName cookie — never in a
// response body — so page JavaScript (and thus any XSS) cannot read or
// exfiltrate it. CSRFCookieName is its non-httpOnly companion for the
// double-submit CSRF defence: the SPA reads it and echoes the value in
// CSRFHeaderName on every state-changing request. RefreshCookieName carries the
// rotation/revocation refresh token, scoped to the auth endpoints.
const (
	AccessCookieName  = "restmail_access"
	RefreshCookieName = "restmail_refresh"
	CSRFCookieName    = "restmail_csrf"
	CSRFHeaderName    = "X-CSRF-Token"
)

// TokenIssuer is the `iss` claim stamped into every minted token and the value
// the parser pins on validation (#204 defense in depth). Beyond the HMAC-alg
// pin, the exp check, and the access/refresh token_type separation, requiring a
// matching issuer means a token minted for some other system that happens to
// share this secret cannot be replayed here.
const TokenIssuer = "restmail"

// Claims represents the JWT claims for access and refresh tokens.
type Claims struct {
	jwt.RegisteredClaims
	Email            string   `json:"email,omitempty"`              // For mailbox users
	WebmailAccountID uint     `json:"webmail_account_id,omitempty"` // For mailbox users
	MailboxID        uint     `json:"mailbox_id,omitempty"`         // For mailbox users
	UserType         string   `json:"user_type"`                    // "mailbox" or "admin"
	AdminUserID      uint     `json:"admin_user_id,omitempty"`      // For admin users
	Username         string   `json:"username,omitempty"`           // For admin users
	Capabilities     []string `json:"capabilities,omitempty"`       // For admin users
	TokenType        string   `json:"token_type"`
}

// TokenPair contains both access and refresh tokens.
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"` // seconds until access token expires

	// RefreshJTI is the refresh token's JWT ID (jti) claim, surfaced so the
	// caller can persist a revocation/rotation ledger row keyed by it. Not
	// serialized to clients (the jti lives inside the signed refresh token).
	RefreshJTI string `json:"-"`
	// RefreshExpiresAt is the refresh token's absolute expiry, surfaced so the
	// ledger row can carry the same horizon for pruning. Not serialized.
	RefreshExpiresAt time.Time `json:"-"`
}

// JWTService handles JWT token creation and validation.
type JWTService struct {
	secret        []byte
	accessExpiry  time.Duration
	refreshExpiry time.Duration
	// issuer is the `iss` claim minted into every token and pinned on
	// validation. It defaults to TokenIssuer.
	issuer string
}

// NewJWTService creates a new JWT service.
func NewJWTService(secret string, accessExpiry, refreshExpiry time.Duration) *JWTService {
	return &JWTService{
		secret:        []byte(secret),
		accessExpiry:  accessExpiry,
		refreshExpiry: refreshExpiry,
		issuer:        TokenIssuer,
	}
}

// GenerateTokenPair creates both access and refresh tokens for a mailbox user.
// The refresh token carries a freshly minted jti (RegisteredClaims.ID) so the
// caller can persist a rotation/revocation ledger row for it.
func (s *JWTService) GenerateTokenPair(mailboxID uint, email string, webmailAccountID uint) (*TokenPair, error) {
	now := time.Now()

	// Access token
	accessClaims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   fmt.Sprintf("mailbox:%d", mailboxID),
			Issuer:    s.issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.accessExpiry)),
		},
		Email:            email,
		WebmailAccountID: webmailAccountID,
		MailboxID:        mailboxID,
		UserType:         "mailbox",
		TokenType:        "access",
	}

	accessToken := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessStr, err := accessToken.SignedString(s.secret)
	if err != nil {
		return nil, fmt.Errorf("failed to sign access token: %w", err)
	}

	// Refresh token
	jti, err := newJTI()
	if err != nil {
		return nil, err
	}
	refreshExpiry := now.Add(s.refreshExpiry)
	refreshClaims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			Subject:   fmt.Sprintf("mailbox:%d", mailboxID),
			Issuer:    s.issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(refreshExpiry),
		},
		Email:            email,
		WebmailAccountID: webmailAccountID,
		MailboxID:        mailboxID,
		UserType:         "mailbox",
		TokenType:        "refresh",
	}

	refreshToken := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims)
	refreshStr, err := refreshToken.SignedString(s.secret)
	if err != nil {
		return nil, fmt.Errorf("failed to sign refresh token: %w", err)
	}

	return &TokenPair{
		AccessToken:      accessStr,
		RefreshToken:     refreshStr,
		ExpiresIn:        int(s.accessExpiry.Seconds()),
		RefreshJTI:       jti,
		RefreshExpiresAt: refreshExpiry,
	}, nil
}

// GenerateAdminTokenPair creates both access and refresh tokens for an admin user.
func (s *JWTService) GenerateAdminTokenPair(adminUserID uint, username string, capabilities []string) (*TokenPair, error) {
	now := time.Now()

	// Access token
	accessClaims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   fmt.Sprintf("admin:%d", adminUserID),
			Issuer:    s.issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.accessExpiry)),
		},
		UserType:     "admin",
		AdminUserID:  adminUserID,
		Username:     username,
		Capabilities: capabilities,
		TokenType:    "access",
	}

	accessToken := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessStr, err := accessToken.SignedString(s.secret)
	if err != nil {
		return nil, fmt.Errorf("failed to sign access token: %w", err)
	}

	// Refresh token
	jti, err := newJTI()
	if err != nil {
		return nil, err
	}
	refreshExpiry := now.Add(s.refreshExpiry)
	refreshClaims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			Subject:   fmt.Sprintf("admin:%d", adminUserID),
			Issuer:    s.issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(refreshExpiry),
		},
		UserType:     "admin",
		AdminUserID:  adminUserID,
		Username:     username,
		Capabilities: capabilities,
		TokenType:    "refresh",
	}

	refreshToken := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims)
	refreshStr, err := refreshToken.SignedString(s.secret)
	if err != nil {
		return nil, fmt.Errorf("failed to sign refresh token: %w", err)
	}

	return &TokenPair{
		AccessToken:      accessStr,
		RefreshToken:     refreshStr,
		ExpiresIn:        int(s.accessExpiry.Seconds()),
		RefreshJTI:       jti,
		RefreshExpiresAt: refreshExpiry,
	}, nil
}

// newJTI returns a cryptographically random 128-bit token id, hex-encoded, used
// as a refresh token's jti claim and the ledger key that rotation/revocation
// look it up by.
func newJTI() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate token id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// ValidateToken parses and validates a JWT token string.
func (s *JWTService) ValidateToken(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return s.secret, nil
	},
		// Pin the issuer (#204): a token whose `iss` is wrong or absent is
		// rejected, in addition to the existing signing-method, expiry, and
		// token_type checks. WithIssuer treats a missing/empty iss as a failure.
		jwt.WithIssuer(s.issuer),
	)
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

// DummyPasswordHash is a valid {BLF-CRYPT} bcrypt hash (cost 10, matching
// HashPassword) of a fixed throwaway password. The login path runs CheckPassword
// against it when no account matches, so an unknown user costs the same bcrypt
// comparison as a wrong password — a missing account and a wrong password become
// indistinguishable by timing (OSI-24 user-enumeration defense-in-depth). It is
// never a valid credential: no real password hashes to it, and it is only ever
// used as the comparison target for the not-found branch.
const DummyPasswordHash = "{BLF-CRYPT}$2a$10$aA7IA.HJ0RwvnriddDdy0OX/T7cjApOOFFehVikg6m7FO01jW4iCu"

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
