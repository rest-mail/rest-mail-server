package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/pipeline/filters"
)

func TestTestDataResetRegisteredPipelineBudgets(t *testing.T) {
	for _, budget := range []struct {
		name   string
		minute int
		hour   int
	}{
		{"minute", 1, 100},
		{"hour", 100, 1},
	} {
		for _, scenario := range []struct {
			name   string
			status int
			action pipeline.Action
		}{
			{"complete", http.StatusOK, pipeline.ActionContinue},
			{"clear_failure", http.StatusInternalServerError, pipeline.ActionDefer},
			{"commit_failure", http.StatusInternalServerError, pipeline.ActionDefer},
			{"production", http.StatusNotFound, pipeline.ActionDefer},
		} {
			t.Run(budget.name+"/"+scenario.name, func(t *testing.T) {
				fixed := time.Date(2026, time.November, 11, 0, 0, 0, 0, time.UTC)
				t.Cleanup(filters.SetRateLimitClock(func() time.Time { return fixed }))
				configuration, err := json.Marshal(map[string]int{"max_per_minute": budget.minute, "max_per_hour": budget.hour})
				if err != nil {
					t.Fatal(err)
				}
				execute := func(want pipeline.Action) {
					t.Helper()
					filter, err := pipeline.DefaultRegistry.Create("rate_limit", configuration)
					if err != nil {
						t.Fatal(err)
					}
					result, err := filter.Execute(context.Background(), &pipeline.EmailJSON{Metadata: map[string]string{"auth_account": "4"}})
					if err != nil || result.Action != want {
						t.Fatalf("registered %s budget action %v, want %s: %v", budget.name, result, want, err)
					}
				}
				execute(pipeline.ActionContinue)
				execute(pipeline.ActionDefer)
				fixture := newTestDataMutationScenarios(t)[scenario.name]
				response := mailboxSessionEndingRequest(t, fixture, http.MethodPost, testDataMutationPaths["resetTestData"], nil)
				if response.Code != scenario.status {
					t.Fatalf("reset returned %d, want %d: %s", response.Code, scenario.status, response.Body.String())
				}
				execute(scenario.action)
				execute(pipeline.ActionDefer)
			})
		}
	}
}
