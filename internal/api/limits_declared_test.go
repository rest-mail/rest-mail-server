package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// limitsSuiteFile holds handlerLimits, the operations whose handler answers 429 itself.
const limitsSuiteFile = "../../tests/e2e/limits_test.go"

// bareRateLimit is what an operation's 429 says when it only lists the status among its
// errors, the limits suite's handlerLimitGroup.
const bareRateLimit = "Too Many Requests"

// TestBareRateLimitsAreInTheLimitsSuite is TestLimits/Declared's check on handlerLimits, without
// the topology that suite needs: an operation that declares the bare 429 must be one the suite
// knows a handler limits, or the run fails only after the whole contract gate has passed.
func TestBareRateLimitsAreInTheLimitsSuite(t *testing.T) {
	listed := handlerLimitOperations(t)
	var missing []string
	declared := 0
	for _, op := range operationsOf(generatedDocument(t)) {
		responses, _ := op.operation["responses"].(map[string]interface{})
		limited, _ := responses["429"].(map[string]interface{})
		if limited["description"] != bareRateLimit {
			continue
		}
		declared++
		name := strings.ToUpper(op.method) + " " + op.path
		if !listed[name] {
			missing = append(missing, name)
		}
	}
	if declared == 0 {
		t.Fatalf("no operation declares the bare 429 %q: this test no longer reads what the suite reads", bareRateLimit)
	}
	sort.Strings(missing)
	for _, name := range missing {
		t.Errorf("%s declares the bare 429 and is not in handlerLimits in %s: describe the limit where it is applied", name, limitsSuiteFile)
	}
}

// handlerLimitOperations reads the keys of handlerLimits from the suite's source, so that a
// package every topology-free run builds does not have to import the e2e suite.
func handlerLimitOperations(t *testing.T) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), limitsSuiteFile, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	operations := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "handlerLimits" || len(spec.Values) != 1 {
			return true
		}
		literal, ok := spec.Values[0].(*ast.CompositeLit)
		if !ok {
			t.Fatalf("handlerLimits in %s is not a map literal", limitsSuiteFile)
		}
		for _, element := range literal.Elts {
			key, ok := element.(*ast.KeyValueExpr).Key.(*ast.BasicLit)
			if !ok {
				t.Fatalf("a key of handlerLimits in %s is not a string literal", limitsSuiteFile)
			}
			name, err := strconv.Unquote(key.Value)
			if err != nil {
				t.Fatal(err)
			}
			operations[name] = true
		}
		return false
	})
	if len(operations) == 0 {
		t.Fatalf("no handlerLimits found in %s", limitsSuiteFile)
	}
	return operations
}
