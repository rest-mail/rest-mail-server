package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/restmail/restmail/internal/auth"
	rmail "github.com/restmail/restmail/internal/mail"
)

const importAccountUsage = `restmail import-account — create an account with its existing password hash

  printf '%s' "$HASH" | restmail import-account --server https://mail.example.com --address user@example.com

Reads one ARGON2ID, ARGON2I, BLF-CRYPT, SHA256-CRYPT or SHA512-CRYPT hash from
stdin. Authentication uses RESTMAIL_API_KEY, a scoped API key with
mailboxes:write on the domain.
The domain must already exist. An existing account is refused, never replaced.
The hash and API key are never printed. This imports one account, without mail,
aliases or forwarding. See docs/migrating-accounts.md for limits and cutover.

  --server URL          the server's address (https; http only for localhost)
  --address ADDR        the new account's address
  --display-name NAME   optional display name
  --json                one JSON object on stdout; errors as one on stderr

Exit status: 0 created, 2 usage, 1 import failed.
`

func runImportAccount(ctx context.Context, args []string, stdin io.Reader, token string, client *http.Client, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("import-account", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "the server's address")
	address := fs.String("address", "", "the new account's address")
	displayName := fs.String("display-name", "", "optional display name")
	asJSON := fs.Bool("json", false, "JSON output")
	help := fs.Bool("help", false, "show help")
	o := output{json: containsFlag(args, "json"), stdout: stdout, stderr: stderr}
	if err := fs.Parse(args); err != nil {
		return o.fail(exitUsage, "bad_flag", err.Error())
	}
	o.json = *asJSON
	if *help {
		_, _ = io.WriteString(stdout, importAccountUsage)
		return exitOK
	}
	base, err := serverURL(*server)
	if err != nil {
		return o.fail(exitUsage, "bad_flag", err.Error())
	}
	name, err := rmail.CanonicalMailboxAddress(*address)
	if err != nil || fs.NArg() != 0 {
		return o.fail(exitUsage, "bad_flag", "give --address as a mail address, with no positional arguments")
	}
	if token == "" {
		return o.fail(exitUsage, "no_api_key", "set RESTMAIL_API_KEY to a scoped API key with mailboxes:write on the domain")
	}
	if isTerminal(stdin) {
		return o.fail(exitUsage, "no_hash", "pipe the existing password hash on stdin")
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, 259))
	if err != nil || len(raw) > 258 {
		return o.fail(exitUsage, "invalid_hash", "could not read one password hash of at most 256 bytes")
	}
	hash, err := auth.NormalizeImportedPasswordHash(strings.TrimRight(string(raw), "\r\n"))
	if err != nil {
		return o.fail(exitUsage, "invalid_hash", err.Error())
	}
	id, status, err := postImportedAccount(ctx, client, base, token, name, *displayName, hash)
	switch {
	case err != nil:
		return o.fail(exitFailed, "request_failed", err.Error())
	case status != http.StatusCreated:
		return o.fail(exitFailed, "import_refused", fmt.Sprintf("the server refused the import (HTTP %d); check the domain, API key scope and whether the account already exists", status))
	case id == 0:
		return o.fail(exitFailed, "invalid_response", "the server returned an invalid account response")
	}
	if o.json {
		_ = json.NewEncoder(stdout).Encode(map[string]any{"id": id, "address": name})
	} else {
		fmt.Fprintf(stdout, "Created %s (account %d).\n", name, id)
	}
	return exitOK
}

// postImportedAccount creates one account with an imported hash, returning the
// new account's ID (0 unless the server answered 201) and the HTTP status. The
// error is only for a request that could not be made or answered.
func postImportedAccount(ctx context.Context, client *http.Client, base *url.URL, token, address, displayName, hash string) (uint, int, error) {
	body, err := json.Marshal(map[string]string{"address": address, "display_name": displayName, "password_hash": hash})
	if err != nil {
		return 0, 0, fmt.Errorf("could not encode the import")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String()+"/api/v1/accounts", bytes.NewReader(body))
	if err != nil {
		return 0, 0, fmt.Errorf("could not prepare the import")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	// A redirect must not forward a password hash or credential elsewhere.
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("the import request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return 0, resp.StatusCode, nil
	}
	var result struct {
		Data struct {
			ID uint `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return 0, resp.StatusCode, nil
	}
	return result.Data.ID, resp.StatusCode, nil
}
