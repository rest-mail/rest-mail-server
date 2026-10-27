# Shared checksum-pinned contract toolchain

Partial #1167/#627/#628 evidence, based on main
`625c2e5b2a26c5787c6beefe928bd454d82b6e51` on 2026-10-27.

The live contract runner previously selected `vertrag` from PATH, while the
complete local command did not select the existing pinned installer before
preparing topology/fixtures. Local and CI runs could therefore use different
tools. The native installer also ignored the workflow's immutable Linux digest
in favor of the fetched release checksum, and wrote directly over the executable.

The installer now requires agreement with the CI digest on Linux x86_64, verifies
the native archive, accepts only a regular executable member and publishes full
binary/receipt files atomically. A project-cache receipt binds version, platform,
workflow/archive digests and installed bytes. Every complete/live-phase invocation
checks the cache and executable startup before contacting the target or preparing
fixtures. A valid cache needs no downloads; damaged, stale, incomplete or
non-executable entries require a verified reinstall. A failed version command
cannot emit a selected path or expose captured tool output.

The whole command, standalone gate/authorization/fuzz phases, API-key probes and
child processes select that absolute cached executable. CI installs the same
cache and reuses it for both boundary checks and the complete command. The pin
remains 0.31.2 and its existing Linux digest agrees with the public release
manifest; the Darwin-arm64 asset is present. No version bump, repository setting,
token provisioning, merge behavior or release/promotion action is included.

Controls use synthetic archives, isolated caches and subprocess stand-ins. They
cover CI/release disagreement, stale/corrupt/non-executable cache entries,
interrupted replacement, link rejection, native-platform selection, safe CLI
output, startup failure before topology/fixtures, all three actual phase scripts,
wrong PATH tools, paths containing spaces, child command lookup and existing
failure/report semantics. A task-local Python environment supplies the tests'
existing PyYAML dependency; host packages and global tool installations are kept.
These are wiring proofs, not a full topology or a test of Vertrag itself. No real
CLI archive was installed, no Docker/Helm build or local PostgreSQL was started,
and no full suite, E2E run or CI/check/review status was fetched.

The parent remains partial. Shared named examples stay #625/#1171; optional-route
visibility and API policy choices stay with their owner decisions. #628's
settings/token requirement for automatic bump creation/merge remains blocked.
