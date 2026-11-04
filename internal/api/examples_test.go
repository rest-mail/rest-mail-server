package api

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
)

// exampleFindingsFile lists what openapi-example.json does not yet get right, one finding a
// line, each to be fixed and taken off the list (#625).
const exampleFindingsFile = "../../tests/contract/example-findings.txt"

// missingExamplePairs lists each documented response that no named pair shows, as
// "operationId status", across the given generators. It reads the same example-missing
// warnings the strict areas enforce, for every operation a generator registers.
func missingExamplePairs(generators []*oapifly.Generator) []string {
	seen := map[string]bool{}
	for _, generator := range generators {
		for _, w := range generator.Report() {
			if w.Kind == oapifly.WarnExampleMissing {
				seen[w.OperationID+" "+w.Status] = true
			}
		}
	}
	missing := make([]string, 0, len(seen))
	for line := range seen {
		missing = append(missing, line)
	}
	sort.Strings(missing)
	return missing
}

// Every documented response of every operation has a named pair (#625). TestStrictAPIAreas
// enforces only the operations listed in internal/api/strict_areas, and a new operation is not
// listed when it is added, so that test cannot catch it. There is no list of exceptions: a new
// operation brings its pairs with it.
func TestEveryDocumentedResponseHasANamedPair(t *testing.T) {
	if missing := missingExamplePairs(gapGenerators(t)); len(missing) > 0 {
		t.Errorf("documented responses with no named pair in openapi-example.json; add a pair and prove it:\n%s",
			strings.Join(missing, "\n"))
	}
}

// The check above fails for an operation no strict area lists, for every status a route
// documents, and for one pair taken away from an operation that has the rest of them.
func TestMissingExamplePairsAreFound(t *testing.T) {
	const operation = "operationWithoutPairs"
	const route = "/api/v1/operation-without-pairs"

	t.Run("new operation", func(t *testing.T) {
		routers := gapRouters(t)[0]
		endpoint.Register(endpoint.Routes{Router: routers.Public.(chi.Router), Registry: routers.Registry},
			endpoint.Operation{Method: http.MethodGet, Path: route, OperationID: operation, Summary: "Has no pairs",
				Errors: []int{http.StatusForbidden, http.StatusNotFound}},
			func(*http.Request, struct{}) (struct{}, error) { return struct{}{}, nil })
		generator := routers.generator("report")

		data, err := json.Marshal(generator.Generate()["paths"])
		if err != nil {
			t.Fatal(err)
		}
		var paths map[string]map[string]struct {
			Responses map[string]json.RawMessage `json:"responses"`
		}
		if err := json.Unmarshal(data, &paths); err != nil {
			t.Fatal(err)
		}
		var want []string
		for status := range paths[route]["get"].Responses {
			want = append(want, operation+" "+status)
		}
		sort.Strings(want)
		if len(want) < 3 {
			t.Fatalf("the new operation documents only %v", want)
		}
		if got := missingExamplePairs([]*oapifly.Generator{generator}); !slices.Equal(got, want) {
			t.Fatalf("missing pairs = %v, want %v", got, want)
		}
	})

	t.Run("one pair removed", func(t *testing.T) {
		file, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
		if err != nil {
			t.Fatal(err)
		}
		delete(file["listMessageRecipients"], "unauthenticated")
		data, err := oapifly.MarshalExamples(file)
		if err != nil {
			t.Fatal(err)
		}
		routers := gapRouters(t)[0]
		generator := describeWith(routers.Registry, "report", fstest.MapFS{"openapi-example.json": {Data: data}})
		want := []string{"listMessageRecipients 401"}
		if got := missingExamplePairs([]*oapifly.Generator{generator}); !slices.Equal(got, want) {
			t.Fatalf("missing pairs = %v, want %v", got, want)
		}
	})
}

// The example pairs in openapi-example.json fit the description generated from the routes
// (#625, antimatter-studios/go-oapifly#20): every pair names an operation, parameter, body and
// status the description has, every body validates against its schema, and the request of a
// 400 or 422 pair does not. A finding not yet fixed is listed in example-findings.txt; one
// that is not listed fails, and so does a listed one that is gone, so the list only shrinks.
// A pair for an operation that is in openapi.yaml and not registered yet waits for it.
func TestExamplePairsFitTheDescription(t *testing.T) {
	inOracle := map[string]bool{}
	for _, op := range operationsOf(oracleDocument(t)) {
		inOracle[op.id] = true
	}

	found := map[string]bool{}
	generators := gapGenerators(t)
	registered := map[string]bool{}
	for _, generator := range generators {
		for operation := range generatedOperationIDs(t, generator) {
			registered[operation] = true
		}
	}
	warnings := map[string]oapifly.Warning{}
	for _, generator := range generators {
		for _, warning := range generator.Report() {
			warnings[exampleFinding(warning)] = warning
		}
	}
	for _, w := range warnings {
		switch w.Kind {
		case oapifly.WarnExampleOrphaned:
			if w.Method == "" && (inOracle[w.OperationID] || registered[w.OperationID]) {
				continue
			}
		case oapifly.WarnExampleInvalid, oapifly.WarnExamplesUnreadable:
		default:
			continue
		}
		found[exampleFinding(w)] = true
	}

	listed := map[string]bool{}
	f, err := os.Open(exampleFindingsFile)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" && !strings.HasPrefix(line, "#") {
			listed[line] = true
		}
	}

	var unlisted, fixed []string
	for finding := range found {
		if !listed[finding] {
			unlisted = append(unlisted, finding)
		}
	}
	for finding := range listed {
		if !found[finding] {
			fixed = append(fixed, finding)
		}
	}
	sort.Strings(unlisted)
	sort.Strings(fixed)
	if len(unlisted) > 0 {
		t.Errorf("openapi-example.json has findings %s does not list; fix the example or the type:\n%s",
			exampleFindingsFile, strings.Join(unlisted, "\n"))
	}
	if len(fixed) > 0 {
		t.Errorf("%s lists findings that are fixed; take them off:\n%s", exampleFindingsFile, strings.Join(fixed, "\n"))
	}
}

// exampleFinding is a warning about the example file as example-findings.txt lists it: its
// kind, the operation and example, and the status when it is about a response.
func exampleFinding(w oapifly.Warning) string {
	line := string(w.Kind) + " " + w.OperationID + "/" + w.Example
	if w.Status != "" {
		line += " " + w.Status
	}
	return line
}
