package handlers

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEscalatingDelayNonIntegralCap(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		base, max time.Duration
		misses    int
		want      time.Duration
	}{
		{"first", 2 * time.Second, 5 * time.Second, 1, 2 * time.Second},
		{"below_cap", 2 * time.Second, 5 * time.Second, 2, 4 * time.Second},
		{"at_cap", 2 * time.Second, 5 * time.Second, 3, 5 * time.Second},
		{"past_cap", 2 * time.Second, 5 * time.Second, 4, 5 * time.Second},
		{"default_below_cap", 75 * time.Millisecond, 2 * time.Second, 26, 1950 * time.Millisecond},
		{"default_at_cap", 75 * time.Millisecond, 2 * time.Second, 27, 2 * time.Second},
		{"base_exceeds_cap", 6 * time.Second, 5 * time.Second, 1, 5 * time.Second},
		{"near_duration_limit", time.Duration(1 << 62), time.Duration(1<<63 - 1), 1, time.Duration(1 << 62)},
		{"multiplication_would_overflow", time.Duration(1 << 62), time.Duration(1<<63 - 1), 2, time.Duration(1<<63 - 1)},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			if got := escalatingDelay(scenario.base, scenario.max, scenario.misses); got != scenario.want {
				t.Fatalf("escalatingDelay(%s, %s, %d) = %s, want %s", scenario.base, scenario.max, scenario.misses, got, scenario.want)
			}
		})
	}
}

func TestRestmailRecipientTarpitRegisteredContract(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			database := &publicReadDB{}
			handler := NewRestmailHandler(publicReadGorm(t, database), nil, nil,
				RestmailTarpitConfig{Enabled: enabled, Base: 2 * time.Second, Max: 5 * time.Second}, RestmailDeliverAuthConfig{})
			clock := time.Unix(1_700_000_000, 0)
			handler.tarpit.now = func() time.Time { return clock }
			var delays []time.Duration
			handler.tarpit.sleep = func(ctx context.Context, delay time.Duration) {
				if ctx.Value(publicReadBudgetKey{}) != true {
					t.Error("tarpit lost the HTTP context")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Error("tarpit lost the HTTP deadline")
				}
				delays = append(delays, delay)
			}
			routes := restmailRoutes(handler)
			for _, scenario := range []struct {
				name, kind, peer string
				want             time.Duration
			}{
				{"first_missing", "missing", "192.0.2.1:1000", 2 * time.Second},
				{"positive", "found", "192.0.2.1:1001", 0},
				{"second_disabled", "inactive", "192.0.2.1:1002", 4 * time.Second},
				{"database_fault", "fault", "192.0.2.1:1003", 0},
				{"third_missing", "missing", "192.0.2.1:1004", 5 * time.Second},
				{"independent_source", "missing", "192.0.2.2:1005", 2 * time.Second},
				{"still_capped", "missing", "192.0.2.1:1006", 5 * time.Second},
				{"decayed", "decayed", "192.0.2.1:1007", 2 * time.Second},
			} {
				t.Run(scenario.name, func(t *testing.T) {
					step := publicMailboxStep(scenario.kind != "inactive")
					status := 200
					if scenario.kind == "fault" {
						step.err = errors.New("private recipient lookup failure")
						status = 500
					} else if scenario.kind == "missing" || scenario.kind == "decayed" {
						step.rows = nil
					}
					if scenario.kind == "decayed" {
						clock = clock.Add(restmailTarpitDecay + time.Second)
					}
					database.steps = []publicReadStep{step}
					if step.rows == nil && step.err == nil {
						database.steps = append(database.steps,
							publicReadStep{query: "aliases", columns: []string{"id"}},
							publicReadStep{query: "aliases", columns: []string{"id"}})
					}
					database.reads = 0
					delays = nil
					request := publicReadRequest(t, "/restmail/mailboxes?address=reader@example.test", "")
					request.RemoteAddr = scenario.peer
					response := httptest.NewRecorder()
					routes.ServeHTTP(response, request)
					assertPublicReadBudget(t, database, response, status, len(database.steps))
					want := scenario.want
					if !enabled {
						want = 0
					}
					if want == 0 {
						if len(delays) != 0 {
							t.Fatalf("unexpected tarpit: %v", delays)
						}
					} else if len(delays) != 1 || delays[0] != want {
						t.Fatalf("tarpit = %v, want exactly [%s]", delays, want)
					}
				})
			}
		})
	}
}
