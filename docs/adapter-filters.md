# Adapter Filters vs Built-in Filters

RestMail's pipeline engine supports two kinds of filters: **built-in** Go filters and **external adapter** filters that delegate to third-party scanning services over HTTP.

## When to Use Which

| Criteria | Built-in Filter | External Adapter |
|----------|----------------|------------------|
| Latency | Sub-millisecond | 10-500ms per call |
| Dependencies | None (pure Go) | Requires sidecar container |
| Accuracy | Good for structured checks | Best-in-class (rspamd, ClamAV) |
| Maintenance | Updated with RestMail | Updated independently |
| Resources | Minimal | Significant (ClamAV uses ~1GB RAM) |

**Use built-in filters** for:
- SPF, DKIM, DMARC checks (already implemented)
- Header validation, size checks, rate limiting
- Sender verification, recipient checks
- Greylisting, allowlists, blocklists
- Sieve script processing

**Use external adapters** for:
- Spam scoring (rspamd) — statistical models, neural networks, fuzzy hashes
- Virus scanning (ClamAV) — signature-based malware detection
- Custom scanning services — any HTTP-based scanner

## Architecture

```
Pipeline Engine
  │
  ├── Built-in Filter (Go code, in-process)
  │     └── Execute(ctx, email) → FilterResult
  │
  └── Adapter Filter (HTTP client → external service)
        ├── Serialize email to RFC 2822
        ├── POST to scanner HTTP endpoint
        ├── Parse scanner response
        └── Map to FilterResult (action + headers)
```

External adapters implement the `pipeline.ExternalAdapter` interface:

```go
type ExternalAdapter interface {
    Name() string
    Scan(ctx context.Context, email *EmailJSON) (*AdapterResult, error)
    Healthy(ctx context.Context) bool
}
```

The adapter receives the structured `*EmailJSON` and serializes it to raw RFC
2822 itself (via `internal/mime.Serialize`) before calling out — the pipeline
does not hand it a pre-serialized byte slice.

## Enabling Scanning Sidecars

This repo is driven entirely by Taskfiles and ships **no** `docker-compose.yml`.
The rspamd/ClamAV scanning services are not part of this repo — spam/virus
scanning comes from the reference mail server stack
([`rest-mail/reference-mailserver`](https://github.com/rest-mail/reference-mailserver))
or any scanner you run yourself. Bring up a reference instance to get them:

```bash
task -d .workspace/reference-mailserver up CONFIG=mail1
```

Then point each filter's `url` (below) at the host/port that stack (or your own
deployment) exposes for the scanner.

## Configuring rspamd in a Pipeline

Add the rspamd filter to your inbound pipeline (via admin API or default templates):

```json
{
  "name": "rspamd",
  "type": "action",
  "enabled": true,
  "config": {
    "url": "http://rspamd:11333",
    "timeout_ms": 5000,
    "fallback_action": "continue"
  }
}
```

### rspamd Actions

rspamd returns one of these actions, which the adapter maps to pipeline actions:

| rspamd Action | Pipeline Action | Meaning |
|---------------|----------------|---------|
| `no action` | Continue | Clean message |
| `greylist` | Defer | Temporary rejection |
| `add header` | Continue | Spam header added |
| `rewrite subject` | Continue | Subject rewritten |
| `soft reject` | Defer | Temporary rejection |
| `reject` | Reject | Permanent rejection |

Headers added: `X-Spam-Status`, `X-Spam-Score`, `X-Spamd-Result`.

## Configuring ClamAV in a Pipeline

Add the ClamAV filter to your inbound pipeline:

```json
{
  "name": "clamav",
  "type": "action",
  "enabled": true,
  "config": {
    "url": "http://clamav:3310",
    "timeout_ms": 30000,
    "fallback_action": "continue"
  }
}
```

### ClamAV Behavior

- Clean messages: ActionContinue with `X-Virus-Status: Clean`
- Infected messages: ActionReject with `X-Virus-Status: Infected (<virus name>)`
- Scanner unavailable + `fallback_action: continue`: message passes through with a warning log

Headers added: `X-Virus-Scanned`, `X-Virus-Status`.

## Recommended Pipeline Order

For inbound mail, place scanning filters after authentication but before delivery:

```
1. size_check        (built-in, reject oversized)
2. spf_check         (built-in, tag/reject)
3. dkim_verify       (built-in, tag)
4. arc_verify        (built-in, chain validation)
5. dmarc_check       (built-in, policy enforcement)
6. domain_allowlist  (built-in, skip scanning for trusted senders)
7. rspamd            (adapter, spam scoring)
8. clamav            (adapter, virus scanning)
9. greylist          (built-in, delay unknown senders)
10. recipient_check  (built-in, verify mailbox exists)
11. extract_attachments (built-in, save to disk)
12. sieve            (built-in, user filtering rules)
```

## Writing Custom Adapters

To add a new external scanner, implement `pipeline.ExternalAdapter`:

```go
package filters

import (
    "context"
    "github.com/restmail/restmail/internal/pipeline"
)

type myScanner struct {
    url     string
    timeout time.Duration
}

func (s *myScanner) Name() string { return "my_scanner" }

func (s *myScanner) Scan(ctx context.Context, email *pipeline.EmailJSON) (*pipeline.AdapterResult, error) {
    // Serialize the email yourself (e.g. internal/mime.Serialize), POST it to
    // your scanner, then parse the response.
    return &pipeline.AdapterResult{
        Clean:  true,
        Action: pipeline.ActionContinue,
    }, nil
}

func (s *myScanner) Healthy(ctx context.Context) bool {
    // Check if scanner is reachable
    return true
}
```

Then register a factory for it. There is no exported wrapper: custom adapters
live in the `filters` package and wrap themselves in the package-internal
`adapterFilter` (the same struct `rspamd`/`clamav` use), reusing `parseAction`
to turn the configured `fallback_action` string into a `pipeline.Action`:

```go
func init() {
    pipeline.DefaultRegistry.Register("my_scanner", newMyScanner)
}

func newMyScanner(config []byte) (pipeline.Filter, error) {
    // Parse config (url, timeout_ms, fallback_action) as rspamd/clamav do.
    adapter := &myScanner{url: "http://scanner:8080"}
    return &adapterFilter{
        adapter:        adapter,
        fallbackAction: parseAction("continue", pipeline.ActionContinue),
    }, nil
}
```

## Health Checks

Each adapter has a `Healthy()` method checked before scanning. If the adapter is unhealthy, the configured `fallback_action` determines behavior:

- `"continue"` (default) — skip scanning, let message through
- `"defer"` — temporary rejection, retry later
- `"reject"` — permanent rejection
