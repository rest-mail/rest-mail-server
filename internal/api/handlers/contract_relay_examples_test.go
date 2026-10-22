package handlers

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
)

const relayContractPath = "/api/v1/admin/domains/{id}/relay"

func TestSetDomainRelay_ExamplePairs(t *testing.T) {
	requests, responses := operationExamples(t, relayContractPath, "put", "setDomainRelay")
	wantNames := map[string][]string{
		"200": {"direct", "relay"},
		"400": {"body_array", "port_wrong_type"},
		"422": {"credentials_mismatch", "host_invalid", "port_invalid", "tls_mode_invalid", "transport_invalid", "transport_missing"},
	}
	for status, want := range wantNames {
		if got := names(responses[status]); !reflect.DeepEqual(got, want) {
			t.Errorf("%s names %v, want %v", status, got, want)
		}
	}
	for name := range requests {
		var statuses []string
		for status, examples := range responses {
			if _, ok := examples[name]; ok {
				statuses = append(statuses, status)
			}
		}
		if len(statuses) != 1 {
			t.Errorf("%s pairs with %v, want exactly one response", name, statuses)
		}
	}
	if len(requests) != 10 {
		t.Fatalf("got %d requests, want 10", len(requests))
	}
	for name, want := range map[string]string{
		"body_array":           `[]`,
		"transport_missing":    `{}`,
		"transport_invalid":    `{"transport":"smtp"}`,
		"port_wrong_type":      `{"transport":"relay","host":"smtp.example.net","port":"587","tls_mode":"starttls","username":"apikey","password":"relay-password"}`,
		"credentials_mismatch": `{"transport":"relay","host":"smtp.example.net","port":587,"tls_mode":"starttls","username":"apikey","password":""}`,
	} {
		if !reflect.DeepEqual(requests[name], parseShapeJSON(t, want)) {
			t.Errorf("%s request is %s, want %s", name, mustJSON(requests[name]), want)
		}
	}
}

// Send the served spec's examples through the real router. The same refusals
// must work before and after configuring credentials, without changing any
// saved settings, encrypted password or timestamps included.
func TestSetDomainRelay_DocumentedExamples(t *testing.T) {
	requests, responses := operationExamples(t, relayContractPath, "put", "setDomainRelay")
	srv, h, id := relayTestServer(t, relayTestMasterKey)
	var domainBefore models.Domain
	if err := h.db.First(&domainBefore, id).Error; err != nil {
		t.Fatal(err)
	}
	readSettings := func() []models.DomainRelay {
		t.Helper()
		var rows []models.DomainRelay
		if err := h.db.Where("domain_id = ?", id).Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		return rows
	}
	refusals := func(phase string) {
		t.Helper()
		before := readSettings()
		for _, status := range []string{"400", "422"} {
			for name, want := range responses[status] {
				t.Run(phase+"/"+name, func(t *testing.T) {
					request, ok := requests[name]
					if !ok {
						t.Fatalf("%s has no request pair", name)
					}
					code, body := relayCall(t, srv, http.MethodPut, id, string(mustJSON(request)))
					if strconv.Itoa(code) != status {
						t.Fatalf("got %d %s, want %s", code, body, status)
					}
					var got interface{}
					if err := json.Unmarshal([]byte(body), &got); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(got, want) {
						t.Errorf("got %s, want %s", body, mustJSON(want))
					}
					if after := readSettings(); !reflect.DeepEqual(after, before) {
						t.Fatal("refusal changed the stored relay settings")
					}
				})
			}
		}
	}
	refusals("unconfigured")
	for _, name := range []string{"direct", "relay"} {
		code, body := relayCall(t, srv, http.MethodPut, id, string(mustJSON(requests[name])))
		if code != http.StatusOK {
			t.Fatalf("%s got %d %s, want 200", name, code, body)
		}
		var got struct {
			Data map[string]interface{} `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatal(err)
		}
		want := responses["200"][name].(map[string]interface{})["data"].(map[string]interface{})
		if got.Data["domain_id"] != float64(id) {
			t.Fatalf("%s saved the wrong domain: %s", name, body)
		}
		got.Data["domain_id"] = float64(1) // The example id is replaced by the createDomain link.
		if !reflect.DeepEqual(got.Data, want) {
			t.Fatalf("%s got %s, want %s", name, body, mustJSON(want))
		}
	}
	stored := readSettings()
	if len(stored) != 1 || stored[0].PasswordEnc == "" {
		t.Fatal("success did not save the relay credentials")
	}
	if password, err := models.LoadRelayPassword(stored[0].PasswordEnc, relayTestMasterKey); err != nil || password != "relay-password" {
		t.Fatalf("saved relay password did not round trip: %v", err)
	}
	refusals("configured")
	var domainAfter models.Domain
	if err := h.db.First(&domainAfter, id).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(domainAfter, domainBefore) {
		t.Fatal("relay examples changed the domain")
	}
}
