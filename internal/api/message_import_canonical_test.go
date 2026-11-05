package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/rest-mail/rest-mail-server/internal/auth"
)

type importScenario struct {
	name, mode string
	status     int
}

var importScenarios = []importScenario{
	{"created", "", 200}, {"duplicate", "", 200}, {"always_store", "", 200}, {"default_folder", "", 200},
	{"missing_message_id", "", 200}, {"binary_source", "", 200}, {"api_key", "", 200},
	{"body_array", "", 422}, {"unauthenticated", "", 401},
	{"insufficient_permissions", "denied", 404}, {"missing_scoped_target", "missing", 404},
	{"unresolved_scoped_id", "", 422}, {"api_key_ceiling", "key_ceiling", 403},
	{"api_key_holder_revoked", "denied", 404}, {"csrf", "", 403}, {"setup_pending", "setup", 403},
	{"missing", "disappeared", 404}, {"mailbox_full", "full", 409},
	{"decoded_too_large", "", 413}, {"request_too_large", "body_limit", 413},
	{"missing_raw", "", 422}, {"raw_wrong_type", "", 422}, {"missing_date", "", 422}, {"invalid_folder", "", 422},
	{"keywords", "", 200}, {"deleted", "", 200},
	{"invalid_keyword", "", 422}, {"keyword_too_long", "", 422}, {"too_many_keywords", "", 422},
	{"invalid_base64", "", 422}, {"invalid_id", "global", 422},
	{"read_failure", "read_failure", 500}, {"insert_failure", "insert_failure", 500},
	{"quota_failure", "quota_failure", 500}, {"audit_failure", "audit_failure", 500},
	{"unavailable", "scope_failure", 503},
}

func importScenarioRequest(t *testing.T, tc importScenario) oapifly.ExampleRequest {
	t.Helper()
	body := importExampleBody()
	id := 12
	switch tc.name {
	case "always_store":
		body["always_store"] = true
	case "default_folder":
		delete(body, "folder")
	case "missing_message_id":
		body["raw_message"] = []byte("From: old@example.test\r\n\r\nhistory\r\n")
	case "binary_source":
		body["raw_message"] = []byte("Message-ID: <binary@example.test>\r\nFrom: old@example.test\r\n\r\nhistory\xff\x00\r\n")
	case "decoded_too_large":
		body["raw_message"] = bytes.Repeat([]byte("x"), 1025)
	case "missing_raw":
		delete(body, "raw_message")
	case "raw_wrong_type":
		body["raw_message"] = 12345
	case "missing_date":
		delete(body, "internal_date")
	case "invalid_folder":
		body["folder"] = "bad\x00folder"
	case "keywords":
		body["flags"] = []string{"$Custom"}
	case "deleted":
		body["flags"] = []string{`\Deleted`}
	case "invalid_keyword":
		body["flags"] = []string{"bad]keyword"}
	case "keyword_too_long":
		body["flags"] = []string{strings.Repeat("x", 256)}
	case "too_many_keywords":
		flags := make([]string, 101)
		for index := range flags {
			flags[index] = fmt.Sprintf("$keyword%d", index)
		}
		body["flags"] = flags
	case "invalid_base64":
		body["raw_message"] = "!not-base64!"
	case "invalid_id", "unresolved_scoped_id":
		id = 0
	case "api_key_ceiling":
		// The key's own account: it may know of it, so it is told what it lacks.
		id = 1
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if tc.name == "body_array" {
		payload = []byte("[]")
	}
	return oapifly.ExampleRequest{Path: map[string]any{"id": id}, Body: payload, MediaType: "application/json", Invalid: tc.name == "body_array" || tc.name == "missing_raw" || tc.name == "raw_wrong_type" || tc.name == "missing_date"}
}

func prepareImportScenario(t *testing.T, tc importScenario, f *importFixture) {
	t.Helper()
	if tc.name == "duplicate" || tc.name == "always_store" {
		w := f.request(t, importExampleBody(), "12", f.token)
		if w.Code != 200 {
			t.Fatalf("seed import: %d %s", w.Code, w.Body)
		}
	}
}

func authorizeImportScenario(tc importScenario, f *importFixture, r *http.Request) {
	if tc.name == "unauthenticated" {
		return
	}
	if tc.name == "csrf" {
		r.AddCookie(&http.Cookie{Name: auth.AccessCookieName, Value: f.token})
		return
	}
	token := f.token
	if strings.HasPrefix(tc.name, "api_key") {
		token = f.key
	}
	r.Header.Set("Authorization", "Bearer "+token)
}

func TestMessageImportCanonicalExamples(t *testing.T) {
	useRealGrants(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	recorded := oapifly.ExampleFile{"importMessage": {}}
	for _, tc := range importScenarios {
		t.Run(tc.name, func(t *testing.T) {
			f := newImportFixture(t, tc.mode)
			prepareImportScenario(t, tc, f)
			request := importScenarioRequest(t, tc)
			id, _ := json.Marshal(request.Path["id"])
			r := httptest.NewRequest("POST", "/api/v1/accounts/"+string(id)+"/messages/import", bytes.NewReader(request.Body))
			r.Header.Set("Content-Type", request.MediaType)
			authorizeImportScenario(tc, f, r)
			w := httptest.NewRecorder()
			f.router.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("got %d, want %d: %s; queries=%v", w.Code, tc.status, w.Body, f.c.queries)
			}
			if strings.Contains(w.Body.String(), "private") {
				t.Fatal("storage detail leaked")
			}
			if tc.status >= 400 && len(f.c.state.messages) != 0 {
				t.Fatal("refused request retained mail")
			}
			if strings.HasSuffix(tc.name, "_failure") && !f.c.failed {
				t.Fatal("storage fault not reached")
			}
			pair := oapifly.ExamplePair{Request: request, Response: oapifly.ExampleResponse{Status: w.Code, Body: append(json.RawMessage(nil), w.Body.Bytes()...), MediaType: "application/json"}}
			recorded["importMessage"][tc.name] = pair
			if os.Getenv("RESTMAIL_IMPORT_RECORD") == "" {
				want, ok := pairs["importMessage"][tc.name]
				examplepairs.Proven(t, "importMessage", tc.name)
				if !ok {
					t.Fatal("canonical pair missing")
				}
				if want.Response.Status != w.Code {
					t.Fatal("canonical status differs")
				}
				assertPublicExampleBody(t, want.Response.Body, w.Body.Bytes())
				var a, b any
				_ = json.Unmarshal(want.Request.Body, &a)
				_ = json.Unmarshal(request.Body, &b)
				if !reflect.DeepEqual(a, b) || string(mustImportJSON(t, want.Request.Path)) != string(mustImportJSON(t, request.Path)) || want.Request.Invalid != request.Invalid {
					t.Fatal("canonical request differs from executed request")
				}
			}
		})
	}
	if path := os.Getenv("RESTMAIL_IMPORT_RECORD"); path != "" {
		data, err := oapifly.MarshalExamples(recorded)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// malformed_json is not an import scenario: its wire body is the raw byte {,
// which the scenario request cannot encode and the per-pair Vertrag hook would
// send JSON-quoted. The production router refuses it before the handler runs.
func TestMessageImportMalformedBodyExample(t *testing.T) {
	useRealGrants(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	pair, ok := pairs["importMessage"]["malformed_json"]
	if !ok {
		t.Fatal("importMessage has no malformed_json pair")
	}
	t.Run("malformed_json", func(t *testing.T) {
		examplepairs.Proven(t, "importMessage", "malformed_json")
		f := newImportFixture(t, "")
		id := string(mustImportJSON(t, pair.Request.Path["id"]))
		r := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/"+id+"/messages/import", strings.NewReader("{"))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+f.token)
		w := httptest.NewRecorder()
		f.router.ServeHTTP(w, r)
		if w.Code != pair.Response.Status {
			t.Fatalf("got %d %s, want %d", w.Code, w.Body, pair.Response.Status)
		}
		if !strings.HasPrefix(w.Header().Get("Content-Type"), pair.Response.MediaType) {
			t.Fatalf("media type %q, want %q", w.Header().Get("Content-Type"), pair.Response.MediaType)
		}
		assertPublicExampleBody(t, pair.Response.Body, w.Body.Bytes())
		if len(f.c.state.messages) != 0 {
			t.Fatal("refused request retained mail")
		}
	})
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if w.OperationID == "importMessage" {
				t.Errorf("message import warning: %v", w)
			}
		}
	}
}

func mustImportJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
