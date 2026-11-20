package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

// exportSessionRoutes are the two operations that open and close an audited
// export (#2116 AC03b).
var exportSessionRoutes = map[string]string{
	"createExportSession": "/api/v1/accounts/{id}/exports",
	"closeExportSession":  "/api/v1/accounts/{id}/exports/{session}/close",
}

// Every example pair of the two operations, sent through the real router on
// PostgreSQL as the account itself. A fault is a real one in the pair's own
// scratch database: the export_sessions table gone for store_failure, the
// mailboxes table out of reach for unavailable. The answers whose ids and times
// the server makes (each opened, closed) are compared with those taken from the
// example, and each export opened is closed again, answering its own data and
// format.
func TestExportSessionExamples(t *testing.T) {
	useRealGrants(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for op, route := range exportSessionRoutes {
		if len(pairs[op]) < 10 {
			t.Fatalf("incomplete %s: %d pairs", op, len(pairs[op]))
		}
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				examplepairs.Proven(t, op, name)
				f := newCredentialFixtureWith(t, &config.Config{Environment: "test"})
				if name == "request_too_large" {
					// The same server with a body cap under the example's body.
					capped := NewRouters(f.db, f.jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 8}, nil)
					t.Cleanup(capped.Close)
					f.router = capped.Public
				}
				authorization := f.session
				id := strconv.FormatUint(uint64(f.mailboxID), 10)
				session, _ := pair.Request.Path["session"].(string)
				switch name {
				case "invalid_id":
					id = "0"
				case "missing":
					id = "4000000000"
				case "unauthenticated":
					authorization = ""
				case "api_key_ceiling":
					_, key, err := repositories.NewCredentialRepository(f.db).CreateAPIKey(f.mailboxID, f.mailboxID, "read only",
						[]auth.ScopedPermission{{Capability: models.CapMailRead, Scope: grants.Mailbox(f.address)}}, nil)
					if err != nil {
						t.Fatal(err)
					}
					authorization = "Bearer " + key
				}
				if op == "closeExportSession" && (name == "closed" || name == "store_failure") {
					rr := f.do(http.MethodPost, "/api/v1/accounts/"+id+"/exports", f.session, `{"data":"mail","format":"maildir"}`)
					if rr.Code != http.StatusCreated {
						t.Fatalf("open: %d %s", rr.Code, rr.Body)
					}
					var opened struct{ Data struct{ ID string } }
					decodeInto(t, rr, &opened)
					session = opened.Data.ID
				}
				switch name {
				case "store_failure":
					if err := f.db.Exec("DROP TABLE export_sessions").Error; err != nil {
						t.Fatal(err)
					}
				case "unavailable":
					if err := f.db.Exec("ALTER TABLE mailboxes RENAME TO mailboxes_out_of_reach").Error; err != nil {
						t.Fatal(err)
					}
				}
				path := strings.NewReplacer("{id}", id, "{session}", session).Replace(route)
				body := string(pair.Request.Body)
				if name == "malformed_json" {
					body = "{"
				}
				var rr *httptest.ResponseRecorder
				if name == "rate_limited" {
					req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
					req.Header.Set("Content-Type", "application/json")
					rec := httptest.NewRecorder()
					apiKeyRefusalLimited(t).ServeHTTP(rec, req)
					rr = rec
				} else {
					rr = f.do(http.MethodPost, path, authorization, body)
				}
				if rr.Code != pair.Response.Status {
					t.Fatalf("got %d %s, want %d", rr.Code, rr.Body, pair.Response.Status)
				}
				if !strings.HasPrefix(rr.Header().Get("Content-Type"), pair.Response.MediaType) {
					t.Fatalf("media type %q, want %q", rr.Header().Get("Content-Type"), pair.Response.MediaType)
				}
				if name != "closed" && !strings.HasPrefix(name, "opened") {
					assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
					return
				}
				// The server's own id, account and times stand in for the example's.
				var want, got struct{ Data map[string]any }
				if err := json.Unmarshal(pair.Response.Body, &want); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got.Data["id"] != session && name == "closed" || got.Data["account_id"] != float64(f.mailboxID) {
					t.Fatalf("answered %s for session %s of account %d", rr.Body, session, f.mailboxID)
				}
				for _, made := range []string{"id", "account_id", "started_at", "ended_at"} {
					if _, ok := want.Data[made]; ok {
						if _, ok := got.Data[made]; !ok {
							t.Fatalf("the answer has no %s: %s", made, rr.Body)
						}
						got.Data[made] = want.Data[made]
					}
				}
				if !reflect.DeepEqual(got.Data, want.Data) {
					t.Fatalf("got %s, want %s", rr.Body, pair.Response.Body)
				}
				if op == "createExportSession" {
					closed := f.do(http.MethodPost, "/api/v1/accounts/"+id+"/exports/"+opened(t, rr)+"/close", f.session,
						`{"result":"aborted","rows":0,"bytes":0}`)
					var summary struct{ Data map[string]any }
					decodeInto(t, closed, &summary)
					if closed.Code != http.StatusOK || summary.Data["data"] != want.Data["data"] ||
						summary.Data["format"] != want.Data["format"] || summary.Data["result"] != "aborted" {
						t.Fatalf("close: %d %s", closed.Code, closed.Body)
					}
				}
			})
		}
	}
}

// opened is the id of the export rr opened.
func opened(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var answer struct{ Data struct{ ID string } }
	decodeInto(t, rr, &answer)
	if answer.Data.ID == "" {
		t.Fatalf("no export id: %s", rr.Body)
	}
	return answer.Data.ID
}
