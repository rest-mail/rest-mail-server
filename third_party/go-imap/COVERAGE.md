# Coverage gate and remaining work

CI requires at least **95% statement coverage**, aiming for 100%. It runs:

```sh
go test -race -count=1 -covermode=atomic -coverpkg=./... -coverprofile=coverage.out ./...
go tool cover -func=coverage.out | tee coverage.txt | python3 -B .github/scripts/check_coverage.py
```

The workflow uses Bash with `set -euo pipefail`, so either command failing fails
the build. The gate rejects absent, duplicate, malformed and out-of-range totals.
It has no configurable lower threshold and no exclusions. The reported total
uses `go tool cover`'s displayed precision, matching the shared RFC workflow.
CI uploads `coverage.out` and the function report `coverage.txt` as the
`statement-coverage` artifact even when the gate fails, unless the run is
cancelled. A run that fails before producing coverage has no profile to upload.

`-coverpkg=./...` is necessary: the checked wire proofs in `pkg/imap/rfctest`
exercise the public library from another package, and the server's own tests
exercise `pkg/imap/internal/wire`, `message` and `mutf7` through it. Their execution must count
toward coverage of the library. The checklist command is still run separately;
statement coverage does not measure RFC conformance or checklist completeness.

## Baseline and gaps

CI run [39689054876](https://github.com/rest-mail/go-imap/actions/runs/39689054876)
measured **93.0%** before the session branch tests were added. The follow-up
measurement on Go 1.25.10 linux/arm64 is **96.1%** (2,432 of 2,530 statements).
Packages were tested separately, sequentially, with `GOMAXPROCS=2 nice -n 10`:

```sh
go test -count=1 -covermode=atomic -coverprofile=root.out .
go test -count=1 -covermode=atomic -coverpkg=github.com/rest-mail/go-imap -coverprofile=rfc.out ./internal/rfctest
go test -count=1 -covermode=atomic -coverprofile=checklist.out ./internal/checklist
```

Those commands predate the package split (v0.13.0), when the library was the
root package. After the split, the same `-coverpkg=./...` run measured
**97.0%** on Go 1.27.1 darwin/arm64, the same total as before the move.

The profiles are combined by source file and block range, counting each block's
statements once and marking it covered when any package executed it. This counts
the external wire proofs with the library tests, like CI's `-coverpkg=./...`.
Local runs omit the race detector; the fresh CI race run remains authoritative.

New wire tests cover quota units and backend errors; LIST delimiter/attributes
and backend errors; CREATE/DELETE/RENAME validation and backend moves;
unauthenticated and unselected command refusals; malformed FETCH/STORE and UID
commands without backend stores; and authentication syntax refusal and recovery.
They assert replies, state recovery and backend effects, without production changes.

Remaining uncovered branches include listener binding/implicit TLS and accept-loop
errors in `pkg/imap/server/server.go`; MIME child/part parse failures; long-line drain boundaries;
authentication bans; selected-message iteration guards; search flag/date/fetch
failures and unknown search keys; deleted-flag removal; APPEND literal mailbox
failures and selected-mailbox refresh errors; IDLE polling/reconciliation guards;
and defensive parsing/formatting branches in `pkg/imap/internal/wire` and
`pkg/imap/internal/message`. These are test work, not
approved exclusions; protocol gaps remain recorded in the partial `CHECKLIST.md`.

The checker's `main` calls `os.Exit`, which cannot be exercised in-process.
Its logic is in `run`, exercised by the checker tests; CI also executes the CLI.
This statement remains in the coverage denominator.

## Shared workflow follow-up

The audited go-spf producer at
`7d291c08d310b9c62afc287e75a534a39150fafb` does not pass `-coverpkg=./...`
and uses duration-bounded fuzzing. go-imap keeps its existing workflow and
execution-bounded fuzz runs (see golang/go#75804) until the shared producer can
preserve both. Its fixed 95..100% total-validation rule is reproduced locally
with offline fixtures, including pipeline failure propagation. Coverage and
checklist badges remain a later slice after the gate is green.

## Unknown transfer encodings

Checked IMAP-CTE-04 wire proofs exercise RFC 2045 §6.4's opaque type override
for absent, malformed, text, application, multipart and message Content-Types,
including multipart/digest defaults and a valid sibling. They check both body
structure forms, raw and numeric section octets, suppression of nested sections,
and SEARCH without charset conversion or recursive decoding. Existing encoding
string and malformed-value tests now expect application/octet-stream for unknown
encodings. The gate remains 95%; a fresh measured total is left to CI.
