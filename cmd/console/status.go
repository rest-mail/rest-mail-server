package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
)

// `restmail status` asks a server what it is and whether it has been claimed.
// Like every command here, it knows a server only by its address: it asks the
// server (GET /api/v1/server) and believes the answer. How the server was
// installed — Helm, Pulumi, Docker, anything — is none of its business.

const statusUsage = `restmail status — what a rest-mail server is, and whether it is claimed

  restmail status --server https://mail.example.com

Asks the server itself, over its public API: whether it answers, which version
it runs, and whether anyone has claimed it yet — and if not, how to.

  --server URL   the server's address (https; http only for localhost)
  --json         one JSON object on stdout; errors as one on stderr

Exit status: 0 the server answered (claimed or not: --json says which),
2 usage, 5 the server did not answer, or not as rest-mail.
`

// serverStatus is GET /api/v1/server's answer.
type serverStatus struct {
	Product      string   `json:"product"`
	Version      string   `json:"version"`
	Claimed      bool     `json:"claimed"`
	ClaimMethods []string `json:"claim_methods"`
}

func runStatus(ctx context.Context, args []string, client *http.Client, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "the server's address")
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

	var got serverStatus
	code, msg, err := apiClient{base: base, http: client}.get(ctx, "/api/v1/server", &got)
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
			"server":        base.String(),
			"product":       got.Product,
			"version":       got.Version,
			"claimed":       got.Claimed,
			"claim_methods": got.ClaimMethods,
		})
		return exitOK
	}

	fmt.Fprintf(stdout, "%s: rest-mail %s\n", base.Host, got.Version)
	if got.Claimed {
		fmt.Fprintln(stdout, "  claimed: it has an admin. Sign in with the rest-mail app.")
		return exitOK
	}
	fmt.Fprintln(stdout, "  not claimed yet. Whoever claims it first becomes its admin. To claim it:")
	for _, method := range got.ClaimMethods {
		switch method {
		case "setup_code":
			fmt.Fprintln(stdout, "    - with the setup code it prints at every start (read its output wherever")
			fmt.Fprintln(stdout, "      it was installed): scan the QR code with your phone, or")
			fmt.Fprintf(stdout, "      restmail setup --server %s --code CODE\n", base)
		case "owner_key":
			fmt.Fprintln(stdout, "    - with a key it lists as an owner's:")
			fmt.Fprintf(stdout, "      restmail setup --server %s --key ~/.ssh/id_ed25519\n", base)
		default:
			// A newer server may offer a way this CLI does not know yet.
			fmt.Fprintf(stdout, "    - %s\n", method)
		}
	}
	return exitOK
}
