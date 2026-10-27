package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// The generated description links each create to the reads and changes of what it made,
// so a sequenced contract run fills their path parameters from the create's answer instead
// of the example's ids (#1232). Every link hangs off a status its operation documents and
// leads to an operation the description declares.
func TestDescriptionLinksCreatesToTheirReads(t *testing.T) {
	routers := describedRouters(t)
	g := routers.generator("test")
	doc := g.Generate()
	for _, w := range g.Report() {
		if strings.HasPrefix(string(w.Kind), "link") {
			t.Errorf("a link does not fit the description: %s", w.Message)
		}
	}
	raw, err := json.Marshal(doc["paths"])
	if err != nil {
		t.Fatal(err)
	}
	var paths map[string]map[string]struct {
		OperationID string `json:"operationId"`
		Responses   map[string]struct {
			Links map[string]struct {
				OperationID string            `json:"operationId"`
				Parameters  map[string]string `json:"parameters"`
			} `json:"links"`
		} `json:"responses"`
	}
	if err := json.Unmarshal(raw, &paths); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, item := range paths {
		for _, op := range item {
			for status, response := range op.Responses {
				for name, link := range response.Links {
					found[op.OperationID+" "+status+" "+name] = true
					if name == "getAlias" && op.OperationID == "createAlias" &&
						(link.OperationID != "getAlias" || link.Parameters["id"] != "$response.body#/data/id") {
						t.Errorf("createAlias links getAlias as %+v", link)
					}
				}
			}
		}
	}
	for operation, statuses := range responseLinks {
		for status, links := range statuses {
			for name := range links {
				if !found[operation+" "+status+" "+name] {
					t.Errorf("%s %s does not carry its %s link", operation, status, name)
				}
			}
		}
	}
	if !found["createAlias 201 getAlias"] {
		t.Error("createAlias does not lead to the read of the alias it made")
	}
}
