package api

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/examplepairs"
)

const mailboxUpdatePath = "/api/v1/admin/mailboxes/{id}"

// mailboxUpdateLinkedID is the account createAccount's link names in the contract
// run; pairs that leave the path to that link are sent to it here.
const mailboxUpdateLinkedID = "2"

// Controlled SQL rows run the real lookup, column-bound update and read-back.
// They prove the route's wiring and answers, not PostgreSQL semantics.
type mailboxUpdateConnection struct {
	accountCredentialListConnection
	store *bulkQueueExampleStore
}

func (c *mailboxUpdateConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c *mailboxUpdateConnection) Driver() driver.Driver { return c }

func (c *mailboxUpdateConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c *mailboxUpdateConnection) Begin() (driver.Tx, error) { return bulkQueueExampleTx{}, nil }

func (c *mailboxUpdateConnection) record(q string, args []driver.NamedValue) {
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	c.store.calls = append(c.store.calls, bulkQueueExampleMutation{q, append([]driver.NamedValue(nil), args...)})
}

func (c *mailboxUpdateConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(q, `FROM "mailboxes"`) && strings.HasPrefix(q, `SELECT "address"`) && !strings.Contains(q, "active"):
		c.record(q, args)
		if len(args) < 1 || !strings.Contains(q, "id =") {
			return nil, errors.New("mailbox scope lost ID")
		}
		r := &authReadSnapshotRows{columns: []string{"address"}}
		if args[0].Value != int64(2147483647) {
			r.rows = [][]driver.Value{{"new-account@restmail.test"}}
		}
		return r, nil
	case strings.Contains(q, `FROM "mailboxes"`) && strings.HasPrefix(q, "SELECT *"):
		c.record(q, args)
		if len(args) < 2 || !strings.Contains(q, `"mailboxes"."id" =`) {
			return nil, errors.New("mailbox load lost exact row or bound")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "domain_id", "local_part", "address", "display_name", "quota_bytes", "quota_used_bytes", "active", "last_login_at", "password_change_required", "two_factor_required", "credential_epoch", "bootstrap_expires_at", "last_password_change", "created_at", "updated_at"}}
		if id := args[0].Value; id != int64(2147483647) {
			stamp := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
			r.rows = [][]driver.Value{{id, int64(1), "new-account", "new-account@restmail.test", "Contract Test", int64(1073741824), int64(0), true, nil, false, false, int64(0), nil, nil, stamp, stamp}}
		}
		return r, nil
	case strings.Contains(q, `FROM "domains"`):
		c.record(q, args)
		return &authReadSnapshotRows{columns: []string{"id", "name", "server_type", "active", "default_quota_bytes", "dkim_selector", "sending_policy", "dmarc_policy", "created_at", "updated_at"}}, nil
	case strings.Contains(q, `FROM "quota_usage"`):
		c.record(q, args)
		return &authReadSnapshotRows{columns: []string{"mailbox_id", "subject_bytes", "body_bytes", "attachment_bytes", "message_count", "updated_at"}}, nil
	}
	return c.accountCredentialListConnection.QueryContext(ctx, q, args)
}

func (c *mailboxUpdateConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(q, `UPDATE "mailboxes" SET`) {
		return nil, errors.New("unexpected ancillary mailbox write")
	}
	c.record(q, args)
	return driver.RowsAffected(1), nil
}

// The description pairs that need a stored account go through the production
// router: the binder's 400 and 422 refusals, the handler's byte-count password
// rule, a missing account and the update itself.
func TestMailboxUpdateExamples(t *testing.T) {
	_, _, _, jwt := authReadBatchRouters(t)
	token := mailboxToken(t, jwt)
	store := &bulkQueueExampleStore{}
	pool := sql.OpenDB(&mailboxUpdateConnection{accountCredentialListConnection: accountCredentialListConnection{mode: "normal"}, store: store})
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	db := openAccountReadDB(t, pool)
	r := NewRouters(db, jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 512}, nil)
	t.Cleanup(r.Close)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"body_array", "malformed_json", "missing", "password_short", "updated"} {
		pair, ok := pairs["updateMailbox"][name]
		if !ok {
			t.Fatalf("updateMailbox has no %s pair", name)
		}
		t.Run(name, func(t *testing.T) {
			examplepairs.Proven(t, "updateMailbox", name)
			body := []byte(pair.Request.Body)
			if name == "malformed_json" {
				body = []byte("{")
			}
			uri := strings.ReplaceAll(messageCollectionReadURI(mailboxUpdatePath, pair), "{id}", mailboxUpdateLinkedID)
			req := httptest.NewRequest(http.MethodPatch, uri, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
			before := len(store.snapshot())
			rr := httptest.NewRecorder()
			r.Public.ServeHTTP(rr, req)
			if rr.Code != pair.Response.Status {
				t.Fatalf("got %d %s, want %d", rr.Code, rr.Body, pair.Response.Status)
			}
			if pair.Response.MediaType != "" && !strings.HasPrefix(rr.Header().Get("Content-Type"), pair.Response.MediaType) {
				t.Fatalf("media type %q, want %q", rr.Header().Get("Content-Type"), pair.Response.MediaType)
			}
			assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			writes := 0
			for _, m := range store.snapshot()[before:] {
				if !strings.HasPrefix(m.query, "UPDATE") {
					continue
				}
				writes++
				fields, err := vacationMutationFields(m.query, m.args)
				if err != nil {
					t.Fatal(err)
				}
				if fields["id"] != int64(2) || fields["display_name"] != "Contract Test" || fields["quota_bytes"] != int64(1073741824) || fields["active"] != true {
					t.Fatalf("update wrote %v", fields)
				}
			}
			if want := map[bool]int{true: 1, false: 0}[name == "updated"]; writes != want {
				t.Fatalf("writes=%d, want %d", writes, want)
			}
		})
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if w.OperationID == "updateMailbox" {
				t.Errorf("mailbox update warning: %v", w)
			}
		}
	}
}
