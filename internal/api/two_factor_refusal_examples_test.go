package api

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/examplepairs"
)

// twoFactorRefusalConnection is an actor that holds everything everywhere, so
// only the session's second factor can refuse. It answers grant, account and
// credential reads and records the refusal's audit entry; any other read or
// write fails the request, so a refusal that reached storage cannot pass.
type twoFactorRefusalConnection struct {
	mu         sync.Mutex
	unexpected []string
	audited    int
}

func (c *twoFactorRefusalConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *twoFactorRefusalConnection) Driver() driver.Driver                        { return c }
func (c *twoFactorRefusalConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c *twoFactorRefusalConnection) Close() error                                 { return nil }
func (c *twoFactorRefusalConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepared statement")
}
func (c *twoFactorRefusalConnection) Begin() (driver.Tx, error) { return c, nil }
func (c *twoFactorRefusalConnection) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c, nil
}
func (c *twoFactorRefusalConnection) Commit() error   { return nil }
func (c *twoFactorRefusalConnection) Rollback() error { return nil }

func (c *twoFactorRefusalConnection) fail(q string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unexpected = append(c.unexpected, q)
	return fmt.Errorf("two-factor refusal reached storage: %s", q)
}

func (c *twoFactorRefusalConnection) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if rows, ok := defaultServerCeilingRows(q, args); ok {
		return rows, nil
	}
	switch {
	case strings.HasPrefix(q, "SELECT pg_advisory_xact_lock"):
		return invitationRows([]string{"pg_advisory_xact_lock"}, []driver.Value{""}), nil
	case strings.Contains(q, "JOIN role_capabilities"):
		ceiling, _ := defaultServerCeilingRows(`FROM grants AS g`, []driver.NamedValue{{Value: "server"}})
		r := &authReadSnapshotRows{columns: []string{"scope", "capability", "expires_at", "activation_expires_at"}}
		for _, row := range ceiling.rows {
			r.rows = append(r.rows, []driver.Value{"*", row[1], nil, nil})
		}
		return r, nil
	case strings.Contains(q, `FROM "roles"`):
		// The role being granted: an administrative one needs a fresh second factor.
		return importRows(map[string]any{"id": int64(2), "name": "admin"}), nil
	case strings.Contains(q, "FROM role_capabilities rc"):
		return invitationRows([]string{"name"}, []driver.Value{"users:write"}), nil
	case strings.Contains(q, "SELECT seq, hash FROM audit_log"):
		return invitationRows([]string{"seq", "hash"}), nil
	case strings.Contains(q, `FROM "credentials"`):
		return invitationRows([]string{"id"}), nil
	case strings.Contains(q, `FROM "mailboxes"`):
		if strings.Contains(q, "owner_id") {
			return invitationRows([]string{"address", "owner_id"}, []driver.Value{"admin@example.test", nil}), nil
		}
		return importRows(map[string]any{"id": int64(1), "domain_id": int64(1), "address": "admin@example.test", "active": true}), nil
	}
	return nil, c.fail(q)
}

func (c *twoFactorRefusalConnection) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	switch {
	case strings.HasPrefix(q, "SELECT pg_advisory_xact_lock"):
		return driver.RowsAffected(0), nil
	case strings.HasPrefix(q, `INSERT INTO "audit_log"`):
		c.mu.Lock()
		c.audited++
		c.mu.Unlock()
		return driver.RowsAffected(1), nil
	}
	return nil, c.fail(q)
}

// The two-factor refusals, sent as written through the production router: a
// password-only session for two_factor_required, and a two-factor session
// signed in longer ago than the fresh window for recent_two_factor_required.
func TestTwoFactorRefusalExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	routes := requirementRoutes(t)
	for _, tc := range []struct{ op, name string }{
		{"changeOwnPassword", "two_factor_required"}, {"changeOwnPassword", "recent_two_factor_required"},
		{"createAccount", "two_factor_required"},
		{"createAPIKey", "two_factor_required"}, {"createAPIKey", "recent_two_factor_required"},
		{"createAppPassword", "two_factor_required"}, {"createAppPassword", "recent_two_factor_required"},
		{"createGrant", "two_factor_required"}, {"createGrant", "recent_two_factor_required"},
		{"deactivateGrant", "two_factor_required"},
		{"disableTwoFactor", "two_factor_required"}, {"disableTwoFactor", "recent_two_factor_required"},
		{"inviteAccount", "two_factor_required"}, {"inviteAccount", "recent_two_factor_required"},
		{"linkAccount", "two_factor_required"}, {"linkAccount", "recent_two_factor_required"},
		{"listAuditEntries", "two_factor_required"}, {"listAuditEntries", "recent_two_factor_required"},
		{"releaseQuarantine", "two_factor_required"},
		{"unlinkAccount", "two_factor_required"},
		{"updateMailbox", "two_factor_required"}, {"updateMailbox", "recent_two_factor_required"},
	} {
		t.Run(tc.op+"/"+tc.name, func(t *testing.T) {
			examplepairs.Proven(t, tc.op, tc.name)
			useRealGrants(t)
			pair, ok := pairs[tc.op][tc.name]
			if !ok {
				t.Fatalf("no example %s of %s", tc.name, tc.op)
			}
			c := &twoFactorRefusalConnection{}
			jwt := auth.NewJWTService(routerTestSecret, time.Minute, time.Hour)
			r := NewRouters(requirementDB(t, c), jwt, &config.Config{Environment: "test", MasterKey: accessMasterKey}, nil)
			t.Cleanup(r.Close)
			amr, opts := []string{auth.AMRPassword}, []auth.TokenOption(nil)
			if tc.name == "recent_two_factor_required" {
				amr = []string{auth.AMRPassword, auth.AMROTP}
				// A refreshed session keeps its original sign-in time.
				opts = append(opts, auth.ContinueSession("stale-session"), auth.ContinueAuthentication(jwtlib.NewNumericDate(time.Now().Add(-time.Hour))))
			}
			session, err := jwt.GenerateSession(1, "admin@example.test", 0, amr, false, opts...)
			if err != nil {
				t.Fatal(err)
			}
			req := requirementRequest(routes[tc.op], pair)
			req.Header.Set("Authorization", "Bearer "+session.AccessToken)
			rr := httptest.NewRecorder()
			r.Public.ServeHTTP(rr, req)
			if len(c.unexpected) != 0 {
				t.Fatalf("refusal reached storage: %v", c.unexpected)
			}
			assertRequirementResponse(t, pair, rr)
			if c.audited > 1 {
				t.Fatalf("refusal audited %d times, want at most once", c.audited)
			}
		})
	}
}
