package repositories

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// This driver exercises the real repository and grants.Load without requiring
// a local PostgreSQL service. It models mutable persisted credential/account
// and grant rows; unexpected SQL fails the test instead of granting access.
type clientSecretStore struct {
	revoked, expires *time.Time
	active, granted  bool
	permissions      []auth.ScopedPermission
	hash             string
	grantReads       int
}

func (s *clientSecretStore) Connect(context.Context) (driver.Conn, error) { return s, nil }
func (s *clientSecretStore) Driver() driver.Driver                        { return s }
func (s *clientSecretStore) Open(string) (driver.Conn, error)             { return s, nil }
func (s *clientSecretStore) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (s *clientSecretStore) Close() error { return nil }
func (s *clientSecretStore) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction")
}
func (s *clientSecretStore) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(q, `FROM "credentials"`):
		if len(args) == 0 {
			return nil, errors.New("credential lookup lost its id")
		}
		if strings.Contains(q, "public_id") && args[0].Value != "public" {
			return &clientSecretRows{}, nil
		}
		scope, _ := json.Marshal(models.CredentialScope{Permissions: s.permissions})
		var revoked, expires driver.Value
		if s.revoked != nil {
			revoked = *s.revoked
		}
		if s.expires != nil {
			expires = *s.expires
		}
		return &clientSecretRows{columns: []string{"id", "account_id", "kind", "public_id", "secret_hash", "scope", "revoked_at", "expires_at"}, values: [][]driver.Value{{int64(34), int64(12), models.CredentialKindClientSecret, "public", s.hash, string(scope), revoked, expires}}}, nil
	case strings.Contains(q, `FROM "mailboxes"`):
		return &clientSecretRows{columns: []string{"id", "address", "active", "owner_id"}, values: [][]driver.Value{{int64(12), "robot@example.test", s.active, nil}}}, nil
	case strings.Contains(q, "FROM grants AS g"):
		for _, arg := range args {
			if arg.Value == models.GrantHolderServer {
				rows := &clientSecretRows{columns: []string{"scope", "capability"}}
				for _, capability := range append([]string{"pipelines:write"}, models.OwnerCapabilities...) {
					rows.values = append(rows.values, []driver.Value{"*", capability})
				}
				return rows, nil
			}
		}
		s.grantReads++
		rows := &clientSecretRows{columns: []string{"scope", "capability", "expires_at", "activation_expires_at"}}
		if s.granted {
			rows.values = [][]driver.Value{{"domain:example.test", "pipelines:write", nil, nil}}
		}
		return rows, nil
	}
	return nil, errors.New("unexpected SQL: " + q)
}

type clientSecretRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *clientSecretRows) Columns() []string { return r.columns }
func (r *clientSecretRows) Close() error      { return nil }
func (r *clientSecretRows) Next(out []driver.Value) error {
	if r.index == len(r.values) {
		return io.EOF
	}
	copy(out, r.values[r.index])
	r.index++
	return nil
}

func TestClientSecretVerifierUsesLiveRows(t *testing.T) {
	store := &clientSecretStore{active: true, granted: true, hash: auth.HashAppPasswordSecret("secret"), permissions: []auth.ScopedPermission{{Capability: "pipelines:write", Scope: "domain:example.test"}}}
	pool := sql.OpenDB(store)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	repo := NewCredentialRepository(db)
	now := time.Now()
	if cred, account, err := repo.VerifyClientSecret("public", "secret", now); err != nil || cred.ID != 34 || account.ID != 12 {
		t.Fatalf("valid: %v", err)
	}
	for _, pair := range [][2]string{{"unknown", "secret"}, {"public", "wrong"}} {
		if _, _, err := repo.VerifyClientSecret(pair[0], pair[1], now); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("bad proof accepted: %v", err)
		}
	}
	verifier := repo.MachineTokenVerifier()
	check := func(want int) {
		c := &auth.Claims{AccountID: 12, MachineCredentialID: 34}
		if err := verifier(c); err != nil {
			t.Fatal(err)
		}
		if c.CredentialID != 34 || c.CredentialKind != models.CredentialKindClientSecret || c.TwoFactor || len(c.APIKeyPermissions) != want {
			t.Fatalf("live claims %+v", c)
		}
	}
	check(1)
	store.granted = false
	check(0)
	store.granted = true
	store.permissions = nil
	check(0)
	if store.grantReads != 3 {
		t.Fatalf("live grants read %d times", store.grantReads)
	}
	// A broad credential scope must retain the narrower access still held by
	// its account, rather than losing that capability altogether.
	store.permissions = []auth.ScopedPermission{{Capability: "mail:read", Scope: "domain:example.test"}}
	store.granted = false
	narrowed := &auth.Claims{AccountID: 12, MachineCredentialID: 34}
	if err := verifier(narrowed); err != nil {
		t.Fatal(err)
	}
	if len(narrowed.APIKeyPermissions) != 1 || narrowed.APIKeyPermissions[0].Scope != "mailbox:robot@example.test" {
		t.Fatalf("intersection lost remaining mailbox access: %+v", narrowed.APIKeyPermissions)
	}
	for _, state := range []string{"revoked", "expired", "inactive"} {
		store.revoked = nil
		store.expires = nil
		store.active = true
		switch state {
		case "revoked":
			store.revoked = &now
		case "expired":
			store.expires = &now
		case "inactive":
			store.active = false
		}
		if _, _, err := repo.VerifyClientSecret("public", "secret", now); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("%s exchange: %v", state, err)
		}
		if err := verifier(&auth.Claims{AccountID: 12, MachineCredentialID: 34}); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("%s token: %v", state, err)
		}
	}
	store.active = true
	store.expires = nil
	if err := verifier(&auth.Claims{AccountID: 99, MachineCredentialID: 34}); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("account binding: %v", err)
	}
}
