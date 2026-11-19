package api

import (
	"errors"
	"net/http"
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

// outsideStrictScope lists the operations the given generators document that no strict
// area names. TestStrictAPIAreas fails on a warning only within the strict scope, so a
// warning about one of these, of any kind but the example kinds examples_test.go checks
// for every operation, fails no test (#2099 AC03).
func outsideStrictScope(t *testing.T, generators []*oapifly.Generator, scope oapifly.StrictScope) []string {
	t.Helper()
	strict := map[string]bool{}
	for _, operation := range scope.Operations {
		strict[operation] = true
	}
	outside := map[string]bool{}
	for _, generator := range generators {
		for operation := range generatedOperationIDs(t, generator) {
			if !strict[operation] {
				outside[operation] = true
			}
		}
	}
	list := make([]string, 0, len(outside))
	for operation := range outside {
		list = append(list, operation)
	}
	sort.Strings(list)
	return list
}

// Every operation that production or the test configuration registers is in a strict
// area, so a description warning about any of them, of any kind, fails
// TestStrictAPIAreas (#2099 AC03). There is no list of exceptions: a new operation
// joins a strict area in the change that adds it, as its named pairs do.
func TestEveryRegisteredOperationIsStrict(t *testing.T) {
	if outside := outsideStrictScope(t, gapGenerators(t), strictAreaScope()); len(outside) > 0 {
		t.Errorf("operations in no internal/api/strict_areas file, whose description warnings fail no test; "+
			"add each to the area of the change that registers it:\n%s", strings.Join(outside, "\n"))
	}
}

// The check above finds an operation added without a strict area and one taken out of
// its area, and the warning such an operation can carry fails TestStrictAPIAreas only
// once the operation is in the scope.
func TestOperationsOutsideTheStrictScopeAreFound(t *testing.T) {
	t.Run("new operation", func(t *testing.T) {
		const operation = "operationOutsideStrictScope"
		routers := gapRouters(t)[1]
		// The handler takes no path parameter for {id}: a description warning.
		endpoint.Register(endpoint.Routes{Router: routers.Public.(chi.Router), Registry: routers.Registry},
			endpoint.Operation{Method: http.MethodGet, Path: "/api/v1/outside-strict-scope/{id}",
				OperationID: operation, Summary: "Is in no strict area"},
			func(*http.Request, struct{}) (struct{}, error) { return struct{}{}, nil })
		generator := routers.generator("report")

		var warned []oapifly.WarningKind
		for _, w := range generator.Report() {
			if w.OperationID == operation && !exampleKinds[w.Kind] {
				warned = append(warned, w.Kind)
			}
		}
		if len(warned) == 0 {
			t.Fatal("the unrouted path parameter gave no description warning; the control proves nothing")
		}
		if err := generator.Check(); err != nil {
			var strict *oapifly.StrictError
			if !errors.As(err, &strict) {
				t.Fatal(err)
			}
			for _, w := range strict.Warnings {
				if w.OperationID == operation {
					t.Fatalf("an operation in no strict area failed Check (%s); this control no longer shows the gap", w.Kind)
				}
			}
		}
		if got := outsideStrictScope(t, []*oapifly.Generator{generator}, strictAreaScope()); !slices.Equal(got, []string{operation}) {
			t.Fatalf("outside the strict scope = %v, want [%s]", got, operation)
		}

		scope := strictAreaScope()
		scope.Operations = append(scope.Operations, operation)
		within := describeWithScope(routers.Registry, "report", restmail.Examples, scope)
		var strict *oapifly.StrictError
		if err := within.Check(); !errors.As(err, &strict) || !slices.ContainsFunc(strict.Warnings, func(w oapifly.Warning) bool {
			return w.OperationID == operation
		}) {
			t.Fatalf("the same operation in a strict area did not fail Check: %v", err)
		}
	})

	// setAccountTimeZone was in no strict area before #2099: a pair of it naming a
	// prerequisite no runner provides was reported, and failed nothing.
	t.Run("operation taken out of its area", func(t *testing.T) {
		const operation = "setAccountTimeZone"
		scope := strictAreaScope()
		scope.Operations = slices.DeleteFunc(slices.Clone(scope.Operations), func(id string) bool {
			return id == operation
		})
		if got := outsideStrictScope(t, gapGenerators(t), scope); !slices.Equal(got, []string{operation}) {
			t.Fatalf("outside the strict scope = %v, want [%s]", got, operation)
		}

		file, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
		if err != nil {
			t.Fatal(err)
		}
		pair := file[operation]["set"]
		pair.Requires = map[string]string{"state": "no_runner_provides_this"}
		file[operation]["set"] = pair
		data, err := oapifly.MarshalExamples(file)
		if err != nil {
			t.Fatal(err)
		}
		examples := fstest.MapFS{"openapi-example.json": {Data: data}}
		routers := gapRouters(t)[1]
		unknownRequirement := func(err error) bool {
			var strict *oapifly.StrictError
			return errors.As(err, &strict) && slices.ContainsFunc(strict.Warnings, func(w oapifly.Warning) bool {
				return w.OperationID == operation && w.Kind == oapifly.WarnExampleRequirementUnknown
			})
		}
		if err := describeWithScope(routers.Registry, "report", examples, scope).Check(); unknownRequirement(err) {
			t.Fatalf("outside its area the unknown requirement already failed Check; this control no longer shows the gap: %v", err)
		}
		if err := describeWithScope(routers.Registry, "report", examples, strictAreaScope()).Check(); !unknownRequirement(err) {
			t.Fatalf("in its area the unknown requirement did not fail Check: %v", err)
		}
	})
}
