# Streamed RETR and TOP patch

This is go-pop3 v0.4.0 (rest-mail/go-pop3 at `72caf751d`, MIT, copyright
rest-mail, see [LICENSE](LICENSE)), retained with its original tests, plus the
one interface and protocol change below. A local module replacement
(`replace github.com/rest-mail/go-pop3 => ./third_party/go-pop3` in the
server's `go.mod`) makes the server build reproducibly until the change can move
upstream. No installed module cache is modified. `.github/workflows/ci.yml` is
left out, because GitHub runs only the workflows at a repository's root.

## Streamed RETR and TOP

`Mailbox.Retrieve` returns a message as one `[]byte`, and RETR and TOP then copy
it to a string, canonicalize its line endings into another, and format each line
again to send it, so a 9 MiB message cost the server several times its size
(rest-mail/rest-mail-server#2107 AC01). `pop3.StreamMailbox` is an optional
`Mailbox` extension, in the shape of go-imap's `StreamFetcher`: `OpenMessage`
returns a `pop3.StoredMessage` (`io.ReaderAt`, `Size`, `Close`) holding exactly
the octets `Retrieve` would have returned, and RETR and TOP frame them as they
read them (`pkg/pop3/internal/wire/stream.go`):

- RETR reads the message twice: once to count the RFC 1939 §11 octets its
  `+OK <n> octets` advertises, then to send them. TOP reads it once and stops
  after the last body line it sends.
- Line endings, bare CRs, the CRLF added to an unterminated last line,
  dot-stuffing, TOP's header/body split and the `.` terminator are those of
  `WireMessage`, `SplitMessageLines`, `TopLines` and `DotStuff`.
  `TestStreamedFramingMatchesInMemory`, `...Exhaustively` (every message of up
  to seven octets of CR, LF, `.` and `a`) and `FuzzStreamedFraming` compare the
  two byte for byte; `TestStreamedRetrAndTopMatchRetrieve` compares whole
  session transcripts.
- A message `OpenMessage` cannot open gets the reply `Retrieve`'s error gets
  (`[UTF8]` for `ErrUTF8Required`), as does one that fails to read while RETR
  counts it. One that fails to read or changes after `+OK` has gone out ends the
  connection without the terminator, so a client cannot take part of a message
  for the whole. Only a complete RETR marks the message seen.
- The server closes the message however the command ends: success, error,
  client disconnect or a recovered panic (`TestStreamed*`).

A Mailbox without `OpenMessage` is served from `Retrieve` exactly as before.

## CI

The server's workflow runs this copy's gates: `go vet` and `golangci-lint` in its
Lint job, and in its Test IMAP module job the RFC checklist, the CPython
`poplib` reference test and `go test -race` with `-coverpkg=./...`, failing
below the 95% statement coverage gate. `TestReadmeCoverageBadge` reads that gate
from the server's `.github/workflows/ci.yml`.

## `Server.Serve` for an already-bound listener

`Server.Serve(listener net.Listener, implicitTLS bool)` serves on a listener the
caller has already bound; `ListenAndServe` binds and then calls the same code.
The library's own tests use it to bind `127.0.0.1:0` and keep that listener,
instead of probing for a free port, closing it and binding again, which races
other processes for the port (rest-mail-server's `internal/testguard`
`TestNoFreePortProbes` refuses that pattern).
