package api

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/attachmentstore"
	"github.com/restmail/restmail/internal/attachmentstore/s3test"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	rmime "github.com/restmail/restmail/internal/mime"
	"github.com/restmail/restmail/internal/pipeline"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Storage faults are controlled, while public routing, current grants, sender
// authorization, MIME, quota transaction, S3 HTTP and forward code run unchanged.
// This ledger does not establish PostgreSQL locking or constraint behavior.
var forwardActorSequence atomic.Uint64

type forwardConnection struct {
	actorID uint
	importConnection
	original    map[string]any
	sources     []map[string]any
	attachments []map[string]any
	queue       []map[string]any
}

func (c *forwardConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *forwardConnection) Driver() driver.Driver                        { return c }
func (c *forwardConnection) Open(string) (driver.Conn, error)             { return c, nil }

type forwardTransaction struct {
	c                  *forwardConnection
	state              importLedger
	attachments, queue []map[string]any
}

func (t forwardTransaction) Commit() error { return nil }
func (t forwardTransaction) Rollback() error {
	t.c.state = t.state
	t.c.attachments = t.attachments
	t.c.queue = t.queue
	return nil
}
func (c *forwardConnection) Begin() (driver.Tx, error) {
	s := c.state
	s.messages = append([]map[string]any(nil), s.messages...)
	return forwardTransaction{c, s, append([]map[string]any(nil), c.attachments...), append([]map[string]any(nil), c.queue...)}, nil
}
func (c *forwardConnection) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.Begin()
}
func (c *forwardConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.queries = append(c.queries, q)
	if strings.Contains(q, "JOIN role_capabilities") {
		cols := []string{"scope", "capability", "expires_at", "activation_expires_at"}
		if c.mode == "denied" {
			return invitationRows(cols), nil
		}
		return invitationRows(cols, []driver.Value{"*", "mail:read", nil, nil}, []driver.Value{"*", "mail:send", nil, nil}), nil
	}
	if strings.HasPrefix(q, "SELECT count(*)") {
		n := int64(0)
		if strings.Contains(q, `FROM "mailboxes"`) || strings.Contains(q, `FROM "domains"`) {
			n = 1
		}
		return invitationRows([]string{"count"}, []driver.Value{n}), nil
	}
	if strings.Contains(q, `FROM "pipelines"`) && c.mode == "outbound_extract" {
		for _, arg := range args {
			if arg.Value == "outbound" {
				return importRows(map[string]any{"id": int64(502), "domain_id": int64(2), "direction": "outbound", "active": true, "filters": []byte(`[{"name":"extract_attachments","type":"transform","enabled":true,"config":{"storage_type":"s3"}}]`)}), nil
			}
		}
	}
	if strings.Contains(q, `FROM "pipelines"`) && strings.HasPrefix(c.mode, "local") {
		for _, arg := range args {
			if arg.Value == "inbound" {
				if c.mode == "local_policies" {
					return importRows(map[string]any{"id": int64(501), "domain_id": int64(2), "direction": "inbound", "active": true, "filters": []byte(`[{"name":"domain_allowlist","type":"action","enabled":true,"config":{}},{"name":"contact_whitelist","type":"action","enabled":true,"config":{}},{"name":"recipient_check","type":"action","enabled":true,"config":{}},{"name":"sender_verify","type":"action","enabled":true,"config":{}},{"name":"extract_attachments","type":"transform","enabled":true,"config":{}}]`)}), nil
				}
				return importRows(map[string]any{"id": int64(501), "domain_id": int64(2), "direction": "inbound", "active": true, "filters": []byte(`[{"name":"extract_attachments","type":"transform","enabled":true,"config":{}}]`)}), nil
			}
		}
	}
	if strings.Contains(q, `FROM "sieve_scripts"`) {
		return invitationRows([]string{"id"}), nil
	}
	if strings.Contains(q, `FROM "pipelines"`) || strings.Contains(q, `FROM "contacts"`) {
		return invitationRows([]string{"id"}), nil
	}
	if strings.HasPrefix(q, `INSERT INTO "message_traces"`) || strings.HasPrefix(q, `INSERT INTO "contacts"`) {
		return invitationRows([]string{"id"}, []driver.Value{int64(901)}), nil
	}
	if strings.Contains(q, `FROM "mailboxes"`) {
		for _, arg := range args {
			if value, ok := arg.Value.(string); ok && strings.Contains(value, "@") && value != "sender@example.test" {
				if strings.HasPrefix(c.mode, "local") && value == "target@remote.test" {
					return importRows(map[string]any{"id": int64(13), "domain_id": int64(2), "address": value, "active": true, "quota_bytes": int64(100000), "quota_used_bytes": int64(0)}), nil
				}
				return invitationRows([]string{"id"}), nil
			}
		}
		id, address := int64(1), "sender@example.test"
		for _, arg := range args {
			if n, ok := arg.Value.(int64); ok && (n == 1 || n == 12 || n == int64(c.actorID)) {
				id = n
				break
			}
		}
		if id == 12 {
			address = "owner@example.test"
		}
		if strings.HasPrefix(q, `SELECT "address"`) {
			return invitationRows([]string{"address"}, []driver.Value{address}), nil
		}
		return importRows(map[string]any{"id": id, "domain_id": int64(2), "address": address, "active": true, "credential_epoch": int64(0), "password_change_required": false, "two_factor_required": false, "quota_bytes": int64(100000), "quota_used_bytes": c.state.quota}), nil
	}
	if strings.Contains(q, `FROM "domains"`) {
		if strings.Contains(q, "sending_policy") {
			return importRows(map[string]any{"id": int64(2), "name": "example.test", "sending_policy": ""}), nil
		}
		if c.mode == "local_policies" && strings.HasPrefix(q, `SELECT "id"`) {
			return invitationRows([]string{"id"}, []driver.Value{int64(2)}), nil
		}
		return invitationRows([]string{"id"}), nil // no configured signing key
	}
	if strings.Contains(q, `FROM "messages"`) {
		return importRows(c.original), nil
	}
	if strings.Contains(q, `FROM "attachments"`) {
		if strings.Contains(q, "checksum =") {
			return invitationRows([]string{"id"}), nil
		}
		if c.mode == "source_failure" {
			c.failed = true
			return nil, errors.New("private source lookup detail")
		}
		return importRows(c.sources...), nil
	}
	if strings.HasPrefix(q, `INSERT INTO "attachments"`) || strings.HasPrefix(q, `INSERT INTO "outbound_queue"`) {
		kind := "attachment"
		if strings.Contains(q, `"outbound_queue"`) {
			kind = "queue"
		}
		if c.mode == kind+"_failure" {
			c.failed = true
			return nil, errors.New("private " + kind + " insert detail")
		}
		columns := strings.Split(q[strings.Index(q, "(")+1:strings.Index(q, ")")], ",")
		var ids [][]driver.Value
		for offset := 0; offset < len(args); offset += len(columns) {
			fields, err := invitationFields(q, args[offset:offset+len(columns)])
			if err != nil {
				return nil, err
			}
			if kind == "attachment" {
				fields["id"] = int64(100 + len(c.attachments))
				c.attachments = append(c.attachments, fields)
			} else {
				fields["id"] = int64(200 + len(c.queue))
				c.queue = append(c.queue, fields)
			}
			ids = append(ids, []driver.Value{fields["id"]})
		}
		return invitationRows([]string{"id"}, ids...), nil
	}
	return c.importConnection.QueryContext(ctx, q, args)
}

type forwardFixture struct {
	c      *forwardConnection
	router http.Handler
	token  string
	store  *attachmentstore.S3
	parts  []pipeline.Attachment
}

func newForwardFixture(t *testing.T, mode string) *forwardFixture {
	t.Helper()
	fake := s3test.New(t)
	cfg := config.Config{Attachments: fake.Config, Environment: "test", SMTPMaxMessageSize: 8192, APIMaxBodyBytes: 70000}
	if mode == "size" {
		cfg.SMTPMaxMessageSize = 512
	}
	store := attachmentstore.NewS3(cfg.Attachments)
	parts := []pipeline.Attachment{{Filename: "invoice.bin", ContentType: "application/octet-stream", Disposition: "attachment", Content: base64.StdEncoding.EncodeToString([]byte("invoice\xff\x00")), Size: 9}, {Filename: "empty.txt", ContentType: "text/plain", Disposition: "attachment", Size: 0}, {Filename: "image.png", ContentType: "image/png", Disposition: "inline", ContentID: "original-image", Content: base64.StdEncoding.EncodeToString([]byte("image")), Size: 5}}
	email := &pipeline.EmailJSON{Headers: pipeline.Headers{From: []pipeline.Address{{Address: "old@example.test"}}, Subject: "receipt", MessageID: "<original@example.test>"}, Body: pipeline.Body{ContentType: "text/html", Content: `<p>original<img src="cid:original-image"></p>`}, Attachments: parts[:2], Inline: parts[2:]}
	raw, err := rmime.Serialize(email)
	if err != nil {
		t.Fatal(err)
	}
	actorID := uint(100000 + forwardActorSequence.Add(1))
	c := &forwardConnection{actorID: actorID, importConnection: importConnection{mode: mode}, original: map[string]any{"id": int64(7), "mailbox_id": int64(12), "message_id": "<original@example.test>", "sender": "old@example.test", "subject": "receipt", "body_text": "original receipt", "body_html": email.Body.Content, "raw_message": string(raw), "has_attachments": true, "received_at": time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}}
	for i, part := range parts {
		if mode == "legacy_empty" && part.Size == 0 {
			continue
		}
		data, _ := base64.StdEncoding.DecodeString(part.Content)
		ref, sum, err := store.Put(t.Context(), data)
		if err != nil {
			t.Fatal(err)
		}
		if mode == "missing_object" && i == 0 {
			if err := store.Delete(t.Context(), ref); err != nil {
				t.Fatal(err)
			}
		}
		if mode == "checksum" && i == 0 {
			sum = strings.Repeat("0", 64)
		}
		c.sources = append(c.sources, map[string]any{"id": int64(11 + i), "message_id": int64(7), "filename": part.Filename, "content_type": part.ContentType, "size_bytes": part.Size, "storage_type": "s3", "storage_ref": ref, "checksum": sum})
	}
	pool := sql.OpenDB(c)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	jwt := auth.NewJWTService(routerTestSecret, 15*time.Minute, 7*24*time.Hour)
	session, err := jwt.GenerateSession(actorID, "sender@example.test", 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	routers := NewRouters(db, jwt, &cfg, nil)
	t.Cleanup(routers.Close)
	return &forwardFixture{c, routers.Public, session.AccessToken, store, parts}
}
func (f *forwardFixture) request(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/v1/messages/7/forward", strings.NewReader(`{"from":"sender@example.test","to":["target@remote.test"],"body_text":"please review"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+f.token)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}
func TestForwardAttachmentsRealRoutePreservesPartsAndIndependentStorage(t *testing.T) {
	useRealGrants(t)
	f := newForwardFixture(t, "")
	w := f.request(t)
	if w.Code != 201 {
		t.Fatalf("forward: %d %s; queries=%v", w.Code, w.Body, f.c.queries)
	}
	if len(f.c.state.messages) != 1 || len(f.c.attachments) != 3 || len(f.c.queue) != 1 || f.c.state.quota <= 14 {
		t.Fatalf("partial forward: sent=%d parts=%d queued=%d quota=%d", len(f.c.state.messages), len(f.c.attachments), len(f.c.queue), f.c.state.quota)
	}
	sent := f.c.state.messages[0]
	raw, ok := sent["raw_message"].(string)
	if !ok || raw != f.c.queue[0]["raw_message"] || sent["has_attachments"] != true {
		t.Fatal("Sent/queue source or attachment marker differs")
	}
	parsed, err := rmime.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Attachments) != 2 || len(parsed.Inline) != 1 || parsed.Inline[0].ContentID != "original-image" {
		t.Fatalf("MIME parts lost: %+v", parsed)
	}
	for i, row := range f.c.attachments {
		if row["storage_ref"] == f.c.sources[i]["storage_ref"] {
			t.Fatal("forward shares original object lifetime")
		}
		if err := f.store.Delete(t.Context(), f.c.sources[i]["storage_ref"].(string)); err != nil {
			t.Fatal(err)
		}
		file, err := f.store.Open(t.Context(), row["storage_ref"].(string))
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(file)
		_ = file.Close()
		want, _ := base64.StdEncoding.DecodeString(f.parts[i].Content)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("forward object changed after deleting original")
		}
	}
}
func TestForwardAttachmentsRealRouteRefusesPartialWrites(t *testing.T) {
	useRealGrants(t)
	for _, tc := range []struct {
		mode   string
		status int
	}{{"full", 422}, {"size", 413}, {"source_failure", 500}, {"missing_object", 500}, {"checksum", 500}, {"attachment_failure", 500}, {"queue_failure", 500}, {"quota_failure", 500}, {"denied", 403}} {
		t.Run(tc.mode, func(t *testing.T) {
			f := newForwardFixture(t, tc.mode)
			w := f.request(t)
			if w.Code != tc.status || strings.Contains(w.Body.String(), "private") || len(f.c.state.messages) != 0 || len(f.c.attachments) != 0 || len(f.c.queue) != 0 || f.c.state.quota != 0 {
				t.Fatalf("partial/refusal: code=%d body=%s sent=%d parts=%d queued=%d quota=%d; queries=%v", w.Code, w.Body, len(f.c.state.messages), len(f.c.attachments), len(f.c.queue), f.c.state.quota, f.c.queries)
			}
			if strings.HasSuffix(tc.mode, "_failure") && !f.c.failed {
				t.Fatalf("%s fault not reached", tc.mode)
			}
		})
	}
}

func TestForwardAttachmentsLocalAndLegacyEmptyPartsRemainReadable(t *testing.T) {
	useRealGrants(t)
	for _, mode := range []string{"local", "local_policies", "legacy_empty", "outbound_extract"} {
		t.Run(mode, func(t *testing.T) {
			f := newForwardFixture(t, mode)
			w := f.request(t)
			if w.Code != 201 {
				t.Fatalf("forward: %d %s; queries=%v", w.Code, w.Body, f.c.queries)
			}
			wantMessages, wantParts, wantQueue := 1, 3, 1
			if strings.HasPrefix(mode, "local") {
				wantMessages, wantParts, wantQueue = 2, 6, 0
			}
			if len(f.c.state.messages) != wantMessages || len(f.c.attachments) != wantParts || len(f.c.queue) != wantQueue {
				t.Fatalf("partial local/legacy forward: sent=%d parts=%d queue=%d", len(f.c.state.messages), len(f.c.attachments), len(f.c.queue))
			}
			for _, message := range f.c.state.messages {
				parsed, err := rmime.Parse([]byte(message["raw_message"].(string)))
				if err != nil || len(parsed.Attachments) != 2 || len(parsed.Inline) != 1 || parsed.Inline[0].ContentID != "original-image" {
					t.Fatalf("transformed MIME parts lost: parsed=%+v err=%v", parsed, err)
				}
			}
			originalRefs := map[string]bool{}
			for _, source := range f.c.sources {
				ref := source["storage_ref"].(string)
				originalRefs[ref] = true
				if err := f.store.Delete(t.Context(), ref); err != nil {
					t.Fatal(err)
				}
			}
			used := map[string]bool{}
			empty := 0
			for _, row := range f.c.attachments {
				ref := row["storage_ref"].(string)
				if originalRefs[ref] || used[ref] {
					t.Fatal("forward or local copy shares an attachment object")
				}
				used[ref] = true
				file, err := f.store.Open(t.Context(), ref)
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(file)
				_ = file.Close()
				if err != nil {
					t.Fatal(err)
				}
				if row["filename"] == "empty.txt" {
					empty++
					if len(data) != 0 {
						t.Fatal("empty attachment changed")
					}
				}
			}
			if empty != wantMessages {
				t.Fatalf("empty attachments=%d want%d", empty, wantMessages)
			}
		})
	}
}
