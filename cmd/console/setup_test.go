package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// fakeServer speaks the owner-key half of rest-mail's API: it issues
// challenges and checks signatures against its owner keys the way the real
// server does (internal/bootstrap/ownerkeys.go, which has its own tests).
type fakeServer struct {
	owners   []ssh.PublicKey
	claimed  bool     // an admin exists already
	admins   []string // for reset-admin
	answered []map[string]any
}

func (f *fakeServer) handler() http.Handler {
	mux := http.NewServeMux()
	fail := func(w http.ResponseWriter, status int, code string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": code}})
	}
	data := func(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": v}) }
	mux.HandleFunc("POST /api/v1/auth/owner/challenge", func(w http.ResponseWriter, r *http.Request) {
		if len(f.owners) == 0 {
			fail(w, 409, "no_owner_keys")
			return
		}
		data(w, map[string]string{"challenge": "ch-" + rand.Text()})
	})
	mux.HandleFunc("POST /api/v1/auth/owner/token", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.answered = append(f.answered, req)
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(req["public_key"].(string)))
		listed := false
		for _, o := range f.owners {
			listed = listed || (err == nil && bytes.Equal(o.Marshal(), pub.Marshal()))
		}
		raw, _ := base64.StdEncoding.DecodeString(req["signature"].(string))
		var sig ssh.Signature
		if !listed || ssh.Unmarshal(raw, &sig) != nil || sig.Format == ssh.KeyAlgoRSA ||
			pub.Verify(ownerMessage(req["challenge"].(string), req["purpose"].(string), req["username"].(string)), &sig) != nil {
			fail(w, 403, "not_owner")
			return
		}
		switch {
		case req["purpose"] == "setup" && f.claimed:
			fail(w, 409, "already_claimed")
		case req["purpose"] == "reset-admin" && !contains(f.admins, req["username"].(string)):
			fail(w, 404, "no_such_admin")
		default:
			data(w, map[string]string{"purpose": req["purpose"].(string), "token": "TOKEN-123", "expires_at": "2026-10-10T21:15:00Z"})
		}
	})
	return mux
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func edSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// keysOf stands in for a key file or ssh-agent holding signers.
func keysOf(signers ...ssh.Signer) keySource {
	return func(string) ([]signer, error) {
		out := []signer{}
		for _, s := range signers {
			out = append(out, fromSSHSigner(s))
		}
		return out, nil
	}
}

func run(t *testing.T, f *fakeServer, keys keySource, command string, args ...string) (int, string, string) {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	var out, errOut bytes.Buffer
	args = append([]string{"--server", srv.URL}, args...)
	status := runSetupCommand(context.Background(), command, args, srv.Client(), keys, &out, &errOut)
	return status, out.String(), errOut.String()
}

func TestSetup_OwnerKeyGetsALink(t *testing.T) {
	owner := edSigner(t)
	f := &fakeServer{owners: []ssh.PublicKey{owner.PublicKey()}}

	status, out, errOut := run(t, f, keysOf(owner), "setup", "--json", "--show-secret")
	if status != exitOK {
		t.Fatalf("status %d, stderr %s", status, errOut)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not one JSON object: %q", out)
	}
	// A web link by default: tappable where a custom scheme is not, and the
	// code rides after the `#`, which no web server is sent.
	link, _ := url.Parse(got["link"].(string))
	carried := carriedBy(t, got["link"].(string))
	if link.Scheme != "https" || link.Host != "rest-mail.com" || link.Path != "/setup" || link.RawQuery != "" {
		t.Errorf("link = %v", got["link"])
	}
	if carried.Get("token") != "TOKEN-123" || !strings.HasPrefix(carried.Get("server"), "http://127.0.0.1:") {
		t.Errorf("link carries %v", carried)
	}
	if got["expires_at"] != "2026-10-10T21:15:00Z" {
		t.Errorf("expires_at = %v", got["expires_at"])
	}
}

func TestSetup_LinkIsRedactedUnlessAsked(t *testing.T) {
	owner := edSigner(t)
	f := &fakeServer{owners: []ssh.PublicKey{owner.PublicKey()}}
	_, out, _ := run(t, f, keysOf(owner), "setup", "--json")
	if strings.Contains(out, "TOKEN-123") || !strings.Contains(out, `"link_redacted":true`) {
		t.Errorf("--json without --show-secret printed %s", out)
	}
	// People get the QR code and the link.
	_, out, _ = run(t, f, keysOf(owner), "setup")
	if !strings.Contains(out, "TOKEN-123") || !strings.Contains(out, "Scan with the rest-mail app") {
		t.Errorf("text output: %s", out)
	}
}

// ssh-agent may hold several keys; any one of them may be the owner's.
func TestSetup_TriesEachKey(t *testing.T) {
	other, owner := edSigner(t), edSigner(t)
	f := &fakeServer{owners: []ssh.PublicKey{owner.PublicKey()}}
	if status, _, errOut := run(t, f, keysOf(other, owner), "setup", "--json"); status != exitOK {
		t.Fatalf("status %d: %s", status, errOut)
	}
	if len(f.answered) != 2 {
		t.Errorf("answered %d challenges, want 2", len(f.answered))
	}
}

// RSA keys sign with SHA-2: the server refuses ssh-rsa (SHA-1) signatures.
func TestSetup_RSAKeySignsWithSHA2(t *testing.T) {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	owner, _ := ssh.NewSignerFromKey(k)
	f := &fakeServer{owners: []ssh.PublicKey{owner.PublicKey()}}
	if status, _, errOut := run(t, f, keysOf(owner), "setup", "--json"); status != exitOK {
		t.Fatalf("status %d: %s", status, errOut)
	}
}

func TestResetAdmin(t *testing.T) {
	owner := edSigner(t)
	f := &fakeServer{owners: []ssh.PublicKey{owner.PublicKey()}, claimed: true, admins: []string{"agent"}}
	status, out, errOut := run(t, f, keysOf(owner), "reset-admin", "--username", "agent", "--json", "--show-secret")
	if status != exitOK || !strings.Contains(out, "restmail://reset?") {
		t.Fatalf("status %d, out %s, err %s", status, out, errOut)
	}
	if f.answered[0]["username"] != "agent" || f.answered[0]["purpose"] != "reset-admin" {
		t.Errorf("asked for %v", f.answered[0])
	}
}

func TestSetup_ExitStatuses(t *testing.T) {
	owner, stranger := edSigner(t), edSigner(t)
	listed := []ssh.PublicKey{owner.PublicKey()}
	cases := []struct {
		name    string
		f       *fakeServer
		keys    keySource
		command string
		args    []string
		want    int
		code    string
	}{
		{"not an owner key", &fakeServer{owners: listed}, keysOf(stranger), "setup", nil, exitNotOwner, "not_owner"},
		{"no keys at all", &fakeServer{owners: listed}, keysOf(), "setup", nil, exitNotOwner, "no_key"},
		{"server lists no owners", &fakeServer{}, keysOf(owner), "setup", nil, exitNotOwner, "no_owner_keys"},
		{"already claimed", &fakeServer{owners: listed, claimed: true}, keysOf(owner), "setup", nil, exitAlreadyClaimed, "already_claimed"},
		{"no such admin", &fakeServer{owners: listed, claimed: true}, keysOf(owner), "reset-admin", []string{"--username", "nobody"}, exitNoSuchAdmin, "no_such_admin"},
		{"reset without a username", &fakeServer{owners: listed}, keysOf(owner), "reset-admin", nil, exitUsage, "bad_flag"},
		{"unknown flag", &fakeServer{owners: listed}, keysOf(owner), "setup", []string{"--kube-context", "prod"}, exitUsage, "bad_flag"},
		{"lifetime too long", &fakeServer{owners: listed}, keysOf(owner), "setup", []string{"--lifetime", "48h"}, exitUsage, "bad_flag"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, out, errOut := run(t, c.f, c.keys, c.command, append(c.args, "--json")...)
			if status != c.want {
				t.Errorf("status %d, want %d (stderr %s)", status, c.want, errOut)
			}
			if out != "" {
				t.Errorf("stdout on failure: %q", out)
			}
			var e struct{ Error struct{ Code string } }
			if json.Unmarshal([]byte(errOut), &e) != nil || e.Error.Code != c.code {
				t.Errorf("stderr = %q, want one JSON error with code %s", errOut, c.code)
			}
		})
	}
}

func TestSetup_NotARestMailServer(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	var out, errOut bytes.Buffer
	status := runSetupCommand(context.Background(), "setup", []string{"--server", srv.URL, "--json"}, srv.Client(), keysOf(edSigner(t)), &out, &errOut)
	if status != exitUnreachable {
		t.Errorf("status %d, want %d: %s", status, exitUnreachable, errOut.String())
	}
}

// A link that grants admin never crosses a network in the clear.
func TestServerURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://mail.example.com":  true,
		"https://mail.example.com/": true,
		"http://localhost:8080":     true,
		"http://127.0.0.1:8080":     true,
		"http://[::1]:8080":         true,
		"http://mail.example.com":   false,
		"http://10.0.0.5":           false,
		"mail.example.com":          false,
		"":                          false,
	} {
		if _, err := serverURL(raw); (err == nil) != ok {
			t.Errorf("serverURL(%q) err = %v, want ok=%v", raw, err, ok)
		}
	}
}

func TestLocalKeys(t *testing.T) {
	dir := t.TempDir()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKey(priv, "")
	plain := filepath.Join(dir, "id_ed25519")
	_ = os.WriteFile(plain, pemEncode(block.Type, block.Bytes), 0o600)
	got, err := localKeys(plain)
	if err != nil || len(got) != 1 {
		t.Fatalf("plain key: %d keys, err %v", len(got), err)
	}

	block, _ = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("secret"))
	locked := filepath.Join(dir, "id_locked")
	_ = os.WriteFile(locked, pemEncode(block.Type, block.Bytes), 0o600)
	if _, err := localKeys(locked); err == nil || !strings.Contains(err.Error(), "ssh-agent") {
		t.Errorf("passphrase-protected key: err = %v, want a pointer to ssh-agent", err)
	}

	t.Setenv("SSH_AUTH_SOCK", "")
	if _, err := localKeys(""); err == nil {
		t.Error("no key and no agent: want an error")
	}
}

func pemEncode(typ string, b []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: b})
}

// The code a server printed becomes the app's link, without asking the server
// anything (and so without a key).
func TestSetup_CodeFromTheServersOutput(t *testing.T) {
	f := &fakeServer{}
	status, out, errOut := run(t, f, keysOf(), "setup", "--code", "ABCD-EFGH", "--json", "--show-secret")
	if status != exitOK {
		t.Fatalf("status %d: %s", status, errOut)
	}
	var got map[string]any
	_ = json.Unmarshal([]byte(out), &got)
	if got := carriedBy(t, got["link"].(string)).Get("token"); got != "ABCD-EFGH" {
		t.Errorf("link carries token %q", got)
	}
	if len(f.answered) != 0 {
		t.Errorf("asked the server %d times, want none", len(f.answered))
	}
	if status, _, _ := run(t, f, keysOf(), "reset-admin", "--username", "a", "--code", "X", "--json"); status != exitUsage {
		t.Errorf("reset-admin --code: status %d, want %d", status, exitUsage)
	}
}

// A server that should involve no site at all, and one pointed at another.
func TestSetup_LinkBaseChoosesWhereLinksPoint(t *testing.T) {
	f := &fakeServer{}
	_, out, _ := run(t, f, keysOf(), "setup", "--code", "ABCD-EFGH", "--link-base", "", "--json", "--show-secret")
	var got map[string]any
	_ = json.Unmarshal([]byte(out), &got)
	if link := got["link"].(string); !strings.HasPrefix(link, "restmail://setup?") {
		t.Errorf("--link-base='': link = %q", link)
	}

	_, out, _ = run(t, f, keysOf(), "setup", "--code", "ABCD-EFGH", "--link-base", "https://mail.example.com/claim", "--json", "--show-secret")
	_ = json.Unmarshal([]byte(out), &got)
	if link := got["link"].(string); !strings.HasPrefix(link, "https://mail.example.com/claim#") {
		t.Errorf("another site: link = %q", link)
	}

	// A base that is not an address would print a link nobody can open.
	if status, _, _ := run(t, f, keysOf(), "setup", "--code", "X", "--link-base", "mail.example.com", "--json"); status != exitUsage {
		t.Errorf("a base that is not an address: status %d, want %d", status, exitUsage)
	}
}

// carriedBy reads a link's parameters, wherever that form of link keeps them.
func carriedBy(t *testing.T, link string) url.Values {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse %q: %v", link, err)
	}
	raw := u.RawQuery
	if u.Fragment != "" {
		raw = u.Fragment
	}
	parameters, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("parse %q of %q: %v", raw, link, err)
	}
	return parameters
}
