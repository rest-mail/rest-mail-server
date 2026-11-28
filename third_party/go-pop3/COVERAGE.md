# Coverage exceptions

CI fails when statement coverage falls below 95%. Coverage is counted across
every package (`-coverpkg=./...`), because the RFC proofs in `pkg/pop3/rfctest`
drive the library from outside it. These are the statements left uncovered,
each with the reason. Add an entry here whenever you leave a statement
uncovered.

| File | Function | Statements | Reason |
|------|----------|------------|--------|
| `internal/checklist/main.go` | `main` | `os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))` | Exiting the process cannot be tested in-process. All of the checker's logic is in `run`, which the tests cover; CI also runs the command itself. |

`pkg/pop3/server/server_accept_test.go` covers the former server exceptions with a controlled
`net.Listener` and `net.Pipe`, without production hooks, sockets or sleeps:

- `TestAcceptErrorRecovers` injects an Accept error while running, then accepts a
  client that receives the greeting, authenticates and quits. Shutdown drains
  both the accept loop and session; the limiter sees exactly one acquire/release
  pair, so the failed Accept consumes no slot.
- `TestAcceptConnectionAfterShutdown` holds a connection acquired by Accept until
  the public `Close` closes the listener. Accept then returns that connection:
  the server refuses registration, releases its limiter slot exactly once,
  closes without a greeting, and stops accepting. No connection remains tracked.

These are server lifecycle and coverage proofs, not additional RFC checklist
claims. The complete RFC inventory and other follow-ups in `CHECKLIST.md` remain
open.

## Package split verification

On Go 1.25.10 darwin/arm64, aggregate statement coverage was **99.9%** before
and **99.9%** after the v0.4.0 package split. The final run used the race
detector and `-coverpkg=./...`; all 183 Test, Fuzz and Example declarations
were retained. The gate remains 95%, with no new exclusions.
