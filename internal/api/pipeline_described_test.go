package api

import (
	"encoding/json"
	"testing"
)

func TestPipelineAreaDescribesCapabilitiesAndPreviewLimits(t *testing.T) {
	paths, _ := describedAPI(t)
	operations := map[string]struct {
		path, method, capability string
		preview                  bool
	}{
		"listPipelines":        {"/pipelines", "get", "pipelines:read", false},
		"createPipeline":       {"/pipelines", "post", "pipelines:write", false},
		"updatePipeline":       {"/pipelines/{id}", "patch", "pipelines:write", false},
		"deletePipeline":       {"/pipelines/{id}", "delete", "pipelines:delete", false},
		"testPipeline":         {"/pipelines/test", "post", "pipelines:write", true},
		"testFilter":           {"/pipelines/test-filter", "post", "pipelines:write", true},
		"listPipelineLogs":     {"/pipelines/logs", "get", "pipelines:read", false},
		"getPipelineAnalytics": {"/pipelines/analytics", "get", "observability:read", false},
		"getMessageTrace":      {"/messages/{id}/trace", "get", "observability:read", false},
		"listCustomFilters":    {"/custom-filters", "get", "pipelines:read", false},
		"createCustomFilter":   {"/custom-filters", "post", "pipelines:write", false},
		"getCustomFilter":      {"/custom-filters/{id}", "get", "pipelines:read", false},
		"updateCustomFilter":   {"/custom-filters/{id}", "patch", "pipelines:write", false},
		"deleteCustomFilter":   {"/custom-filters/{id}", "delete", "pipelines:delete", false},
		"validateCustomFilter": {"/custom-filters/validate", "post", "pipelines:write", false},
		"testCustomFilter":     {"/custom-filters/{id}/test", "post", "pipelines:write", true},
	}
	for id, want := range operations {
		t.Run(id, func(t *testing.T) {
			op := paths["/api/v1/admin"+want.path][want.method]
			if op.OperationID != id {
				t.Fatalf("operation = %q", op.OperationID)
			}
			security, _ := json.Marshal(op.Security)
			expected := `[{"CookieAuth":["` + want.capability + `"]},{"BearerAuth":["` + want.capability + `"]},{"APIKeyAuth":["` + want.capability + `"]}]`
			if string(security) != expected {
				t.Fatalf("security = %s, want %s", security, expected)
			}
			for _, status := range []string{"401", "403", "413", "422", "500", "503"} {
				if _, ok := op.Responses[status]; !ok {
					t.Errorf("missing response %s", status)
				}
			}
			if _, limited := op.Responses["429"]; limited != want.preview {
				t.Errorf("preview throttle described = %v", limited)
			}
		})
	}
	doc := generatedDocument(t)
	for _, path := range []string{"/api/v1/admin/custom-filters/validate", "/api/v1/admin/custom-filters/{id}/test"} {
		op := doc["paths"].(map[string]interface{})[path].(map[string]interface{})["post"].(map[string]interface{})
		response := op["responses"].(map[string]interface{})["200"].(map[string]interface{})
		schema := response["content"].(map[string]interface{})["application/json"].(map[string]interface{})["schema"]
		props := resolved(doc, schema).(map[string]interface{})["properties"].(map[string]interface{})
		if _, wrapped := props["data"]; wrapped {
			t.Fatalf("%s must describe the bare sidecar response", path)
		}
	}
}
