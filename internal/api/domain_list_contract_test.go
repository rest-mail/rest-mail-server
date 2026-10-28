package api

import "testing"

// listDomains has always bound limit and offset, including the limit in its
// canonical example. The hand-written contract must expose both to clients
// instead of hiding their absence behind the oracle-difference baseline (#388).
func TestListDomainsPaginationParametersMatchTheRegisteredRoute(t *testing.T) {
	oracle, generated := oracleDocument(t), generatedDocument(t)
	parameters := func(doc map[string]interface{}) map[string]map[string]interface{} {
		t.Helper()
		item := doc["paths"].(map[string]interface{})["/api/v1/admin/domains"].(map[string]interface{})
		op := item["get"].(map[string]interface{})
		if op["operationId"] != "listDomains" {
			t.Fatalf("domain list operation = %v, want listDomains", op["operationId"])
		}
		return parametersOf(doc, item, op)
	}
	written, registered := parameters(oracle), parameters(generated)
	for _, name := range []string{"limit", "offset"} {
		t.Run(name, func(t *testing.T) {
			key := "query " + name
			want, ok := registered[key]
			if !ok {
				t.Fatalf("registered route no longer binds %s", key)
			}
			got, ok := written[key]
			if !ok {
				t.Fatalf("openapi.yaml does not document %s", key)
			}
			if truthy(got["required"]) || truthy(want["required"]) {
				t.Error("pagination parameters must remain optional")
			}
			for _, difference := range diffSchemas(key, oracle, got["schema"], generated, want["schema"]) {
				t.Error(difference)
			}
		})
	}
}
