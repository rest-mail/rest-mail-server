package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline/filters"
)

// answered is what a registered route writes for out: the real writer, so the wire bytes are
// what these tests read. Decoding them would hide a null, which decodes to the same nil slice.
func answered[T any](t *testing.T, out T) string {
	t.Helper()
	r := chi.NewRouter()
	endpoint.Register(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()},
		endpoint.Operation{Method: http.MethodGet, Path: "/answer"},
		func(*http.Request, struct{}) (T, error) { return out, nil })
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/answer", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	return rr.Body.String()
}

// A trace saved without a timeline has no column value, or a JSON null when the pipeline ran
// no steps (traceRow marshals a nil slice). Both answer [] on both of the routes that read one.
func TestMessageTraceWithoutATimelineAnswersAnEmptyList(t *testing.T) {
	for name, stored := range map[string]json.RawMessage{"no value": nil, "json null": json.RawMessage(`null`), "empty": json.RawMessage(`[]`)} {
		t.Run(name, func(t *testing.T) {
			trace, err := messageTraceOf(models.MessageTrace{Stages: stored})
			if err != nil {
				t.Fatal(err)
			}
			if body := answered(t, trace); !strings.Contains(body, `"stages":[]`) {
				t.Errorf("getMessageTrace answered %s", body)
			}
			if body := answered(t, []MessageTrace{trace}); !strings.Contains(body, `"stages":[]`) {
				t.Errorf("listPipelineLogs answered %s", body)
			}
		})
	}
}

// A snapshot of an empty database answers each table as a list. snapshotJSON crosses a JSON
// boundary, so a table the models left nil arrives as null there and has to leave as [].
func TestSnapshotOfNoRowsAnswersEmptyLists(t *testing.T) {
	snapshot, err := snapshotJSON[TestSnapshot](testSnapshotModels{})
	if err != nil {
		t.Fatal(err)
	}
	body := answered(t, snapshot)
	for _, table := range []string{"domains", "mailboxes", "aliases", "messages", "queue", "pipelines", "bans"} {
		if !strings.Contains(body, `"`+table+`":[]`) {
			t.Errorf("%s is not an empty list in %s", table, body)
		}
	}
}

// The sidecar writes logs as a list today (projects/js-filter-sidecar/test). These routes
// forward its bytes past the writer's normalization, so a sidecar that stopped doing so must
// not reach the caller as null, whichever of its answers carries the list.
func TestForwardedScriptAnswerKeepsLogsAList(t *testing.T) {
	for _, tc := range []struct{ name, sidecar, want string }{
		{"validate dry run", `{"valid":true,"dry_run":{"result":{"action":"continue"},"logs":null}}`, `"logs":[]`},
		{"validate failure", `{"valid":true,"dry_run":{"error":"boom","logs":null},"phase":"execution"}`, `"logs":[]`},
		{"untouched", `{"valid":true,"dry_run":{"result":{"logs":null},"logs":["hi"]}}`, `{"valid":true,"dry_run":{"result":{"logs":null},"logs":["hi"]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.sidecar))
			}))
			t.Cleanup(sidecar.Close)
			rec := validateCustomFilter(NewPipelineHandler(nil, nil, filters.JSSidecar{URL: sidecar.URL, Token: testJSSidecarToken}))
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tc.want) || strings.Contains(strings.ReplaceAll(rec.Body.String(), `"result":{"logs":null}`, ""), `"logs":null`) {
				t.Fatalf("got %d %s, want %s", rec.Code, rec.Body, tc.want)
			}
		})
	}
	for _, tc := range []struct{ sidecar, want string }{
		{`{"result":{"action":"continue"},"logs":null}`, `{"logs":[],"result":{"action":"continue"}}`},
		{`{"result":{"logs":null},"logs":[]}`, `{"result":{"logs":null},"logs":[]}`},
		{`{"error":"execution timeout"}`, `{"error":"execution timeout"}`},
		{`[1]`, `[1]`},
	} {
		if got := string(scriptLogsListed([]byte(tc.sidecar))); got != tc.want {
			t.Errorf("execute answer %s became %s, want %s", tc.sidecar, got, tc.want)
		}
	}
}
