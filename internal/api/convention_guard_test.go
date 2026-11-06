package api

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The API's Decided conventions that a description can show, checked on the generated
// description of every route, disabled ones included (gapGenerators). What is not decided
// stays out: #672 holds delete statuses, pagination, naming, identifiers and dates.
//
// tests/contract/convention-exceptions.json holds each departure with its reason:
//
//	nullable_success_lists  "<operation> <status> <where>": a list a success may answer null
//	create_resource         "<operation>": where what a create made lives, when not under it
//	create_lifecycle        "<operation>": {"read"|"update"|"delete"|"*": why it has no such link}
type conventionExceptions struct {
	NullableSuccessLists map[string]string            `json:"nullable_success_lists"`
	CreateResource       map[string]createResource    `json:"create_resource"`
	CreateLifecycle      map[string]map[string]string `json:"create_lifecycle"`
}

type createResource struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// A reason has to say something: the same floor as the open-object exceptions.
const minExceptionReason = 30

func readConventionExceptions(t *testing.T) conventionExceptions {
	t.Helper()
	data, err := os.ReadFile("../../tests/contract/convention-exceptions.json")
	if err != nil {
		t.Fatal(err)
	}
	var exceptions conventionExceptions
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&exceptions); err != nil {
		t.Fatal(err)
	}
	return exceptions
}

func goodReason(reason string) bool { return len(strings.TrimSpace(reason)) >= minExceptionReason }

func schemaNamed(doc map[string]interface{}, ref string) (map[string]interface{}, string) {
	name := strings.TrimPrefix(ref, "#/components/schemas/")
	components, _ := doc["components"].(map[string]interface{})
	schemas, _ := components["schemas"].(map[string]interface{})
	schema, _ := schemas[name].(map[string]interface{})
	return schema, name
}

// listAdmitsNull reports whether a schema is a list that may also be null, in either of the
// spellings the generator and a hand-written schema use.
func listAdmitsNull(schema map[string]interface{}) bool {
	if types, ok := schema["type"].([]interface{}); ok {
		array, null := false, false
		for _, each := range types {
			array = array || each == "array"
			null = null || each == "null"
		}
		return array && null
	}
	if schema["nullable"] == true && schema["type"] == "array" {
		return true
	}
	for _, key := range []string{"oneOf", "anyOf"} {
		alternatives, _ := schema[key].([]interface{})
		array, null := false, false
		for _, each := range alternatives {
			alternative, _ := each.(map[string]interface{})
			array = array || alternative["type"] == "array"
			null = null || alternative["type"] == "null"
		}
		if array && null {
			return true
		}
	}
	return false
}

// nullableSuccessLists names every list a documented success may answer null, walking each
// JSON response schema through its references.
func nullableSuccessLists(doc map[string]interface{}) []string {
	found := map[string]bool{}
	var walk func(schema map[string]interface{}, where, site string, visiting map[string]bool)
	walk = func(schema map[string]interface{}, where, site string, visiting map[string]bool) {
		if schema == nil {
			return
		}
		if ref, ok := schema["$ref"].(string); ok {
			target, name := schemaNamed(doc, ref)
			if visiting[name] {
				return
			}
			visiting[name] = true
			walk(target, where, site, visiting)
			delete(visiting, name)
			return
		}
		if listAdmitsNull(schema) {
			found[site+" "+where] = true
		}
		for _, key := range []string{"oneOf", "anyOf", "allOf"} {
			alternatives, _ := schema[key].([]interface{})
			for _, each := range alternatives {
				alternative, _ := each.(map[string]interface{})
				walk(alternative, where, site, visiting)
			}
		}
		properties, _ := schema["properties"].(map[string]interface{})
		for name, each := range properties {
			property, _ := each.(map[string]interface{})
			walk(property, where+"/"+name, site, visiting)
		}
		items, _ := schema["items"].(map[string]interface{})
		walk(items, where+"/[]", site, visiting)
		additional, _ := schema["additionalProperties"].(map[string]interface{})
		walk(additional, where+"/*", site, visiting)
	}
	eachConventionOperation(doc, func(op conventionOperation) {
		for status, response := range op.responses {
			if !strings.HasPrefix(status, "2") {
				continue
			}
			described, _ := response.(map[string]interface{})
			content, _ := described["content"].(map[string]interface{})
			for media, each := range content {
				if media != "application/json" && !strings.HasSuffix(media, "+json") {
					continue
				}
				body, _ := each.(map[string]interface{})
				schema, _ := body["schema"].(map[string]interface{})
				walk(schema, "", op.id+" "+status, map[string]bool{})
			}
		}
	})
	names := make([]string, 0, len(found))
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type conventionOperation struct {
	id, method, path string
	responses        map[string]interface{}
}

func eachConventionOperation(doc map[string]interface{}, visit func(conventionOperation)) {
	paths, _ := doc["paths"].(map[string]interface{})
	for path, each := range paths {
		item, _ := each.(map[string]interface{})
		for method, value := range item {
			operation, ok := value.(map[string]interface{})
			if !ok {
				continue
			}
			id, _ := operation["operationId"].(string)
			if id == "" {
				continue
			}
			responses, _ := operation["responses"].(map[string]interface{})
			visit(conventionOperation{id: id, method: strings.ToUpper(method), path: path, responses: responses})
		}
	}
}

// A list is never null (#622, docs/api-conventions.md). A pointer to a slice is how one gets
// back in: oapifly.EmptyLists cannot tell its nil from a value, and the description says
// nullable. So the description is what is checked, and this fails for the type alone, before
// any route has been caught answering null.
func TestNoSuccessListIsNullable(t *testing.T) {
	exceptions := readConventionExceptions(t)
	for site, reason := range exceptions.NullableSuccessLists {
		if !goodReason(reason) {
			t.Errorf("nullable list exception %s has no real reason", site)
		}
	}
	seen := map[string]bool{}
	for _, generator := range gapGenerators(t) {
		for _, site := range nullableSuccessLists(contractDocument(t, generator)) {
			seen[site] = true
			if _, recorded := exceptions.NullableSuccessLists[site]; !recorded {
				t.Errorf("%s may answer null, and a list is never null: use a slice, not a pointer to one", site)
			}
		}
	}
	for site := range exceptions.NullableSuccessLists {
		if !seen[site] {
			t.Errorf("stale nullable list exception %s; remove it", site)
		}
	}
}

func TestNullableListGuardCatchesEverySpelling(t *testing.T) {
	generator := gapGenerators(t)[0]
	listOf := map[string]interface{}{"type": "string"}
	for name, nullable := range map[string]map[string]interface{}{
		"type array": {"type": []interface{}{"array", "null"}, "items": listOf},
		"one of":     {"oneOf": []interface{}{map[string]interface{}{"type": "array", "items": listOf}, map[string]interface{}{"type": "null"}}},
		"any of":     {"anyOf": []interface{}{map[string]interface{}{"type": "array", "items": listOf}, map[string]interface{}{"type": "null"}}},
		"nullable":   {"type": "array", "nullable": true, "items": listOf},
	} {
		t.Run(name, func(t *testing.T) {
			doc := contractDocument(t, generator)
			if got := nullableSuccessLists(doc); len(got) != 0 {
				t.Fatalf("baseline is not clean: %v", got)
			}
			// A field of a shared type, reached only through references, as the dashboard's was.
			activity, _ := schemaNamed(doc, "#/components/schemas/DashboardStats")
			activity["properties"].(map[string]interface{})["injected"] = nullable
			got := strings.Join(nullableSuccessLists(doc), "\n")
			if !strings.Contains(got, "getDashboardStats 200 /data/injected") {
				t.Fatalf("guard missed a nullable list: %q", got)
			}
		})
	}
}

// A create is an operation with a 201, or one named as a create whatever its status: the
// recovery address answers 200. A send and an import are not named here: they answer what
// they did, and a send with a 201 is caught by its status.
var namedAsACreate = regexp.MustCompile(`^(create|add|register|save|link)[A-Z]`)

var lifecycleVerbs = map[string]string{"GET": "read", "PUT": "update", "PATCH": "update", "DELETE": "delete"}

// A path parameter directly under the create's path: /aliases/{id} under /aliases.
var childOfCreate = regexp.MustCompile(`^/\{[^/{}]+\}$`)

// lifecycleFindings is what stands between a description and the rule: for each create, every
// read, update and delete of what it made is linked from a success of the create, or recorded
// with a reason, and so is a verb with no route at all (#1749).
func lifecycleFindings(doc map[string]interface{}, exceptions conventionExceptions) (findings []string, used map[string]bool) {
	used = map[string]bool{}
	var operations []conventionOperation
	eachConventionOperation(doc, func(op conventionOperation) { operations = append(operations, op) })
	sort.Slice(operations, func(i, j int) bool { return operations[i].id < operations[j].id })

	for _, create := range operations {
		_, status201 := create.responses["201"]
		if !status201 && !namedAsACreate.MatchString(create.id) {
			continue
		}
		linked := map[string]bool{}
		for status, each := range create.responses {
			if !strings.HasPrefix(status, "2") {
				continue
			}
			response, _ := each.(map[string]interface{})
			links, _ := response["links"].(map[string]interface{})
			for _, value := range links {
				link, _ := value.(map[string]interface{})
				if id, ok := link["operationId"].(string); ok {
					linked[id] = true
				}
			}
		}

		// Where it lives: stated, or under the create's path, or, for a resource with one
		// value per parent, the create's own path without its list.
		routes := map[string][]string{}
		add := func(op conventionOperation) {
			if verb, lifecycle := lifecycleVerbs[op.method]; lifecycle && op.id != create.id {
				routes[verb] = append(routes[verb], op.id)
			}
		}
		if stated, ok := exceptions.CreateResource[create.id]; ok {
			for _, op := range operations {
				if op.path == stated.Path {
					add(op)
				}
			}
			if len(routes) == 0 {
				findings = append(findings, fmt.Sprintf("%s: create_resource %s has no routes", create.id, stated.Path))
			}
		} else {
			for _, op := range operations {
				if strings.HasPrefix(op.path, create.path+"/") && childOfCreate.MatchString(strings.TrimPrefix(op.path, create.path)) {
					add(op)
				}
			}
			if len(routes) == 0 {
				for _, op := range operations {
					if op.path == create.path && !answersAList(doc, op) {
						add(op)
					}
				}
			}
		}

		reasons := exceptions.CreateLifecycle[create.id]
		for _, verb := range []string{"read", "update", "delete"} {
			missing := []string{}
			for _, id := range routes[verb] {
				if !linked[id] {
					missing = append(missing, id)
				}
			}
			sort.Strings(missing)
			reason, recorded := reasons[verb]
			if !recorded {
				reason, recorded = reasons["*"]
			}
			needsReason := len(routes[verb]) == 0 || len(missing) > 0
			switch {
			case needsReason && !recorded && len(missing) > 0:
				findings = append(findings, fmt.Sprintf("%s links no %s: %s; link it, or record why not", create.id, verb, strings.Join(missing, ", ")))
			case needsReason && !recorded:
				findings = append(findings, fmt.Sprintf("%s has no %s route; record why", create.id, verb))
			case needsReason && !goodReason(reason):
				findings = append(findings, fmt.Sprintf("%s: the reason for no %s link is not a reason", create.id, verb))
			case needsReason:
				if _, own := reasons[verb]; own {
					used[create.id+" "+verb] = true
				} else {
					used[create.id+" *"] = true
				}
			}
		}
	}
	return findings, used
}

// answersAList reports a read of a collection, which is not a read of what a create made.
func answersAList(doc map[string]interface{}, op conventionOperation) bool {
	if op.method != "GET" {
		return false
	}
	response, _ := op.responses["200"].(map[string]interface{})
	content, _ := response["content"].(map[string]interface{})
	body, _ := content["application/json"].(map[string]interface{})
	schema, _ := body["schema"].(map[string]interface{})
	if ref, ok := schema["$ref"].(string); ok {
		schema, _ = schemaNamed(doc, ref)
	}
	properties, _ := schema["properties"].(map[string]interface{})
	data, _ := properties["data"].(map[string]interface{})
	return data["type"] == "array"
}

func TestEveryCreateLeadsToItsLifecycle(t *testing.T) {
	exceptions := readConventionExceptions(t)
	for id, stated := range exceptions.CreateResource {
		if !goodReason(stated.Reason) {
			t.Errorf("create_resource %s has no real reason", id)
		}
	}
	used := map[string]bool{}
	for _, generator := range gapGenerators(t) {
		findings, each := lifecycleFindings(contractDocument(t, generator), exceptions)
		for _, finding := range findings {
			t.Error(finding)
		}
		for key := range each {
			used[key] = true
		}
	}
	for id, reasons := range exceptions.CreateLifecycle {
		for verb := range reasons {
			if !used[id+" "+verb] {
				t.Errorf("stale create_lifecycle exception %s %s; remove it", id, verb)
			}
		}
	}
}

func TestLifecycleGuardCatchesAMissingLinkOrReason(t *testing.T) {
	exceptions := readConventionExceptions(t)
	generator := gapGenerators(t)[0]
	baseline := func() map[string]interface{} {
		doc := contractDocument(t, generator)
		if findings, _ := lifecycleFindings(doc, exceptions); len(findings) != 0 {
			t.Fatalf("baseline is not clean: %v", findings)
		}
		return doc
	}
	t.Run("a link removed", func(t *testing.T) {
		doc := baseline()
		post := doc["paths"].(map[string]interface{})["/api/v1/admin/domains"].(map[string]interface{})["post"].(map[string]interface{})
		delete(post["responses"].(map[string]interface{})["201"].(map[string]interface{})["links"].(map[string]interface{}), "deleteDomain")
		findings, _ := lifecycleFindings(doc, exceptions)
		if len(findings) != 1 || !strings.Contains(findings[0], "createDomain links no delete: deleteDomain") {
			t.Fatalf("got %v", findings)
		}
	})
	t.Run("a reason removed", func(t *testing.T) {
		reasons := map[string]map[string]string{}
		for id, each := range exceptions.CreateLifecycle {
			reasons[id] = each
		}
		delete(reasons, "createContact")
		stripped := exceptions
		stripped.CreateLifecycle = reasons
		findings, _ := lifecycleFindings(baseline(), stripped)
		if len(findings) != 1 || !strings.Contains(findings[0], "createContact has no read route") {
			t.Fatalf("got %v", findings)
		}
	})
	t.Run("a reason that says nothing", func(t *testing.T) {
		reasons := map[string]map[string]string{}
		for id, each := range exceptions.CreateLifecycle {
			reasons[id] = each
		}
		reasons["createContact"] = map[string]string{"read": "none"}
		stripped := exceptions
		stripped.CreateLifecycle = reasons
		findings, _ := lifecycleFindings(baseline(), stripped)
		if len(findings) != 1 || !strings.Contains(findings[0], "createContact: the reason for no read link is not a reason") {
			t.Fatalf("got %v", findings)
		}
	})
}
