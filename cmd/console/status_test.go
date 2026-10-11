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

// describing stands in for a server's GET /api/v1/server.
func describing(t *testing.T, body string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/server", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func status(t *testing.T, srv *httptest.Server, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := runStatus(context.Background(), append([]string{"--server", srv.URL}, args...), srv.Client(), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestStatus_AnUnclaimedServerSaysHowToClaimIt(t *testing.T) {
	srv := describing(t, `{"data":{"product":"rest-mail","version":"0.4.0","claimed":false,"claim_methods":["setup_code","owner_key"]}}`)

	code, out, errOut := status(t, srv)

	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"rest-mail 0.4.0", "not claimed yet", "--code CODE", "--key ~/.ssh/id_ed25519"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestStatus_OnlyTheWaysTheServerOffers(t *testing.T) {
	srv := describing(t, `{"data":{"product":"rest-mail","version":"0.4.0","claimed":false,"claim_methods":["setup_code"]}}`)
	_, out, _ := status(t, srv)
	if strings.Contains(out, "--key") {
		t.Errorf("offered an owner key the server does not list:\n%s", out)
	}
}

func TestStatus_AClaimedServerSendsYouToSignIn(t *testing.T) {
	srv := describing(t, `{"data":{"product":"rest-mail","version":"0.4.0","claimed":true,"claim_methods":[]}}`)
	code, out, _ := status(t, srv)
	if code != exitOK || !strings.Contains(out, "claimed: it has an admin") || strings.Contains(out, "restmail setup") {
		t.Errorf("exit %d:\n%s", code, out)
	}
}

// For scripts and agents: one object, and the claim state in it.
func TestStatus_JSON(t *testing.T) {
	srv := describing(t, `{"data":{"product":"rest-mail","version":"0.4.0","claimed":false,"claim_methods":["setup_code"]}}`)
	code, out, _ := status(t, srv, "--json")
	var got map[string]any
	if code != exitOK || json.Unmarshal([]byte(out), &got) != nil {
		t.Fatalf("exit %d, stdout not one JSON object: %q", code, out)
	}
	if got["claimed"] != false || got["version"] != "0.4.0" || got["server"] != srv.URL {
		t.Errorf("got %v", got)
	}
}

func TestStatus_NotARestMailServer(t *testing.T) {
	for name, srv := range map[string]*httptest.Server{
		"nothing at that path":   httptest.NewServer(http.NotFoundHandler()),
		"some other JSON server": describing(t, `{"data":{"product":"something-else"}}`),
	} {
		t.Run(name, func(t *testing.T) {
			defer srv.Close()
			code, out, errOut := status(t, srv, "--json")
			if code != exitUnreachable || out != "" {
				t.Errorf("exit %d, stdout %q, want %d and nothing", code, out, exitUnreachable)
			}
			var e struct{ Error struct{ Code string } }
			if json.Unmarshal([]byte(errOut), &e) != nil || e.Error.Code != "unreachable" {
				t.Errorf("stderr = %q", errOut)
			}
		})
	}
}

func TestStatus_Usage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runStatus(context.Background(), []string{"--json"}, http.DefaultClient, &out, &errOut); code != exitUsage {
		t.Errorf("no --server: exit %d, want %d", code, exitUsage)
	}
	if code := runStatus(context.Background(), []string{"--server", "http://mail.example.com"}, http.DefaultClient, &out, &errOut); code != exitUsage {
		t.Errorf("plain http to another host: exit %d, want %d", code, exitUsage)
	}
}
