package handlers

import (
	"bytes"
	"encoding/json"
	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testRoutes(h *TestHandler) http.Handler {
	r := chi.NewRouter()
	pass := func(string) endpoint.Middleware {
		return endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }}
	}
	h.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, TestRouteChecks{At: pass})
	return r
}

// Model changes must update the generated fixture types before strict binding
// can refuse a snapshot captured by this same server.
func TestSnapshotFieldsFollowModels(t *testing.T) {
	pairs := [][3]reflect.Type{
		{reflect.TypeOf(models.Domain{}), reflect.TypeOf(TestSnapshotDomain{}), reflect.TypeOf(RestoreSnapshotDomain{})},
		{reflect.TypeOf(models.Mailbox{}), reflect.TypeOf(TestSnapshotMailbox{}), reflect.TypeOf(RestoreSnapshotMailbox{})},
		{reflect.TypeOf(models.Alias{}), reflect.TypeOf(TestSnapshotAlias{}), reflect.TypeOf(RestoreSnapshotAlias{})},
		{reflect.TypeOf(models.Message{}), reflect.TypeOf(TestSnapshotMessage{}), reflect.TypeOf(RestoreSnapshotMessage{})},
		{reflect.TypeOf(models.OutboundQueue{}), reflect.TypeOf(TestSnapshotOutboundQueue{}), reflect.TypeOf(RestoreSnapshotOutboundQueue{})},
		{reflect.TypeOf(models.Pipeline{}), reflect.TypeOf(TestSnapshotPipeline{}), reflect.TypeOf(RestoreSnapshotPipeline{})},
		{reflect.TypeOf(models.Ban{}), reflect.TypeOf(TestSnapshotBan{}), reflect.TypeOf(RestoreSnapshotBan{})},
		{reflect.TypeOf(models.QuotaUsage{}), reflect.TypeOf(TestSnapshotQuotaUsage{}), reflect.TypeOf(RestoreSnapshotQuotaUsage{})},
	}
	for _, pair := range pairs {
		fields := func(typ reflect.Type) map[string]reflect.StructField {
			out := map[string]reflect.StructField{}
			for i := 0; i < typ.NumField(); i++ {
				f := typ.Field(i)
				tag := f.Tag.Get("json")
				if !f.IsExported() || tag == "-" {
					continue
				}
				name, _, _ := strings.Cut(tag, ",")
				if name == "" {
					name = f.Name
				}
				out[name] = f
			}
			return out
		}
		want := fields(pair[0])
		for _, typ := range pair[1:] {
			got := fields(typ)
			if len(got) != len(want) {
				t.Errorf("%s has %d JSON fields, model %s has %d; run go generate", typ, len(got), pair[0], len(want))
			}
			for key, f := range want {
				actual, ok := got[key]
				if !ok || actual.Type.Kind() != f.Type.Kind() {
					t.Errorf("%s.%s no longer matches %s; run go generate", typ, key, pair[0])
				}
				if typ == pair[1] && actual.Tag.Get("json") != f.Tag.Get("json") {
					t.Errorf("%s.%s changed JSON presence", typ, key)
				}
			}
		}
	}
}

func TestSnapshotJSONRoundTripThroughBinding(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	for _, original := range []testSnapshotModels{
		{SnapshotAt: now},
		{
			Domains:    []models.Domain{{ID: 1, Name: "mail1.test", DKIMPrivateKey: "hidden", Active: true}},
			Mailboxes:  []models.Mailbox{{ID: 2, DomainID: 1, Address: "alice@mail1.test", LastLoginAt: &now}},
			Aliases:    []models.Alias{{ID: 3, DomainID: 1, SourceAddress: "alias@mail1.test", DestinationAddress: "alice@mail1.test"}},
			Messages:   []models.Message{{ID: 4, MailboxID: 2, MsgID: "<test@mail1.test>", RecipientsTo: models.JSONB(`["alice@mail1.test"]`), Headers: models.JSONB(`{"X-Test":"value"}`)}},
			Queue:      []models.OutboundQueue{{ID: 5, Status: "pending"}},
			Pipelines:  []models.Pipeline{{ID: 6, DomainID: 1, Filters: json.RawMessage(`[{"name":"test","config":{"flag":true}}]`)}},
			Bans:       []models.Ban{{ID: 7, IP: "192.0.2.1", ExpiresAt: &now}},
			SnapshotAt: now,
		},
	} {
		snapshot, err := snapshotJSON[TestSnapshot](original)
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(oapifly.EmptyLists(snapshot))
		if err != nil {
			t.Fatal(err)
		}
		want, err := snapshotJSON[any](original)
		if err != nil {
			t.Fatal(err)
		}
		var got any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("snapshot changed model JSON: got %s; want %s", body, mustJSON(want))
		}

		r := chi.NewRouter()
		reached := false
		endpoint.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, endpoint.Operation{Method: http.MethodPost, Path: "/restore"}, func(_ *http.Request, in RestoreTestSnapshot) (TestDataStatus, error) {
			reached = true
			restored, err := snapshotJSON[testSnapshotModels](in)
			if err != nil {
				return TestDataStatus{}, err
			}
			actual, err := snapshotJSON[any](restored)
			if err != nil {
				return TestDataStatus{}, err
			}
			if !reflect.DeepEqual(actual, want) {
				t.Errorf("restore changed model JSON: got %s; want %s", mustJSON(actual), mustJSON(want))
			}
			return TestDataStatus{Status: "restore_complete"}, nil
		})
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/restore", bytes.NewReader(body)))
		if rr.Code != 200 || !reached {
			t.Fatalf("full snapshot rejected: %d %s", rr.Code, rr.Body)
		}
	}
}

func TestTestRoutesBindingFailures(t *testing.T) {
	h := testRoutes(NewTestHandler(nil, &config.Config{Environment: "test"}))
	for _, tc := range []struct {
		path, body     string
		status         int
		field, message string
	}{
		{"send", `{`, 400, "", ""},
		{"send", `[]`, 400, "", ""},
		{"send", `{"to":"alice@mail1.test"}`, 422, "from", "required"},
		{"send", `{"from":"","to":"alice@mail1.test"}`, 422, "from", "sender address required"},
		{"send", `{"from":"alice@mail1.test","to":3}`, 422, "to", ""},
		{"send", `{"from":"alice@mail1.test","to":"bob@mail1.test","extra":true}`, 422, "extra", "not allowed"},
		{"probe", `{"services":["ftp"]}`, 422, "services", `unknown service "ftp": want smtp, smtp_submission, imap, pop3, dns or db`},
		{"snapshot/restore", `{"unexpected":true}`, 422, "unexpected", "not allowed"},
	} {
		t.Run(tc.path+tc.body, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/admin/test/"+tc.path, strings.NewReader(tc.body)))
			if rr.Code != tc.status {
				t.Fatalf("got %d: %s", rr.Code, rr.Body)
			}
			if tc.field != "" {
				var response struct {
					Error struct {
						Details struct {
							Fields map[string]string `json:"fields"`
						} `json:"details"`
					} `json:"error"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if _, ok := response.Error.Details.Fields[tc.field]; !ok {
					t.Errorf("missing %s: %s", tc.field, rr.Body)
				}
				if tc.message != "" && response.Error.Details.Fields[tc.field] != tc.message {
					t.Errorf("wrong message: %s", rr.Body)
				}
				if tc.field == "from" && len(response.Error.Details.Fields) != 1 {
					t.Errorf("refusal names an unbroken field: %s", rr.Body)
				}
			}
		})
	}
}
