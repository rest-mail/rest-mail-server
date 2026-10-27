package handlers

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/bootstrap"
	"golang.org/x/crypto/ssh"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The owner routes spend a signed challenge, and mint a reset token, with the
// request's context, and a failure to record the spent challenge is the
// server's, not an invalid challenge (#1162, area #633). An owner told
// "invalid_challenge" signs again, and gets the same answer for as long as
// the database is down.

type ownerContextKey struct{}

type ownerDB struct {
	serviceProbeTestDB
	ownerKey   string
	insertFail error
	mu         sync.Mutex
	seen, lost []string
}

func (d *ownerDB) Connect(context.Context) (driver.Conn, error) { return d, nil }
func (d *ownerDB) Driver() driver.Driver                        { return d }
func (d *ownerDB) Open(string) (driver.Conn, error)             { return d, nil }

func (d *ownerDB) observe(ctx context.Context, q string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen = append(d.seen, q)
	if _, ok := ctx.Deadline(); !ok || ctx.Value(ownerContextKey{}) != true {
		d.lost = append(d.lost, q)
	}
}

func (d *ownerDB) QueryContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	d.observe(ctx, q)
	switch {
	case strings.HasPrefix(q, "INSERT") && d.insertFail != nil:
		return nil, d.insertFail
	case strings.HasPrefix(q, "INSERT"):
		return &deliveryReadRows{columns: []string{"id"}, values: []driver.Value{int64(1)}}, nil
	case strings.Contains(q, `"owner_keys"`) && d.ownerKey != "":
		return &deliveryReadRows{columns: []string{"id", "public_key"}, values: []driver.Value{int64(1), d.ownerKey}}, nil
	}
	return &deliveryReadRows{}, nil
}

func (d *ownerDB) ExecContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	d.observe(ctx, q)
	return driver.RowsAffected(0), nil
}

func (d *ownerDB) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	d.observe(ctx, "BEGIN")
	return ownerTx{}, nil
}

type ownerTx struct{}

func (ownerTx) Commit() error   { return nil }
func (ownerTx) Rollback() error { return nil }

const ownerTestSecret = "owner-challenge-secret"

func ownerRouter(t *testing.T, d *ownerDB, statusKeys string) http.Handler {
	t.Helper()
	pool := sql.OpenDB(d)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}),
		&gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := bootstrap.ParseStatusKeys(statusKeys)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	noop := endpoint.Middleware{Wrap: func(n http.Handler) http.Handler { return n }}
	NewOwnerHandler(db, keys, ownerTestSecret).Register(endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()},
		noop, func(string, ...middleware.AuditOption) endpoint.Middleware { return noop })
	return router
}

func ownerSigner(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
}

// ownerPost signs a fresh challenge's message and posts it to path.
func ownerPost(t *testing.T, router http.Handler, path string, signer ssh.Signer, publicKey string,
	message func(challenge string) []byte, extra map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	challenge, _, err := bootstrap.NewChallenge(bootstrap.ChallengeKey(ownerTestSecret), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	sig, err := signer.Sign(rand.Reader, message(challenge))
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"challenge": challenge, "public_key": publicKey,
		"signature": base64.StdEncoding.EncodeToString(ssh.Marshal(sig))}
	for k, v := range extra {
		body[k] = v
	}
	encoded, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), ownerContextKey{}, true), 4*time.Second)
	t.Cleanup(cancel)
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, r)
	return rr
}

func TestOwnerRoutesSpendTheChallengeWithTheRequestContext(t *testing.T) {
	signer, publicKey := ownerSigner(t)

	status := &ownerDB{}
	rr := ownerPost(t, ownerRouter(t, status, publicKey), "/api/v1/auth/owner/status", signer, publicKey,
		bootstrap.StatusMessage, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: %d %s, want 200", rr.Code, rr.Body.String())
	}
	if len(status.lost) > 0 {
		t.Errorf("status: %d of %d statements ran without the request's context, first %q",
			len(status.lost), len(status.seen), status.lost[0])
	}

	// The token route spends the challenge and looks the account up with the
	// request's context too; nobody of that address is a 404.
	token := &ownerDB{ownerKey: publicKey}
	rr = ownerPost(t, ownerRouter(t, token, ""), "/api/v1/auth/owner/token", signer, publicKey,
		func(c string) []byte { return bootstrap.OwnerMessage(c, "reset-admin", "nobody@example.test") },
		map[string]any{"purpose": "reset-admin", "address": "nobody@example.test"})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("token: %d %s, want 404 no_such_account", rr.Code, rr.Body.String())
	}
	if len(token.lost) > 0 {
		t.Errorf("token: %d of %d statements ran without the request's context, first %q",
			len(token.lost), len(token.seen), token.lost[0])
	}
}

func TestOwnerStatusReportsAFailedSpendNotAnInvalidChallenge(t *testing.T) {
	signer, publicKey := ownerSigner(t)

	failed := &ownerDB{insertFail: errors.New("connection to 10.0.0.9:5432 lost")}
	rr := ownerPost(t, ownerRouter(t, failed, publicKey), "/api/v1/auth/owner/status", signer, publicKey,
		bootstrap.StatusMessage, nil)
	if rr.Code != http.StatusInternalServerError || errorCode(rr) != "internal_error" {
		t.Fatalf("failed spend: %d %s, want 500 internal_error", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "10.0.0.9") {
		t.Errorf("the dependency failure reached the client: %s", rr.Body.String())
	}

	// A challenge answered before is still refused as invalid.
	replayed := &ownerDB{insertFail: errors.New(`ERROR: duplicate key value violates unique constraint "idx_one_time_tokens_token_hash" (SQLSTATE 23505)`)}
	rr = ownerPost(t, ownerRouter(t, replayed, publicKey), "/api/v1/auth/owner/status", signer, publicKey,
		bootstrap.StatusMessage, nil)
	if rr.Code != http.StatusUnauthorized || errorCode(rr) != "invalid_challenge" {
		t.Fatalf("replayed challenge: %d %s, want 401 invalid_challenge", rr.Code, rr.Body.String())
	}
}
