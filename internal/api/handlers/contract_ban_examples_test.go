package handlers

import (
	"reflect"
	"testing"
)

// createBan's ip_missing request (#386) is refused by the real handler with
// exactly its documented 422, before anything is looked up or stored: the
// handler has no database.
func TestBanCreate_DocumentedRefusals(t *testing.T) {
	documentedRefusals(t, "/api/v1/admin/bans", "createBan", NewBanHandler(nil).CreateBan)
}

// The pairs vertrag compiles for createBan: created with the 201 and
// ip_missing with the 422, nothing else. ip_missing is created without its
// IP, so the IP is the only reason it is refused.
func TestBanCreate_ExamplePairs(t *testing.T) {
	requests, responses := postExamples(t, "/api/v1/admin/bans", "createBan")

	want := map[string][]string{
		"request": {"created", "ip_missing"},
		"201":     {"created"},
		"422":     {"ip_missing"},
	}
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("createBan's named examples are %v, want %v", got, want)
	}

	created := map[string]interface{}{
		"ip":         "192.0.2.10",
		"reason":     "Repeated failed sign-ins",
		"protocol":   "all",
		"duration":   "1h",
		"created_by": "admin@restmail.test",
	}
	if !reflect.DeepEqual(requests["created"], created) {
		t.Errorf("created is %s, want the valid ban request %s",
			mustJSON(requests["created"]), mustJSON(created))
	}
	missing := map[string]interface{}{}
	for k, v := range created {
		if k != "ip" {
			missing[k] = v
		}
	}
	if !reflect.DeepEqual(requests["ip_missing"], missing) {
		t.Errorf("ip_missing is %s, want created without its IP: %s",
			mustJSON(requests["ip_missing"]), mustJSON(missing))
	}
}
