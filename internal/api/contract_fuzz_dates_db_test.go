package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// fuzzDateFindings are the date rows of the contract-fuzz board (#2051), by finding key. Each
// query parameter takes an RFC 3339 date-time and nothing else: the description says so with
// format date-time, which its draft-07 dialect asserts, and in its words, and the handler
// refuses anything else 422. The nightly's "a" was drawn as a valid value only because Vertrag
// drew strings that ignore the format (antimatter-studios/vertrag#131); neither side is wrong.
var fuzzDateFindings = []struct {
	finding, path, query string
}{
	{"100deb689625b741", "/api/v1/admin/audit", "since"},
	{"280e3f2691bb5dc2", "/api/v1/admin/audit", "until"},
	{"bfa5ec1d849e119b", "/api/v1/admin/logs/delivery", "since"},
	{"2776c64da865e0fb", "/api/v1/admin/logs/delivery", "until"},
	{"2b00c426d427f8a0", "/api/v1/admin/api-keys", "expires_before"},
	{"b26abd109ed1ddbf", "/api/v1/admin/api-keys", "last_used_before"},
	{"5a849c51b43ffe79", "/api/v1/accounts/{id}/search", "before"},
	{"eda23c548c0a28ab", "/api/v1/accounts/{id}/search", "after"},
}

// The description states the rule twice, as a format a validator asserts and in words a
// reader can follow, and the two never disagree with the handler.
func TestFuzzFindingDateParametersAreDescribed(t *testing.T) {
	paths := fuzzFindingDocument(t)["paths"].(map[string]interface{})
	for _, row := range fuzzDateFindings {
		t.Run(row.finding+"/"+row.query, func(t *testing.T) {
			operation := paths[row.path].(map[string]interface{})["get"].(map[string]interface{})
			for _, raw := range operation["parameters"].([]interface{}) {
				parameter := raw.(map[string]interface{})
				if parameter["name"] != row.query {
					continue
				}
				if format := parameter["schema"].(map[string]interface{})["format"]; format != "date-time" {
					t.Errorf("schema format = %v, want date-time", format)
				}
				description, _ := parameter["description"].(string)
				for _, want := range []string{"RFC 3339 date-time", "refused 422 validation_failed naming " + row.query} {
					if !strings.Contains(description, want) {
						t.Errorf("description does not say %q: %q", want, description)
					}
				}
				if _, ok := operation["responses"].(map[string]interface{})["422"]; !ok {
					t.Error("422 is not a documented response")
				}
				return
			}
			t.Fatal("query parameter missing")
		})
	}
}

// Through the production router on PostgreSQL: the nightly's minimized "a" is refused 422
// naming the parameter, and a real RFC 3339 date-time, with an offset, is answered 200.
func TestFuzzFindingDateParametersAgainstPostgres(t *testing.T) {
	fixture := newGrantsFixture(t)
	owner := fixture.token(t, "owner")
	for _, row := range fuzzDateFindings {
		path := strings.Replace(row.path, "{id}", fmt.Sprint(fixture.ma.ID), 1)
		t.Run(row.finding+"/"+row.query, func(t *testing.T) {
			refused := doRequest(fixture.router, http.MethodGet, path+"?"+row.query+"=a", owner, "")
			if refused.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%s=a: %d %s, want 422", row.query, refused.Code, refused.Body)
			}
			var answer struct {
				Error struct {
					Code    string `json:"code"`
					Details struct {
						Fields map[string]string `json:"fields"`
					} `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(refused.Body.Bytes(), &answer); err != nil {
				t.Fatal(err)
			}
			if answer.Error.Code != "validation_failed" || answer.Error.Details.Fields[row.query] != "must be a date-time" {
				t.Fatalf("%s=a answered %s, want validation_failed naming %s", row.query, refused.Body, row.query)
			}
			accepted := doRequest(fixture.router, http.MethodGet,
				path+"?"+row.query+"="+url.QueryEscape("2026-11-07T10:00:00+01:00"), owner, "")
			if accepted.Code != http.StatusOK {
				t.Fatalf("%s=2026-11-07T10:00:00+01:00: %d %s, want 200", row.query, accepted.Code, accepted.Body)
			}
		})
	}
}
