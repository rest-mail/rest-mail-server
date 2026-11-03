package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

func TestDMARCReportExamplePairs(t *testing.T) {
	f := newGrantsFixture(t, fixtureAdmin{name: "dmarc-domain"})
	if rr := f.grant(t, f.token(t, "owner"), f.admins["dmarc-domain"].user.ID, "admin", "domain:a.test"); rr.Code != 201 {
		t.Fatalf("grant: %d %s", rr.Code, rr.Body)
	}
	report := models.DMARCReportReceived{DomainID: f.a.ID, Reporter: "reporter.test", ReportingOrg: "Reporter", ReportID: "daily-1", StartDate: time.Unix(1793577600, 0).UTC(), EndDate: time.Unix(1793663999, 0).UTC(), ReceivedAt: time.Unix(1793664000, 0).UTC(), PolicyPublished: models.DMARCReceivedPolicy{Domain: "a.test", P: "none"}, Records: []models.DMARCReceivedRecord{{Row: models.DMARCReceivedRow{SourceIP: "192.0.2.1", Count: 12, PolicyEvaluated: models.DMARCReceivedEvaluation{Disposition: "none", DKIM: "pass", SPF: "fail"}}, Identifiers: models.DMARCReceivedIdentifiers{HeaderFrom: "a.test"}, AuthResults: models.DMARCReceivedAuthResults{DKIM: []models.DMARCReceivedDKIM{{Domain: "a.test", Selector: "mail", Result: "pass"}}}}}}
	if err := f.db.Create(&report).Error; err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{"viewer": f.token(t, "viewer"), "nobody": f.token(t, "nobody"), "domain": f.token(t, "dmarc-domain")}
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	recorded := oapifly.ExampleFile{"listDmarcReports": {}, "getDmarcReport": {}}
	for _, tc := range []struct {
		op, name, principal, suffix string
		status                      int
	}{
		{"listDmarcReports", "available", "viewer", "?domain_id=4294967295&limit=20", 200},
		{"listDmarcReports", "received", "viewer", "?reporter=reporter.test&limit=20", 200},
		{"listDmarcReports", "domain_unfiltered", "domain", "", 200},
		{"listDmarcReports", "domain_own_filter", "domain", fmt.Sprintf("?domain_id=%d", f.a.ID), 200},
		{"listDmarcReports", "domain_other_filter", "domain", fmt.Sprintf("?domain_id=%d", f.b.ID), 200},
		{"listDmarcReports", "domain_missing_filter", "domain", "?domain_id=4294967295", 200},
		{"listDmarcReports", "required_capability", "nobody", "", 403},
		{"listDmarcReports", "unauthenticated", "", "", 401},
		{"listDmarcReports", "invalid_limit", "viewer", "?limit=not-an-integer", 422},
		{"listDmarcReports", "invalid_domain", "viewer", "?domain_id=-1", 422},
		{"listDmarcReports", "invalid_reporter", "viewer", "?reporter=" + strings.Repeat("x", 256), 422},
		{"getDmarcReport", "received", "viewer", fmt.Sprintf("/%d", report.ID), 200},
		{"getDmarcReport", "domain_own", "domain", fmt.Sprintf("/%d", report.ID), 200},
		{"getDmarcReport", "missing", "viewer", "/4294967295", 404},
		{"getDmarcReport", "required_capability", "nobody", "/1", 403},
		{"getDmarcReport", "unauthenticated", "", "/1", 401},
		{"getDmarcReport", "invalid_id", "viewer", "/0", 422},
	} {
		t.Run(tc.op+"/"+tc.name, func(t *testing.T) {
			request := oapifly.ExampleRequest{}
			if tc.op == "getDmarcReport" {
				request.Path = map[string]any{"id": strings.TrimPrefix(tc.suffix, "/")}
			} else {
				query, err := url.ParseQuery(strings.TrimPrefix(tc.suffix, "?"))
				if err != nil {
					t.Fatal(err)
				}
				if len(query) > 0 {
					request.Query = map[string]any{}
					for name, values := range query {
						request.Query[name] = values[0]
					}
				}
			}
			if tc.status == 422 {
				request.Invalid = true
			}
			rr := doRequest(f.router, "GET", "/api/v1/admin/dmarc-reports"+tc.suffix, tokens[tc.principal], "")
			if rr.Code != tc.status {
				t.Fatalf("got %d want %d: %s", rr.Code, tc.status, rr.Body)
			}
			pair := oapifly.ExamplePair{Summary: strings.ReplaceAll(tc.name, "_", " "), Request: request, Response: oapifly.ExampleResponse{Status: rr.Code, MediaType: "application/json", Body: bytes.TrimSpace(rr.Body.Bytes())}}
			if tc.principal == "nobody" {
				pair.Requires = map[string]string{"principal": "without_capability"}
			} else if tc.principal == "" {
				pair.Requires = map[string]string{"principal": "anonymous"}
			}
			recorded[tc.op][tc.name] = pair
			if os.Getenv("RESTMAIL_DMARC_EXAMPLES_RECORD") == "" {
				want, ok := pairs[tc.op][tc.name]
				if !ok {
					t.Fatal("named pair missing")
				}
				examplepairs.Proven(t, tc.op, tc.name)
				if want.Response.Status != rr.Code {
					t.Fatal("status changed")
				}
				for kind, value := range pair.Requires {
					if want.Requires[kind] != value {
						t.Fatal("example lost its required principal")
					}
				}
				assertPublicExampleBody(t, want.Response.Body, rr.Body.Bytes())
			}
		})
	}
	if path := os.Getenv("RESTMAIL_DMARC_EXAMPLES_RECORD"); path != "" {
		data, err := oapifly.MarshalExamples(recorded)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDMARCReportDependencyExamples(t *testing.T) {
	unavailable, failed, _, jwt := paginatedReadRouters(t)
	session, err := jwt.GenerateSession(10, "user@example.com", 0, []string{"pwd", "otp"}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"listDmarcReports", "getDmarcReport"} {
		path := "/api/v1/admin/dmarc-reports"
		if operation == "getDmarcReport" {
			path += "/1"
		}
		for _, tc := range []struct {
			name    string
			status  int
			handler http.Handler
		}{
			{"internal_error", 500, failed}, {"session_unavailable", 503, unavailable}, {"request_too_large", 413, failed},
			{"rate_limited", 429, apiKeyRefusalLimited(t)},
		} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				request := httptest.NewRequest("GET", path, nil)
				request.Header.Set("Authorization", "Bearer "+session.AccessToken)
				if tc.status == 413 {
					request.ContentLength = 9
				}
				rr := httptest.NewRecorder()
				tc.handler.ServeHTTP(rr, request)
				if rr.Code != tc.status {
					t.Fatalf("got %d want %d: %s", rr.Code, tc.status, rr.Body)
				}
				pairs, err := examplepairs.Read()
				if err != nil {
					t.Fatal(err)
				}
				pair, ok := pairs[operation][tc.name]
				if !ok {
					t.Fatal("dependency pair missing")
				}
				examplepairs.Proven(t, operation, tc.name)
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			})
		}
	}
}

func TestDMARCReportDescription(t *testing.T) {
	paths, schemas := describedAPI(t)
	for path, operation := range map[string]string{"/api/v1/admin/dmarc-reports": "listDmarcReports", "/api/v1/admin/dmarc-reports/{id}": "getDmarcReport"} {
		op := paths[path]["get"]
		if op.OperationID != operation {
			t.Fatalf("missing %s", operation)
		}
		for _, status := range []string{"200", "401", "403", "413", "422", "429", "500", "503"} {
			if _, ok := op.Responses[status]; !ok {
				t.Errorf("%s lacks %s", operation, status)
			}
		}
	}
	data, err := json.Marshal(schemas)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"maxItems":100`)) || !bytes.Contains(data, []byte(`"maxLength":255`)) {
		t.Fatal("bounded reports lost schema limits")
	}
}
