package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
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
	code := runStatus(context.Background(), append([]string{"--server", srv.URL}, args...), srv.Client(), keysOf(), &out, &errOut)
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
	if code := runStatus(context.Background(), []string{"--json"}, http.DefaultClient, keysOf(), &out, &errOut); code != exitUsage {
		t.Errorf("no --server: exit %d, want %d", code, exitUsage)
	}
	if code := runStatus(context.Background(), []string{"--server", "http://mail.example.com"}, http.DefaultClient, keysOf(), &out, &errOut); code != exitUsage {
		t.Errorf("plain http to another host: exit %d, want %d", code, exitUsage)
	}
}

// askSigned runs `restmail status --key` against f, signing with keys.
func askSigned(t *testing.T, f *fakeServer, keys keySource, args ...string) (int, string, string) {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	var out, errOut bytes.Buffer
	args = append([]string{"--server", srv.URL, "--key", "/a/key"}, args...)
	code := runStatus(context.Background(), args, srv.Client(), keys, &out, &errOut)
	return code, out.String(), errOut.String()
}

// With a status key, the version, commit, build and start time: what a deploy
// is confirmed with.
func TestStatus_AStatusKeyReadsWhatRuns(t *testing.T) {
	deployer := edSigner(t)
	f := &fakeServer{status: []ssh.PublicKey{deployer.PublicKey()}}

	code, out, errOut := askSigned(t, f, keysOf(deployer))
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"rest-mail 0.8.0", "abc1234", "2026-10-17T09:00:00Z", "2026-10-17T09:05:00Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	req := f.answered[0]
	if _, ok := req["purpose"]; ok || req["challenge"] == "" {
		t.Errorf("asked with %v: the purpose is the endpoint's, and signed", req)
	}

	code, out, _ = askSigned(t, f, keysOf(deployer), "--json")
	var got map[string]any
	if code != exitOK || json.Unmarshal([]byte(out), &got) != nil {
		t.Fatalf("exit %d, stdout not one JSON object: %q", code, out)
	}
	if got["version"] != "0.8.0" || got["commit"] != "abc1234" || got["built_at"] != "2026-10-17T09:00:00Z" ||
		got["started_at"] != "2026-10-17T09:05:00Z" || !strings.HasPrefix(got["server"].(string), "http://127.0.0.1:") {
		t.Errorf("got %v", got)
	}
}

// Whoever may reset the admins may know what runs.
func TestStatus_AnOwnerKeyReadsItToo(t *testing.T) {
	owner := edSigner(t)
	f := &fakeServer{owners: []ssh.PublicKey{owner.PublicKey()}}
	if code, _, errOut := askSigned(t, f, keysOf(owner), "--json"); code != exitOK {
		t.Errorf("exit %d: %s", code, errOut)
	}
}

func TestStatus_SignedExitStatuses(t *testing.T) {
	deployer, stranger := edSigner(t), edSigner(t)
	listed := []ssh.PublicKey{deployer.PublicKey()}
	unreadable := func(string) ([]signer, error) { return nil, fmt.Errorf("/a/key: %w", errPassphrase) }
	cases := []struct {
		name string
		f    *fakeServer
		keys keySource
		want int
		code string
	}{
		{"a key on neither list", &fakeServer{status: listed}, keysOf(stranger), exitNotOwner, "not_status_key"},
		{"the server lists no keys", &fakeServer{}, keysOf(deployer), exitNotOwner, "no_owner_keys"},
		{"a passphrase-protected key", &fakeServer{status: listed}, unreadable, exitNotOwner, "no_key"},
		{"no key in the file", &fakeServer{status: listed}, keysOf(), exitNotOwner, "no_key"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out, errOut := askSigned(t, c.f, c.keys, "--json")
			if code != c.want || out != "" {
				t.Errorf("exit %d, stdout %q, want %d and nothing (stderr %s)", code, out, c.want, errOut)
			}
			var e struct{ Error struct{ Code string } }
			if json.Unmarshal([]byte(errOut), &e) != nil || e.Error.Code != c.code {
				t.Errorf("stderr = %q, want one JSON error with code %s", errOut, c.code)
			}
		})
	}
}

// A server from before status keys has no status endpoint: it says so, as for
// any server that does not answer like rest-mail.
func TestStatus_AnOlderServerHasNoSignedStatus(t *testing.T) {
	deployer := edSigner(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/owner/challenge", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"challenge":"ch"}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	var out, errOut bytes.Buffer
	code := runStatus(context.Background(), []string{"--server", srv.URL, "--key", "/a/key"}, srv.Client(), keysOf(deployer), &out, &errOut)
	if code != exitUnreachable || !strings.Contains(errOut.String(), "older than this CLI") {
		t.Errorf("exit %d, stderr %q, want %d and a word about the server's age", code, errOut.String(), exitUnreachable)
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
