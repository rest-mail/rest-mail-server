package api

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
)

type mailRemovalConnection struct {
	mailResourceReadConnection
	store *bulkQueueExampleStore
}

func (c mailRemovalConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c mailRemovalConnection) Driver() driver.Driver { return c }

func (c mailRemovalConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c mailRemovalConnection) Begin() (driver.Tx, error) { return bulkQueueExampleTx{}, nil }

func (c mailRemovalConnection) record(q string, args []driver.NamedValue) {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	c.store.calls = append(c.store.calls, bulkQueueExampleMutation{q, append([]driver.NamedValue(nil), args...)})
}

// Capture actual selectors and state transitions. Controlled results prove
// wiring, not SQL isolation, worker races or live mail/audit persistence.
func (c mailRemovalConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if rows, ok := defaultServerCeilingRows(q, args); ok {
		return rows, nil
	}
	if strings.Contains(q, "JOIN role_capabilities") {
		rows, err := c.mailResourceReadConnection.QueryContext(ctx, q, args)
		if err != nil {
			return rows, err
		}
		r := rows.(*authReadSnapshotRows)
		if c.mode == "normal" {
			for _, address := range []string{"linked@example.com", "empty@example.com", "account4@example.com"} {
				r.rows = append(r.rows, []driver.Value{"mailbox:" + address, "mail:write", nil, nil})
			}
		}
		if c.mode == "domain_manager" {
			r.rows = append(r.rows, []driver.Value{"domain:example.com", "mail:write", nil, nil}, []driver.Value{"domain:example.com", "mail:send", nil, nil}, []driver.Value{"domain:example.com", "mail:send_as", nil, nil})
		}
		return r, nil
	}
	if !strings.Contains(q, `FROM "scheduled_sends"`) {
		return c.mailResourceReadConnection.QueryContext(ctx, q, args)
	}
	if len(args) != 5 || !strings.Contains(q, "id =") || !strings.Contains(q, "account_id =") || !strings.Contains(q, "status IN") || !strings.Contains(q, "LIMIT") || args[1].Value != int64(10) || args[2].Value != "pending" || args[3].Value != "held" || args[4].Value != int64(1) {
		return nil, errors.New("cancellation lookup lost ID, account or pending/held-state restriction")
	}
	c.record(q, args)
	id := fmt.Sprint(args[0].Value)
	if id == "3" {
		return nil, errors.New("private scheduled lookup detail")
	}
	r := &authReadSnapshotRows{columns: []string{"id", "account_id", "from", "status"}}
	// These fixtures represent rows excluded by owner or waiting-state predicates.
	if id == "2147483647" || id == "54" || id == "55" || id == "56" || id == "57" {
		return r, nil
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return nil, err
	}
	sender := "linked@example.com"
	if id == "51" {
		sender = "user@example.com"
	}
	if id == "53" {
		sender = "other@example.com"
	}
	r.rows = [][]driver.Value{{n, int64(10), sender, "pending"}}
	return r, nil
}

func (c mailRemovalConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(q, `UPDATE "scheduled_sends" SET`) {
		if len(args) != 5 || args[0].Value != "cancelled" || !strings.Contains(q, "id =") || !strings.Contains(q, "status IN") || args[3].Value != "pending" || args[4].Value != "held" {
			return nil, errors.New("cancellation lost pending/held-state compare-and-update")
		}
		c.record(q, args)
		if args[2].Value == int64(59) {
			return nil, errors.New("private cancellation update detail")
		}
		if args[2].Value == int64(58) {
			return driver.RowsAffected(0), nil
		}
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(q, `DELETE FROM "quarantine"`) {
		if len(args) != 2 || !strings.Contains(q, "id =") || !strings.Contains(q, "mailbox_id =") {
			return nil, errors.New("quarantine deletion lost row or mailbox restriction")
		}
		c.record(q, args)
		if args[0].Value == int64(43) {
			return nil, errors.New("private quarantine delete detail")
		}
		if args[0].Value == int64(2147483647) || args[0].Value == int64(42) {
			return driver.RowsAffected(0), nil
		}
		return driver.RowsAffected(1), nil
	}
	return nil, errors.New("unexpected ancillary write in mail removal fixture")
}

type mailRemovalScenarios struct {
	scenarios map[string]accountReadScenario
	store     *bulkQueueExampleStore
}

func newMailRemovalScenarios(t *testing.T, connections ...func(mailResourceReadConnection, *bulkQueueExampleStore) driver.Connector) mailRemovalScenarios {
	t.Helper()
	extra := map[string]accountListKey{}
	tokens := map[string]string{}
	for name, permission := range map[string]auth.ScopedPermission{
		"key_send":             {Capability: "mail:send_as", Scope: "mailbox:linked@example.com"},
		"key_send_domain":      {Capability: "mail:send_as", Scope: "domain:example.com"},
		"key_send_wrong_scope": {Capability: "mail:send_as", Scope: "mailbox:other@example.com"},
		"key_write":            {Capability: "mail:write", Scope: "mailbox:linked@example.com"},
		"key_write_domain":     {Capability: "mail:write", Scope: "domain:example.com"},
	} {
		token, public, hash, err := auth.NewAPIKey()
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(models.CredentialScope{Permissions: []auth.ScopedPermission{permission}})
		if err != nil {
			t.Fatal(err)
		}
		extra[public] = accountListKey{accountReadKey{hash: hash, scope: data}, models.CredentialKindAPIKey}
		tokens[name] = token
	}
	store := &bulkQueueExampleStore{}
	out := accountListScenarios(t, func(mode string, keys map[string]accountListKey) driver.Connector {
		for public, key := range extra {
			keys[public] = key
		}
		base := mailResourceReadConnection{personalDataReadConnection{accountCredentialListConnection{mode: mode, keys: keys}}}
		if len(connections) != 0 {
			return connections[0](base, store)
		}
		return mailRemovalConnection{base, store}
	})
	for _, name := range []string{"lookup_unavailable", "write_unavailable", "concurrent", "foreign_owner", "sending", "sent", "cancelled", "wrong_mailbox", "invalid_mid", "invalid_mid_format", "missing_account"} {
		out[name] = out["found"]
	}
	out["domain_grant"] = out["domain_manager"]
	out["app_password_other"] = out["app_password"]
	out["sender_unavailable"] = out["primary_unavailable"]
	for name, token := range tokens {
		out[name] = accountReadScenario{out["found"].handler, token}
	}
	out["key_send_holder_missing"] = accountReadScenario{out["no_grants"].handler, tokens["key_send"]}
	out["key_write_holder_missing"] = accountReadScenario{out["no_grants"].handler, tokens["key_write"]}
	normal := out["found"]
	for _, name := range []string{"cookie_session", "csrf_missing", "csrf_mismatch"} {
		name := name
		out[name] = accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.Clone(r.Context())
			r.Header.Del("Authorization")
			r.AddCookie(&http.Cookie{Name: auth.AccessCookieName, Value: normal.token})
			r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "fixture-csrf"})
			if name == "cookie_session" {
				r.Header.Set(auth.CSRFHeaderName, "fixture-csrf")
			}
			if name == "csrf_mismatch" {
				r.Header.Set(auth.CSRFHeaderName, "wrong-csrf")
			}
			normal.handler.ServeHTTP(w, r)
		}), ""}
	}
	return mailRemovalScenarios{out, store}
}
