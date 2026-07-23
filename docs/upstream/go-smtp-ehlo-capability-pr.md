# Prepared upstream PR: go-smtp custom EHLO capabilities

**Status:** ready to submit — awaiting human review. Do not merge rest-mail onto
upstream until this (or an equivalent) lands; until then we consume the fork via
a `replace` directive in `go.mod`.

- **Upstream repo:** `emersion/go-smtp`
- **Fork:** `rest-mail/go-smtp`, branch `custom-ehlo-capabilities`
  (commit `1df43ece562eb4fb95085d460d0dfcd447a104ee`)
- **Submit with:** open a PR from `rest-mail:custom-ehlo-capabilities` against
  `emersion:master` with the title and body below, verbatim.

---

## PR title

```
server: allow advertising extra EHLO capabilities
```

## PR body

```markdown
This adds an `ExtraCaps` field to `Server` holding additional capability
lines to advertise in response to EHLO. Each entry is advertised verbatim
as a separate capability line, after the built-in capabilities:

    srv := smtp.NewServer(be)
    srv.ExtraCaps = []string{"XEXAMPLE https://example.org/smtp-ext"}

    C: EHLO client.example.org
    S: 250-mx.example.org Hello client.example.org
    S: ...
    S: 250 XEXAMPLE https://example.org/smtp-ext

Motivation: servers sometimes need to advertise site-specific extensions
that go-smtp cannot know about — in our case a discovery capability that
points peer servers at an HTTPS upgrade endpoint (`RESTMAIL
https://<host>/restmail`). Today the capability list is built entirely
inside `handleGreet` from hard-wired `Enable*` flags, so the only options
are forking or proxying the connection. A verbatim string slice is the
smallest hook that covers this: no callback, no per-connection state, and
zero overhead for servers that do not set it.

Handling any commands such an extension defines remains the caller's
responsibility (same contract as the existing `Enable*` flags, which only
control advertising).

Includes a test (`TestServerExtraCaps`) in the style of the existing
capability tests via `testServerEhlo`.
```

## The API (as committed on the fork)

`server.go` — new field on `Server`:

```go
// Additional capabilities to advertise in response to EHLO, e.g.
// "XEXAMPLE https://example.org/smtp-ext". Each entry is advertised
// verbatim as a separate capability line.
//
// This can be used to advertise site-specific extensions that are not
// natively supported by this package. It is the caller's responsibility
// to handle any commands such an extension defines.
ExtraCaps []string
```

`conn.go` — one line in `handleGreet`, after the built-in capability blocks:

```go
caps = append(caps, c.server.ExtraCaps...)
```

`server_test.go` — `TestServerExtraCaps`, asserting the capability appears in
the EHLO response using the existing `testServerEhlo` helper.

Full diff: 26 insertions, 0 deletions, across the three files above.
`go vet` and the full upstream test suite pass.

## How rest-mail uses it

`internal/gateway/smtp/server.go` sets, per server:

```go
srv.ExtraCaps = []string{fmt.Sprintf("RESTMAIL https://%s/restmail", s.hostname)}
```

which preserves the wire-visible `250-RESTMAIL https://<host>/restmail`
advertisement (guarded by `TestSMTP_EhloAdvertisements`).

## After the upstream PR merges

1. Drop the `replace github.com/emersion/go-smtp => github.com/rest-mail/go-smtp ...`
   line from `go.mod`.
2. `go get github.com/emersion/go-smtp@<first release containing the hook>`.
3. Archive the fork branch.
