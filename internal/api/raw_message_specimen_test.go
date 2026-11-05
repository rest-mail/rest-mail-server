package api

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// rawMessageSpecimen is what the contract run's coverage phase draws for a format: byte field:
// base64 of "vertrag". It is no message: it has no headers, and so no recipient and no Message-ID.
const rawMessageSpecimen = "dmVydHJhZw=="

const lostProbesBaselineFile = "../../tests/contract/lost-probes-baseline.txt"

// The coverage phase sends the specimen as a valid probe wherever the description permits a body
// holding it, and fails the run unless the answer is a 2xx. Only a real message gets that. So an
// operation taking a raw_message either has a description that refuses the specimen, or a hook
// that gives its probe a message, which lost-probes-baseline.txt holds. The contract suite doesn't
// run on a developer's machine; this says the same thing about the same description.
func TestDescriptionRefusesTheRawMessageSpecimen(t *testing.T) {
	doc := generatedDocument(t)
	hooked := lostProbeOperations(t)
	const resource = "https://rest-mail.test/raw-message-body.json"
	found := 0
	for _, op := range operationsOf(doc) {
		schema, ok := rawMessageBodySchema(doc, op)
		if !ok {
			continue
		}
		found++
		body := map[string]interface{}{"raw_message": rawMessageSpecimen}
		properties, _ := schema["properties"].(map[string]interface{})
		required, _ := schema["required"].([]interface{})
		for _, name := range required {
			field, _ := resolved(doc, properties[name.(string)]).(map[string]interface{})
			if _, held := body[name.(string)]; held {
				continue
			}
			// A format isn't checked here, nor by the contract run deciding whether to send a probe.
			if field["type"] != "string" {
				t.Fatalf("%s requires %v as well, and this check only fills in a string: give it a value for that", op.id, name)
			}
			body[name.(string)] = "x"
		}
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource(resource, schema); err != nil {
			t.Fatal(err)
		}
		compiled, err := compiler.Compile(resource)
		if err != nil {
			t.Fatalf("%s: %v", op.id, err)
		}
		permitted := compiled.Validate(body) == nil
		if permitted && !hooked[op.id] {
			t.Errorf("%s permits raw_message %q, which is no message: the contract run will call the refusal a disagreement. State what makes the field valid, or give the probe a message in a hook", op.id, rawMessageSpecimen)
		}
	}
	if found == 0 {
		t.Fatal("no operation takes a raw_message of format byte")
	}
}

// rawMessageBodySchema is the JSON request body of an operation taking a raw_message as base64.
func rawMessageBodySchema(doc map[string]interface{}, op describedOp) (map[string]interface{}, bool) {
	body, _ := resolved(doc, op.operation["requestBody"]).(map[string]interface{})
	content, _ := body["content"].(map[string]interface{})
	media, _ := content["application/json"].(map[string]interface{})
	schema, _ := resolved(doc, media["schema"]).(map[string]interface{})
	properties, _ := schema["properties"].(map[string]interface{})
	field, _ := resolved(doc, properties["raw_message"]).(map[string]interface{})
	return schema, field["format"] == "byte"
}

// lostProbeOperations names the operations the baseline says a hook ends probes of.
func lostProbeOperations(t *testing.T) map[string]bool {
	t.Helper()
	file, err := os.Open(lostProbesBaselineFile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	operations := map[string]bool{}
	lines := bufio.NewScanner(file)
	for lines.Scan() {
		if fields := strings.Fields(lines.Text()); len(fields) == 2 && !strings.HasPrefix(fields[0], "#") {
			operations[fields[0]] = true
		}
	}
	if err := lines.Err(); err != nil {
		t.Fatal(err)
	}
	return operations
}
