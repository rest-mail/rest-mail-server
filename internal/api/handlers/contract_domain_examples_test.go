package handlers

import (
	"reflect"
	"testing"
)

// The pairs vertrag compiles for createDomain (#386): created with the 201,
// body_array with the 400, and name_missing, sending_policy_invalid and
// server_type_invalid with the 422, nothing else.
// contract_domain_body_examples_test.go checks body_array.
// server_type_invalid is the created request with a server type that is
// neither traditional nor restmail, so the server type is the only reason it
// is refused. TestDomainCreate_DocumentedRefusals sends each 422's request to
// the real handler.
func TestDomainCreate_ExamplePairs(t *testing.T) {
	requests, responses := postExamples(t, "/api/v1/admin/domains", "createDomain")

	want := map[string][]string{
		"request": {"body_array", "created", "name_missing", "sending_policy_invalid", "server_type_invalid"},
		"201":     {"created"},
		"400":     {"body_array"},
		"422":     {"name_missing", "sending_policy_invalid", "server_type_invalid"},
	}
	got := map[string][]string{"request": names(requests)}
	for status, examples := range responses {
		got[status] = names(examples)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("createDomain's named examples are %v, want %v", got, want)
	}

	created := map[string]interface{}{
		"name":        "example.org",
		"server_type": "restmail",
	}
	if !reflect.DeepEqual(requests["created"], created) {
		t.Errorf("created is %s, want the valid domain request %s",
			mustJSON(requests["created"]), mustJSON(created))
	}
	if !reflect.DeepEqual(requests["name_missing"], map[string]interface{}{}) {
		t.Errorf("name_missing is %s, want {}", mustJSON(requests["name_missing"]))
	}
	invalid := map[string]interface{}{}
	for k, v := range created {
		invalid[k] = v
	}
	invalid["server_type"] = "external"
	if !reflect.DeepEqual(requests["server_type_invalid"], invalid) {
		t.Errorf("server_type_invalid is %s, want created with server type external: %s",
			mustJSON(requests["server_type_invalid"]), mustJSON(invalid))
	}

	invalidPolicy := map[string]interface{}{}
	for k, v := range created {
		invalidPolicy[k] = v
	}
	invalidPolicy["sending_policy"] = "newsletter"
	if !reflect.DeepEqual(requests["sending_policy_invalid"], invalidPolicy) {
		t.Errorf("sending_policy_invalid is %s, want created with sending policy newsletter: %s",
			mustJSON(requests["sending_policy_invalid"]), mustJSON(invalidPolicy))
	}

	createdBody := map[string]interface{}{
		"data": map[string]interface{}{
			"id":                  float64(1),
			"name":                "example.org",
			"server_type":         "restmail",
			"active":              false,
			"default_quota_bytes": float64(0),
			"sending_policy":      "",
			"created_at":          "2026-01-01T00:00:00Z",
			"updated_at":          "2026-01-01T00:00:00Z",
		},
	}
	if !reflect.DeepEqual(responses["201"]["created"], createdBody) {
		t.Errorf("the 201's created is %s, want %s",
			mustJSON(responses["201"]["created"]), mustJSON(createdBody))
	}
	for name, field := range map[string]map[string]interface{}{
		"name_missing":           {"name": "required"},
		"sending_policy_invalid": {"sending_policy": "must be 'transactional', 'bulk' or empty"},
		"server_type_invalid":    {"server_type": "must be 'traditional' or 'restmail'"},
	} {
		refused := map[string]interface{}{
			"error": map[string]interface{}{
				"code":    "validation_failed",
				"message": "Request body failed validation",
				"details": map[string]interface{}{"fields": field},
			},
		}
		if !reflect.DeepEqual(responses["422"][name], refused) {
			t.Errorf("the 422's %s is %s, want %s",
				name, mustJSON(responses["422"][name]), mustJSON(refused))
		}
	}
}
