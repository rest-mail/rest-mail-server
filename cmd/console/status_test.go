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

func TestStatus_SaysWhatTheServerRuns(t *testing.T) {
	srv := describing(t, `{"data":{"product":"rest-mail","version":"0.7.0"}}`)

	code, out, errOut := status(t, srv)

	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(out, "rest-mail 0.7.0") {
		t.Errorf("output lacks the version:\n%s", out)
	}
	if strings.Contains(out, "claim") || strings.Contains(out, "setup") {
		t.Errorf("output still talks of claiming:\n%s", out)
	}
}

// A server from before 0.7 still says whether it is claimed. That is no longer
// anything to act on: the CLI reads what it needs and ignores the rest.
func TestStatus_AnOlderServersClaimStateIsIgnored(t *testing.T) {
	srv := describing(t, `{"data":{"product":"rest-mail","version":"0.6.0","claimed":false,"claim_methods":["setup_code"]}}`)
	code, out, _ := status(t, srv)
	if code != exitOK || !strings.Contains(out, "rest-mail 0.6.0") || strings.Contains(out, "claim") {
		t.Errorf("exit %d:\n%s", code, out)
	}
}

// For scripts and agents: one object.
func TestStatus_JSON(t *testing.T) {
	srv := describing(t, `{"data":{"product":"rest-mail","version":"0.7.0"}}`)
	code, out, _ := status(t, srv, "--json")
	var got map[string]any
	if code != exitOK || json.Unmarshal([]byte(out), &got) != nil {
		t.Fatalf("exit %d, stdout not one JSON object: %q", code, out)
	}
	if got["version"] != "0.7.0" || got["product"] != "rest-mail" || got["server"] != srv.URL {
		t.Errorf("got %v", got)
	}
	if _, ok := got["claimed"]; ok {
		t.Errorf("claim state in %v", got)
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

// An argument meant as a command must be answered as one. Before this, a word
// the binary did not know fell through to the admin console: `restmail status`
// on a build from before that command existed opened a TUI.
func TestSubcommand_AnUnknownCommandIsNotTheConsole(t *testing.T) {
	for _, args := range [][]string{
		{"restmail", "stauts"},           // a typo
		{"restmail", "status-of-things"}, // a command from some other version
		{"restmail", "claim"},            // a plausible command that does not exist
		{"restmail", "setup"},            // a command that existed before 0.7
	} {
		var out, errOut bytes.Buffer
		code, handled := runSubcommand(args, &out, &errOut)
		if !handled || code != exitUsage {
			t.Errorf("%q: code %d handled %v, want %d and true", args[1], code, handled, exitUsage)
		}
		if !strings.Contains(errOut.String(), args[1]) || !strings.Contains(errOut.String(), "restmail reset-admin") {
			t.Errorf("%q: stderr should name it and show the commands:\n%s", args[1], errOut.String())
		}
	}
}

// The console's own flags, and bare `restmail`, still reach the console.
func TestSubcommand_TheConsoleIsStillReachable(t *testing.T) {
	for _, args := range [][]string{
		{"restmail"},
		{"restmail", "-api", "http://localhost:8080"},
		{"restmail", "--version"},
	} {
		var out, errOut bytes.Buffer
		if _, handled := runSubcommand(args, &out, &errOut); handled {
			t.Errorf("%v was handled as a command, so the console is unreachable", args[1:])
		}
	}
}

// The commands it does know still work.
func TestSubcommand_KnownCommands(t *testing.T) {
	var out, errOut bytes.Buffer
	if code, handled := runSubcommand([]string{"restmail", "version"}, &out, &errOut); !handled || code != exitOK {
		t.Errorf("version: code %d handled %v", code, handled)
	} else if !strings.HasPrefix(out.String(), "restmail ") {
		t.Errorf("version printed %q", out.String())
	}
	out.Reset()
	if code, handled := runSubcommand([]string{"restmail", "help"}, &out, &errOut); !handled || code != exitOK || !strings.Contains(out.String(), "restmail status") {
		t.Errorf("help: code %d handled %v, out %q", code, handled, out.String())
	}
}
