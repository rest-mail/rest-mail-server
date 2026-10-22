package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/restmail/restmail/internal/config"
)

// sendProbe sends one of the spec's requests to h and recovers a nil
// database dereference as reached: a refusal must come before any probe.
func sendProbe(t *testing.T, h *TestHandler, request interface{}) (rr *httptest.ResponseRecorder, reached bool) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/test/probe", bytes.NewReader(mustJSON(request)))
	rr = httptest.NewRecorder()
	func() {
		defer func() {
			if r := recover(); r != nil {
				reached = true
			}
		}()
		h.ProbeServices(rr, req)
	}()
	return rr, reached
}

// The pairs vertrag compiles for probeServices: probed with the 200 and
// service_unknown with the 422, nothing else. service_unknown is probed with
// a service the handler cannot probe, so that is the only reason it is
// refused.
func TestProbeServices_ExamplePairs(t *testing.T) {
	requests, responses := postExamples(t, "/api/v1/admin/test/probe", "probeServices")

	want := map[string][]string{
		"request": {"probed", "service_unknown"},
		"200":     {"probed"},
		"422":     {"service_unknown"},
	}
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("probeServices' named examples are %v, want %v", got, want)
	}

	probed := map[string]interface{}{"services": []interface{}{"db"}}
	if !reflect.DeepEqual(requests["probed"], probed) {
		t.Errorf("probed is %s, want %s", mustJSON(requests["probed"]), mustJSON(probed))
	}
	unknown, _ := requests["service_unknown"].(map[string]interface{})
	services, _ := unknown["services"].([]interface{})
	if len(unknown) != 1 || len(services) != 1 {
		t.Fatalf("service_unknown is %s, want one service and nothing else", mustJSON(unknown))
	}
	if name, _ := services[0].(string); name == "" || probeableServices[name] {
		t.Errorf("service_unknown names %q, want a service the handler cannot probe", services[0])
	}
}

// service_unknown is refused by the real handler, outside production, with
// exactly its documented 422, before anything is probed: the handler has no
// database.
func TestProbeServices_DocumentedRefusal(t *testing.T) {
	requests, responses := postExamples(t, "/api/v1/admin/test/probe", "probeServices")
	h := NewTestHandler(nil, &config.Config{Environment: "test"})

	rr, reached := sendProbe(t, h, requests["service_unknown"])
	if reached {
		t.Fatal("service_unknown reached a probe before it was refused")
	}
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rr.Code, rr.Body)
	}
	var body interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if want := responses["422"]["service_unknown"]; !reflect.DeepEqual(body, want) {
		t.Errorf("body = %s, want the documented %s", rr.Body, mustJSON(want))
	}
}

// probed reaches the database and nothing else: its 200 has the db result
// alone, as its example does.
func TestProbeServices_DocumentedProbe(t *testing.T) {
	db := openAuditTestDB(t)
	requests, responses := postExamples(t, "/api/v1/admin/test/probe", "probeServices")
	h := NewTestHandler(db, &config.Config{Environment: "test"})

	rr, reached := sendProbe(t, h, requests["probed"])
	if reached {
		t.Fatal("probed panicked")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body)
	}
	var body struct {
		Data map[string]map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got := names(body.Data); !reflect.DeepEqual(got, []string{"db"}) {
		t.Fatalf("probed answered for %v, want only db: %s", got, rr.Body)
	}
	if status := body.Data["db"]["status"]; status != "ok" {
		t.Errorf("db status = %v, want ok: %s", status, rr.Body)
	}
	example, _ := responses["200"]["probed"].(map[string]interface{})
	data, _ := example["data"].(map[string]interface{})
	if got := names(data); !reflect.DeepEqual(got, []string{"db"}) {
		t.Errorf("the 200's probed example answers for %v, want only db", got)
	}
}

// In production the test endpoints stay shut: both documented requests are
// answered 403 before they are read, and nothing is probed.
func TestProbeServices_ProductionRefusesExamples(t *testing.T) {
	requests, _ := postExamples(t, "/api/v1/admin/test/probe", "probeServices")
	h := NewTestHandler(nil, &config.Config{Environment: "production"})

	for name, request := range requests {
		t.Run(name, func(t *testing.T) {
			rr, reached := sendProbe(t, h, request)
			if reached {
				t.Fatal("reached a probe in production")
			}
			if rr.Code != http.StatusForbidden {
				t.Fatalf("status %d in production, want 403; body %s", rr.Code, rr.Body)
			}
		})
	}
}
