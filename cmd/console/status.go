package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	apitypes "github.com/restmail/restmail/internal/api/types"
)

// `restmail status` asks a server what it is and which version it runs.
// Like every command here, it knows a server only by its address: it asks the
// server (GET /api/v1/server) and believes the answer. How the server was
// installed — Helm, Pulumi, Docker, anything — is none of its business.
//
// With --key it proves it may know more, the way reset-admin proves ownership:
// it signs a challenge for the purpose "status" with a key the deployment lists
// in RESTMAIL_STATUS_KEYS (or an owner key), and the server answers with its
// commit, when it was built and when it started (POST /api/v1/auth/owner/status).

const statusUsage = `restmail status — what a rest-mail server is

  restmail status --server https://mail.example.com [--key PATH]

Asks the server itself, over its public API: whether it answers, and which
version it runs.

With --key, signs the question with a private key whose public half the
deployment lists in RESTMAIL_STATUS_KEYS or RESTMAIL_OWNER_KEYS, and also
prints the commit, when it was built and when it started: how a deploy is
confirmed from outside. A status key reads this and nothing else.

  --server URL   the server's address (https; http only for localhost)
  --key PATH     a private key file (unencrypted) to sign with
  --json         one JSON object on stdout; errors as one on stderr

Exit status: 0 the server answered, 2 usage, 5 the server did not answer, or
not as rest-mail, 6 the key is not one of the server's status or owner keys
(or it lists none), 1 anything else.
`

// statusPurpose is what --key signs: the server's bootstrap.StatusPurpose.
const statusPurpose = "status"

func runStatus(ctx context.Context, args []string, client *http.Client, keys keySource, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "the server's address")
	keyPath := fs.String("key", "", "a private key file")
	asJSON := fs.Bool("json", false, "JSON output")
	help := fs.Bool("help", false, "show help")

	o := output{json: containsFlag(args, "json"), stdout: stdout, stderr: stderr}
	if err := fs.Parse(args); err != nil {
		return o.fail(exitUsage, "bad_flag", err.Error())
	}
	o.json = *asJSON
	if *help {
		_, _ = io.WriteString(stdout, statusUsage)
		return exitOK
	}
	base, err := serverURL(*server)
	if err != nil {
		return o.fail(exitUsage, "bad_flag", err.Error())
	}
	api := apiClient{base: base, http: client}
	if *keyPath != "" {
		return signedStatusOf(ctx, api, keys, *keyPath, o)
	}

	var got apitypes.ServerDescription
	code, msg, err := api.get(ctx, "/api/v1/server", &got)
	if err != nil {
		return o.fail(exitUnreachable, "unreachable", err.Error())
	}
	if code != "" {
		return o.fail(exitFailed, code, msg)
	}
	if got.Product != "rest-mail" {
		return o.fail(exitUnreachable, "unreachable", fmt.Sprintf("%s answered, but not as a rest-mail server", base.Host))
	}

	if o.json {
		_ = json.NewEncoder(stdout).Encode(map[string]any{
			"server":  base.String(),
			"product": got.Product,
			"version": got.Version,
		})
		return exitOK
	}

	fmt.Fprintf(stdout, "%s: rest-mail %s\n", base.Host, got.Version)
	return exitOK
}

// signedStatusOf asks for the status with a challenge signed by the key at
// path.
func signedStatusOf(ctx context.Context, api apiClient, keys keySource, path string, o output) int {
	signers, err := keys(path)
	if errors.Is(err, errPassphrase) {
		return o.fail(exitNotOwner, "no_key", fmt.Sprintf("%s is passphrase-protected, and restmail never prompts: give status a key file without one", path))
	}
	if err != nil {
		return o.fail(exitNotOwner, "no_key", err.Error())
	}

	var got apitypes.ServerStatus
	var refused, refusal string
	for _, s := range signers {
		// A fresh challenge per key: each is spent once it is answered.
		var ch apitypes.OwnerChallenge
		if code, msg, err := api.post(ctx, "/api/v1/auth/owner/challenge", nil, &ch); err != nil {
			return o.fail(exitUnreachable, "unreachable", err.Error())
		} else if code != "" {
			return o.fail(statusExit(code), code, msg)
		}
		sig, err := s.sign(ownerMessage(ch.Challenge, statusPurpose, ""))
		if err != nil {
			return o.fail(exitFailed, "sign_failed", err.Error())
		}
		code, msg, err := api.post(ctx, "/api/v1/auth/owner/status", apitypes.OwnerStatusRequest{
			Challenge: ch.Challenge,
			PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.pub))),
			Signature: base64.StdEncoding.EncodeToString(ssh.Marshal(sig)),
		}, &got)
		if err != nil {
			return o.fail(exitUnreachable, "unreachable", err.Error())
		}
		if code == "not_status_key" {
			refused, refusal = code, msg
			continue
		}
		if code != "" {
			return o.fail(statusExit(code), code, msg)
		}
		return o.signedStatus(api.base, got)
	}
	if refused == "" {
		return o.fail(exitNotOwner, "no_key", "no key to sign with in "+path)
	}
	return o.fail(exitNotOwner, refused, refusal)
}

// signedStatus prints a signed status answer.
func (o output) signedStatus(base *url.URL, got apitypes.ServerStatus) int {
	if got.Version == "" {
		return o.fail(exitUnreachable, "unreachable", fmt.Sprintf("%s answered, but not as a rest-mail server", base.Host))
	}
	if o.json {
		_ = json.NewEncoder(o.stdout).Encode(map[string]any{
			"server":     base.String(),
			"version":    got.Version,
			"commit":     got.Commit,
			"built_at":   got.BuiltAt,
			"started_at": got.StartedAt,
		})
		return exitOK
	}
	fmt.Fprintf(o.stdout, "%s: rest-mail %s\n  commit   %s\n  built    %s\n  started  %s\n",
		base.Host, got.Version, got.Commit, got.BuiltAt, got.StartedAt.Format(time.RFC3339))
	return exitOK
}

// statusExit maps the server's refusals of a signed status request to exit
// statuses.
func statusExit(code string) int {
	switch code {
	case "not_status_key", "no_status_keys", "no_owner_keys":
		return exitNotOwner
	}
	return exitFailed
}
