package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/respond"
)

// `restmail slow-statements` lists the database's slowest statements and resets
// their counters through the public API (/api/v1/admin/database/statements*,
// #1629), with an API key in RESTMAIL_API_KEY. It never needs a shell in the
// database pod.

const slowStatementsUsage = `restmail slow-statements — the database's slowest statements, from pg_stat_statements

  restmail slow-statements list  --server URL [--sort total|mean] [--limit N]
  restmail slow-statements reset --server URL

Talks only to the server's public API. Set RESTMAIL_API_KEY to a token with
database:read (list) or database:manage (reset).

list shows the statements slowest first, by total execution time (--sort
total, the default) or by mean time per call (--sort mean), with their calls
and rows. Each statement is the text Postgres records, constants replaced by
$1, $2 and so on, so no parameter value is shown. The figures count from the
last reset or the database's start; reset, let traffic run, then list.

  --server URL   the server's address (https; http only for localhost)
  --sort S       (list) total or mean (default total)
  --limit N      (list) how many statements, 1–100 (default 20)
  --json         one JSON object on stdout; errors as one on stderr

A server whose database does not preload pg_stat_statements answers
statistics_unavailable: the chart's bundled Postgres preloads it; an external
one needs shared_preload_libraries = 'pg_stat_statements'.

Exit status: 0 done, 2 usage, 5 the server did not answer, or not as rest-mail,
1 anything else.
`

// slowStatement is a statement as GET /api/v1/admin/database/statements
// answers it.
type slowStatement struct {
	Query   string  `json:"query"`
	Calls   int64   `json:"calls"`
	TotalMS float64 `json:"total_ms"`
	MeanMS  float64 `json:"mean_ms"`
	Rows    int64   `json:"rows"`
}

type slowStatementsView struct {
	Sort       string          `json:"sort"`
	Statements []slowStatement `json:"statements"`
}

// errSlowNotAnswered: the server could not be reached, or did not answer like
// rest-mail.
var errSlowNotAnswered = errors.New("not answered")

// slowStatementsCall sends one request with the bearer token. It answers the
// server's refusal code and message, or err when the server could not be
// reached or did not answer like rest-mail. A 204 with into nil is success.
func slowStatementsCall(ctx context.Context, client *http.Client, base *url.URL, token, method, path string, into any) (code, message string, err error) {
	req, err := http.NewRequestWithContext(ctx, method, base.String()+path, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("%w: %s did not answer: %v", errSlowNotAnswered, base.Host, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var refusal respond.ErrorResponse
	if json.Unmarshal(raw, &refusal) == nil && refusal.Error.Code != "" {
		return refusal.Error.Code, refusal.Error.Message, nil
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return "", "", fmt.Errorf("%w: %s does not offer %s: not a rest-mail server, or one older than this CLI", errSlowNotAnswered, base.Host, path)
	}
	if resp.StatusCode == http.StatusNoContent && into == nil {
		return "", "", nil
	}
	var env endpoint.Data[json.RawMessage]
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &env) != nil || env.Data == nil {
		return "", "", fmt.Errorf("%w: %s did not answer like a rest-mail server (HTTP %d)", errSlowNotAnswered, base.Host, resp.StatusCode)
	}
	if into != nil && json.Unmarshal(env.Data, into) != nil {
		return "", "", fmt.Errorf("%w: %s answered something unexpected", errSlowNotAnswered, base.Host)
	}
	return "", "", nil
}

func runSlowStatements(ctx context.Context, args []string, token string, client *http.Client, stdout, stderr io.Writer) int {
	o := output{json: containsFlag(args, "json"), stdout: stdout, stderr: stderr}
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, slowStatementsUsage)
		return exitUsage
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, _ = io.WriteString(stdout, slowStatementsUsage)
		return exitOK
	}
	sub := args[0]
	if sub != "list" && sub != "reset" {
		return o.fail(exitUsage, "bad_command", fmt.Sprintf("there is no slow-statements %q command: list or reset", sub))
	}

	fs := flag.NewFlagSet("slow-statements "+sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "the server's address")
	sort := fs.String("sort", "total", "total or mean")
	limit := fs.Int("limit", 20, "how many statements")
	asJSON := fs.Bool("json", false, "JSON output")
	help := fs.Bool("help", false, "show help")
	if err := fs.Parse(args[1:]); err != nil {
		return o.fail(exitUsage, "bad_flag", err.Error())
	}
	o.json = *asJSON
	if *help {
		_, _ = io.WriteString(stdout, slowStatementsUsage)
		return exitOK
	}
	if fs.NArg() > 0 {
		return o.fail(exitUsage, "bad_flag", fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
	}
	if *sort != "total" && *sort != "mean" {
		return o.fail(exitUsage, "bad_flag", "--sort must be total or mean")
	}
	if *limit < 1 || *limit > 100 {
		return o.fail(exitUsage, "bad_flag", "--limit must be 1 to 100")
	}
	base, err := serverURL(*server)
	if err != nil {
		return o.fail(exitUsage, "bad_flag", err.Error())
	}
	if token == "" {
		return o.fail(exitUsage, "no_token", "set RESTMAIL_API_KEY to an API key with database:read (list) or database:manage (reset)")
	}

	fail := func(code, msg string, err error) int {
		if errors.Is(err, errSlowNotAnswered) {
			return o.fail(exitUnreachable, "unreachable", strings.TrimPrefix(err.Error(), errSlowNotAnswered.Error()+": "))
		}
		if err != nil {
			return o.fail(exitFailed, "failed", err.Error())
		}
		return o.fail(exitFailed, code, msg)
	}

	if sub == "reset" {
		code, msg, err := slowStatementsCall(ctx, client, base, token, http.MethodPost, "/api/v1/admin/database/statements/reset", nil)
		if err != nil || code != "" {
			return fail(code, msg, err)
		}
		if o.json {
			_ = json.NewEncoder(stdout).Encode(map[string]any{"reset": true})
		} else {
			fmt.Fprintln(stdout, "Statement statistics reset. List them again once traffic has run.")
		}
		return exitOK
	}

	q := url.Values{"sort": {*sort}, "limit": {strconv.Itoa(*limit)}}
	var view slowStatementsView
	code, msg, err := slowStatementsCall(ctx, client, base, token, http.MethodGet, "/api/v1/admin/database/statements?"+q.Encode(), &view)
	if err != nil || code != "" {
		return fail(code, msg, err)
	}
	if view.Statements == nil {
		view.Statements = []slowStatement{}
	}
	if o.json {
		_ = json.NewEncoder(stdout).Encode(view)
		return exitOK
	}
	if len(view.Statements) == 0 {
		fmt.Fprintln(stdout, "No statements recorded since the last reset.")
		return exitOK
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TOTAL MS\tMEAN MS\tCALLS\tROWS\tSTATEMENT")
	for _, s := range view.Statements {
		fmt.Fprintf(tw, "%.1f\t%.2f\t%d\t%d\t%s\n", s.TotalMS, s.MeanMS, s.Calls, s.Rows, statementLine(s.Query, 160))
	}
	_ = tw.Flush()
	return exitOK
}

// statementLine folds a statement's whitespace to single spaces and cuts it to at
// most n runes, for a table row; --json gives the whole text.
func statementLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
