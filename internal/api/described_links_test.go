package api

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v2"
)

// The generated description links each create to the reads and changes of what it made,
// so a sequenced contract run fills their path parameters from the create's answer instead
// of the example's ids (#1232). Every link hangs off a status its operation documents and
// leads to an operation the description declares.
func TestDescriptionLinksCreatesToTheirReads(t *testing.T) {
	// The disabled-by-default routes too, since some of the links are theirs.
	found := map[string]bool{}
	for _, g := range gapGenerators(t) {
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

// getMessage's answer links getThread, which only holds for a message with a thread. A send always
// has one; a new draft that replies to nothing has an empty thread_id. A sequenced contract run
// follows the first link to an operation in document order, so a link from saveDraft, which comes
// before sendMessage, would send getThread an empty id (#1749).
func TestOnlyASendLeadsToTheMessageThatLinksItsThread(t *testing.T) {
	for _, generator := range gapGenerators(t) {
		eachConventionOperation(contractDocument(t, generator), func(op conventionOperation) {
			for status, value := range op.responses {
				response, _ := value.(map[string]interface{})
				links, _ := response["links"].(map[string]interface{})
				for name, each := range links {
					link, _ := each.(map[string]interface{})
					if link["operationId"] == "getMessage" && op.id != "sendMessage" {
						t.Errorf("%s %s links getMessage as %s: only a send has the thread its answer links", op.id, status, name)
					}
				}
			}
		})
	}
}

// openapi.yaml states the same links, by operation and status, so the comparison oracle does
// not fall behind the description a client reads.
func TestOracleStatesTheGeneratedLinks(t *testing.T) {
	data, err := os.ReadFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	stated, described := map[string]bool{}, map[string]bool{}
	paths, _ := doc["paths"].(map[interface{}]interface{})
	for _, item := range paths {
		methods, _ := item.(map[interface{}]interface{})
		for _, value := range methods {
			operation, _ := value.(map[interface{}]interface{})
			id, _ := operation["operationId"].(string)
			described[id] = true
			responses, _ := operation["responses"].(map[interface{}]interface{})
			for status, response := range responses {
				answer, _ := response.(map[interface{}]interface{})
				links, _ := answer["links"].(map[interface{}]interface{})
				for name := range links {
					stated[fmt.Sprintf("%s %v %v", id, status, name)] = true
				}
			}
		}
	}
	generated := map[string]bool{}
	for operation, statuses := range responseLinks {
		for status, links := range statuses {
			for name := range links {
				generated[operation+" "+status+" "+name] = true
				// oracle-differences.yml records the operations the oracle does not have.
				if described[operation] && !stated[operation+" "+status+" "+name] {
					t.Errorf("openapi.yaml does not state %s %s's %s link", operation, status, name)
				}
			}
		}
	}
	for link := range stated {
		if !generated[link] {
			t.Errorf("openapi.yaml states the link %s, which the description does not", link)
		}
	}
}
