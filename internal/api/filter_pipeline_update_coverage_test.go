package api

import "testing"

// The coverage phase sends every value a request schema permits, and the server
// refused these two: the description has to say so.
func TestFilterAndPipelineUpdatesDescribeWhatTheyRefuse(t *testing.T) {
	spec := generatedDocument(t)
	properties := func(route string) map[string]interface{} {
		path := spec["paths"].(map[string]interface{})[route].(map[string]interface{})
		body := path["patch"].(map[string]interface{})["requestBody"].(map[string]interface{})
		media := body["content"].(map[string]interface{})["application/json"].(map[string]interface{})
		return resolved(spec, media["schema"]).(map[string]interface{})["properties"].(map[string]interface{})
	}
	for _, route := range []string{"/api/v1/admin/custom-filters/{id}", "/api/v1/admin/pipelines/{id}"} {
		domain := properties(route)["domain_id"].(map[string]interface{})
		if minimum, ok := domain["minimum"].(float64); !ok || minimum != 0 {
			t.Errorf("%s domain_id permits a negative number: %v", route, domain)
		}
	}
	name := properties("/api/v1/admin/custom-filters/{id}")["name"].(map[string]interface{})
	if length, ok := name["minLength"].(float64); !ok || length != 1 {
		t.Errorf("custom filter name permits the empty string: %v", name)
	}
}
