package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
)

// fakeCluster answers the kubectl and docker calls the setup commands make, and
// records them.
type fakeCluster struct {
	host     string // the ingress's host; empty for none
	mintOut  string // what restmail-api mint-token prints on stdout
	mintErr  string // ... on stderr
	mintCode int
	missing  bool // kubectl/docker not installed
	calls    [][]string
}

func (f *fakeCluster) run(_ context.Context, name string, args ...string) ([]byte, []byte, int, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if f.missing {
		return nil, nil, -1, errors.New("executable file not found in $PATH")
	}
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "get ingress"):
		if f.host == "" {
			return nil, []byte("error: no ingress"), 1, nil
		}
		return []byte(f.host), nil, 0, nil
	case strings.Contains(joined, "get deployment"):
		return []byte("deployment.apps/rest-mail-server-api\n"), nil, 0, nil
	case strings.Contains(joined, "mint-token"):
		return []byte(f.mintOut), []byte(f.mintErr), f.mintCode, nil
	}
	return nil, []byte("unexpected call: " + joined), 1, nil
}

const mintedSetup = `{"purpose":"setup","token":"tok-123","expires_at":"2026-10-10T18:30:00Z"}` + "\n"

func runCmd(f *fakeCluster, command string, args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	status := runSetupCommand(context.Background(), command, args, f.run, &out, &errOut)
	return status, out.String(), errOut.String()
}

func TestSetup_KubernetesMintsInTheAPIContainerAndPrintsALink(t *testing.T) {
	f := &fakeCluster{host: "mail.rest-mail.com", mintOut: mintedSetup}
	status, out, errOut := runCmd(f, "setup", "--namespace", "rest-mail", "--context", "prod")
	if status != exitOK {
		t.Fatalf("status %d, stderr %s", status, errOut)
	}

	// The token was minted inside the API's own container, found by its label.
	exec := f.calls[len(f.calls)-1]
	got := strings.Join(exec, " ")
	for _, want := range []string{"kubectl", "--context prod", "-n rest-mail",
		"exec deployment.apps/rest-mail-server-api -c api --", "restmail-api mint-token --purpose setup"} {
		if !strings.Contains(got, want) {
			t.Errorf("exec %q lacks %q", got, want)
		}
	}

	// A person gets the link to open and a QR code of it.
	link := "restmail://setup?server=https%3A%2F%2Fmail.rest-mail.com&token=tok-123"
	if !strings.Contains(out, link) {
		t.Errorf("output lacks the link %s:\n%s", link, out)
	}
	if !strings.ContainsAny(out, "█▀▄") {
		t.Error("output has no QR code")
	}
}

func TestSetup_JSONRedactsTheLinkUnlessAsked(t *testing.T) {
	f := &fakeCluster{host: "mail.rest-mail.com", mintOut: mintedSetup}

	status, out, _ := runCmd(f, "setup", "--json")
	if status != exitOK {
		t.Fatalf("status %d", status)
	}
	var redacted map[string]any
	if err := json.Unmarshal([]byte(out), &redacted); err != nil {
		t.Fatalf("stdout is not one JSON object: %q", out)
	}
	if _, ok := redacted["link"]; ok || redacted["link_redacted"] != true {
		t.Errorf("--json without --show-secret = %v; the link grants admin and must not reach logs by default", redacted)
	}
	if redacted["server"] != "https://mail.rest-mail.com" || redacted["expires_at"] != "2026-10-10T18:30:00Z" {
		t.Errorf("--json = %v", redacted)
	}

	status, out, _ = runCmd(f, "setup", "--json", "--show-secret")
	var shown map[string]string
	if status != exitOK || json.Unmarshal([]byte(out), &shown) != nil {
		t.Fatalf("status %d, out %q", status, out)
	}
	u, err := url.Parse(shown["link"])
	if err != nil || u.Scheme != "restmail" || u.Host != "setup" {
		t.Fatalf("link %q", shown["link"])
	}
	if u.Query().Get("server") != "https://mail.rest-mail.com" || u.Query().Get("token") != "tok-123" {
		t.Errorf("link carries %v", u.Query())
	}
}

func TestResetAdmin_LinksToResetForTheNamedAdmin(t *testing.T) {
	f := &fakeCluster{host: "mail.rest-mail.com",
		mintOut: `{"purpose":"reset-admin","token":"tok-9","expires_at":"2026-10-10T18:30:00Z"}`}
	status, out, _ := runCmd(f, "reset-admin", "--username", "agent", "--json", "--show-secret")
	if status != exitOK {
		t.Fatalf("status %d", status)
	}
	if !strings.Contains(strings.Join(f.calls[len(f.calls)-1], " "), "--purpose reset-admin --username agent") {
		t.Errorf("minted with %v", f.calls[len(f.calls)-1])
	}
	var res map[string]string
	_ = json.Unmarshal([]byte(out), &res)
	if !strings.HasPrefix(res["link"], "restmail://reset?") {
		t.Errorf("link %q, want a reset link", res["link"])
	}
}

// The operator command's own failures come through with its exit status and
// code, so an agent can tell "already set up" from "could not reach it".
func TestSetup_PassesThroughTheServersRefusal(t *testing.T) {
	f := &fakeCluster{host: "mail.rest-mail.com", mintCode: exitAlreadyClaimed,
		mintErr: `{"error":{"code":"already_claimed","message":"the server already has an admin"}}`}
	status, _, errOut := runCmd(f, "setup", "--json")
	if status != exitAlreadyClaimed {
		t.Errorf("status %d, want %d", status, exitAlreadyClaimed)
	}
	var e struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal([]byte(errOut), &e); err != nil || e.Error.Code != "already_claimed" {
		t.Fatalf("stderr %q", errOut)
	}
	if !strings.Contains(e.Error.Message, "reset-admin") {
		t.Errorf("message %q does not say what to do instead", e.Error.Message)
	}

	f = &fakeCluster{host: "mail.rest-mail.com", mintCode: exitNoSuchAdmin,
		mintErr: `{"error":{"code":"no_such_admin","message":"no admin named \"x\""}}`}
	if status, _, _ := runCmd(f, "reset-admin", "--username", "x", "--json"); status != exitNoSuchAdmin {
		t.Errorf("no such admin: status %d, want %d", status, exitNoSuchAdmin)
	}
}

func TestSetup_UnreachableAndUsageErrors(t *testing.T) {
	cases := []struct {
		name   string
		f      *fakeCluster
		args   []string
		status int
	}{
		{"kubectl missing", &fakeCluster{missing: true}, []string{"--json"}, exitUnreachable},
		{"no ingress and no --server", &fakeCluster{}, []string{"--json"}, exitUnreachable},
		{"docker without --server", &fakeCluster{}, []string{"--docker", "api", "--json"}, exitUsage},
		{"plain-http server", &fakeCluster{}, []string{"--server", "http://mail.example.com", "--json"}, exitUsage},
		{"unknown flag", &fakeCluster{}, []string{"--nope", "--json"}, exitUsage},
	}
	for _, c := range cases {
		status, out, errOut := runCmd(c.f, "setup", c.args...)
		if status != c.status {
			t.Errorf("%s: status %d, want %d (stderr %s)", c.name, status, c.status, errOut)
		}
		if out != "" {
			t.Errorf("%s: printed a result: %q", c.name, out)
		}
		var e map[string]any
		if err := json.Unmarshal([]byte(errOut), &e); err != nil {
			t.Errorf("%s: stderr is not one JSON object: %q", c.name, errOut)
		}
	}
	if status, _, _ := runCmd(&fakeCluster{}, "reset-admin", "--json"); status != exitUsage {
		t.Errorf("reset-admin without --username: status %d", status)
	}
}

func TestSetup_DockerExecsIntoTheNamedContainer(t *testing.T) {
	f := &fakeCluster{mintOut: mintedSetup}
	status, _, errOut := runCmd(f, "setup", "--docker", "restmail-api", "--server", "https://mail.example.com", "--json")
	if status != exitOK {
		t.Fatalf("status %d, %s", status, errOut)
	}
	if got := strings.Join(f.calls[0], " "); !strings.HasPrefix(got, "docker exec restmail-api restmail-api mint-token") {
		t.Errorf("ran %q", got)
	}
}
