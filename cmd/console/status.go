package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
)

// `restmail status` asks a server what it is and which version it runs.
// Like every command here, it knows a server only by its address: it asks the
// server (GET /api/v1/server) and believes the answer. How the server was
// installed — Helm, Pulumi, Docker, anything — is none of its business.

const statusUsage = `restmail status — what a rest-mail server is

  restmail status --server https://mail.example.com

Asks the server itself, over its public API: whether it answers, and which
version it runs.

  --server URL   the server's address (https; http only for localhost)
  --json         one JSON object on stdout; errors as one on stderr

Exit status: 0 the server answered, 2 usage, 5 the server did not answer, or
not as rest-mail.
`

// serverStatus is GET /api/v1/server's answer.
type serverStatus struct {
	Product string `json:"product"`
	Version string `json:"version"`
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
			"server":  base.String(),
			"product": got.Product,
			"version": got.Version,
		})
		return exitOK
	}

	fmt.Fprintf(stdout, "%s: rest-mail %s\n", base.Host, got.Version)
	return exitOK
}
