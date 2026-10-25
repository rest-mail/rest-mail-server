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

// Authentication methods a session's amr claim lists (RFC 8176): how the
// session signed in. AMRPassword is a password; AMROTP is a second factor, a
// TOTP code or a recovery code, on top of it.
const (
	AMRPassword = "pwd"
	AMROTP      = "otp"
)

// TokenIssuer is the `iss` claim stamped into every minted token and the value
// the parser pins on validation (#204 defense in depth). Beyond the HMAC-alg
// pin, the exp check, and the access/refresh token_type separation, requiring a
// matching issuer means a token minted for some other system that happens to
// share this secret cannot be replayed here.
const TokenIssuer = "restmail"

// Claims are the JWT claims of an account's session, in its access and
// refresh tokens. There is one kind of account (a mailbox), so one kind of
// token: what the session may do is read from the account's grants on every
// request (internal/grants), never from the token.
type Claims struct {
	jwt.RegisteredClaims
	// AccountID is the account (mailbox) the session is for; Address is its
	// address when the token was minted.
	AccountID uint   `json:"account_id"`
	Address   string `json:"address"`
	// AMR is how the session signed in (AMRPassword, AMROTP), carried from the
	// sign-in to every token refreshed from it.
	AMR []string `json:"amr,omitempty"`
	// TwoFactor marks a session whose sign-in took a second factor as well as
	// the password: AMR holds AMROTP. A refresh keeps it.
	TwoFactor bool `json:"mfa,omitempty"`
	// SetupPending marks a session whose account is not finished setting up (a
	// new password, a 2FA enrollment). It reaches only the self-service
	// endpoints that finish the setup; everything else refuses it.
	SetupPending bool `json:"setup,omitempty"`
	// CredentialEpoch is the account's credential version when the token was
	// minted; RejectStaleSessions refuses an outdated one.
	CredentialEpoch int `json:"cep,omitempty"`
	// SessionID is the session the token belongs to: minted at sign-in, in
	// both tokens, and carried unchanged by every refresh, which gives each
	// token a new jti. What lives as long as a session, such as a phone
	// registered for push, is keyed on it. Every token has one: ValidateToken
	// refuses a token without it.
	SessionID string `json:"sid,omitempty"`
	TokenType string `json:"token_type"`

	// CredentialID and CredentialKind are the credential a request was made
	// with over HTTP Basic: an app password, or the account's password through
	// a protocol gateway. Zero for a session. They are never part of a token:
	// only the API sets them, on claims it builds from a credential it has just
	// checked.
	CredentialID   uint   `json:"-"`
	CredentialKind string `json:"-"`
	// APIKeyPermissions are what a request made with an API key may do, on
	// top of its account's live grants: the request may use a capability at
	// a scope only where both allow it. Empty, it may do nothing. Set only
	// with CredentialKind CredentialKindAPIKey, never part of a token.
	APIKeyPermissions []ScopedPermission `json:"-"`
}

// IsAPIKey reports whether the request was made with an API key.
func (c *Claims) IsAPIKey() bool {
	return c.CredentialKind == CredentialKindAPIKey
}

// HasAMR reports whether the session signed in with method.
func (c *Claims) HasAMR(method string) bool {
	return hasMethod(c.AMR, method)
}

// MailboxOnly reports whether the request reaches only its own account's
// mailbox: one made with an app password, whose scope is that mailbox (#344).
// A session, or the account's own password, reaches whatever the account's
// grants reach.
func (c *Claims) MailboxOnly() bool {
	return c.CredentialKind == "app_password"
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
	// SessionID is the sid both tokens carry, for the ledger row. Not
	// serialized: the client has it inside its tokens.
	SessionID string `json:"-"`
}

// TokenOption adjusts the tokens a Generate method mints.
type TokenOption func(*tokenOptions)

type tokenOptions struct {
	sessionID string
	continued bool
}

// ContinueSession makes the tokens part of session sid, as a refresh does,
// instead of a new session.
func ContinueSession(sid string) TokenOption {
	return func(o *tokenOptions) { o.sessionID, o.continued = sid, true }
}

// sessionFor is the sid the tokens get: the one ContinueSession names, or a
// new one, as at sign-in. Continuing a session without a sid is an error: a
// sid is only ever minted at sign-in.
func sessionFor(opts []TokenOption) (string, error) {
	var o tokenOptions
	for _, opt := range opts {
		opt(&o)
	}
	if !o.continued {
		return newSessionID()
	}
	if o.sessionID == "" {
		return "", errors.New("cannot continue a session without a sid")
	}
	return o.sessionID, nil
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

// GenerateSession creates the tokens of an account's session at the account's
// current credential epoch, so that a later reset or password change ends it
// at once. amr is how it signed in. setupPending marks a session of an
// account still in setup, on both tokens, so neither the access token nor
// anything refreshed from it reaches more than finishing the setup.
//
// The refresh token carries a freshly minted jti (RegisteredClaims.ID) so the
// caller can persist a rotation/revocation ledger row for it. The access token
// carries one of its own, which the audit log records as the session. Both
// carry a new sid, or the one ContinueSession names.
func (s *JWTService) GenerateSession(accountID uint, address string, epoch int, amr []string, setupPending bool, opts ...TokenOption) (*TokenPair, error) {
	now := time.Now()
	sid, err := sessionFor(opts)
	if err != nil {
		return nil, err
	}
	claims := func(jti, tokenType string, expires time.Time) Claims {
		return Claims{
			RegisteredClaims: jwt.RegisteredClaims{
				ID:        jti,
				Subject:   fmt.Sprintf("account:%d", accountID),
				Issuer:    s.issuer,
				IssuedAt:  jwt.NewNumericDate(now),
				ExpiresAt: jwt.NewNumericDate(expires),
			},
			AccountID:       accountID,
			Address:         address,
			AMR:             amr,
			TwoFactor:       hasMethod(amr, AMROTP),
			SetupPending:    setupPending,
			CredentialEpoch: epoch,
			SessionID:       sid,
			TokenType:       tokenType,
		}
	}

	accessJTI, err := newJTI()
	if err != nil {
		return nil, err
	}
	accessStr, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims(accessJTI, "access", now.Add(s.accessExpiry))).SignedString(s.secret)
	if err != nil {
		return nil, fmt.Errorf("failed to sign access token: %w", err)
	}

	jti, err := newJTI()
	if err != nil {
		return nil, err
	}
	refreshExpiry := now.Add(s.refreshExpiry)
	refreshStr, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims(jti, "refresh", refreshExpiry)).SignedString(s.secret)
	if err != nil {
		return nil, fmt.Errorf("failed to sign refresh token: %w", err)
	}

	return &TokenPair{
		AccessToken:      accessStr,
		RefreshToken:     refreshStr,
		ExpiresIn:        int(s.accessExpiry.Seconds()),
		RefreshJTI:       jti,
		RefreshExpiresAt: refreshExpiry,
		SessionID:        sid,
	}, nil
}

func hasMethod(amr []string, method string) bool {
	for _, m := range amr {
		if m == method {
			return true
		}
	}
	return false
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

// newSessionID returns a new sid: 128 random bits, hex-encoded, like a jti. A
// sid is minted only at sign-in, so no two sessions share one.
func newSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate session id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// ValidateToken parses and validates a JWT token string. A token without a
// sid belongs to no session, and is invalid.
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
	if !ok || !token.Valid || claims.SessionID == "" {
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

// CheckPassword verifies bcrypt or an imported SHA-crypt or Argon2 password hash.
func CheckPassword(password, hash string) error {
	if isImportedHash(hash) {
		return checkImportedPassword(password, hash)
	}
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
