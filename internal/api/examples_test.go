package api

import (
	"bufio"
	"os"
	"sort"
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
)

// exampleFindingsFile lists what openapi-example.json does not yet get right, one finding a
// line, each to be fixed and taken off the list (#625).
const exampleFindingsFile = "../../tests/contract/example-findings.txt"

// phaseStatuses are the statuses #625 leaves to Vertrag's own phases and principals, and the
// ones no request can provoke on purpose: an example pair is not asked for them.
var phaseStatuses = map[string]bool{"401": true, "403": true, "429": true, "500": true, "503": true}

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
	missing := 0
	for _, w := range describedRouters(t).generator("test").Report() {
		switch w.Kind {
		case oapifly.WarnExampleMissing:
			if !phaseStatuses[w.Status] {
				missing++
			}
			continue
		case oapifly.WarnExampleOrphaned:
			if w.Method == "" && inOracle[w.OperationID] {
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
	t.Logf("%d documented responses have no example pair yet (#625)", missing)
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
