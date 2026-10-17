package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mdp/qrterminal/v4"
	"github.com/restmail/restmail/internal/setuplink"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// `restmail reset-admin` lets an admin back in when every other way is lost,
// proving ownership of the server over its public API, whatever runs it. The
// deployment lists the owner's public keys (RESTMAIL_OWNER_KEYS); the CLI signs
// a challenge the server issues with the matching private key, and gets back a
// single-use link for the app, shown as a QR code.
//
// Nothing about how the server is deployed is assumed: no kubectl, no docker,
// only HTTPS — the same requests the app makes.
//
// Written for agents as much as people. Every input is a flag, nothing prompts,
// --json gives one object on stdout and errors as one on stderr, and the exit
// status says what kind of failure it was.

// Exit statuses of reset-admin.
const (
	exitOK     = 0
	exitFailed = 1 // anything unexpected
	exitUsage  = 2 // a flag that does not exist, or is missing
	// 3 was already_claimed, from when a server could be claimed. Unused, so a
	// script written against it does not read some other failure into it.
	exitNoSuchAdmin = 4 // reset-admin named an account that does not exist
	exitUnreachable = 5 // the server did not answer, or not as rest-mail
	exitNotOwner    = 6 // no key offered is one of the server's owner keys (for status: status or owner keys)
)

const resetUsage = `restmail reset-admin — let an admin back in when every other way is lost

  restmail reset-admin --server https://mail.example.com --username NAME [--key PATH]

Proves you own the server with a private key whose public half the deployment
lists in RESTMAIL_OWNER_KEYS (authorized_keys format), then shows a single-use
link for that admin as a QR code. Whoever redeems it chooses a new password and
enrolls a new authenticator, and every session the account had ends: anyone who
held the old credentials is locked out. This is also how a server set up by
someone else, or by an agent, is handed over.

Works the same however the server is deployed: it only talks to the server's
public API.

  --server URL    the server's address (https; http only for localhost)
  --username NAME the admin to let back in
  --key PATH      a private key file (unencrypted; put a passphrase-protected
                  key in ssh-agent instead). Without it, the keys in ssh-agent
                  are tried.
  --json          one JSON object on stdout; errors as one on stderr
  --show-secret   include the link itself in --json output (it grants admin)
  --lifetime D    how long the link works, e.g. 1h (default 15m, at most 24h)

Exit status: 0 ok, 2 usage, 4 no such admin, 5 server not reachable, 6 no key
offered is an owner key, 1 anything else.
`

// signer is one key the CLI can prove ownership with.
type signer struct {
	pub  ssh.PublicKey
	sign func(data []byte) (*ssh.Signature, error)
}

// keySource finds the keys to try. Replaced in tests.
type keySource func(path string) ([]signer, error)

// runResetAdmin runs `restmail reset-admin`.
func runResetAdmin(ctx context.Context, args []string, client *http.Client, keys keySource, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reset-admin", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "the server's address")
	keyPath := fs.String("key", "", "a private key file")
	username := fs.String("username", "", "the admin to let back in")
	lifetime := fs.Duration("lifetime", 15*time.Minute, "how long the link works")
	asJSON := fs.Bool("json", false, "JSON output")
	showSecret := fs.Bool("show-secret", false, "include the link in --json output")
	help := fs.Bool("help", false, "show help")

	const purpose = "reset-admin"
	o := output{json: containsFlag(args, "json"), stdout: stdout, stderr: stderr}
	if err := fs.Parse(args); err != nil {
		return o.fail(exitUsage, "bad_flag", err.Error())
	}
	o.json = *asJSON
	if *help {
		_, _ = io.WriteString(stdout, resetUsage)
		return exitOK
	}
	if *username == "" {
		return o.fail(exitUsage, "bad_flag", "--username is required")
	}
	if *lifetime <= 0 || *lifetime > 24*time.Hour {
		return o.fail(exitUsage, "bad_flag", "--lifetime must be between 1s and 24h")
	}
	base, err := serverURL(*server)
	if err != nil {
		return o.fail(exitUsage, "bad_flag", err.Error())
	}

	signers, err := keys(*keyPath)
	if err != nil {
		return o.fail(exitNotOwner, "no_key", err.Error())
	}
	if len(signers) == 0 {
		return o.fail(exitNotOwner, "no_key", "no key to prove ownership with: pass --key, or load one into ssh-agent")
	}

	api := apiClient{base: base, http: client}
	var token, expires string
	var lastNotOwner string
	for _, s := range signers {
		// A fresh challenge per key: each is spent once it is answered.
		var ch struct {
			Challenge string `json:"challenge"`
		}
		if code, msg, err := api.post(ctx, "/api/v1/auth/owner/challenge", nil, &ch); err != nil {
			return o.fail(exitUnreachable, "unreachable", err.Error())
		} else if code != "" {
			return o.fail(statusFor(code), code, msg)
		}
		sig, err := s.sign(ownerMessage(ch.Challenge, purpose, *username))
		if err != nil {
			return o.fail(exitFailed, "sign_failed", err.Error())
		}
		var minted struct {
			Token     string `json:"token"`
			ExpiresAt string `json:"expires_at"`
		}
		code, msg, err := api.post(ctx, "/api/v1/auth/owner/token", map[string]any{
			"challenge":        ch.Challenge,
			"public_key":       strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.pub))),
			"signature":        base64.StdEncoding.EncodeToString(ssh.Marshal(sig)),
			"purpose":          purpose,
			"username":         *username,
			"lifetime_seconds": int(lifetime.Seconds()),
		}, &minted)
		if err != nil {
			return o.fail(exitUnreachable, "unreachable", err.Error())
		}
		if code == "not_owner" {
			lastNotOwner = msg
			continue // the next key in the agent may be the one
		}
		if code != "" {
			return o.fail(statusFor(code), code, msg)
		}
		token, expires = minted.Token, minted.ExpiresAt
		break
	}
	if token == "" {
		return o.fail(exitNotOwner, "not_owner", fmt.Sprintf("none of the %d key(s) offered is an owner key of this server: %s", len(signers), lastNotOwner))
	}

	return o.link(purpose, base, token, expires, *username, *showSecret)
}

// link shows the app's link for token: as a QR code and text for people, or as
// JSON — without the link unless showSecret — for programs.
func (o output) link(purpose string, base *url.URL, token, expires, username string, showSecret bool) int {
	link := setuplink.Reset(base.String(), token)
	if o.json {
		result := map[string]any{"purpose": purpose, "server": base.String()}
		if expires != "" {
			result["expires_at"] = expires
		}
		if showSecret {
			result["link"] = link
		} else {
			result["link_redacted"] = true
		}
		_ = json.NewEncoder(o.stdout).Encode(result)
		return exitOK
	}
	what := "choose a new password and authenticator for " + username
	fmt.Fprintf(o.stdout, "Scan with the rest-mail app to %s.\n\n", what)
	qrterminal.GenerateWithConfig(link, qrterminal.Config{Level: qrterminal.M, Writer: o.stdout, HalfBlocks: true, QuietZone: 2})
	fmt.Fprintf(o.stdout, "\nOr open: %s\n", link)
	if expires != "" {
		fmt.Fprintf(o.stdout, "Works once, until %s. Anyone holding it can %s.\n", expires, what)
	} else {
		fmt.Fprintf(o.stdout, "Works once. Anyone holding it can %s.\n", what)
	}
	return exitOK
}

// ownerMessage must match bootstrap.OwnerMessage on the server.
func ownerMessage(challenge, purpose, username string) []byte {
	return []byte("restmail-owner-v1\n" + purpose + "\n" + username + "\n" + challenge)
}

// statusFor maps the server's error codes to exit statuses.
func statusFor(code string) int {
	switch code {
	case "no_such_admin":
		return exitNoSuchAdmin
	case "not_owner", "no_owner_keys":
		return exitNotOwner
	}
	return exitFailed
}

// serverURL accepts an https address, or http for this machine only: a link
// that grants admin does not cross a network in the clear.
func serverURL(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, errors.New("--server is required")
	}
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("--server %q is not an address", raw)
	}
	switch u.Scheme {
	case "https":
		return u, nil
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return u, nil
		}
	}
	return nil, fmt.Errorf("--server must be https:// (http only for localhost), got %q", raw)
}

// apiClient speaks rest-mail's JSON envelope.
type apiClient struct {
	base *url.URL
	http *http.Client
}

// post sends body to path; see do.
func (c apiClient) post(ctx context.Context, path string, body any, into any) (code, message string, err error) {
	return c.do(ctx, http.MethodPost, path, body, into)
}

// get reads path; see do.
func (c apiClient) get(ctx context.Context, path string, into any) (code, message string, err error) {
	return c.do(ctx, http.MethodGet, path, nil, into)
}

// do returns the server's error code and message when it refused, or err when
// it could not be reached or did not answer like rest-mail.
func (c apiClient) do(ctx context.Context, method, path string, body any, into any) (code, message string, err error) {
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, reader)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("%s did not answer: %w", c.base.Host, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env struct {
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &env) == nil && env.Error != nil {
		return env.Error.Code, env.Error.Message, nil
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return "", "", fmt.Errorf("%s does not offer %s: not a rest-mail server, or one older than this CLI", c.base.Host, path)
	}
	if resp.StatusCode != http.StatusOK || env.Data == nil {
		return "", "", fmt.Errorf("%s did not answer like a rest-mail server (HTTP %d)", c.base.Host, resp.StatusCode)
	}
	if into != nil && json.Unmarshal(env.Data, into) != nil {
		return "", "", fmt.Errorf("%s answered something unexpected", c.base.Host)
	}
	return "", "", nil
}

// localKeys loads the key at path, or else every key in ssh-agent. It never
// prompts: an encrypted key file is refused with a pointer to ssh-agent.
func localKeys(path string) ([]signer, error) {
	if path != "" {
		signers, err := keyFile(path)
		if errors.Is(err, errPassphrase) {
			return nil, fmt.Errorf("%s is passphrase-protected: add it to ssh-agent (ssh-add %s) and leave out --key", path, path)
		}
		return signers, err
	}
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, errors.New("no --key given and no ssh-agent (SSH_AUTH_SOCK is unset)")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("ssh-agent: %w", err)
	}
	signers, err := agent.NewClient(conn).Signers()
	if err != nil {
		return nil, fmt.Errorf("ssh-agent: %w", err)
	}
	out := make([]signer, 0, len(signers))
	for _, s := range signers {
		out = append(out, fromSSHSigner(s))
	}
	return out, nil
}

// errPassphrase: a key file that needs a passphrase, which the CLI never asks
// for.
var errPassphrase = errors.New("passphrase-protected")

// keyFile loads the unencrypted private key at path.
func keyFile(path string) ([]signer, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s, err := ssh.ParsePrivateKey(pem)
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		return nil, fmt.Errorf("%s: %w", path, errPassphrase)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return []signer{fromSSHSigner(s)}, nil
}

// fromSSHSigner signs with SHA-2 for RSA keys: the server refuses SHA-1.
func fromSSHSigner(s ssh.Signer) signer {
	return signer{pub: s.PublicKey(), sign: func(data []byte) (*ssh.Signature, error) {
		if as, ok := s.(ssh.AlgorithmSigner); ok && s.PublicKey().Type() == ssh.KeyAlgoRSA {
			return as.SignWithAlgorithm(rand.Reader, data, ssh.KeyAlgoRSASHA256)
		}
		return s.Sign(rand.Reader, data)
	}}
}

func containsFlag(args []string, name string) bool {
	for _, a := range args {
		if a == "-"+name || a == "--"+name || strings.HasPrefix(a, "--"+name+"=") {
			return true
		}
	}
	return false
}

// output writes results and errors as text for people or JSON for programs.
type output struct {
	json           bool
	stdout, stderr io.Writer
}

func (o output) fail(status int, code, message string) int {
	if o.json {
		_ = json.NewEncoder(o.stderr).Encode(map[string]any{
			"error": map[string]string{"code": code, "message": message},
		})
	} else {
		fmt.Fprintf(o.stderr, "restmail: %s\n", message)
	}
	return status
}
