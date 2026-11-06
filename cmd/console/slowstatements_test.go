package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The slow-statements command speaks only to the public API: it sends the
// token, the order and the limit, prints the answer as a table or JSON, and
// turns the server's refusals into its own exit statuses (#1629).
func TestSlowStatementsCommand(t *testing.T) {
	var got []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/admin/database/statements", func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization")+" "+r.URL.RawQuery)
		switch r.Header.Get("Authorization") {
		case "Bearer reader":
			_, _ = w.Write([]byte(`{"data":{"sort":"mean","statements":[{"query":"SELECT *\n  FROM messages WHERE mailbox_id = $1","calls":4,"total_ms":120.5,"mean_ms":30.125,"rows":8}]}}`))
		case "Bearer unloaded":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"statistics_unavailable","message":"The database does not have pg_stat_statements loaded"}}`))
		default:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"forbidden","message":"Insufficient permissions"}}`))
		}
	})
	mux.HandleFunc("POST /api/v1/admin/database/statements/reset", func(w http.ResponseWriter, r *http.Request) {
		got = append(got, "reset "+r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	run := func(token string, args ...string) (int, string, string) {
		var out, errs bytes.Buffer
		code := runSlowStatements(context.Background(), args, token, srv.Client(), &out, &errs)
		return code, out.String(), errs.String()
	}

	code, out, errs := run("reader", "list", "--server", srv.URL, "--sort", "mean", "--limit", "5")
	if code != exitOK || errs != "" || !strings.Contains(out, "120.5") || !strings.Contains(out, "SELECT * FROM messages WHERE mailbox_id = $1") {
		t.Fatalf("list: %d %q %q", code, out, errs)
	}
	if got[len(got)-1] != "Bearer reader limit=5&sort=mean" {
		t.Fatalf("sent %q", got[len(got)-1])
	}

	code, out, _ = run("reader", "list", "--server", srv.URL, "--json")
	var view slowStatementsView
	if code != exitOK || json.Unmarshal([]byte(out), &view) != nil || len(view.Statements) != 1 || view.Statements[0].Calls != 4 {
		t.Fatalf("list --json: %d %q", code, out)
	}
	if got[len(got)-1] != "Bearer reader limit=20&sort=total" {
		t.Fatalf("defaults sent %q", got[len(got)-1])
	}

	code, out, _ = run("manager", "reset", "--server", srv.URL, "--json")
	if code != exitOK || strings.TrimSpace(out) != `{"reset":true}` || got[len(got)-1] != "reset Bearer manager" {
		t.Fatalf("reset: %d %q %v", code, out, got)
	}

	if code, _, errs = run("unloaded", "list", "--server", srv.URL, "--json"); code != exitFailed || !strings.Contains(errs, `"statistics_unavailable"`) {
		t.Fatalf("unavailable: %d %q", code, errs)
	}
	if code, _, errs = run("nobody", "list", "--server", srv.URL); code != exitFailed || !strings.Contains(errs, "Insufficient permissions") {
		t.Fatalf("forbidden: %d %q", code, errs)
	}

	before := len(got)
	for _, args := range [][]string{
		{"list", "--server", srv.URL, "--sort", "slowest"},
		{"list", "--server", srv.URL, "--limit", "0"},
		{"drop", "--server", srv.URL},
		{"list"},
	} {
		if code, _, _ := run("reader", args...); code != exitUsage {
			t.Fatalf("%v: exit %d, want usage", args, code)
		}
	}
	if code, _, _ := run("", "list", "--server", srv.URL); code != exitUsage {
		t.Fatalf("no token: exit %d", code)
	}
	if len(got) != before {
		t.Fatalf("a usage error reached the server: %v", got[before:])
	}

	nothing := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(nothing.Close)
	var o, e bytes.Buffer
	if code := runSlowStatements(context.Background(), []string{"list", "--server", nothing.URL}, "reader", nothing.Client(), &o, &e); code != exitUnreachable {
		t.Fatalf("not rest-mail: exit %d %q", code, e.String())
	}
}
