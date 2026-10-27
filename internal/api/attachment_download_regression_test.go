package api

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/attachmentstore"
	"github.com/restmail/restmail/internal/attachmentstore/s3test"
	"github.com/restmail/restmail/internal/config"
)

const attachmentDownloadBytes = "attachment café\r\n"

type attachmentDownloadConnection struct {
	personalDataReadConnection
	fullRef, emptyRef string
}

func (c attachmentDownloadConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c attachmentDownloadConnection) Driver() driver.Driver { return c }

func (c attachmentDownloadConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c attachmentDownloadConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, `FROM "attachments"`) {
		if len(args) != 2 || !strings.Contains(query, `"attachments"."id" =`) || args[1].Value != int64(1) {
			return nil, errors.New("attachment lost ID or single-row bound")
		}
		id := fmt.Sprint(args[0].Value)
		if id == "3" {
			return nil, errors.New("private attachment lookup detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "message_id", "filename", "content_type", "storage_type", "storage_ref"}}
		if id == "2147483647" {
			return r, nil
		}
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return nil, err
		}
		ref, storage, filename := c.fullRef, "s3", "résumé.html\r\nX-Injected: yes"
		switch id {
		case "2":
			ref = c.emptyRef
			filename = "empty.txt"
		case "4":
			ref = "attachments/" + strings.Repeat("b", 64)
		case "6":
			storage = "unknown"
		case "7":
			storage = "filesystem"
			ref = "/outside/private"
		case "8":
			storage = "filesystem"
			ref = "/attachments/restmail-contract-missing-73ef"
		}
		r.rows = [][]driver.Value{{n, n, filename, "text/html", storage, ref}}
		return r, nil
	}
	if strings.Contains(query, `FROM "messages"`) {
		if len(args) != 1 || !strings.Contains(query, `SELECT "mailbox_id"`) || !strings.Contains(query, "id =") {
			return nil, errors.New("attachment lost message link")
		}
		id := fmt.Sprint(args[0].Value)
		if id == "5" {
			return nil, errors.New("private attachment message link detail")
		}
		r := &authReadSnapshotRows{columns: []string{"mailbox_id"}}
		if id == "9" {
			return r, nil
		}
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return nil, err
		}
		if id == "4" || id == "6" || id == "7" || id == "8" {
			n = 1
		}
		if id == "11" {
			n = 3
		}
		r.rows = [][]driver.Value{{n}}
		return r, nil
	}
	return c.personalDataReadConnection.QueryContext(ctx, query, args)
}

func attachmentDownloadScenarios(t *testing.T) (map[string]accountReadScenario, *s3test.Server) {
	t.Helper()
	fake := s3test.New(t)
	store := attachmentstore.NewS3(fake.Config)
	full, _, err := store.Put(t.Context(), []byte(attachmentDownloadBytes))
	if err != nil {
		t.Fatal(err)
	}
	empty, _, err := store.Put(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := accountListScenarios(t, func(mode string, keys map[string]accountListKey) driver.Connector {
		return attachmentDownloadConnection{personalDataReadConnection{accountCredentialListConnection{mode: mode, keys: keys}}, full, empty}
	}, func(cfg *config.Config) { cfg.Attachments = fake.Config })
	out["domain_grant"] = out["domain_manager"]
	out["app_password_other"] = out["app_password"]
	for _, name := range []string{"mailbox_unavailable", "message_unavailable", "source_missing", "missing_message", "unknown_storage", "invalid_storage_path", "file_missing",
		"range_single", "not_modified", "precondition_failed", "range_not_satisfiable"} {
		out[name] = out["found"]
	}
	normal := out["found"]
	out["object_unavailable"] = accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.Failure.Store(403)
		defer fake.Failure.Store(0)
		normal.handler.ServeHTTP(w, r)
	}), normal.token}
	return out, fake
}

func TestAttachmentDownloadFailuresAreNotMissing(t *testing.T) {
	scenarios, fake := attachmentDownloadScenarios(t)
	for _, tc := range []struct{ id, scenario string }{{"3", "found"}, {"5", "found"}, {"11", "found"}, {"1", "grant_unavailable"}, {"1", "ownership_unavailable"}} {
		s := scenarios[tc.scenario]
		req := httptest.NewRequest(http.MethodGet, "/api/v1/attachments/"+tc.id, nil)
		req.Header.Set("Authorization", "Bearer "+s.token)
		rr := httptest.NewRecorder()
		before := fake.Requests.Load()
		s.handler.ServeHTTP(rr, req)
		if rr.Code != 500 || rr.Body.String() != "{\"error\":{\"code\":\"internal_error\",\"message\":\"Internal server error\"}}\n" {
			t.Errorf("%s/%s: got %d %s, want sanitized 500", tc.id, tc.scenario, rr.Code, rr.Body.String())
		}
		if fake.Requests.Load() != before {
			t.Error("failed authorization reached object storage")
		}
	}
}

func TestAttachmentDownloadRangeAndConditionalWiring(t *testing.T) {
	scenarios, _ := attachmentDownloadScenarios(t)
	s := scenarios["found"]
	get := func(headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/attachments/1", nil)
		req.Header.Set("Authorization", "Bearer "+s.token)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rr := httptest.NewRecorder()
		s.handler.ServeHTTP(rr, req)
		return rr
	}
	full := get(nil)
	if full.Code != 200 || full.Body.String() != attachmentDownloadBytes || full.Header().Get("ETag") == "" {
		t.Fatalf("initial download: %d %q", full.Code, full.Body.String())
	}
	for _, tc := range []struct {
		headers map[string]string
		status  int
		body    string
	}{{map[string]string{"Range": "bytes=0-9"}, 206, "attachment"}, {map[string]string{"If-None-Match": full.Header().Get("ETag")}, 304, ""}, {map[string]string{"If-Match": "\"other\""}, 412, ""}, {map[string]string{"Range": "bytes=999-"}, 416, "invalid range: failed to overlap\n"}, {map[string]string{"If-Range": "\"other\"", "Range": "bytes=0-9"}, 200, attachmentDownloadBytes}} {
		rr := get(tc.headers)
		if rr.Code != tc.status || rr.Body.String() != tc.body {
			t.Errorf("%v: got %d %q", tc.headers, rr.Code, rr.Body.String())
		}
		if tc.status == 206 && rr.Header().Get("Content-Range") != fmt.Sprintf("bytes 0-9/%d", len(attachmentDownloadBytes)) {
			t.Fatal("range lost content bounds")
		}
	}
}
