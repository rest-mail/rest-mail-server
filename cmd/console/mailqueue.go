package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/respond"
	apitypes "github.com/rest-mail/rest-mail-server/internal/api/types"
)

// `restmail mail-queue` inspects and acts on the outbound mail queue through
// the public API (/api/v1/admin/queue*), with an API key in RESTMAIL_API_KEY.
// It is named mail-queue so it is never confused with pull request merge
// queues.

const mailQueueUsage = `restmail mail-queue — inspect, retry, bounce and purge the outbound mail queue

  restmail mail-queue stats  --server URL
  restmail mail-queue list   --server URL [--status S] [--domain D] [--sender S] [--recipient R] [--limit N] [--offset N]
  restmail mail-queue show   --server URL ID
  restmail mail-queue retry  --server URL (ID | filter flags) [--yes]
  restmail mail-queue bounce --server URL (ID | filter flags) [--yes]
  restmail mail-queue purge  --server URL (ID | filter flags) [--yes]

Talks only to the server's public API. Set RESTMAIL_API_KEY to a token with
queue:read (stats, list, show) or queue:manage (retry, bounce, purge).

retry, bounce and purge act on one entry when given its ID, or on every entry
matching the filter flags. With a filter they first say how many entries they
will touch and ask; --yes skips the question, for scripts. A filter must name at
least one of --status, --domain, --sender or --recipient; --all selects every
entry the action applies to.

  retry   pending and deferred entries: try them now
  bounce  pending, deferred and delivering entries: give up, keeping the entry
          as bounced; the sender is not told (#1950)
  purge   any entry: delete it; the sender is not told

  --server URL       the server's address (https; http only for localhost)
  --status S         pending, delivering, deferred, delivered, bounced or expired
  --domain D         destination domain, exactly
  --sender S         sender address contains S
  --recipient R      recipient address contains R
  --all              (retry, bounce, purge) no filter: every entry
  --yes              (retry, bounce, purge) do not ask
  --limit N          (list) entries per page, 1–200 (default 50)
  --offset N         (list) entries to skip
  --json             one JSON object on stdout; errors as one on stderr

Exit status: 0 done, 2 usage, 5 the server did not answer, or not as rest-mail,
1 anything else, including a question answered no.
`

var queueStatuses = []string{"pending", "delivering", "deferred", "delivered", "bounced", "expired"}

// queueAction is what retry, bounce and purge send, and which statuses each
// applies to: the server's own eligibility rules (handlers/queue.go).
type queueAction struct {
	verb       string // in prompts: "Retry", "Bounce", "Purge"
	done       string // in results: "retried", "bounced", "purged"
	method     string // for one entry
	suffix     string // for one entry, after /{id}
	bulkMethod string
	bulkPath   string
	applies    []string
}

var queueActions = map[string]queueAction{
	"retry":  {"Retry", "retried", http.MethodPost, "/retry", http.MethodPost, "/api/v1/admin/queue/bulk-retry", []string{"pending", "deferred"}},
	"bounce": {"Bounce", "bounced", http.MethodPost, "/bounce", http.MethodPost, "/api/v1/admin/queue/bulk-bounce", []string{"pending", "deferred", "delivering"}},
	"purge":  {"Purge", "purged", http.MethodDelete, "", http.MethodDelete, "/api/v1/admin/queue/bulk-delete", queueStatuses},
}

// queueEntry is an entry as GET /api/v1/admin/queue answers it.
type queueEntry struct {
	ID              uint       `json:"id"`
	MessageID       *uint      `json:"message_id"`
	Sender          string     `json:"sender"`
	Recipient       string     `json:"recipient"`
	Domain          string     `json:"domain"`
	BodyType        string     `json:"body_type"`
	Status          string     `json:"status"`
	Attempts        int        `json:"attempts"`
	LastAttempt     *time.Time `json:"last_attempt"`
	NextAttempt     time.Time  `json:"next_attempt"`
	LastError       string     `json:"last_error"`
	LastErrorCode   int        `json:"last_error_code"`
	FailureCategory string     `json:"failure_category"`
	MaxRetries      int        `json:"max_retries"`
	CreatedAt       time.Time  `json:"created_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
}

type queuePage struct {
	Items  []queueEntry `json:"items"`
	Total  int64        `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

// queueFilter is the filter of GET /api/v1/admin/queue and of the bulk
// actions' body.
type queueFilter struct {
	Status    string `json:"status,omitempty"`
	Domain    string `json:"domain,omitempty"`
	Sender    string `json:"sender,omitempty"`
	Recipient string `json:"recipient,omitempty"`
}

func (f queueFilter) empty() bool { return f == queueFilter{} }

func (f queueFilter) query() url.Values {
	q := url.Values{}
	for k, v := range map[string]string{"status": f.Status, "domain": f.Domain, "sender": f.Sender, "recipient": f.Recipient} {
		if v != "" {
			q.Set(k, v)
		}
	}
	return q
}

// queueClient speaks to the queue endpoints with a bearer token. Unlike
// apiClient it accepts 204 No Content, which a single purge answers.
type queueClient struct {
	base  *url.URL
	http  *http.Client
	token string
}

// errNotAnswered: the server could not be reached, or did not answer like
// rest-mail.
var errNotAnswered = errors.New("not answered")

func (c queueClient) do(ctx context.Context, method, path string, body any, into any) (code, message string, err error) {
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
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
	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("%w: %s did not answer: %v", errNotAnswered, c.base.Host, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var refusal respond.ErrorResponse
	if json.Unmarshal(raw, &refusal) == nil && refusal.Error.Code != "" {
		return refusal.Error.Code, refusal.Error.Message, nil
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return "", "", fmt.Errorf("%w: %s does not offer %s: not a rest-mail server, or one older than this CLI", errNotAnswered, c.base.Host, path)
	}
	if resp.StatusCode == http.StatusNoContent && into == nil {
		return "", "", nil
	}
	var env endpoint.Data[json.RawMessage]
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &env) != nil || env.Data == nil {
		return "", "", fmt.Errorf("%w: %s did not answer like a rest-mail server (HTTP %d)", errNotAnswered, c.base.Host, resp.StatusCode)
	}
	if into != nil && json.Unmarshal(env.Data, into) != nil {
		return "", "", fmt.Errorf("%w: %s answered something unexpected", errNotAnswered, c.base.Host)
	}
	return "", "", nil
}

// call runs do and turns a refusal or failure into an exit status; ok is true
// when the request succeeded.
func (c queueClient) call(ctx context.Context, o output, method, path string, body, into any) (status int, ok bool) {
	code, msg, err := c.do(ctx, method, path, body, into)
	if errors.Is(err, errNotAnswered) {
		return o.fail(exitUnreachable, "unreachable", strings.TrimPrefix(err.Error(), errNotAnswered.Error()+": ")), false
	}
	if err != nil {
		return o.fail(exitFailed, "failed", err.Error()), false
	}
	if code != "" {
		return o.fail(exitFailed, code, msg), false
	}
	return exitOK, true
}

func runMailQueue(ctx context.Context, args []string, token string, stdin io.Reader, client *http.Client, stdout, stderr io.Writer) int {
	o := output{json: containsFlag(args, "json"), stdout: stdout, stderr: stderr}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		if len(args) == 0 {
			_, _ = io.WriteString(stderr, mailQueueUsage)
			return exitUsage
		}
		_, _ = io.WriteString(stdout, mailQueueUsage)
		return exitOK
	}
	sub := args[0]
	_, isAction := queueActions[sub]
	if sub != "stats" && sub != "list" && sub != "show" && !isAction {
		return o.fail(exitUsage, "bad_command", fmt.Sprintf("there is no mail-queue %q command: stats, list, show, retry, bounce or purge", sub))
	}

	fs := flag.NewFlagSet("mail-queue "+sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "the server's address")
	var f queueFilter
	fs.StringVar(&f.Status, "status", "", "status")
	fs.StringVar(&f.Domain, "domain", "", "destination domain")
	fs.StringVar(&f.Sender, "sender", "", "sender contains")
	fs.StringVar(&f.Recipient, "recipient", "", "recipient contains")
	all := fs.Bool("all", false, "every entry")
	yes := fs.Bool("yes", false, "do not ask")
	limit := fs.Int("limit", 50, "entries per page")
	offset := fs.Int("offset", 0, "entries to skip")
	asJSON := fs.Bool("json", false, "JSON output")
	help := fs.Bool("help", false, "show help")
	if err := parseInterspersed(fs, args[1:]); err != nil {
		return o.fail(exitUsage, "bad_flag", err.Error())
	}
	o.json = *asJSON
	if *help {
		_, _ = io.WriteString(stdout, mailQueueUsage)
		return exitOK
	}
	if f.Status != "" && !slices.Contains(queueStatuses, f.Status) {
		return o.fail(exitUsage, "bad_flag", fmt.Sprintf("--status %q is not one of %s", f.Status, strings.Join(queueStatuses, ", ")))
	}
	base, err := serverURL(*server)
	if err != nil {
		return o.fail(exitUsage, "bad_flag", err.Error())
	}
	if token == "" {
		return o.fail(exitUsage, "no_api_key", "set RESTMAIL_API_KEY to a token with queue:read, or queue:manage to change the queue")
	}
	c := queueClient{base: base, http: client, token: token}

	// One entry's ID, for show and the actions.
	var id uint64
	if fs.NArg() > 1 {
		return o.fail(exitUsage, "bad_flag", "give at most one entry ID")
	}
	if fs.NArg() == 1 {
		if sub == "stats" || sub == "list" {
			return o.fail(exitUsage, "bad_flag", sub+" takes no entry ID")
		}
		id, err = strconv.ParseUint(fs.Arg(0), 10, 32)
		if err != nil || id == 0 {
			return o.fail(exitUsage, "bad_flag", fmt.Sprintf("%q is not an entry ID", fs.Arg(0)))
		}
		if !f.empty() || *all {
			return o.fail(exitUsage, "bad_flag", "give an entry ID or filter flags, not both")
		}
	}

	switch sub {
	case "stats":
		if !f.empty() {
			return o.fail(exitUsage, "bad_flag", "stats takes no filter")
		}
		return queueStats(ctx, c, o)
	case "list":
		if *limit < 1 || *limit > 200 || *offset < 0 {
			return o.fail(exitUsage, "bad_flag", "--limit is 1–200 and --offset at least 0")
		}
		return queueList(ctx, c, o, f, *limit, *offset)
	case "show":
		if id == 0 {
			return o.fail(exitUsage, "bad_flag", "show takes an entry ID")
		}
		return queueShow(ctx, c, o, id)
	}

	action := queueActions[sub]
	if id != 0 {
		return queueActOnOne(ctx, c, o, sub, action, id)
	}
	if f.empty() && !*all {
		return o.fail(exitUsage, "bad_flag", sub+" needs an entry ID, a filter (--status, --domain, --sender, --recipient) or --all")
	}
	if *all && !f.empty() {
		return o.fail(exitUsage, "bad_flag", "--all selects every entry: give it without filter flags")
	}
	return queueActOnMany(ctx, c, o, sub, action, f, *yes, stdin)
}

// parseInterspersed parses flags given before and after the entry ID.
func parseInterspersed(fs *flag.FlagSet, args []string) error {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
	return fs.Parse(append([]string{"--"}, positional...))
}

func queueStats(ctx context.Context, c queueClient, o output) int {
	var stats apitypes.QueueStatistics
	if status, ok := c.call(ctx, o, http.MethodGet, "/api/v1/admin/queue/stats", nil, &stats); !ok {
		return status
	}
	if o.json {
		_ = json.NewEncoder(o.stdout).Encode(stats)
		return exitOK
	}
	tw := tabwriter.NewWriter(o.stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	for _, row := range []struct {
		name  string
		count int64
	}{
		{"pending", stats.Pending}, {"delivering", stats.Delivering}, {"deferred", stats.Deferred},
		{"delivered", stats.Delivered}, {"bounced", stats.Bounced}, {"expired", stats.Expired}, {"total", stats.Total},
	} {
		fmt.Fprintf(tw, "%s\t%d\t\n", row.name, row.count)
	}
	_ = tw.Flush()
	return exitOK
}

func queueList(ctx context.Context, c queueClient, o output, f queueFilter, limit, offset int) int {
	q := f.query()
	q.Set("limit", strconv.Itoa(limit))
	q.Set("offset", strconv.Itoa(offset))
	var page queuePage
	if status, ok := c.call(ctx, o, http.MethodGet, "/api/v1/admin/queue?"+q.Encode(), nil, &page); !ok {
		return status
	}
	if o.json {
		_ = json.NewEncoder(o.stdout).Encode(page)
		return exitOK
	}
	if len(page.Items) == 0 {
		fmt.Fprintf(o.stdout, "no entries (of %d)\n", page.Total)
		return exitOK
	}
	tw := tabwriter.NewWriter(o.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tATTEMPTS\tNEXT ATTEMPT\tCATEGORY\tSENDER\tRECIPIENT\tLAST ERROR")
	for _, e := range page.Items {
		fmt.Fprintf(tw, "%d\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n", e.ID, e.Status, e.Attempts,
			e.NextAttempt.UTC().Format(time.RFC3339), dash(e.FailureCategory), e.Sender, e.Recipient, oneLine(e.LastError))
	}
	_ = tw.Flush()
	fmt.Fprintf(o.stdout, "%d–%d of %d\n", page.Offset+1, page.Offset+len(page.Items), page.Total)
	return exitOK
}

func queueShow(ctx context.Context, c queueClient, o output, id uint64) int {
	var e queueEntry
	if status, ok := c.call(ctx, o, http.MethodGet, "/api/v1/admin/queue/"+strconv.FormatUint(id, 10), nil, &e); !ok {
		return status
	}
	if o.json {
		_ = json.NewEncoder(o.stdout).Encode(e)
		return exitOK
	}
	last := "-"
	if e.LastAttempt != nil {
		last = e.LastAttempt.UTC().Format(time.RFC3339)
	}
	message := "-"
	if e.MessageID != nil {
		message = strconv.FormatUint(uint64(*e.MessageID), 10)
	}
	code := "-"
	if e.LastErrorCode != 0 {
		code = strconv.Itoa(e.LastErrorCode)
	}
	tw := tabwriter.NewWriter(o.stdout, 0, 0, 2, ' ', 0)
	for _, row := range [][2]string{
		{"id", strconv.FormatUint(uint64(e.ID), 10)},
		{"status", e.Status},
		{"sender", e.Sender},
		{"recipient", e.Recipient},
		{"domain", e.Domain},
		{"message", message},
		{"attempts", fmt.Sprintf("%d of %d", e.Attempts, e.MaxRetries)},
		{"last attempt", last},
		{"next attempt", e.NextAttempt.UTC().Format(time.RFC3339)},
		{"failure category", dash(e.FailureCategory)},
		{"last error code", code},
		{"last error", dash(oneLine(e.LastError))},
		{"created", e.CreatedAt.UTC().Format(time.RFC3339)},
		{"expires", e.ExpiresAt.UTC().Format(time.RFC3339)},
	} {
		fmt.Fprintf(tw, "%s\t%s\n", row[0], row[1])
	}
	_ = tw.Flush()
	return exitOK
}

func queueActOnOne(ctx context.Context, c queueClient, o output, sub string, a queueAction, id uint64) int {
	var result struct {
		Status string `json:"status"`
	}
	var into any = &result
	if a.method == http.MethodDelete {
		into = nil
	}
	if status, ok := c.call(ctx, o, a.method, "/api/v1/admin/queue/"+strconv.FormatUint(id, 10)+a.suffix, nil, into); !ok {
		return status
	}
	if o.json {
		_ = json.NewEncoder(o.stdout).Encode(map[string]any{"action": sub, "id": id, "affected": 1})
		return exitOK
	}
	fmt.Fprintf(o.stdout, "%s entry %d\n", a.done, id)
	return exitOK
}

// queueActOnMany counts what the filter selects among the statuses the action
// applies to, asks, and then sends the bulk request with the same filter.
func queueActOnMany(ctx context.Context, c queueClient, o output, sub string, a queueAction, f queueFilter, yes bool, stdin io.Reader) int {
	statuses := a.applies
	if f.Status != "" {
		statuses = nil
		if slices.Contains(a.applies, f.Status) {
			statuses = []string{f.Status}
		}
	}
	var count int64
	for _, s := range statuses {
		q := f.query()
		q.Set("status", s)
		q.Set("limit", "1")
		var page queuePage
		if status, ok := c.call(ctx, o, http.MethodGet, "/api/v1/admin/queue?"+q.Encode(), nil, &page); !ok {
			return status
		}
		count += page.Total
	}
	if count == 0 {
		if o.json {
			_ = json.NewEncoder(o.stdout).Encode(map[string]any{"action": sub, "matched": 0, "affected": 0})
		} else {
			fmt.Fprintf(o.stdout, "no entries match: nothing to %s\n", sub)
		}
		return exitOK
	}
	if !yes {
		fmt.Fprintf(o.stderr, "%s %d %s entr%s? [y/N] ", a.verb, count, strings.Join(statuses, ", "), plural(count, "y", "ies"))
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
			return o.fail(exitFailed, "not_confirmed", "nothing changed")
		}
	}
	var result struct {
		Affected int64 `json:"affected"`
	}
	body := struct {
		Filter queueFilter `json:"filter"`
	}{f}
	if status, ok := c.call(ctx, o, a.bulkMethod, a.bulkPath, body, &result); !ok {
		return status
	}
	if o.json {
		_ = json.NewEncoder(o.stdout).Encode(map[string]any{"action": sub, "matched": count, "affected": result.Affected})
		return exitOK
	}
	fmt.Fprintf(o.stdout, "%s %d entr%s\n", a.done, result.Affected, plural(result.Affected, "y", "ies"))
	return exitOK
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// oneLine keeps a remote's multi-line reply on one table row.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
