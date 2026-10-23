package handlers

import (
	"reflect"
	"testing"
)

// createBan's ip_missing and duration_invalid requests (#386) are each refused
// by the real handler with exactly their documented 422, before anything is
// looked up or stored: the handler has no database.
func TestBanCreate_DocumentedRefusals(t *testing.T) {
	documentedRefusals(t, "/api/v1/admin/bans", "createBan", banRoutes(NewBanHandler(nil)).ServeHTTP)
}

// The pairs vertrag compiles for createBan: created with the 201, and
// ip_missing and duration_invalid with the 422, nothing else. ip_missing is
// created without its IP and duration_invalid is created with a duration Go
// cannot parse, so each is refused for that one field alone.
func TestBanCreate_ExamplePairs(t *testing.T) {
	requests, responses := postExamples(t, "/api/v1/admin/bans", "createBan")

	want := map[string][]string{
		"request": {"created", "duration_invalid", "ip_missing"},
		"201":     {"created"},
		"422":     {"duration_invalid", "ip_missing"},
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
	invalid := map[string]interface{}{}
	for k, v := range created {
		invalid[k] = v
	}
	invalid["duration"] = "not-a-duration"
	if !reflect.DeepEqual(requests["duration_invalid"], invalid) {
		t.Errorf("duration_invalid is %s, want created with an unparseable duration: %s",
			mustJSON(requests["duration_invalid"]), mustJSON(invalid))
	}

	refusal := map[string]interface{}{
		"error": map[string]interface{}{
			"code":    "validation_failed",
			"message": "Request body failed validation",
			"details": map[string]interface{}{
				"fields": map[string]interface{}{
					"duration": "invalid duration format (e.g. 24h, 168h)",
				},
			},
		},
	}
	if !reflect.DeepEqual(responses["422"]["duration_invalid"], refusal) {
		t.Errorf("the 422 duration_invalid is %s, want the duration refusal %s",
			mustJSON(responses["422"]["duration_invalid"]), mustJSON(refusal))
	}
}
