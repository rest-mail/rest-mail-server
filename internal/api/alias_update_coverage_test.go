package api

import "testing"

func TestAliasUpdateCoverageDescribesDestinationAddress(t *testing.T) {
	spec := generatedDocument(t)
	paths := spec["paths"].(map[string]interface{})
	path := paths["/api/v1/admin/aliases/{id}"].(map[string]interface{})
	operation := path["patch"].(map[string]interface{})
	body := operation["requestBody"].(map[string]interface{})
	content := body["content"].(map[string]interface{})
	media := content["application/json"].(map[string]interface{})
	schema := resolved(spec, media["schema"]).(map[string]interface{})
	properties := schema["properties"].(map[string]interface{})
	destination := properties["destination_address"].(map[string]interface{})
	if destination["format"] != "idn-email" {
		t.Fatalf("destination schema permits non-address strings: %v", destination)
	}
}
