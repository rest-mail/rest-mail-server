package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// Through the production router and a real database: a script that requires an
// extension REST Mail does not enable is refused at every upload route with the
// capability named, and nothing it carried is stored (#2108, the owner's decision
// on #1468). A script on the enabled list is the control.
func TestSieveUploadRefusesExtensionsNotEnabled(t *testing.T) {
	f := newGrantsFixture(t)
	token := f.token(t, "owner")
	account := "/api/v1/accounts/" + strconv.FormatUint(uint64(f.admins["owner"].user.ID), 10)
	domain := strconv.FormatUint(uint64(f.a.ID), 10)

	body := func(field, script string) string {
		encoded, err := json.Marshal(map[string]string{field: script})
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	refusedScripts := map[string]string{
		"regex":  `require "regex"; if false { if header :regex "Subject" "a.*" { keep; } }`,
		"notify": `require "notify"; if false { notify :method "mailto:a@example.com"; }`,
	}
	const enabled = `require "fileinto"; fileinto "Filed";`

	if got := doRequest(f.router, http.MethodGet, account+"/sieve", token, ""); got.Code != http.StatusNotFound {
		t.Fatalf("fixture already has a script: %d %s", got.Code, got.Body)
	}
	for capability, script := range refusedScripts {
		t.Run("first upload of "+capability, func(t *testing.T) {
			got := doRequest(f.router, http.MethodPut, account+"/sieve", token, body("script", script))
			assertCapabilityField(t, got.Code, got.Body.Bytes(), "script", capability)
			if read := doRequest(f.router, http.MethodGet, account+"/sieve", token, ""); read.Code != http.StatusNotFound {
				t.Fatalf("a refused script was stored: %d %s", read.Code, read.Body)
			}
		})
	}

	if got := doRequest(f.router, http.MethodPut, account+"/sieve", token, body("script", enabled)); got.Code != http.StatusCreated {
		t.Fatalf("an enabled script was refused: %d %s", got.Code, got.Body)
	}
	for capability, script := range refusedScripts {
		t.Run("replacement with "+capability, func(t *testing.T) {
			got := doRequest(f.router, http.MethodPut, account+"/sieve", token, body("script", script))
			assertCapabilityField(t, got.Code, got.Body.Bytes(), "script", capability)
			read := doRequest(f.router, http.MethodGet, account+"/sieve", token, "")
			if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `fileinto \"Filed\"`) {
				t.Fatalf("the stored script changed: %d %s", read.Code, read.Body)
			}
		})
		t.Run("validation of "+capability, func(t *testing.T) {
			got := doRequest(f.router, http.MethodPost, account+"/sieve/validate", token, body("script", script))
			if got.Code != http.StatusOK {
				t.Fatalf("got %d %s", got.Code, got.Body)
			}
			var answer struct {
				Data struct {
					Valid   bool   `json:"valid"`
					Message string `json:"message"`
				} `json:"data"`
			}
			if err := json.Unmarshal(got.Body.Bytes(), &answer); err != nil {
				t.Fatal(err)
			}
			if answer.Data.Valid || !strings.Contains(answer.Data.Message, `"`+capability+`"`) {
				t.Fatalf("validation disagrees with upload: %s", got.Body)
			}
		})
		t.Run("domain pipeline with "+capability, func(t *testing.T) {
			filters, err := json.Marshal([]map[string]any{{"name": "sieve", "type": "transform", "enabled": false, "config": map[string]string{"script": script}}})
			if err != nil {
				t.Fatal(err)
			}
			got := doRequest(f.router, http.MethodPost, "/api/v1/admin/pipelines", token,
				`{"domain_id":`+domain+`,"direction":"inbound","filters":`+string(filters)+`}`)
			assertCapabilityField(t, got.Code, got.Body.Bytes(), "filters", capability)
			list := doRequest(f.router, http.MethodGet, "/api/v1/admin/pipelines", token, "")
			if list.Code != http.StatusOK || strings.Contains(list.Body.String(), `require \"`+capability) {
				t.Fatalf("a refused pipeline was stored: %d %s", list.Code, list.Body)
			}
		})
	}

	validated := doRequest(f.router, http.MethodPost, account+"/sieve/validate", token, body("script", enabled))
	if validated.Code != http.StatusOK || !strings.Contains(validated.Body.String(), `"valid":true`) {
		t.Fatalf("an enabled script failed validation: %d %s", validated.Code, validated.Body)
	}
}

func assertCapabilityField(t *testing.T, status int, raw []byte, field, capability string) {
	t.Helper()
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("got %d %s, want 422", status, raw)
	}
	var refusal struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				Fields map[string]string `json:"fields"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &refusal); err != nil {
		t.Fatal(err)
	}
	if refusal.Error.Code != "validation_failed" || !strings.Contains(refusal.Error.Details.Fields[field], `"`+capability+`"`) {
		t.Fatalf("refusal does not name %q in %s: %s", capability, field, raw)
	}
}
