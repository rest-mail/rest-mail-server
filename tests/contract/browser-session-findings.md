# Browser-session proof boundaries

For #1166, retaining the decided #659 and #654 acceptance.

The disposable-topology browser command previously ignored JSON decoding errors
and silently truncated non-stream responses at 1 MiB. A valid JSON prefix followed
by another object or arbitrary bytes, a truncated document, and an oversized
response could therefore pass a safe-route comparison. The command now reads one
byte beyond the allowance and fails on excess, requires exactly one JSON object
when the media type is JSON (including `+json`), and reports framing/read errors
without response values. Empty HEAD, 204 and 304 responses remain valid; plain
HTTP errors need no JSON envelope. Event streams are closed without waiting for
stream data.

Safe cookie-authenticated operations now compare missing-header, mismatched-token
and missing-CSRF-cookie requests with the valid-header control. Previously only
the missing-header variant was compared, so a middleware fault that wrongly
checks mismatches on safe methods went unnoticed. Unsafe operation probes retain
all three refusal variants and avoid mutations using invalid IDs and malformed
bodies. A known valid-header folder request still proves binding is reachable.

A response's error-code field is untrusted. Failure messages report the expected
fixed code and observed status, never the observed code; safe-route comparisons
also compare codes without including their values in diagnostics. The receipt
continues to contain only operation, principal variant and status.

Local regression fixtures inject these faults through HTTP and exercise the
response reader directly. They are proof-checker tests, not a claim that the live
API returns malformed bodies or rejects safe CSRF variants. No API handler,
request/response type, oracle entry, named example pair, cookie policy, 403/404
policy or 400/422 convention changes here. Full disposable-topology acceptance,
recovery/reset cookie flows, header-schema coverage, wider authorization/limit
faults and installed-cluster behavior remain separate acceptance work. No local
PostgreSQL or full E2E run is claimed.
