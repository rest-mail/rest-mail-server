package api

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/tests/e2e/limitgroups"
)

// The limits suite refuses a declared 429 it cannot spend, but it needs a whole
// topology, so a new limit used to fail only in a dispatched E2E run: #1597's
// `POST /api/v1/messages/{id}/unsubscribe/mailto`. This runs the same check on
// the generated description with no servers.
func TestEveryDeclared429IsALimitTheLimitsSuiteSpends(t *testing.T) {
	doc, _ := describedRouters(t).Description("test")
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var described struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(data, &described); err != nil {
		t.Fatal(err)
	}
	limits := map[string][]string{}
	for path, item := range described.Paths {
		for method, raw := range item {
			var operation struct {
				Responses map[string]struct {
					Description string `json:"description"`
				} `json:"responses"`
			}
			if json.Unmarshal(raw, &operation) != nil {
				continue
			}
			if limit, ok := operation.Responses["429"]; ok {
				limits[limit.Description] = append(limits[limit.Description], strings.ToUpper(method)+" "+path)
			}
		}
	}
	if len(limits[limitgroups.AuthThrottleShared]) == 0 {
		t.Fatalf("the description declares no sign-in limit, so this checked nothing: %v", limits)
	}
	for description := range limits {
		sort.Strings(limits[description])
	}
	for _, problem := range limitgroups.Problems(limits) {
		t.Error(problem)
	}
}

func TestLimitGroupProblemsNameTheOperation(t *testing.T) {
	problems := limitgroups.Problems(map[string][]string{
		limitgroups.HandlerLimitGroup: {"POST /api/v1/messages/send", "POST /api/v1/not-a-handler-limit"},
		"a limit nobody spends":       {"GET /api/v1/new"},
	})
	if len(problems) != 2 ||
		!strings.Contains(problems[0], "GET /api/v1/new") ||
		!strings.Contains(problems[1], "POST /api/v1/not-a-handler-limit declares the bare 429") {
		t.Fatalf("got %q", problems)
	}
}
