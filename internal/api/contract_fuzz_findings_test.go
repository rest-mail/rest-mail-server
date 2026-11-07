package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func fuzzFindingDocument(t *testing.T) map[string]interface{} {
	t.Helper()
	doc, warnings := describedRouters(t).Description("test")
	if len(warnings) != 0 {
		t.Fatalf("description warnings: %v", warnings)
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return value.(map[string]interface{})
}

func fuzzFindingSchema(t *testing.T, compiler *jsonschema.Compiler, pointer string) *jsonschema.Schema {
	t.Helper()
	schema, err := compiler.Compile("https://rest-mail.test/fuzz-findings.json#" + pointer)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func fuzzFindingCompiler() *jsonschema.Compiler {
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft7)
	compiler.RegisterFormat(&jsonschema.Format{Name: "idn-email", Validate: func(value any) error {
		if address, ok := value.(string); ok {
			return rmail.ValidateAddress(address)
		}
		return nil
	}})
	compiler.RegisterFormat(&jsonschema.Format{Name: "idn-hostname", Validate: func(value any) error {
		if domain, ok := value.(string); ok {
			_, err := rmail.DomainASCII(domain)
			return err
		}
		return nil
	}})
	return compiler
}

func TestContractFuzzMessageItemConstraintsMatchHandlers(t *testing.T) {
	compiler := fuzzFindingCompiler()
	if err := compiler.AddResource("https://rest-mail.test/fuzz-findings.json", fuzzFindingDocument(t)); err != nil {
		t.Fatal(err)
	}
	pointer := "/components/schemas/updateMessageBody/properties/"
	keyword := fuzzFindingSchema(t, compiler, pointer+"keywords/items")
	for character := 0; character < 128; character++ {
		value := string(rune(character))
		_, err := goimap.NormalizeImportFlags([]string{value})
		if got, want := keyword.Validate(value) == nil, err == nil; got != want {
			t.Errorf("keyword U+%04X: schema accepts %t, handler accepts %t", character, got, want)
		}
	}
	for _, value := range []string{"", strings.Repeat("a", 255), strings.Repeat("a", 256), "é", `\Seen`} {
		_, err := goimap.NormalizeImportFlags([]string{value})
		if got, want := keyword.Validate(value) == nil, err == nil && !strings.Contains(value, `\`); got != want {
			t.Errorf("keyword %q: schema accepts %t, handler accepts %t", value, got, want)
		}
	}
	folder := fuzzFindingSchema(t, compiler, pointer+"folder")
	for _, value := range []string{"", " ", "..", "a..b", "a.b", "Work/Projects", "Café", "Work\n", "Work\u0085", "Work\u00a0", "a\x00b", strings.Repeat("a", 255), strings.Repeat("a", 256)} {
		if got, want := folder.Validate(value) == nil, rmail.FolderNameProblem(value) == ""; got != want {
			t.Errorf("folder %q: schema accepts %t, handler accepts %t", value, got, want)
		}
	}
}

func fuzzFindingPointer(path, method string) string {
	return "/paths/" + strings.ReplaceAll(strings.ReplaceAll(path, "~", "~0"), "/", "~1") + "/" + method
}

func TestContractFuzzFindingsDescribeValidation(t *testing.T) {
	doc := fuzzFindingDocument(t)
	const dialect = "http://json-schema.org/draft-07/schema#"
	if doc["jsonSchemaDialect"] != dialect {
		t.Fatalf("format must be asserted, not left as a 2020-12 annotation: %v", doc["jsonSchemaDialect"])
	}
	compiler := fuzzFindingCompiler()
	if err := compiler.AddResource("https://rest-mail.test/fuzz-findings.json", doc); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		issue              int
		path, method, body string
		valid              bool
	}{
		{2015, "/api/v1/admin/domains", "post", `{"name":".."}`, false},
		{2015, "/api/v1/admin/domains", "post", `{"name":"example.test"}`, true},
		{2015, "/api/v1/admin/domains", "post", `{"name":"例子.广告"}`, true},
		{2017, "/api/v1/admin/aliases", "post", `{"source_address":"a","destination_address":"a@example.test"}`, false},
		{2017, "/api/v1/admin/aliases", "post", `{"source_address":"a@example.test","destination_address":"b@example.test"}`, true},
		{2017, "/api/v1/admin/aliases", "post", `{"source_address":"用户@例子.广告","destination_address":"b@example.test"}`, true},
		{2018, "/api/v1/admin/queue/bulk-retry", "post", `{"ids":[-1]}`, false},
		{2018, "/api/v1/admin/queue/bulk-retry", "post", `{"ids":[0]}`, true},
		{2018, "/api/v1/admin/queue/bulk-retry", "post", `{"ids":[18446744073709551615]}`, true},
		{2018, "/api/v1/admin/queue/bulk-retry", "post", `{"ids":[18446744073709551616]}`, false},
		{2023, "/api/v1/accounts/{id}/vacation", "put", `{"enabled":null,"subject":"Away","body":"Back soon"}`, false},
		{2023, "/api/v1/accounts/{id}/vacation", "put", `{"enabled":true,"subject":"Away","body":"Back soon"}`, true},
		{2023, "/api/v1/accounts/{id}/vacation", "put", `{"enabled":false}`, true},
		{2023, "/api/v1/accounts/{id}/vacation", "put", `{"enabled":false,"start_date":"a"}`, false},
		{2023, "/api/v1/accounts/{id}/vacation", "put", `{"enabled":false,"end_date":"a"}`, false},
		{2024, "/api/v1/accounts/{id}/threads/{threadID}/due", "post", `{"due":"a","due_source":"user"}`, false},
		{2024, "/api/v1/accounts/{id}/threads/{threadID}/due", "post", `{"due":null,"due_source":"user"}`, true},
		{2025, "/api/v1/admin/queue/bulk-bounce", "post", `{"ids":[-1]}`, false},
		{2025, "/api/v1/admin/queue/bulk-bounce", "post", `{"filter":{}}`, true},
		{2028, "/api/v1/admin/domains/{id}/relay", "put", `{"transport":"direct","username":"\n"}`, false},
		{2028, "/api/v1/admin/domains/{id}/relay", "put", `{"transport":"direct","password":"\u007f"}`, false},
		{2028, "/api/v1/admin/domains/{id}/relay", "put", `{"transport":"direct"}`, true},
		{2028, "/api/v1/admin/domains/{id}/relay", "put", `{"transport":"relay","host":"smtp.example.test","port":null,"tls_mode":"starttls"}`, false},
		{2028, "/api/v1/admin/domains/{id}/relay", "put", `{"transport":"relay","host":"smtp.example.test","port":587,"tls_mode":null}`, false},
		{2028, "/api/v1/admin/domains/{id}/relay", "put", `{"transport":"relay","host":"smtp.example.test","port":587,"tls_mode":"starttls"}`, true},
		{2028, "/api/v1/admin/domains/{id}/relay", "put", `{"transport":"direct","port":null,"tls_mode":null}`, true},
		{2029, "/api/v1/messages/draft/{id}", "put", `{"from":"a"}`, false},
		{2029, "/api/v1/messages/draft/{id}", "put", `{"from":""}`, true},
		{2029, "/api/v1/messages/draft/{id}", "put", `{"from":null}`, true},
		{2029, "/api/v1/messages/draft/{id}", "put", `{"from":"用户@例子.广告"}`, true},
		{2029, "/api/v1/messages/draft/{id}", "put", `{"to":["a"]}`, false},
		{2029, "/api/v1/messages/draft/{id}", "put", `{"to":["a@example.test"]}`, true},
		{2030, "/api/v1/messages/send", "post", `{"from":"a","to":["b@example.test"]}`, false},
		{2030, "/api/v1/messages/send", "post", `{"from":"a@example.test","to":["b@example.test"]}`, true},
		{2030, "/api/v1/messages/send", "post", `{"from":"用户@例子.广告","to":["b@example.test"]}`, true},
		{2033, "/api/v1/messages/draft", "post", `{"from":"a"}`, false},
		{2033, "/api/v1/messages/draft", "post", `{"from":""}`, true},
		{2033, "/api/v1/messages/draft", "post", `{"from":"用户@例子.广告"}`, true},
		{2033, "/api/v1/messages/draft", "post", `{"cc":["a"]}`, false},
		{2033, "/api/v1/messages/draft", "post", `{"cc":["a@example.test"]}`, true},
		{2034, "/api/v1/admin/queue/bulk-delete", "delete", `{"ids":[-1]}`, false},
		{2034, "/api/v1/admin/queue/bulk-delete", "delete", `{"ids":[0]}`, true},
		{2037, "/api/v1/admin/aliases/{id}", "patch", `{"destination_address":"a"}`, false},
		{2037, "/api/v1/admin/aliases/{id}", "patch", `{"destination_address":"a@example.test"}`, true},
		{2038, "/api/v1/messages/{id}", "patch", `{"keywords":[""]}`, false},
		{2038, "/api/v1/messages/{id}", "patch", `{"keywords":["\\Seen"]}`, false},
		{2038, "/api/v1/messages/{id}", "patch", `{"keywords":["$Junk","$junk"],"folder":"Work/Projects"}`, true},
		{2038, "/api/v1/messages/{id}", "patch", `{"folder":".."}`, false},
		{2038, "/api/v1/messages/{id}", "patch", `{"folder":" "}`, false},
		{2038, "/api/v1/messages/{id}", "patch", `{"folder":null}`, true},
	} {
		t.Run(fmt.Sprintf("%d/%s", test.issue, test.body), func(t *testing.T) {
			pointer := fuzzFindingPointer(test.path, test.method) + "/requestBody/content/application~1json/schema"
			schema := fuzzFindingSchema(t, compiler, pointer)
			value, err := jsonschema.UnmarshalJSON(strings.NewReader(test.body))
			if err != nil {
				t.Fatal(err)
			}
			err = schema.Validate(value)
			if (err == nil) != test.valid {
				t.Errorf("accepts=%t, want %t: %v", err == nil, test.valid, err)
			}
		})
	}
	paths := doc["paths"].(map[string]interface{})
	for _, test := range []struct {
		issue       int
		path, query string
	}{
		{2016, "/api/v1/admin/audit", "since"},
		{2019, "/api/v1/admin/logs/delivery", "until"},
		{2020, "/api/v1/admin/audit", "until"},
		{2021, "/api/v1/admin/api-keys", "expires_before"},
		{2027, "/api/v1/accounts/{id}/search", "before"},
		{2035, "/api/v1/admin/api-keys", "last_used_before"},
		{2036, "/api/v1/admin/logs/delivery", "since"},
		{2039, "/api/v1/accounts/{id}/search", "after"},
	} {
		t.Run(fmt.Sprintf("%d/%s", test.issue, test.query), func(t *testing.T) {
			operation := paths[test.path].(map[string]interface{})["get"].(map[string]interface{})
			for index, parameter := range operation["parameters"].([]interface{}) {
				if parameter.(map[string]interface{})["name"] != test.query {
					continue
				}
				pointer := fuzzFindingPointer(test.path, "get") + fmt.Sprintf("/parameters/%d/schema", index)
				schema := fuzzFindingSchema(t, compiler, pointer)
				if err := schema.Validate("a"); err == nil {
					t.Fatal("the nightly minimized date is still permitted")
				}
				if err := schema.Validate("2026-11-07T10:00:00+01:00"); err != nil {
					t.Fatal(err)
				}
				return
			}
			t.Fatal("query parameter missing")
		})
	}
	for _, test := range []struct {
		issue        int
		path, method string
	}{
		{2022, "/api/v1/accounts/{id}/sieve", "delete"},
		{2031, "/api/v1/accounts/{id}/sieve", "get"},
		{2032, "/api/v1/accounts/{id}/vacation", "delete"},
	} {
		t.Run(fmt.Sprintf("%d/path", test.issue), func(t *testing.T) {
			operation := paths[test.path].(map[string]interface{})[test.method].(map[string]interface{})
			for index, parameter := range operation["parameters"].([]interface{}) {
				if parameter.(map[string]interface{})["name"] != "id" {
					continue
				}
				pointer := fuzzFindingPointer(test.path, test.method) + fmt.Sprintf("/parameters/%d/schema", index)
				schema := fuzzFindingSchema(t, compiler, pointer)
				if err := schema.Validate(0); err == nil {
					t.Fatal("zero is not an account ID")
				}
				if err := schema.Validate(1000); err != nil {
					t.Fatalf("a missing account is a valid path, answered with 404: %v", err)
				}
				return
			}
			t.Fatal("path parameter missing")
		})
	}
}
