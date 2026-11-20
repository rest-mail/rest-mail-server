package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// folderNameProbes are names on each side of rmail.FolderNameProblem, the nightly's " " first.
var folderNameProbes = []string{" ", "", "  ", " Work", "Work ", "..", "a..b", "a.b", ".", "Work/Projects", "Café",
	"Work\n", "Work\t", "a\x00b", "Work\u0085", "Work ", "　", "a b", "INBOX", "x",
	strings.Repeat("a", 255), strings.Repeat("a", 256)}

// The folder-name rows of the contract-fuzz board (#2051): createFolder and renameFolder took a
// body whose name was " ", and subscribeFolder a path whose folder was " ". Each handler holds
// the name to rmail.FolderNameProblem and was right to refuse it 422; the schemas permitted any
// non-empty string, so they were wrong. They now state the rule, and say the same as the
// handler for every name here.
func TestFuzzFindingFolderNameSchemasAreTheFolderRule(t *testing.T) {
	doc := fuzzFindingDocument(t)
	compiler := fuzzFindingCompiler()
	if err := compiler.AddResource("https://rest-mail.test/fuzz-findings.json", doc); err != nil {
		t.Fatal(err)
	}
	paths := doc["paths"].(map[string]interface{})
	subscribe := paths["/api/v1/accounts/{id}/subscriptions/{folder}"].(map[string]interface{})
	parameterPointer := func(method string) (string, string) {
		for index, raw := range subscribe[method].(map[string]interface{})["parameters"].([]interface{}) {
			if parameter := raw.(map[string]interface{}); parameter["name"] == "folder" {
				description, _ := parameter["description"].(string)
				return fuzzFindingPointer("/api/v1/accounts/{id}/subscriptions/{folder}", method) + fmt.Sprintf("/parameters/%d/schema", index), description
			}
		}
		t.Fatalf("%s has no folder parameter", method)
		return "", ""
	}
	subscribePointer, subscribeDescription := parameterPointer("put")
	for _, test := range []struct {
		finding, pointer string
		wrap             func(string) any
	}{
		{"73cd741f0dac328a", fuzzFindingPointer("/api/v1/accounts/{id}/folders", "post") + "/requestBody/content/application~1json/schema",
			func(name string) any { return map[string]any{"name": name} }},
		{"90445d100751ed3c", fuzzFindingPointer("/api/v1/accounts/{id}/folders/{folder}", "patch") + "/requestBody/content/application~1json/schema",
			func(name string) any { return map[string]any{"name": name} }},
		{"58f5b984a09963f3", subscribePointer, func(name string) any { return name }},
	} {
		schema := fuzzFindingSchema(t, compiler, test.pointer)
		for _, name := range folderNameProbes {
			t.Run(test.finding+"/"+fmt.Sprintf("%q", name), func(t *testing.T) {
				// Through JSON, as a request carries it, so the schema sees what a validator sees.
				encoded, err := json.Marshal(test.wrap(name))
				if err != nil {
					t.Fatal(err)
				}
				value, err := jsonschema.UnmarshalJSON(strings.NewReader(string(encoded)))
				if err != nil {
					t.Fatal(err)
				}
				if got, want := schema.Validate(value) == nil, rmail.FolderNameProblem(name) == ""; got != want {
					t.Errorf("schema accepts %t, handler accepts %t", got, want)
				}
			})
		}
	}
	for _, want := range []string{"refused 422 validation_failed", "neither blank nor beginning or ending with a space"} {
		if !strings.Contains(subscribeDescription, want) {
			t.Errorf("subscribeFolder's folder does not say %q: %s", want, subscribeDescription)
		}
	}
	// unsubscribeFolder takes any name, so its parameter keeps no rule it does not apply.
	unsubscribePointer, _ := parameterPointer("delete")
	if err := fuzzFindingSchema(t, compiler, unsubscribePointer).Validate(" "); err != nil {
		t.Errorf("unsubscribeFolder's folder took on the rule it does not apply: %v", err)
	}
}

// Through the production router on PostgreSQL: the nightly's " " is refused 422 naming the
// field on each route, a name the schema permits is taken, and unsubscribing still takes any name.
func TestFuzzFindingFolderNamesAgainstPostgres(t *testing.T) {
	f := newCredentialFixture(t)
	folders := fmt.Sprintf("/api/v1/accounts/%d/folders", f.mailboxID)
	subscriptions := fmt.Sprintf("/api/v1/accounts/%d/subscriptions/", f.mailboxID)
	refused := func(body, field string) {
		t.Helper()
		var answer struct {
			Error struct {
				Code    string `json:"code"`
				Details struct {
					Fields map[string]string `json:"fields"`
				} `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(body), &answer); err != nil {
			t.Fatal(err)
		}
		if answer.Error.Code != "validation_failed" || answer.Error.Details.Fields[field] != "required" {
			t.Fatalf("answered %s, want validation_failed naming %s required", body, field)
		}
	}
	t.Run("73cd741f0dac328a", func(t *testing.T) {
		rr := f.do(http.MethodPost, folders, f.session, `{"name":" "}`)
		if rr.Code != http.StatusUnprocessableEntity {
			t.Fatalf("create \" \": %d %s, want 422", rr.Code, rr.Body)
		}
		refused(rr.Body.String(), "name")
		if rr := f.do(http.MethodPost, folders, f.session, `{"name":"Work"}`); rr.Code != http.StatusCreated {
			t.Fatalf("create Work: %d %s, want 201", rr.Code, rr.Body)
		}
	})
	t.Run("90445d100751ed3c", func(t *testing.T) {
		rr := f.do(http.MethodPatch, folders+"/Work", f.session, `{"name":" "}`)
		if rr.Code != http.StatusUnprocessableEntity {
			t.Fatalf("rename Work to \" \": %d %s, want 422", rr.Code, rr.Body)
		}
		refused(rr.Body.String(), "name")
		if rr := f.do(http.MethodPatch, folders+"/Work", f.session, `{"name":"Work 2026"}`); rr.Code != http.StatusOK {
			t.Fatalf("rename Work to Work 2026: %d %s, want 200", rr.Code, rr.Body)
		}
	})
	t.Run("58f5b984a09963f3", func(t *testing.T) {
		rr := f.do(http.MethodPut, subscriptions+"%20", f.session, "")
		if rr.Code != http.StatusUnprocessableEntity {
			t.Fatalf("subscribe \" \": %d %s, want 422", rr.Code, rr.Body)
		}
		refused(rr.Body.String(), "folder")
		if rr := f.do(http.MethodPut, subscriptions+url.PathEscape("Work 2026"), f.session, ""); rr.Code != http.StatusOK {
			t.Fatalf("subscribe Work 2026: %d %s, want 200", rr.Code, rr.Body)
		}
		if rr := f.do(http.MethodDelete, subscriptions+"%20", f.session, ""); rr.Code != http.StatusNoContent {
			t.Fatalf("unsubscribe \" \": %d %s, want 204", rr.Code, rr.Body)
		}
	})
}
