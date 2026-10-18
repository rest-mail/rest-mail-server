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
	status   []ssh.PublicKey // RESTMAIL_STATUS_KEYS
	admins   []string
	answered []map[string]any
}

// verifies reports whether req carries a signature over message by one of keys.
func verifies(req map[string]any, keys []ssh.PublicKey, message []byte) bool {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(req["public_key"].(string)))
	listed := false
	for _, k := range keys {
		listed = listed || (err == nil && bytes.Equal(k.Marshal(), pub.Marshal()))
	}
	raw, _ := base64.StdEncoding.DecodeString(req["signature"].(string))
	var sig ssh.Signature
	return listed && ssh.Unmarshal(raw, &sig) == nil && sig.Format != ssh.KeyAlgoRSA && pub.Verify(message, &sig) == nil
}

func (f *fakeServer) handler() http.Handler {
	mux := http.NewServeMux()
	fail := func(w http.ResponseWriter, status int, code string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": code}})
	}
	data := func(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": v}) }
	mux.HandleFunc("POST /api/v1/auth/owner/challenge", func(w http.ResponseWriter, r *http.Request) {
		if len(f.owners) == 0 && len(f.status) == 0 {
			fail(w, 409, "no_owner_keys")
			return
		}
		data(w, map[string]string{"challenge": "ch-" + rand.Text()})
	})
	mux.HandleFunc("POST /api/v1/auth/owner/token", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.answered = append(f.answered, req)
		if len(f.owners) == 0 {
			fail(w, 409, "no_owner_keys")
			return
		}
		if !verifies(req, f.owners, ownerMessage(req["challenge"].(string), req["purpose"].(string), req["address"].(string))) {
			fail(w, 403, "not_owner")
			return
		}
		switch {
		case req["purpose"] != "reset-admin":
			fail(w, 422, "validation_error")
		case !contains(f.admins, req["address"].(string)):
			fail(w, 404, "no_such_account")
		default:
			data(w, map[string]string{"purpose": req["purpose"].(string), "token": "TOKEN-123", "expires_at": "2026-10-10T21:15:00Z"})
		}
	})
	mux.HandleFunc("POST /api/v1/auth/owner/status", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.answered = append(f.answered, req)
		if !verifies(req, append(f.status, f.owners...), ownerMessage(req["challenge"].(string), "status", "")) {
			fail(w, 403, "not_status_key")
			return
		}
		data(w, map[string]string{"version": "0.8.0", "commit": "abc1234",
			"built_at": "2026-10-17T09:00:00Z", "started_at": "2026-10-17T09:05:00Z"})
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

func run(t *testing.T, f *fakeServer, keys keySource, args ...string) (int, string, string) {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	var out, errOut bytes.Buffer
	args = append([]string{"--server", srv.URL}, args...)
	status := runResetAdmin(context.Background(), args, srv.Client(), keys, &out, &errOut)
	return status, out.String(), errOut.String()
}

func TestResetAdmin_OwnerKeyGetsALink(t *testing.T) {
	owner := edSigner(t)
	f := &fakeServer{owners: []ssh.PublicKey{owner.PublicKey()}, admins: []string{"agent@example.com"}}

	status, out, errOut := run(t, f, keysOf(owner), "--address", "agent@example.com", "--json", "--show-secret")
	if status != exitOK {
		t.Fatalf("status %d, stderr %s", status, errOut)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not one JSON object: %q", out)
	}
	link, err := url.Parse(got["link"].(string))
	if err != nil || link.Scheme != "rest-mail" || link.Host != "reset" {
		t.Fatalf("link = %v", got["link"])
	}
	if link.Query().Get("token") != "TOKEN-123" || !strings.HasPrefix(link.Query().Get("server"), "http://127.0.0.1:") {
		t.Errorf("link carries %v", link.Query())
	}
	if got["expires_at"] != "2026-10-10T21:15:00Z" {
		t.Errorf("expires_at = %v", got["expires_at"])
	}
	if f.answered[0]["address"] != "agent@example.com" || f.answered[0]["purpose"] != "reset-admin" {
		t.Errorf("asked for %v", f.answered[0])
	}
}

func TestResetAdmin_LinkIsRedactedUnlessAsked(t *testing.T) {
	owner := edSigner(t)
	f := &fakeServer{owners: []ssh.PublicKey{owner.PublicKey()}, admins: []string{"agent@example.com"}}
	_, out, _ := run(t, f, keysOf(owner), "--address", "agent@example.com", "--json")
	if strings.Contains(out, "TOKEN-123") || !strings.Contains(out, `"link_redacted":true`) {
		t.Errorf("--json without --show-secret printed %s", out)
	}
	// People get the QR code and the link.
	_, out, _ = run(t, f, keysOf(owner), "--address", "agent@example.com")
	if !strings.Contains(out, "TOKEN-123") || !strings.Contains(out, "Scan with the rest-mail app") {
		t.Errorf("text output: %s", out)
	}
}

// ssh-agent may hold several keys; any one of them may be the owner's.
func TestResetAdmin_TriesEachKey(t *testing.T) {
	other, owner := edSigner(t), edSigner(t)
	f := &fakeServer{owners: []ssh.PublicKey{owner.PublicKey()}, admins: []string{"agent@example.com"}}
	if status, _, errOut := run(t, f, keysOf(other, owner), "--address", "agent@example.com", "--json"); status != exitOK {
		t.Fatalf("status %d: %s", status, errOut)
	}
	if len(f.answered) != 2 {
		t.Errorf("answered %d challenges, want 2", len(f.answered))
	}
}

// RSA keys sign with SHA-2: the server refuses ssh-rsa (SHA-1) signatures.
func TestResetAdmin_RSAKeySignsWithSHA2(t *testing.T) {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	owner, _ := ssh.NewSignerFromKey(k)
	f := &fakeServer{owners: []ssh.PublicKey{owner.PublicKey()}, admins: []string{"agent@example.com"}}
	if status, _, errOut := run(t, f, keysOf(owner), "--address", "agent@example.com", "--json"); status != exitOK {
		t.Fatalf("status %d: %s", status, errOut)
	}
}

func TestResetAdmin_ExitStatuses(t *testing.T) {
	owner, stranger := edSigner(t), edSigner(t)
	listed := []ssh.PublicKey{owner.PublicKey()}
	agent := []string{"--address", "agent@example.com"}
	cases := []struct {
		name string
		f    *fakeServer
		keys keySource
		args []string
		want int
		code string
	}{
		{"not an owner key", &fakeServer{owners: listed, admins: []string{"agent@example.com"}}, keysOf(stranger), agent, exitNotOwner, "not_owner"},
		{"no keys at all", &fakeServer{owners: listed}, keysOf(), agent, exitNotOwner, "no_key"},
		{"server lists no owners", &fakeServer{}, keysOf(owner), agent, exitNotOwner, "no_owner_keys"},
		{"no such account", &fakeServer{owners: listed, admins: []string{"agent@example.com"}}, keysOf(owner), []string{"--address", "nobody@example.com"}, exitNoSuchAccount, "no_such_account"},
		{"no address", &fakeServer{owners: listed}, keysOf(owner), nil, exitUsage, "bad_flag"},
		{"unknown flag", &fakeServer{owners: listed}, keysOf(owner), append([]string{"--kube-context", "prod"}, agent...), exitUsage, "bad_flag"},
		{"the setup code is gone", &fakeServer{owners: listed}, keysOf(owner), append([]string{"--code", "ABCD-EFGH"}, agent...), exitUsage, "bad_flag"},
		{"lifetime too long", &fakeServer{owners: listed}, keysOf(owner), append([]string{"--lifetime", "48h"}, agent...), exitUsage, "bad_flag"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, out, errOut := run(t, c.f, c.keys, append(c.args, "--json")...)
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

func TestResetAdmin_NotARestMailServer(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	var out, errOut bytes.Buffer
	status := runResetAdmin(context.Background(), []string{"--server", srv.URL, "--address", "agent@example.com", "--json"}, srv.Client(), keysOf(edSigner(t)), &out, &errOut)
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
