package api

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v2"

	"github.com/restmail/restmail"
)

// Every operation registered with endpoint.Register is described from its code, and that
// description is held to openapi.yaml, the proven hand-written one, operation by operation
// (#671): the operationId, parameters, request bodies, responses and security with roles,
// compared after resolving every reference and spelling nullable the 3.1 way. A difference
// that is accepted is recorded with its reason in tests/contract/oracle-differences.yml; any
// other fails, and so does a recorded one that no longer occurs.

type oracleDifference struct {
	// Operation is an operationId, or * for every operation.
	Operation string `yaml:"operation"`
	// Difference is the difference as this test reports it; * matches any run of characters.
	Difference string `yaml:"difference"`
	Reason     string `yaml:"reason"`
}

const oracleDifferencesFile = "../../tests/contract/oracle-differences.yml"

func TestGeneratedDescriptionMatchesOracle(t *testing.T) {
	oracle := oracleDocument(t)
	generated := generatedDocument(t)

	data, err := os.ReadFile(oracleDifferencesFile)
	if err != nil {
		t.Fatal(err)
	}
	var accepted []oracleDifference
	if err := yaml.Unmarshal(data, &accepted); err != nil {
		t.Fatalf("%s: %v", oracleDifferencesFile, err)
	}
	for _, a := range accepted {
		if a.Operation == "" || a.Difference == "" || strings.TrimSpace(a.Reason) == "" {
			t.Errorf("%s: every accepted difference names its operation, the difference and a reason: %+v", oracleDifferencesFile, a)
		}
	}

	used := make([]bool, len(accepted))
	var unrecorded []string
	for _, op := range operationsOf(generated) {
		for _, difference := range compareOperation(oracle, generated, op) {
			matched := false
			for i, a := range accepted {
				if (a.Operation == "*" || a.Operation == op.id) && globMatch(a.Difference, difference) {
					used[i], matched = true, true
				}
			}
			if !matched {
				unrecorded = append(unrecorded, op.id+": "+difference)
			}
		}
	}
	sort.Strings(unrecorded)
	if len(unrecorded) > 0 {
		t.Errorf("the generated description differs from openapi.yaml; fix the code or record the difference, with its reason, in %s:\n%s",
			filepath.Base(oracleDifferencesFile), strings.Join(unrecorded, "\n"))
	}
	for i, a := range accepted {
		if !used[i] {
			t.Errorf("%s records a difference that no longer occurs, take it off: %s: %s", filepath.Base(oracleDifferencesFile), a.Operation, a.Difference)
		}
	}
}

func oracleDocument(t *testing.T) map[string]interface{} {
	t.Helper()
	data, err := restmail.OpenAPI.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc interface{}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return jsonShaped(doc).(map[string]interface{})
}

// jsonShaped turns YAML's map[interface{}]interface{} into the map[string]interface{} JSON
// decodes to, and its integers into JSON's numbers.
func jsonShaped(v interface{}) interface{} {
	switch v := v.(type) {
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(v))
		for k, child := range v {
			out[fmt.Sprint(k)] = jsonShaped(child)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, child := range v {
			out[i] = jsonShaped(child)
		}
		return out
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case uint64:
		return float64(v)
	}
	return v
}

func generatedDocument(t *testing.T) map[string]interface{} {
	t.Helper()
	doc, warnings := describedRouters(t).Description("test")
	if len(warnings) > 0 {
		t.Fatalf("the registered routes describe with warnings: %v", warnings)
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

type describedOp struct {
	id, path, method string
	operation        map[string]interface{}
}

func operationsOf(doc map[string]interface{}) []describedOp {
	var ops []describedOp
	paths, _ := doc["paths"].(map[string]interface{})
	for path, item := range paths {
		methods, _ := item.(map[string]interface{})
		for method, op := range methods {
			operation, ok := op.(map[string]interface{})
			if !ok {
				continue
			}
			id, _ := operation["operationId"].(string)
			ops = append(ops, describedOp{id: id, path: path, method: method, operation: operation})
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].id < ops[j].id })
	return ops
}

// compareOperation lists how one generated operation differs from the oracle's.
func compareOperation(oracle, generated map[string]interface{}, op describedOp) []string {
	paths, _ := oracle["paths"].(map[string]interface{})
	item, _ := paths[op.path].(map[string]interface{})
	theirs, ok := item[op.method].(map[string]interface{})
	if !ok {
		return []string{"not in openapi.yaml"}
	}
	ours := op.operation
	var out []string

	if theirs["operationId"] != ours["operationId"] {
		out = append(out, fmt.Sprintf("operationId: openapi.yaml %v, generated %v", theirs["operationId"], ours["operationId"]))
	}

	// Parameters, by where they are and their name, the path's own with the operation's.
	generatedPaths, _ := generated["paths"].(map[string]interface{})
	ourItem, _ := generatedPaths[op.path].(map[string]interface{})
	theirParams, ourParams := parametersOf(oracle, item, theirs), parametersOf(generated, ourItem, ours)
	for _, key := range unionKeys(theirParams, ourParams) {
		a, inA := theirParams[key]
		b, inB := ourParams[key]
		switch {
		case !inA:
			out = append(out, "parameter "+key+": generated only")
		case !inB:
			out = append(out, "parameter "+key+": openapi.yaml only")
		default:
			if a["required"] != b["required"] && !(a["required"] == nil && b["required"] == false) && !(a["required"] == false && b["required"] == nil) {
				out = append(out, fmt.Sprintf("parameter %s: required: openapi.yaml %v, generated %v", key, a["required"], b["required"]))
			}
			out = append(out, diffSchemas("parameter "+key+" schema", oracle, a["schema"], generated, b["schema"])...)
		}
	}

	// The request body, per media type.
	theirBody, ourBody := resolved(oracle, theirs["requestBody"]), resolved(generated, ours["requestBody"])
	switch {
	case theirBody == nil && ourBody != nil:
		out = append(out, "request body: generated only")
	case theirBody != nil && ourBody == nil:
		out = append(out, "request body: openapi.yaml only")
	case theirBody != nil:
		tb, ob := theirBody.(map[string]interface{}), ourBody.(map[string]interface{})
		if truthy(tb["required"]) != truthy(ob["required"]) {
			out = append(out, fmt.Sprintf("request body: required: openapi.yaml %v, generated %v", truthy(tb["required"]), truthy(ob["required"])))
		}
		out = append(out, diffContent("request body", oracle, tb["content"], generated, ob["content"])...)
	}

	// Responses, per status.
	theirResponses, _ := theirs["responses"].(map[string]interface{})
	ourResponses, _ := ours["responses"].(map[string]interface{})
	for _, status := range unionKeys(theirResponses, ourResponses) {
		a, inA := theirResponses[status]
		b, inB := ourResponses[status]
		where := "response " + status
		switch {
		case !inA:
			out = append(out, where+": generated only")
		case !inB:
			out = append(out, where+": openapi.yaml only")
		default:
			ra, _ := resolved(oracle, a).(map[string]interface{})
			rb, _ := resolved(generated, b).(map[string]interface{})
			out = append(out, diffContent(where, oracle, ra["content"], generated, rb["content"])...)
			ha, _ := ra["headers"].(map[string]interface{})
			hb, _ := rb["headers"].(map[string]interface{})
			for _, name := range unionKeys(ha, hb) {
				if _, ok := ha[name]; !ok {
					out = append(out, where+" header "+name+": generated only")
				} else if _, ok := hb[name]; !ok {
					out = append(out, where+" header "+name+": openapi.yaml only")
				}
			}
		}
	}

	// Security with its roles, the document's default where the operation states none.
	if a, b := securityOf(oracle, theirs), securityOf(generated, ours); a != b {
		out = append(out, fmt.Sprintf("security: openapi.yaml %s, generated %s", a, b))
	}
	return out
}

func parametersOf(doc, item, op map[string]interface{}) map[string]map[string]interface{} {
	out := map[string]map[string]interface{}{}
	shared, _ := item["parameters"].([]interface{})
	own, _ := op["parameters"].([]interface{})
	for _, p := range append(append([]interface{}(nil), shared...), own...) {
		param, ok := resolved(doc, p).(map[string]interface{})
		if !ok {
			continue
		}
		out[fmt.Sprintf("%v %v", param["in"], param["name"])] = param
	}
	return out
}

func diffContent(where string, docA map[string]interface{}, a interface{}, docB map[string]interface{}, b interface{}) []string {
	ca, _ := a.(map[string]interface{})
	cb, _ := b.(map[string]interface{})
	var out []string
	for _, media := range unionKeys(ca, cb) {
		ma, inA := ca[media].(map[string]interface{})
		mb, inB := cb[media].(map[string]interface{})
		switch {
		case !inA:
			out = append(out, where+" "+media+": generated only")
		case !inB:
			out = append(out, where+" "+media+": openapi.yaml only")
		default:
			out = append(out, diffSchemas(where+" "+media, docA, ma["schema"], docB, mb["schema"])...)
		}
	}
	return out
}

func diffSchemas(where string, docA map[string]interface{}, a interface{}, docB map[string]interface{}, b interface{}) []string {
	na := normalSchema(docA, a, map[string]bool{})
	nb := normalSchema(docB, b, map[string]bool{})
	var out []string
	diffValues(where, na, nb, &out)
	return out
}

func diffValues(where string, a, b interface{}, out *[]string) {
	ma, aMap := a.(map[string]interface{})
	mb, bMap := b.(map[string]interface{})
	if aMap && bMap {
		for _, key := range unionKeys(ma, mb) {
			diffValues(where+"."+key, ma[key], mb[key], out)
		}
		return
	}
	if compact(a) != compact(b) {
		*out = append(*out, fmt.Sprintf("%s: openapi.yaml %s, generated %s", where, compact(a), compact(b)))
	}
}

// normalSchema is a schema with every reference resolved and only its structure left: no
// descriptions or examples, nullable spelt as a type that includes null, single-branch allOf
// merged, and required and enum sorted.
func normalSchema(doc map[string]interface{}, v interface{}, seen map[string]bool) interface{} {
	schema, ok := v.(map[string]interface{})
	if !ok {
		return v
	}
	if ref, ok := schema["$ref"].(string); ok {
		if seen[ref] {
			return map[string]interface{}{"$ref": ref}
		}
		seen[ref] = true
		defer delete(seen, ref)
		return normalSchema(doc, resolved(doc, schema), seen)
	}

	out := map[string]interface{}{}
	for key, child := range schema {
		switch {
		case key == "description" || key == "title" || key == "example" || key == "examples" ||
			key == "externalDocs" || key == "deprecated" || strings.HasPrefix(key, "x-"):
			continue
		case key == "properties":
			props := map[string]interface{}{}
			for name, prop := range child.(map[string]interface{}) {
				props[name] = normalSchema(doc, prop, seen)
			}
			out[key] = props
		case key == "items" || key == "additionalProperties" || key == "not":
			out[key] = normalSchema(doc, child, seen)
		case key == "allOf" || key == "oneOf" || key == "anyOf":
			var branches []interface{}
			for _, branch := range child.([]interface{}) {
				branches = append(branches, normalSchema(doc, branch, seen))
			}
			out[key] = branches
		default:
			out[key] = child
		}
	}

	// One allOf branch is that branch, with what sits beside it.
	if branches, ok := out["allOf"].([]interface{}); ok && len(branches) == 1 {
		delete(out, "allOf")
		merged, _ := branches[0].(map[string]interface{})
		for k, v := range out {
			merged[k] = v
		}
		out = merged
	}
	// The reference or null is the referenced shape, with null among its types.
	for _, key := range []string{"oneOf", "anyOf"} {
		if branches, ok := out[key].([]interface{}); ok && len(branches) == 2 {
			for i, branch := range branches {
				if b, ok := branch.(map[string]interface{}); ok && len(b) == 1 && b["type"] == "null" {
					other, _ := branches[1-i].(map[string]interface{})
					delete(out, key)
					for k, v := range other {
						out[k] = v
					}
					out["nullable"] = true
				}
			}
		}
	}
	if nullable, _ := out["nullable"].(bool); nullable {
		delete(out, "nullable")
		out["type"] = withNull(out["type"])
		if enum, ok := out["enum"].([]interface{}); ok && !containsNil(enum) {
			out["enum"] = append(enum, nil)
		}
	}
	delete(out, "nullable")
	// Any value at all is what additionalProperties allows when it is not there.
	if extra, ok := out["additionalProperties"].(map[string]interface{}); ok && len(extra) == 0 {
		delete(out, "additionalProperties")
	}
	if extra, ok := out["additionalProperties"].(bool); ok && extra {
		delete(out, "additionalProperties")
	}
	if types, ok := out["type"].([]interface{}); ok {
		if len(types) == 1 {
			out["type"] = types[0]
		} else {
			out["type"] = sortedValues(types)
		}
	}
	for _, key := range []string{"required", "enum"} {
		if list, ok := out[key].([]interface{}); ok {
			out[key] = sortedValues(list)
		}
		if list, ok := out[key].([]string); ok {
			values := make([]interface{}, len(list))
			for i, s := range list {
				values[i] = s
			}
			out[key] = sortedValues(values)
		}
	}
	return out
}

func withNull(t interface{}) interface{} {
	switch t := t.(type) {
	case string:
		return []interface{}{t, "null"}
	case []interface{}:
		for _, each := range t {
			if each == "null" {
				return t
			}
		}
		return append(t, "null")
	}
	return t
}

func containsNil(list []interface{}) bool {
	for _, v := range list {
		if v == nil {
			return true
		}
	}
	return false
}

func sortedValues(list []interface{}) []interface{} {
	out := append([]interface{}(nil), list...)
	sort.Slice(out, func(i, j int) bool { return compact(out[i]) < compact(out[j]) })
	return out
}

// resolved follows a $ref within doc, as many times as it takes.
func resolved(doc map[string]interface{}, v interface{}) interface{} {
	for i := 0; i < 16; i++ {
		m, ok := v.(map[string]interface{})
		if !ok {
			return v
		}
		ref, ok := m["$ref"].(string)
		if !ok || !strings.HasPrefix(ref, "#/") {
			return v
		}
		var node interface{} = doc
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			next, _ := node.(map[string]interface{})
			node = next[strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")]
		}
		v = node
	}
	return v
}

// securityOf spells an operation's requirements as a sorted list of alternatives, each a
// sorted list of scheme[roles].
func securityOf(doc map[string]interface{}, op map[string]interface{}) string {
	list, ok := op["security"].([]interface{})
	if !ok {
		list, _ = doc["security"].([]interface{})
	}
	var alternatives []string
	for _, requirement := range list {
		req, _ := requirement.(map[string]interface{})
		var schemes []string
		for name, roles := range req {
			var names []string
			for _, role := range roles.([]interface{}) {
				names = append(names, fmt.Sprint(role))
			}
			sort.Strings(names)
			schemes = append(schemes, name+"["+strings.Join(names, ",")+"]")
		}
		sort.Strings(schemes)
		alternatives = append(alternatives, strings.Join(schemes, "+"))
	}
	sort.Strings(alternatives)
	return "[" + strings.Join(alternatives, " | ") + "]"
}

func unionKeys[V any](a, b map[string]V) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func truthy(v interface{}) bool {
	b, _ := v.(bool)
	return b
}

func compact(v interface{}) string {
	if v == nil {
		return "absent"
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(data)
}

// globMatch matches s against pattern, where * stands for any run of characters.
func globMatch(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return regexp.MustCompile("^" + strings.Join(parts, ".*") + "$").MatchString(s)
}
