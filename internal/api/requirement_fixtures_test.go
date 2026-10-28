package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"
)

// The generic failure connector is appropriate only when the first SQL operation
// is the intended fault. These fixtures let prerequisite reads succeed and refuse
// unexpected SQL, so a later fault cannot be proved by an earlier refusal.
type requirementConnection struct {
	capabilityExampleConnection
	operation string
	publicKey string
	faults    *int
	fail      bool
}

func (c requirementConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c requirementConnection) Driver() driver.Driver                        { return c }
func (c requirementConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c requirementConnection) Begin() (driver.Tx, error)                    { return bulkQueueExampleTx{}, nil }
func (c requirementConnection) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(q, "SAVEPOINT ") || strings.HasPrefix(q, "ROLLBACK TO SAVEPOINT ") {
		return driver.RowsAffected(0), nil
	}
	if (c.operation == "resetAdmin" || c.operation == "acceptInvitation") && strings.HasPrefix(q, `UPDATE "one_time_tokens"`) {
		return driver.RowsAffected(0), nil // A healthy store, but no such token.
	}
	return nil, errors.New("unexpected requirement fixture write")
}
func (c requirementConnection) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if rows, ok := defaultServerCeilingRows(q, args); ok {
		return rows, nil
	}
	switch {
	case c.operation == "sendDraft" && strings.Contains(q, `FROM "messages"`):
		*c.faults++
		return nil, errors.New("injected draft lookup database failure")
	case strings.HasPrefix(c.operation, "search_"):
		phase := strings.TrimPrefix(c.operation, "search_")
		switch {
		case strings.Contains(q, `FROM "mailboxes"`):
			if phase == "lookup" {
				*c.faults++
				return nil, errors.New("injected search account database failure")
			}
			if phase == "missing" {
				return invitationRows([]string{"id"}), nil
			}
			if strings.Contains(q, "owner_id") {
				return invitationRows([]string{"address", "owner_id"}, []driver.Value{"actor@example.test", nil}), nil
			}
			return invitationRows([]string{"id", "address"}, []driver.Value{int64(1), "target@example.test"}), nil
		case strings.Contains(q, "role_capabilities"):
			if phase == "grants" {
				(*c.faults)++
				return nil, errors.New("injected search permission database failure")
			}
			if phase == "denied" {
				return invitationRows([]string{"scope", "capability", "expires_at", "activation_expires_at"}), nil
			}
			return invitationRows([]string{"scope", "capability", "expires_at", "activation_expires_at"}, []driver.Value{"*", "*", nil, nil}), nil
		case strings.Contains(q, `FROM "messages"`):
			if phase == "count" || !strings.Contains(q, "count(*)") {
				*c.faults++
				return nil, errors.New("injected search results database failure")
			}
			return invitationRows([]string{"count"}, []driver.Value{int64(1)}), nil
		}
	case c.operation == "createAccount" && strings.Contains(q, "role_capabilities"):
		return invitationRows([]string{"scope", "capability", "expires_at", "activation_expires_at"}, []driver.Value{"*", "*", nil, nil}), nil
	case c.operation == "createAccount" && strings.Contains(q, `FROM "mailboxes"`):
		return invitationRows([]string{"address", "owner_id"}, []driver.Value{"admin@example.test", nil}), nil
	case c.operation == "deliverMessage" && strings.Contains(q, `FROM "mailboxes"`):
		return invitationRows([]string{"id", "domain_id", "address", "active"}, []driver.Value{int64(10), int64(1), "admin@restmail.test", true}), nil
	case c.operation == "login" && strings.Contains(q, `FROM "mailboxes"`):
		return invitationRows([]string{"id", "address", "active"}, []driver.Value{int64(10), "admin@restmail.test", true}), nil
	case c.operation == "login" && strings.Contains(q, `FROM "credentials"`):
		return invitationRows([]string{"id", "account_id", "kind", "secret_hash"}, []driver.Value{int64(1), int64(10), "password", auth.DummyPasswordHash}), nil
	case c.operation == "recoverAccount" && strings.Contains(q, `FROM "mailboxes"`):
		return invitationRows([]string{"id"}), nil
	case c.operation == "acceptInvitation" && strings.Contains(q, `FROM "one_time_tokens"`):
		if c.fail {
			*c.faults++
			return nil, errors.New("injected invitation database failure")
		}
		return invitationRows([]string{"id"}), nil
	case (c.operation == "ownerToken" || c.operation == "ownerStatus") && strings.Contains(q, `FROM "owner_keys"`):
		return invitationRows([]string{"public_key"}, []driver.Value{c.publicKey}), nil
	case c.operation == "deliverMessage" && strings.Contains(q, `FROM "pipelines"`):
		*c.faults++
		return nil, errors.New("injected inbound policy database failure")
	case c.operation == "createAccount" && strings.Contains(q, `FROM "domains"`):
		*c.faults++
		return nil, errors.New("injected domain database failure")
	}
	return nil, errors.New("unexpected requirement fixture query: " + q)
}

func requirementDB(t *testing.T, connector driver.Connector) *gorm.DB {
	t.Helper()
	pool := sql.OpenDB(connector)
	t.Cleanup(func() { _ = pool.Close() })
	return openAccountReadDB(t, pool)
}

func requirementExampleSetup(t *testing.T, operation, name, requirement string, jwt *auth.JWTService, cfg *config.Config, router http.Handler, token string, body []byte) (http.Handler, string, []byte) {
	t.Helper()
	var db *gorm.DB
	if requirement == "principal=anonymous" {
		switch operation {
		case "login", "recoverAccount", "resetAdmin", "acceptInvitation", "ownerToken", "ownerStatus":
			publicKey, err := ssh.NewPublicKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, ed25519.SeedSize)).Public())
			if err != nil {
				t.Fatal(err)
			}
			db = requirementDB(t, requirementConnection{operation: operation, publicKey: string(ssh.MarshalAuthorizedKey(publicKey))})
		}
	}
	if requirement == "fault=database" {
		switch {
		case operation == "refreshToken" || operation == "logout":
			session, err := jwt.GenerateSession(testAdminHolding([]string{"*"}), "admin@example.test", 0, []string{auth.AMRPassword, auth.AMROTP}, false)
			if err != nil {
				t.Fatal(err)
			}
			body, err = json.Marshal(map[string]string{"refresh_token": session.RefreshToken})
			if err != nil {
				t.Fatal(err)
			}
		case operation == "createAccount" || operation == "acceptInvitation" || operation == "sendDraft" || (operation == "deliverMessage" && name == "deferred"):
			faults := 0
			t.Cleanup(func() {
				if faults != 1 {
					t.Errorf("injected database fault reached %d times, want 1", faults)
				}
			})
			db = requirementDB(t, requirementConnection{operation: operation, faults: &faults, fail: true})
		case operation == "listContacts" && name == "count_unavailable":
			db = requirementDB(t, personalDataReadConnection{accountCredentialListConnection{mode: "normal"}})
			token = mailboxToken(t, jwt)
		case operation == "deleteQuarantine" && name == "write_unavailable":
			db = requirementDB(t, mailRemovalConnection{mailResourceReadConnection{personalDataReadConnection{accountCredentialListConnection{mode: "normal"}}}, &bulkQueueExampleStore{}})
			token = mailboxToken(t, jwt)
		case operation == "forwardMessage" && name == "attachment_write_failed":
			useRealGrants(t)
			fixture := newForwardFixture(t, "attachment_failure")
			return fixture.router, fixture.token, body
		}
	}
	if db != nil {
		routers := NewRouters(db, jwt, cfg, nil)
		t.Cleanup(routers.Close)
		router = routers.Public
	}
	return router, token, body
}
