package api

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/rest-mail/rest-mail-server/internal/examplepairs"

	oapifly "github.com/antimatter-studios/go-oapifly"
)

// pairGenerator describes the routes with every registered example pair, the
// matrix's as well as the published ones, so a boundary check can select any
// pair by name. The served description keeps one pair for each status.
func (rt *Routers) pairGenerator(t *testing.T, version string) *oapifly.Generator {
	t.Helper()
	merged, err := examplepairs.Merged()
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{"openapi-example.json": {Data: merged}}
	return describeWith(rt.Registry, version, files)
}

// marshalPairDescription is the description one boundary check hands to Vertrag.
// Two things in the full description do not apply to a run of a single exchange
// against a handler built for it. The check itself sets up what x-requires names,
// so nothing is left for another run to prove. And the description is cut down to
// the operation under test, so a response's links name operations it no longer
// holds; TestDescriptionLinksCreatesToTheirReads and the full contract run check those against the
// whole description.
func marshalPairDescription(doc map[string]interface{}) ([]byte, error) {
	paths, _ := doc["paths"].(map[string]interface{})
	for _, item := range paths {
		operations, _ := item.(map[string]interface{})
		for _, value := range operations {
			if operation, ok := value.(map[string]interface{}); ok {
				relaxOperationForPair(operation)
			}
		}
	}
	return json.Marshal(doc)
}

func relaxOperationForPair(operation map[string]interface{}) {
	if parameters, ok := operation["parameters"].([]interface{}); ok {
		for _, value := range parameters {
			if parameter, ok := value.(map[string]interface{}); ok {
				dropExampleRequirements(parameter)
			}
		}
	}
	if body, ok := operation["requestBody"].(map[string]interface{}); ok {
		dropMediaRequirements(body)
	}
	responses, _ := operation["responses"].(map[string]interface{})
	for _, value := range responses {
		if response, ok := value.(map[string]interface{}); ok {
			delete(response, "links")
			dropMediaRequirements(response)
		}
	}
}

func dropMediaRequirements(holder map[string]interface{}) {
	content, _ := holder["content"].(map[string]interface{})
	for _, value := range content {
		if media, ok := value.(map[string]interface{}); ok {
			dropExampleRequirements(media)
		}
	}
}

// dropExampleRequirements touches the Example Objects only, never their values:
// a response body may hold a member of the same name.
func dropExampleRequirements(holder map[string]interface{}) {
	examples, _ := holder["examples"].(map[string]interface{})
	for _, value := range examples {
		if example, ok := value.(map[string]interface{}); ok {
			delete(example, "x-requires")
		}
	}
}

func TestPairDescriptionDropsRequirementsAndLinksOnly(t *testing.T) {
	doc := map[string]interface{}{}
	source := `{"paths":{"/a":{"get":{
		"parameters":[{"name":"q","examples":{"one":{"value":1,"x-requires":{"state":"s"}}}}],
		"responses":{"200":{"links":{"next":{"operationId":"gone"}},"content":{"application/json":{"examples":{
			"found":{"x-requires":{"fault":"database"},"value":{"links":{"self":"/a"},"x-requires":"kept"}}}}}}}}}}}`
	if err := json.Unmarshal([]byte(source), &doc); err != nil {
		t.Fatal(err)
	}
	data, err := marshalPairDescription(doc)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, gone := range []string{`"operationId":"gone"`, `"state":"s"`, `"fault":"database"`} {
		if strings.Contains(got, gone) {
			t.Errorf("%s stayed in %s", gone, got)
		}
	}
	for _, kept := range []string{`"self":"/a"`, `"x-requires":"kept"`} {
		if !strings.Contains(got, kept) {
			t.Errorf("response value %s was changed in %s", kept, got)
		}
	}
}
