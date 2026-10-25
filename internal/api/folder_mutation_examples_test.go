package api

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

var folderMutationPaths = map[string]string{"createFolder": "/api/v1/accounts/{id}/folders", "renameFolder": "/api/v1/accounts/{id}/folders/{folder}"}

type folderMutationConnection struct{ mailRemovalConnection }

func (c folderMutationConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c folderMutationConnection) Driver() driver.Driver                        { return c }
func (c folderMutationConnection) Open(string) (driver.Conn, error)             { return c, nil }
func exampleInsertFields(q string, args []driver.NamedValue) (map[string]driver.Value, error) {
	start, end := strings.Index(q, "("), strings.Index(q, ") VALUES (")
	ret := strings.Index(q, " RETURNING")
	if start < 0 || end < start || ret < end {
		return nil, errors.New("unexpected marker insert")
	}
	cols := strings.Split(q[start+1:end], ",")
	values := strings.Split(strings.TrimSuffix(q[end+len(") VALUES ("):ret], ")"), ",")
	if len(cols) != len(values) {
		return nil, errors.New("marker insert value count changed")
	}
	fields := map[string]driver.Value{}
	for i, col := range cols {
		v := strings.TrimSpace(values[i])
		var value driver.Value
		if v != "DEFAULT" {
			index, err := strconv.Atoi(strings.TrimPrefix(v, "$"))
			if err != nil || index < 1 || index > len(args) {
				return nil, errors.New("unexpected marker binding")
			}
			value = args[index-1].Value
		}
		fields[strings.Trim(col, " \"")] = value
	}
	return fields, nil
}
func (c folderMutationConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.HasPrefix(q, `INSERT INTO "messages"`) {
		c.record(q, args)
		fields, err := exampleInsertFields(q, args)
		if err != nil {
			return nil, err
		}
		if fields["folder"] == "WriteFailure" {
			return nil, errors.New("private marker insert detail")
		}
		return &authReadSnapshotRows{columns: []string{"id", "received_at"}, rows: [][]driver.Value{{int64(99), sieveInstallationTime}}}, nil
	}
	if strings.HasPrefix(q, `SELECT count(*) FROM "messages"`) {
		if len(args) != 2 || !strings.Contains(q, "mailbox_id =") || !strings.Contains(q, "folder =") {
			return nil, errors.New("folder existence lost mailbox/name")
		}
		c.record(q, args)
		name := args[1].Value
		if name == "LookupFailure" {
			return nil, errors.New("private folder count detail")
		}
		count := int64(0)
		if name == "Work" || name == "Existing" || name == "Empty" || name == "Race" {
			count = 2
		}
		return &authReadSnapshotRows{columns: []string{"count"}, rows: [][]driver.Value{{count}}}, nil
	}
	return c.mailRemovalConnection.QueryContext(ctx, q, args)
}
func (c folderMutationConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.HasPrefix(q, `UPDATE "messages" SET "folder"=`) || len(args) != 4 || !strings.Contains(q, "mailbox_id =") || !strings.Contains(q, "folder =") {
		return nil, errors.New("rename lost mailbox/from or changed other fields")
	}
	c.record(q, args)
	if args[0].Value == "WriteFailure" {
		return nil, errors.New("private folder rename detail")
	}
	if args[3].Value == "Race" {
		return driver.RowsAffected(0), nil
	}
	return driver.RowsAffected(2), nil
}
func newFolderMutationScenarios(t *testing.T) mailRemovalScenarios {
	f := newMailRulesWriteScenarios(t, func(base mailRemovalConnection) driver.Connector { return folderMutationConnection{base} }, 512)
	for _, n := range []string{"example", "name_empty", "name_missing", "name_wrong_type", "name_space", "name_control", "name_parent", "name_too_long", "name_boundary", "unicode", "literal_folder", "source_missing", "destination_exists", "same_name", "source_unavailable", "destination_unavailable", "write_unavailable", "concurrent", "standard_inbox", "standard_inbox_case", "standard_sent", "standard_drafts", "standard_trash", "standard_spam", "standard_archive", "destination_standard"} {
		f.scenarios[n] = f.scenarios["found"]
	}
	return f
}
func TestFolderMutationFailuresDoNotSucceedOrBecomeMissing(t *testing.T) {
	f := newFolderMutationScenarios(t)
	for _, tc := range []struct{ method, uri, name string }{{"POST", "/api/v1/accounts/1/folders", "LookupFailure"}, {"POST", "/api/v1/accounts/1/folders", "WriteFailure"}, {"PATCH", "/api/v1/accounts/1/folders/LookupFailure", "New"}, {"PATCH", "/api/v1/accounts/1/folders/Work", "LookupFailure"}, {"PATCH", "/api/v1/accounts/1/folders/Work", "WriteFailure"}} {
		t.Run(tc.method+tc.uri+tc.name, func(t *testing.T) {
			s := f.scenarios["found"]
			body, _ := json.Marshal(map[string]string{"name": tc.name})
			req := httptest.NewRequest(tc.method, tc.uri, bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+s.token)
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			s.handler.ServeHTTP(rr, req)
			if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal server error\"}}\n" {
				t.Fatalf("got %d %s, want sanitized500", rr.Code, rr.Body)
			}
		})
	}
}
func TestFolderMutationExamples(t *testing.T) {
	f := newFolderMutationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range folderMutationPaths {
		if len(pairs[op]) < 30 {
			t.Fatalf("incomplete %s pairs", op)
		}
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := f.scenarios[name]
				if !ok {
					t.Fatalf("missing folder scenario %s", name)
				}
				method := http.MethodPost
				if op == "renameFolder" {
					method = http.MethodPatch
				}
				body := []byte(pair.Request.Body)
				if name == "malformed_json" {
					body = []byte("{")
				}
				if name == "request_too_large" {
					body = bytes.Repeat([]byte("x"), 513)
				}
				before := len(f.store.snapshot())
				req := httptest.NewRequest(method, messageCollectionReadURI(path, pair), bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				if s.token != "" {
					req.Header.Set("Authorization", "Bearer "+s.token)
				}
				rr := httptest.NewRecorder()
				s.handler.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status {
					t.Fatalf("got %d %s, want %d", rr.Code, rr.Body, pair.Response.Status)
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
				calls := f.store.snapshot()[before:]
				writes := 0
				var input struct{ Name string }
				_ = json.Unmarshal(pair.Request.Body, &input)
				for _, m := range calls {
					if strings.HasPrefix(m.query, "SELECT") {
						if len(m.args) != 2 || m.args[0].Value != int64(pair.Request.Path["id"].(float64)) {
							t.Fatal("folder count changed mailbox")
						}
						continue
					}
					writes++
					if op == "createFolder" {
						fields, err := exampleInsertFields(m.query, m.args)
						if err != nil {
							t.Fatal(err)
						}
						if fields["mailbox_id"] != int64(pair.Request.Path["id"].(float64)) || fields["folder"] != input.Name || fields["is_deleted"] != true || fields["size_bytes"] != int64(0) || fields["sender"] != "system" {
							t.Fatalf("marker changed:%v", fields)
						}
					} else {
						if m.args[0].Value != input.Name || m.args[2].Value != int64(pair.Request.Path["id"].(float64)) || m.args[3].Value != pair.Request.Path["folder"] {
							t.Fatal("rename changed selector or destination")
						}
					}
				}
				wantWrite := rr.Code < 300 || name == "write_unavailable" || name == "concurrent"
				if (writes == 1) != wantWrite || writes > 1 {
					t.Fatalf("writes=%d want %t", writes, wantWrite)
				}
				if !wantWrite && rr.Code != 404 && rr.Code != 409 && name != "source_unavailable" && name != "destination_unavailable" && len(calls) != 0 {
					t.Fatalf("refusal reached folder storage:%v", calls)
				}
				if name == "source_unavailable" && len(calls) != 1 {
					t.Fatal("failed source count proceeded")
				}
				if name == "destination_unavailable" && len(calls) != (map[bool]int{true: 1, false: 2}[op == "createFolder"]) {
					t.Fatal("failed destination count proceeded")
				}
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if _, ok := folderMutationPaths[w.OperationID]; ok {
				t.Errorf("folder mutation warning:%v", w)
			}
		}
	}
}
func TestFolderMutationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for folder mutation exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newFolderMutationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for op, path := range folderMutationPaths {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				s, ok := f.scenarios[name]
				if !ok {
					t.Fatalf("missing scenario %s", name)
				}
				method := http.MethodPost
				if op == "renameFolder" {
					method = http.MethodPatch
				}
				checkQueueMutationPairWithVertrag(t, binary, bulkQueueExampleScenario{s, nil}, method, path, name, pair, 513)
			})
		}
	}
}
