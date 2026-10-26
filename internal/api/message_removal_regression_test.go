package api

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
)

// Controlled SQL verifies authorization and mutation wiring. Scratch database
// regressions separately exercise durable rows, quota rollback and events.
type messageRemovalConnection struct {
	mailRemovalConnection
	tx     *messageRemovalTx
	target int64
}

type messageRemovalTx struct {
	c        *messageRemovalConnection
	relevant bool
}

func (c *messageRemovalConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c *messageRemovalConnection) Driver() driver.Driver { return c }

func (c *messageRemovalConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c *messageRemovalConnection) Begin() (driver.Tx, error) {
	tx := &messageRemovalTx{c: c}
	c.tx = tx
	return tx, nil
}

func (tx *messageRemovalTx) Commit() error {
	if tx.relevant {
		tx.c.record("COMMIT", nil)
	}
	tx.c.tx = nil
	return nil
}

func (tx *messageRemovalTx) Rollback() error {
	if tx.relevant {
		tx.c.record("ROLLBACK", nil)
	}
	tx.c.tx = nil
	return nil
}

func (c *messageRemovalConnection) mutation(q string, args []driver.NamedValue) {
	if c.tx != nil && !c.tx.relevant {
		c.tx.relevant = true
		c.record("BEGIN", nil)
	}
	c.record(q, args)
}

func (c *messageRemovalConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(q, `FROM "messages"`) {
		c.record(q, args)
		rows, err := (messageSourceReadConnection{c.personalDataReadConnection}).QueryContext(ctx, q, args)
		if err != nil {
			return nil, err
		}
		r := rows.(*authReadSnapshotRows)
		if len(r.rows) == 0 {
			return r, nil
		}
		c.target = args[0].Value.(int64)
		for i, col := range r.columns {
			switch col {
			case "mailbox_id":
				if c.target >= 30 {
					r.rows[0][i] = int64(1)
				}
				if c.target == 51 {
					r.rows[0][i] = int64(10)
				}
			case "folder":
				if c.target == 31 {
					r.rows[0][i] = "Trash"
				}
			case "is_deleted":
				if c.target == 32 {
					r.rows[0][i] = true
				}
			case "has_attachments":
				if c.target == 33 || c.target == 34 {
					r.rows[0][i] = false
				}
			case "size_bytes":
				if c.target == 34 {
					r.rows[0][i] = int64(0)
				}
			}
		}
		return r, nil
	}
	if strings.Contains(q, `FROM "attachments"`) && strings.Contains(q, "SUM(size_bytes)") {
		c.mutation(q, args)
		if len(args) != 1 || args[0].Value != c.target || !strings.Contains(q, "message_id =") {
			return nil, errors.New("attachment sum lost message selector")
		}
		if c.target == 36 {
			return nil, errors.New("private attachment sum detail")
		}
		size := int64(100)
		if c.target == 41 {
			size = 0
		}
		return &authReadSnapshotRows{columns: []string{"coalesce"}, rows: [][]driver.Value{{size}}}, nil
	}
	return c.mailRemovalConnection.QueryContext(ctx, q, args)
}

func (c *messageRemovalConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(q, `UPDATE "messages"`) {
		c.mutation(q, args)
		fields, err := vacationMutationFields(q, args)
		if err != nil {
			return nil, err
		}
		if fields["id"] != c.target || fields["folder"] != "Trash" || len(fields) != 3 {
			return nil, errors.New("Trash move changed identity/flags")
		}
		stamp, ok := fields["updated_at"].(time.Time)
		if !ok || stamp.Before(time.Now().Add(-time.Minute)) || stamp.After(time.Now()) {
			return nil, errors.New("Trash move lost update time")
		}
		if c.target == 35 {
			return nil, errors.New("private Trash update detail")
		}
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(q, `DELETE FROM "attachments"`) || strings.HasPrefix(q, `DELETE FROM "messages"`) {
		c.mutation(q, args)
		if len(args) != 1 || args[0].Value != c.target {
			return nil, errors.New("purge lost message selector")
		}
		if c.target == 37 && strings.HasPrefix(q, `DELETE FROM "attachments"`) || c.target == 38 && strings.HasPrefix(q, `DELETE FROM "messages"`) {
			return nil, errors.New("private purge detail")
		}
		if c.target == 40 {
			return driver.RowsAffected(0), nil
		}
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(q, `UPDATE "mailboxes"`) {
		c.mutation(q, args)
		if len(args) != 3 || args[2].Value != int64(1) || !strings.Contains(q, "GREATEST(quota_used_bytes -") || !strings.Contains(q, "WHERE id =") {
			return nil, errors.New("quota reclaim lost owner/clamp")
		}
		if c.target == 39 {
			return nil, errors.New("private quota detail")
		}
		return driver.RowsAffected(1), nil
	}
	return nil, errors.New("unexpected ancillary message deletion write")
}

func newMessageRemovalScenarios(t *testing.T) mailRemovalScenarios {
	t.Helper()
	previousSessionCheck := sessionCheck
	defer func() { sessionCheck = previousSessionCheck }()
	extra := map[string]accountListKey{}
	tokens := map[string]string{}
	for name, permission := range map[string]auth.ScopedPermission{
		"key_send":             {Capability: "mail:send", Scope: "mailbox:linked@example.com"},
		"key_send_domain":      {Capability: "mail:send", Scope: "domain:example.com"},
		"key_send_wrong_scope": {Capability: "mail:send", Scope: "mailbox:other@example.com"},
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
		return &messageRemovalConnection{mailRemovalConnection: mailRemovalConnection{mailResourceReadConnection: base, store: store}}
	}, func(cfg *config.Config) { cfg.APIMaxBodyBytes = 512; sessionCheck = cachedSessionCheck })
	// Explicit fixture cases keep regression tests independent of published examples.
	for _, name := range []string{
		"already_deleted",
		"app_password",
		"app_password_other",
		"attachment_sum_unavailable",
		"attachments_delete_unavailable",
		"attachments_zero",
		"body_boundary",
		"cancelled",
		"concurrent",
		"cookie_session",
		"csrf_mismatch",
		"csrf_missing",
		"grant_unavailable",
		"inaccessible",
		"invalid_format",
		"invalid_id",
		"key_read",
		"key_write",
		"key_write_domain",
		"key_write_holder_missing",
		"key_wrong_scope",
		"lookup_unavailable",
		"mailbox_unavailable",
		"message_delete_unavailable",
		"missing",
		"move_unavailable",
		"moved",
		"no_attachments",
		"ownership_unavailable",
		"permanent",
		"permanent_1",
		"permanent_false",
		"permanent_garbage",
		"permanent_uppercase",
		"primary",
		"quota_unavailable",
		"rate_limited",
		"request_too_large",
		"session_unavailable",
		"setup_required",
		"trash",
		"unauthenticated",
		"write_only",
		"zero_size",
	} {
		if _, ok := out[name]; !ok {
			out[name] = out["found"]
		}
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
	s := out["no_grants"]
	s.token = tokens["key_write"]
	out["key_write_holder_missing"] = s
	s = out["found"]
	out["cancelled"] = accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		cancel()
		s.handler.ServeHTTP(w, r.WithContext(ctx))
	}), s.token}
	return mailRemovalScenarios{out, store}
}

func TestMessageRemovalFailuresAndQuotaGuard(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		status     int
	}{{"failed Trash move", "/api/v1/messages/35", 500}, {"failed attachment sum", "/api/v1/messages/36?permanent=true", 500}, {"already purged", "/api/v1/messages/40?permanent=true", 204}, {"cancelled", "/api/v1/messages/1", 500}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMessageRemovalScenarios(t)
			s := f.scenarios["found"]
			if tc.name == "cancelled" {
				s = f.scenarios["cancelled"]
			}
			rr := mailboxSessionEndingRequest(t, bulkQueueExampleScenario{accountReadScenario: s, store: f.store}, http.MethodDelete, tc.path, nil)
			if rr.Code != tc.status {
				t.Fatalf("got%d %s want%d", rr.Code, rr.Body, tc.status)
			}
			if tc.name == "already purged" {
				for _, q := range f.store.snapshot() {
					if strings.HasPrefix(q.query, `UPDATE "mailboxes"`) {
						t.Fatal("already purged row reclaimed quota twice")
					}
				}
			}
		})
	}
}
