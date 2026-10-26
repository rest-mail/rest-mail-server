package api

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

type folderMutationConnection struct{ mailRemovalConnection }

func (c folderMutationConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c folderMutationConnection) Driver() driver.Driver { return c }

func (c folderMutationConnection) Open(string) (driver.Conn, error) { return c, nil }

func exampleInsertFields(q string, args []driver.NamedValue) (map[string]driver.Value, error) {
	start, end := strings.Index(q, "("), strings.Index(q, ") VALUES (")
	ret := strings.Index(q, " RETURNING")
	if start < 0 || end < start || ret < end {
		return nil, errors.New("unexpected marker insert")
	}
	cols := strings.Split(q[start+1:end], ",")
	valuesStart := end + len(") VALUES (")
	valuesEnd := strings.Index(q[valuesStart:], ")")
	if valuesEnd < 0 {
		return nil, errors.New("unterminated insert values")
	}
	values := strings.Split(q[valuesStart:valuesStart+valuesEnd], ",")
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
