package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// queueRequest is one request the stand-in API saw.
type queueRequest struct {
	Method, Path, Query, Auth, Body string
}

// queueAPI stands in for the public API's queue endpoints. answers maps
// "METHOD /path" to a status and body; anything else is 404.
type queueAPI struct {
	mu   sync.Mutex
	seen []queueRequest
	srv  *httptest.Server
}

type queueAnswer struct {
	status int
	body   string
}

func newQueueAPI(t *testing.T, answers map[string]queueAnswer) *queueAPI {
	t.Helper()
	api := &queueAPI{}
	api.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		api.mu.Lock()
		api.seen = append(api.seen, queueRequest{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), string(body)})
		api.mu.Unlock()
		key := r.Method + " " + r.URL.Path
		if r.URL.Path == "/api/v1/admin/queue" && r.URL.Query().Get("status") != "" && r.URL.Query().Get("limit") == "1" {
			key += "?status=" + r.URL.Query().Get("status")
		}
		a, ok := answers[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(a.status)
		_, _ = io.WriteString(w, a.body)
	}))
	t.Cleanup(api.srv.Close)
	return api
}

func (a *queueAPI) requests() []queueRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]queueRequest(nil), a.seen...)
}

func mailQueue(t *testing.T, api *queueAPI, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	full := append([]string{args[0], "--server", api.srv.URL}, args[1:]...)
	code := runMailQueue(context.Background(), full, "test-token", strings.NewReader(stdin), api.srv.Client(), &out, &errOut)
	return code, out.String(), errOut.String()
}

const queueEntryJSON = `{"id":42,"message_id":7,"sender":"a@example.com","recipient":"b@example.org","domain":"example.org",
"status":"deferred","attempts":3,"last_attempt":"2026-11-03T10:00:00Z","next_attempt":"2026-11-03T11:00:00Z",
"last_error":"451 4.7.1 try\nlater","last_error_code":451,"failure_category":"greylisted","max_retries":30,
"created_at":"2026-11-03T09:00:00Z","expires_at":"2026-11-08T09:00:00Z"}`

func TestMailQueueStats(t *testing.T) {
	api := newQueueAPI(t, map[string]queueAnswer{
		"GET /api/v1/admin/queue/stats": {200, `{"data":{"pending":4,"deferred":9,"delivered":120,"bounced":2,"total":136,"delivering":1}}`},
	})
	code, out, errOut := mailQueue(t, api, "", "stats")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"pending", "4", "deferred", "9", "delivered", "120", "delivering", "total", "136"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	got := api.requests()
	if len(got) != 1 || got[0].Method != http.MethodGet || got[0].Path != "/api/v1/admin/queue/stats" || got[0].Auth != "Bearer test-token" {
		t.Errorf("requests %+v", got)
	}
}

func TestMailQueueListSendsTheFilterAndShowsWhy(t *testing.T) {
	api := newQueueAPI(t, map[string]queueAnswer{
		"GET /api/v1/admin/queue": {200, `{"data":{"items":[` + queueEntryJSON + `],"total":1,"limit":20,"offset":0}}`},
	})
	code, out, errOut := mailQueue(t, api, "", "list", "--status", "deferred", "--domain", "example.org",
		"--sender", "a@", "--recipient", "b@", "--limit", "20")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	got := api.requests()
	if len(got) != 1 || got[0].Method != http.MethodGet || got[0].Path != "/api/v1/admin/queue" {
		t.Fatalf("requests %+v", got)
	}
	if want := "domain=example.org&limit=20&offset=0&recipient=b%40&sender=a%40&status=deferred"; got[0].Query != want {
		t.Errorf("query %q, want %q", got[0].Query, want)
	}
	for _, want := range []string{"42", "deferred", "3", "2026-11-03T11:00:00Z", "greylisted", "451 4.7.1 try later", "1–1 of 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestMailQueueListJSON(t *testing.T) {
	api := newQueueAPI(t, map[string]queueAnswer{
		"GET /api/v1/admin/queue": {200, `{"data":{"items":[` + queueEntryJSON + `],"total":1,"limit":50,"offset":0}}`},
	})
	code, out, _ := mailQueue(t, api, "", "list", "--json")
	var page queuePage
	if code != exitOK || json.Unmarshal([]byte(out), &page) != nil || len(page.Items) != 1 || page.Items[0].FailureCategory != "greylisted" {
		t.Fatalf("exit %d, %q", code, out)
	}
}

func TestMailQueueShow(t *testing.T) {
	api := newQueueAPI(t, map[string]queueAnswer{
		"GET /api/v1/admin/queue/42": {200, `{"data":` + queueEntryJSON + `}`},
	})
	code, out, errOut := mailQueue(t, api, "", "show", "42")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"b@example.org", "3 of 30", "greylisted", "451", "2026-11-03T11:00:00Z", "try later"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if got := api.requests(); len(got) != 1 || got[0].Method != http.MethodGet || got[0].Path != "/api/v1/admin/queue/42" {
		t.Errorf("requests %+v", got)
	}
}

func TestMailQueueShowNotFound(t *testing.T) {
	api := newQueueAPI(t, map[string]queueAnswer{
		"GET /api/v1/admin/queue/9": {404, `{"error":{"code":"not_found","message":"Queue entry not found"}}`},
	})
	code, _, errOut := mailQueue(t, api, "", "show", "9")
	if code != exitFailed || !strings.Contains(errOut, "Queue entry not found") {
		t.Errorf("exit %d: %s", code, errOut)
	}
}

func TestMailQueueActOnOneEntry(t *testing.T) {
	for _, tc := range []struct {
		sub, method, path, answer, says string
		status                          int
	}{
		{"retry", http.MethodPost, "/api/v1/admin/queue/42/retry", `{"data":{"status":"retrying"}}`, "retried entry 42", 200},
		{"bounce", http.MethodPost, "/api/v1/admin/queue/42/bounce", `{"data":{"status":"bounced"}}`, "bounced entry 42", 200},
		{"purge", http.MethodDelete, "/api/v1/admin/queue/42", ``, "purged entry 42", 204},
	} {
		t.Run(tc.sub, func(t *testing.T) {
			api := newQueueAPI(t, map[string]queueAnswer{tc.method + " " + tc.path: {tc.status, tc.answer}})
			// One entry: no question asked, so nothing on stdin.
			code, out, errOut := mailQueue(t, api, "", tc.sub, "42")
			if code != exitOK || !strings.Contains(out, tc.says) {
				t.Fatalf("exit %d, %q: %s", code, out, errOut)
			}
			got := api.requests()
			if len(got) != 1 || got[0].Method != tc.method || got[0].Path != tc.path || got[0].Body != "" || got[0].Auth != "Bearer test-token" {
				t.Errorf("requests %+v", got)
			}
		})
	}
}

// Each bulk action counts what the filter selects in the statuses it applies
// to, then sends the same filter.
func TestMailQueueBulkWithYes(t *testing.T) {
	counts := map[string]queueAnswer{}
	for i, s := range queueStatuses {
		counts["GET /api/v1/admin/queue?status="+s] = queueAnswer{200, `{"data":{"items":[],"total":` + string(rune('1'+i)) + `,"limit":1,"offset":0}}`}
	}
	for _, tc := range []struct {
		sub, method, path string
		counted           []string
		matched           int
	}{
		{"retry", http.MethodPost, "/api/v1/admin/queue/bulk-retry", []string{"pending", "deferred"}, 1 + 3},
		{"bounce", http.MethodPost, "/api/v1/admin/queue/bulk-bounce", []string{"pending", "deferred", "delivering"}, 1 + 3 + 2},
		{"purge", http.MethodDelete, "/api/v1/admin/queue/bulk-delete", queueStatuses, 1 + 2 + 3 + 4 + 5 + 6},
	} {
		t.Run(tc.sub, func(t *testing.T) {
			answers := map[string]queueAnswer{tc.method + " " + tc.path: {200, `{"data":{"affected":5}}`}}
			for k, v := range counts {
				answers[k] = v
			}
			api := newQueueAPI(t, answers)
			code, out, errOut := mailQueue(t, api, "", tc.sub, "--domain", "example.org", "--yes", "--json")
			if code != exitOK {
				t.Fatalf("exit %d: %s", code, errOut)
			}
			var result map[string]any
			if json.Unmarshal([]byte(out), &result) != nil || result["matched"] != float64(tc.matched) || result["affected"] != float64(5) {
				t.Errorf("output %q, want %d matched and 5 affected", out, tc.matched)
			}
			got := api.requests()
			if len(got) != len(tc.counted)+1 {
				t.Fatalf("requests %+v", got)
			}
			for i, s := range tc.counted {
				if want := "domain=example.org&limit=1&status=" + s; got[i].Method != http.MethodGet || got[i].Query != want {
					t.Errorf("count request %d: %+v, want query %q", i, got[i], want)
				}
			}
			last := got[len(got)-1]
			if last.Method != tc.method || last.Path != tc.path || last.Body != `{"filter":{"domain":"example.org"}}` {
				t.Errorf("bulk request %+v", last)
			}
		})
	}
}

func TestMailQueueBulkAsksAndNoSendsNothing(t *testing.T) {
	api := newQueueAPI(t, map[string]queueAnswer{
		"GET /api/v1/admin/queue?status=bounced": {200, `{"data":{"items":[],"total":12,"limit":1,"offset":0}}`},
		"DELETE /api/v1/admin/queue/bulk-delete": {200, `{"data":{"affected":12}}`},
	})
	code, out, errOut := mailQueue(t, api, "n\n", "purge", "--status", "bounced")
	if code != exitFailed || !strings.Contains(errOut, "Purge 12 bounced entries? [y/N]") || !strings.Contains(errOut, "nothing changed") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	for _, r := range api.requests() {
		if r.Method != http.MethodGet {
			t.Errorf("sent %+v after a no", r)
		}
	}
}

func TestMailQueueBulkAsksAndYesActs(t *testing.T) {
	api := newQueueAPI(t, map[string]queueAnswer{
		"GET /api/v1/admin/queue?status=deferred": {200, `{"data":{"items":[],"total":3,"limit":1,"offset":0}}`},
		"POST /api/v1/admin/queue/bulk-retry":     {200, `{"data":{"affected":3}}`},
	})
	code, out, errOut := mailQueue(t, api, "y\n", "retry", "--status", "deferred", "--recipient", "b@")
	if code != exitOK || !strings.Contains(errOut, "Retry 3 deferred entries?") || !strings.Contains(out, "retried 3 entries") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	got := api.requests()
	if last := got[len(got)-1]; last.Body != `{"filter":{"status":"deferred","recipient":"b@"}}` {
		t.Errorf("bulk request %+v", last)
	}
}

// No answer at all (stdin closed) is a no.
func TestMailQueueBulkNoAnswerIsNo(t *testing.T) {
	api := newQueueAPI(t, map[string]queueAnswer{
		"GET /api/v1/admin/queue?status=pending":  {200, `{"data":{"items":[],"total":1,"limit":1,"offset":0}}`},
		"GET /api/v1/admin/queue?status=deferred": {200, `{"data":{"items":[],"total":1,"limit":1,"offset":0}}`},
	})
	if code, _, _ := mailQueue(t, api, "", "retry", "--all"); code != exitFailed {
		t.Errorf("exit %d", code)
	}
	for _, r := range api.requests() {
		if r.Method != http.MethodGet {
			t.Errorf("sent %+v without an answer", r)
		}
	}
}

// A status the action does not apply to matches nothing: no question, no
// request beyond none at all.
func TestMailQueueBulkNothingMatches(t *testing.T) {
	api := newQueueAPI(t, nil)
	code, out, _ := mailQueue(t, api, "", "retry", "--status", "delivered")
	if code != exitOK || !strings.Contains(out, "nothing to retry") || len(api.requests()) != 0 {
		t.Errorf("exit %d, %q, requests %+v", code, out, api.requests())
	}
}

func TestMailQueueRefusesBadUse(t *testing.T) {
	api := newQueueAPI(t, nil)
	for name, args := range map[string][]string{
		"bulk with no filter":  {"purge"},
		"id and filter":        {"retry", "42", "--status", "deferred"},
		"all and filter":       {"bounce", "--all", "--domain", "example.org"},
		"unknown status":       {"list", "--status", "stuck"},
		"show without id":      {"show"},
		"id that is no number": {"show", "abc"},
		"limit too large":      {"list", "--limit", "500"},
		"unknown command":      {"flush"},
	} {
		t.Run(name, func(t *testing.T) {
			if code, _, _ := mailQueue(t, api, "", args...); code != exitUsage {
				t.Errorf("exit %d, want %d", code, exitUsage)
			}
		})
	}
	if got := api.requests(); len(got) != 0 {
		t.Errorf("requests %+v", got)
	}
}

func TestMailQueueNeedsAnAPIKey(t *testing.T) {
	api := newQueueAPI(t, nil)
	var out, errOut bytes.Buffer
	code := runMailQueue(context.Background(), []string{"stats", "--server", api.srv.URL}, "", strings.NewReader(""), api.srv.Client(), &out, &errOut)
	if code != exitUsage || !strings.Contains(errOut.String(), "RESTMAIL_API_KEY") || len(api.requests()) != 0 {
		t.Errorf("exit %d: %s", code, errOut.String())
	}
}
