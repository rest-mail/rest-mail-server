package api

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

func assertTestSnapshotRestoredColumns(t *testing.T, calls []bulkQueueExampleMutation) {
	t.Helper()
	var tables []string
	expected := map[string]map[string]driver.Value{
		"domains":        {"id": int64(11), "active": false, "default_quota_bytes": int64(0)},
		"mailboxes":      {"id": int64(21), "domain_id": int64(11), "active": false, "quota_bytes": int64(0)},
		"aliases":        {"id": int64(31), "domain_id": int64(11), "active": false},
		"messages":       {"id": int64(41), "mailbox_id": int64(21), "is_read": false, "message_id": "<snapshot-fixture@example.com>"},
		"outbound_queue": {"id": int64(51), "message_id": int64(41), "max_retries": int64(0)},
		"pipelines":      {"id": int64(61), "domain_id": int64(11), "active": false},
		"bans":           {"id": int64(71), "ip": "192.0.2.17"},
	}
	for _, call := range calls {
		if !strings.HasPrefix(call.query, "INSERT INTO ") {
			continue
		}
		match := testDataInsertTable.FindStringSubmatch(call.query)
		if len(match) != 2 {
			t.Fatal("invalid restore table")
		}
		table := match[1]
		tables = append(tables, table)
		fields, err := exampleInsertFields(call.query, call.args)
		if err != nil {
			t.Fatal(err)
		}
		for col, want := range expected[table] {
			if fields[col] != want {
				t.Errorf("restore %s.%s lost snapshot value: got %v want %v", table, col, fields[col], want)
			}
		}
		if table == "domains" || table == "mailboxes" {
			if !strings.Contains(call.query, "ON CONFLICT DO NOTHING") {
				t.Fatal("restore can overwrite retained administrator rows")
			}
		}
		for _, col := range []string{"domain", "mailbox", "mailboxes", "aliases", "messages", "quota_usage", "snapshot_at"} {
			if table == "outbound_queue" && col == "domain" {
				continue // The queue's destination domain is a real string column.
			}
			if _, ok := fields[col]; ok {
				t.Fatalf("restore inserted association/metadata column %s", col)
			}
		}
		if table == "messages" || table == "pipelines" {
			col, want := "headers", `{"X-Fixture":"true"}`
			if table == "pipelines" {
				col = "filters"
				want = `[{"name":"header_validate","enabled":false,"config":{"nested":[1,false]}}]`
			}
			value := fields[col]
			var raw []byte
			switch v := value.(type) {
			case string:
				raw = []byte(v)
			case []byte:
				raw = v
			default:
				t.Fatalf("%s JSON lost", col)
			}
			assertPublicExampleBody(t, json.RawMessage(want), raw)
		}
	}
	if strings.Join(tables, ",") != "domains,mailboxes,aliases,messages,outbound_queue,pipelines,bans" {
		t.Fatalf("restore order/associations changed: %v", tables)
	}
}

func TestSnapshotRestoreExamples(t *testing.T) {
	f := newTestSnapshotRestoreScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs["restoreTestSnapshot"]) < 25 {
		t.Fatal("restore examples incomplete")
	}
	for name, pair := range pairs["restoreTestSnapshot"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f[name]
			if !ok {
				t.Fatal("missing restore scenario")
			}
			before := 0
			if s.store != nil {
				before = len(s.store.snapshot())
			}
			body := []byte(pair.Request.Body)
			if name == "malformed_json" {
				body = []byte("{")
			}
			if name == "request_too_large" {
				body = bytes.Repeat([]byte("x"), 16385)
			}
			rr := mailboxSessionEndingRequest(t, s, http.MethodPost, testSnapshotRestorePath, body)
			if rr.Code != pair.Response.Status {
				t.Fatalf("status %d want%d", rr.Code, pair.Response.Status)
			}
			assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			var calls []bulkQueueExampleMutation
			if s.store != nil {
				calls = s.store.snapshot()[before:]
			}
			begins, commits, rollbacks, inserts := 0, 0, 0, 0
			var insertedTables []string
			for _, call := range calls {
				switch call.query {
				case "BEGIN":
					begins++
				case "COMMIT":
					commits++
				case "ROLLBACK":
					rollbacks++
				case "DATA_CONTEXT":
					if call.args[0].Value != true {
						t.Error("restore lost request context")
					}
				}
				if strings.HasPrefix(call.query, "INSERT INTO ") {
					inserts++
					m := testDataInsertTable.FindStringSubmatch(call.query)
					if len(m) == 2 {
						insertedTables = append(insertedTables, m[1])
					}
				}
			}
			if rr.Code == 200 {
				if begins != 1 || commits != 1 || rollbacks != 0 {
					t.Fatalf("restore transaction begin%d commit%d rollback%d", begins, commits, rollbacks)
				}
				if strings.HasPrefix(name, "snapshot") {
					assertTestSnapshotRestoredColumns(t, calls)
				} else if name == "sparse_id" {
					if inserts != 1 {
						t.Fatal("sparse fixture did not create exactly one domain")
					}
					for _, call := range calls {
						if !strings.HasPrefix(call.query, "INSERT INTO ") {
							continue
						}
						fields, err := exampleInsertFields(call.query, call.args)
						if err != nil {
							t.Fatal(err)
						}
						if _, explicit := fields["id"]; explicit || fields["name"] != "sparse.test" || fields["active"] != false || fields["default_quota_bytes"] != int64(0) {
							t.Fatal("sparse fixture lost generated ID or explicit scalar values")
						}
					}
				} else if inserts != 0 {
					t.Fatal("empty snapshot inserted rows")
				}
			} else if rr.Code == 500 {
				if begins != 1 || commits != 0 || (name != "commit_failure" && rollbacks != 1) {
					t.Fatal("failed restore escaped transaction")
				}
				if strings.HasPrefix(name, "failure_") {
					table := strings.TrimPrefix(name, "failure_")
					if len(insertedTables) == 0 || insertedTables[len(insertedTables)-1] != table {
						t.Fatalf("restore did not stop at selected failure %s: %v", table, insertedTables)
					}
				}
			} else if len(calls) != 0 {
				t.Fatal("refused restore accessed fixture resources")
			}
		})
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if w.OperationID == "restoreTestSnapshot" && gapWarningApplies(map[string]gapOperation{"restoreTestSnapshot": {}}, w) {
				t.Errorf("restore warning: %v", w)
			}
		}
	}
}

func TestSnapshotRestoreExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f := newTestSnapshotRestoreScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["restoreTestSnapshot"] {
		t.Run(name, func(t *testing.T) {
			checkQueueMutationPairWithVertrag(t, binary, f[name], http.MethodPost, testSnapshotRestorePath, name, pair, 16385)
		})
	}
}
