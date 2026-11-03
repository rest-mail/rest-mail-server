package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/respond"
	"golang.org/x/term"
)

// `restmail app-password` lists, makes and revokes an account's app passwords
// through the public API (/api/v1/accounts/{id}/credentials), with the access
// token of a person's session in RESTMAIL_API_KEY. It keeps the API's rules
// and adds none: making one needs a recent two-factor sign-in and the current
// password entered again, which the command asks for without echo and never
// takes as a flag; the new app password is printed once.

const appPasswordUsage = `restmail app-password — list, make and revoke an account's app passwords

  restmail app-password list   --server URL [--account ID] [--json]
  restmail app-password create --server URL [--account ID] --name NAME [--expires-at TIME] [--password-stdin] [--json]
  restmail app-password revoke --server URL [--account ID] CREDENTIAL_ID [--yes] [--json]

Talks only to the server's public API. Set RESTMAIL_API_KEY to the access token
of your session: sign in with POST /api/v1/auth/login, "client": "native" and
your authenticator code. An API key or an app password is refused: managing
app passwords takes a person.

  list    the account's app passwords that are not revoked
  create  make one, named NAME; it is printed once and never again
  revoke  revoke one by its ID, as list shows it; asks first

create needs a sign-in with two-factor authentication at most 15 minutes old
(less where the server shortens it), and your current password entered again.
It asks for the password on the terminal, without echo. --password-stdin reads
it from stdin instead, for scripts:

  printf '%s' "$PASSWORD" | restmail app-password create --server URL --name laptop --password-stdin

revoke needs a session signed in with two-factor authentication.

  --server URL          the server's address (https; http only for localhost)
  --account ID          whose app passwords: an account the session holds
                        mailbox:manage on (default: the session's own account)
  --name NAME           (create) what the app password is for, such as "laptop"
  --expires-at TIME     (create) when it stops working, RFC 3339; default never
  --password-stdin      (create) read the current password from stdin
  --yes                 (revoke) do not ask
  --json                one JSON object on stdout; errors as one on stderr

Exit status: 0 done, 2 usage, 5 the server did not answer, or not as rest-mail,
1 anything else, including a refusal or a question answered no.
`

// appPasswordCredential is an app password as the API lists it: never its secret.
type appPasswordCredential struct {
	ID         uint       `json:"id"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	PublicID   string     `json:"public_id"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
}

// appPasswordCreated is the answer to making one: the app password itself,
// this once.
type appPasswordCreated struct {
	appPasswordCredential
	Password string `json:"password"`
}

// appPasswordCreateBody is POST /api/v1/accounts/{id}/credentials's body.
type appPasswordCreateBody struct {
	Name      string     `json:"name"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Password  string     `json:"password"`
}

// appPasswordTerminal says whether r is a person at a terminal; and
// readHiddenPassword reads a line from that terminal without echoing it.
// Replaced in tests.
var (
	appPasswordTerminal = isTerminal
	readHiddenPassword  = func(r io.Reader) ([]byte, error) {
		return term.ReadPassword(int(r.(*os.File).Fd()))
	}
)

// appPasswordClient speaks to the credential routes with a session's token.
type appPasswordClient struct {
	base  *url.URL
	http  *http.Client
	token string
}

// errAppPasswordUnanswered: the server could not be reached, or did not answer
// like rest-mail.
var errAppPasswordUnanswered = errors.New("not answered")

// do sends the request and decodes the data envelope into into. code and
// message are the server's refusal, when it refused.
func (c appPasswordClient) do(ctx context.Context, method, path string, body, into any) (code, message string, err error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return "", "", err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, reader)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// A redirect must not carry the token or a password anywhere else.
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("%w: %s did not answer", errAppPasswordUnanswered, c.base.Host)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var refusal respond.ErrorResponse
	if json.Unmarshal(raw, &refusal) == nil && refusal.Error.Code != "" {
		return refusal.Error.Code, refusal.Error.Message, nil
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return "", "", fmt.Errorf("%w: %s does not offer %s: not a rest-mail server, or one older than this CLI", errAppPasswordUnanswered, c.base.Host, path)
	}
	if resp.StatusCode == http.StatusNoContent && into == nil {
		return "", "", nil
	}
	var env endpoint.Data[json.RawMessage]
	if (resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated) || json.Unmarshal(raw, &env) != nil || env.Data == nil {
		return "", "", fmt.Errorf("%w: %s did not answer like a rest-mail server (HTTP %d)", errAppPasswordUnanswered, c.base.Host, resp.StatusCode)
	}
	if into != nil && json.Unmarshal(env.Data, into) != nil {
		return "", "", fmt.Errorf("%w: %s answered something unexpected", errAppPasswordUnanswered, c.base.Host)
	}
	return "", "", nil
}

// call runs do and turns a refusal or failure into an exit status; ok is true
// when the request succeeded.
func (c appPasswordClient) call(ctx context.Context, o output, method, path string, body, into any) (status int, ok bool) {
	code, msg, err := c.do(ctx, method, path, body, into)
	switch {
	case errors.Is(err, errAppPasswordUnanswered):
		return o.fail(exitUnreachable, "unreachable", strings.TrimPrefix(err.Error(), errAppPasswordUnanswered.Error()+": ")), false
	case err != nil:
		return o.fail(exitFailed, "failed", err.Error()), false
	case code != "":
		return o.fail(exitFailed, code, appPasswordRefusal(code, msg)), false
	}
	return exitOK, true
}

// appPasswordRefusal adds what to do to the refusals a CLI user can act on.
// The CLI cannot sign in with a second factor itself, and does not try to get
// around the rule: it says how to get a session that passes.
func appPasswordRefusal(code, message string) string {
	switch code {
	case "two_factor_required", "recent_two_factor_required":
		return message + ". Sign in again with your authenticator code (POST /api/v1/auth/login with \"client\": \"native\" and \"totp_code\") and set RESTMAIL_API_KEY to the new access_token, then run this again within 15 minutes. An account without two-factor authentication must set it up first."
	case "invalid_password":
		return message + ": enter the account's current password, not an app password."
	case "forbidden":
		return message + ". RESTMAIL_API_KEY must be a person's session token, not an API key or an app password."
	}
	return message
}

func runAppPassword(ctx context.Context, args []string, token string, stdin io.Reader, client *http.Client, stdout, stderr io.Writer) int {
	o := output{json: containsFlag(args, "json"), stdout: stdout, stderr: stderr}
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, appPasswordUsage)
		return exitUsage
	}
	sub := args[0]
	switch sub {
	case "help", "--help", "-help", "-h":
		_, _ = io.WriteString(stdout, appPasswordUsage)
		return exitOK
	case "list", "create", "revoke":
	default:
		return o.fail(exitUsage, "bad_command", fmt.Sprintf("there is no app-password %q command: list, create or revoke", sub))
	}

	fs := flag.NewFlagSet("app-password "+sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "the server's address")
	account := fs.String("account", "", "whose app passwords")
	name := fs.String("name", "", "what the app password is for")
	expiresAt := fs.String("expires-at", "", "when it stops working")
	passwordStdin := fs.Bool("password-stdin", false, "read the current password from stdin")
	yes := fs.Bool("yes", false, "do not ask")
	asJSON := fs.Bool("json", false, "JSON output")
	help := fs.Bool("help", false, "show help")
	// Flags may follow the credential ID: parse, take one positional, repeat.
	var positional []string
	rest := args[1:]
	for {
		if err := fs.Parse(rest); err != nil {
			return o.fail(exitUsage, "bad_flag", err.Error())
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	o.json = *asJSON
	if *help {
		_, _ = io.WriteString(stdout, appPasswordUsage)
		return exitOK
	}
	base, err := serverURL(*server)
	if err != nil {
		return o.fail(exitUsage, "bad_flag", err.Error())
	}
	if sub != "create" && (*name != "" || *expiresAt != "" || *passwordStdin) {
		return o.fail(exitUsage, "bad_flag", "--name, --expires-at and --password-stdin are for create")
	}
	if sub != "revoke" && *yes {
		return o.fail(exitUsage, "bad_flag", "--yes is for revoke")
	}
	if sub == "revoke" && len(positional) != 1 {
		return o.fail(exitUsage, "bad_flag", "revoke takes one credential ID, as list shows it")
	}
	if sub != "revoke" && len(positional) != 0 {
		return o.fail(exitUsage, "bad_flag", sub+" takes no positional arguments")
	}
	if token == "" {
		return o.fail(exitUsage, "no_api_key", "set RESTMAIL_API_KEY to the access token of your session, signed in with your authenticator code")
	}
	accountID, err := appPasswordAccount(*account, token)
	if err != nil {
		return o.fail(exitUsage, "bad_flag", err.Error())
	}
	c := appPasswordClient{base: base, http: client, token: token}
	path := "/api/v1/accounts/" + strconv.FormatUint(accountID, 10) + "/credentials"

	switch sub {
	case "list":
		return appPasswordList(ctx, c, o, path)
	case "create":
		return appPasswordCreate(ctx, c, o, path, *name, *expiresAt, *passwordStdin, stdin)
	}
	credentialID, err := strconv.ParseUint(positional[0], 10, 32)
	if err != nil || credentialID == 0 {
		return o.fail(exitUsage, "bad_flag", fmt.Sprintf("%q is not a credential ID", positional[0]))
	}
	return appPasswordRevoke(ctx, c, o, path, credentialID, *yes, stdin)
}

// appPasswordAccount is the account named by --account, or else the one the
// session's access token was issued for: its account_id claim. The token is
// read only to find that number; the server checks it on every request.
func appPasswordAccount(flagValue, token string) (uint64, error) {
	if flagValue != "" {
		id, err := strconv.ParseUint(flagValue, 10, 32)
		if err != nil || id == 0 {
			return 0, fmt.Errorf("--account %q is not an account ID", flagValue)
		}
		return id, nil
	}
	parts := strings.Split(token, ".")
	if len(parts) == 3 {
		if payload, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
			var claims struct {
				AccountID uint64 `json:"account_id"`
			}
			if json.Unmarshal(payload, &claims) == nil && claims.AccountID != 0 && claims.AccountID <= 1<<32-1 {
				return claims.AccountID, nil
			}
		}
	}
	return 0, errors.New("RESTMAIL_API_KEY is not a session's access token, so give the account with --account ID")
}

func appPasswordList(ctx context.Context, c appPasswordClient, o output, path string) int {
	var creds []appPasswordCredential
	if status, ok := c.call(ctx, o, http.MethodGet, path, nil, &creds); !ok {
		return status
	}
	if creds == nil {
		creds = []appPasswordCredential{}
	}
	if o.json {
		_ = json.NewEncoder(o.stdout).Encode(map[string]any{"app_passwords": creds})
		return exitOK
	}
	if len(creds) == 0 {
		fmt.Fprintln(o.stdout, "No app passwords.")
		return exitOK
	}
	tw := tabwriter.NewWriter(o.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tCREATED\tLAST USED\tEXPIRES")
	for _, cr := range creds {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", cr.ID, cr.Name, appPasswordTime(&cr.CreatedAt, "-"), appPasswordTime(cr.LastUsedAt, "never"), appPasswordTime(cr.ExpiresAt, "never"))
	}
	_ = tw.Flush()
	return exitOK
}

func appPasswordTime(t *time.Time, none string) string {
	if t == nil || t.IsZero() {
		return none
	}
	return t.UTC().Format("2006-01-02 15:04Z")
}

func appPasswordCreate(ctx context.Context, c appPasswordClient, o output, path, name, expiresAt string, passwordStdin bool, stdin io.Reader) int {
	name = strings.TrimSpace(name)
	if name == "" {
		return o.fail(exitUsage, "bad_flag", "--name is required: what the app password is for, such as \"laptop\"")
	}
	body := appPasswordCreateBody{Name: name}
	if expiresAt != "" {
		t, err := time.Parse(time.RFC3339, expiresAt)
		if err != nil {
			return o.fail(exitUsage, "bad_flag", fmt.Sprintf("--expires-at %q is not an RFC 3339 time, such as 2027-01-31T00:00:00Z", expiresAt))
		}
		body.ExpiresAt = &t
	}
	password, status, ok := appPasswordCurrentPassword(o, passwordStdin, stdin)
	if !ok {
		return status
	}
	body.Password = password

	var created appPasswordCreated
	if status, ok := c.call(ctx, o, http.MethodPost, path, body, &created); !ok {
		return status
	}
	if created.Password == "" {
		return o.fail(exitFailed, "invalid_response", "the server made the app password but did not answer with it: revoke it and make another")
	}
	if o.json {
		_ = json.NewEncoder(o.stdout).Encode(created)
		return exitOK
	}
	fmt.Fprintf(o.stdout, "Made app password %q (ID %d).\n\n  %s\n\nCopy it now: you won't see it again. Revoke it with: restmail app-password revoke %d\n",
		created.Name, created.ID, created.Password, created.ID)
	return exitOK
}

// appPasswordCurrentPassword asks for the account's current password: on the
// terminal without echo, or from stdin with --password-stdin. It never comes
// from a flag, where any process on the machine could read it. A terminal
// is always read without echo, --password-stdin or not.
func appPasswordCurrentPassword(o output, passwordStdin bool, stdin io.Reader) (password string, status int, ok bool) {
	var raw []byte
	switch {
	case appPasswordTerminal(stdin):
		fmt.Fprint(o.stderr, "Current password: ")
		b, err := readHiddenPassword(stdin)
		fmt.Fprintln(o.stderr)
		if err != nil {
			return "", o.fail(exitFailed, "read_failed", "could not read the password from the terminal"), false
		}
		raw = b
	case passwordStdin:
		b, err := io.ReadAll(io.LimitReader(stdin, 4097))
		if err != nil {
			return "", o.fail(exitFailed, "read_failed", "could not read the password from stdin"), false
		}
		if len(b) > 4096 {
			return "", o.fail(exitUsage, "no_password", "the password on stdin is too long"), false
		}
		raw = b
	default:
		return "", o.fail(exitUsage, "no_password", `no terminal to ask for your current password on: pipe it with --password-stdin, printf '%s' "$PASSWORD" | restmail app-password create ... --password-stdin`), false
	}
	password = strings.TrimRight(string(raw), "\r\n")
	if password == "" {
		return "", o.fail(exitUsage, "no_password", "no password given: making an app password takes your current password"), false
	}
	return password, exitOK, true
}

func appPasswordRevoke(ctx context.Context, c appPasswordClient, o output, path string, credentialID uint64, yes bool, stdin io.Reader) int {
	if !yes {
		fmt.Fprintf(o.stderr, "Revoke app password %d? Whatever signs in with it stops at its next request. [y/N] ", credentialID)
		line, _ := bufio.NewReader(io.LimitReader(stdin, 1024)).ReadString('\n')
		if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
			return o.fail(exitFailed, "not_confirmed", "nothing revoked")
		}
	}
	if status, ok := c.call(ctx, o, http.MethodDelete, path+"/"+strconv.FormatUint(credentialID, 10), nil, nil); !ok {
		return status
	}
	if o.json {
		_ = json.NewEncoder(o.stdout).Encode(map[string]any{"revoked": credentialID})
		return exitOK
	}
	fmt.Fprintf(o.stdout, "Revoked app password %d.\n", credentialID)
	return exitOK
}
