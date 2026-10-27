# Attachment range and conditional response contract

## Follow-up, 2026-10-27

Vertrag 0.31.3 is pinned with its published archive checksum. Named topology
examples now select whole-file 200, single-range 206 and unsatisfiable-range 416
requests. Optional validators no longer become required header expectations.
The bodyless 304/412, multipart response and dependency/writer faults retain
explicit, narrow topology exclusions and their dedicated real-handler tests.
Four Python regression checks keep request/response names, header optionality
and exclusions aligned. Fresh GitHub CI/E2E results are pending; these changes
are not a claim that the complete topology matrix has passed.

## Original findings

This is partial #1166/#674 evidence against main
`aced5b62c68696720303e23eb1827fa040da502f` on 2026-10-27.
The existing download writer and ordinary 200 header work are retained.

## Findings and change

The attachment route described Range and conditional requests in prose, but its
registration omitted 206, 304, 412 and 416. Streaming failures were described as
JSON even when `http.ServeContent` writes plain text. The registration adapter
also replaced an earlier response override when a second media type used the
same status, hiding binary/multipart 206 and JSON/plain-text 500 alternatives.

The adapter now overrides an automatic response once, preserving its position,
and retains every explicitly declared media type. Attachment registration and
the independently updated oracle describe single binary ranges, multipart byte
ranges, bodyless cache/precondition responses, plain-text range refusals and both
500 bodies. The six existing Range/conditional request headers are declared as
optional strings, preserving ServeContent parsing and fallback behavior, and the
wire matrix exercises their production request binding. Header requirements follow the actual writer: partial Content-Length
counts the partial/multipart response; Content-Range is optional at the response
level because multipart ranges carry it on each part; conditional responses do
not promise ordinary byte-download headers. ETag and Last-Modified are optional
storage validators. A dependency failure does not promise Content-Disposition.

Both frontend clients are regenerated with pinned openapi-typescript 7.13.0 from
this revision's served description. This also catches up pre-existing main drift
in generated comments and fields, including live-account descriptions, existing
message flags and TLS-report types; those implementations are not changed here.

## Evidence and remaining work

The new adapter regression checks both media types at 206/500 and preserves the
position of an overridden 200. The 15-case download matrix uses the actual
attachment registration, safe filename formatter and Served writer. It checks
whole, single/suffix/multipart, If-Range match/fallback, empty-range fallback, 304, 412, malformed and
non-overlapping 416, seek failure and handler failure responses, declared headers,
body/media shapes and content closure. The original four filename/header cases
are retained. A new public-router test checks and exports the description the
server actually serves. Baseline execution reproduced the missing wire statuses,
plain-text 500 mismatch and media-type loss.

These local proofs isolate the writer from authorization/storage and do not claim
full live filesystem/S3, PostgreSQL, cross-account, fault-matrix or E2E acceptance.
No local PostgreSQL or full suite is run. The existing binary-schema spelling
difference for the oracle's 200 remains; no new oracle exception is added.

The four newly declared statuses (206/304/412/416) raise the reported missing named
example-pair inventory from 135 to 139. Named examples remain owned by #625 / PR
#1171; shared example files are preserved and these four gaps are handed off there.
Local wire fixtures prove these answers, but are not claimed as named Vertrag
pairs or a full running-server contract gate. The parent remains partial, and
#388/#661/#672 owner choices are not changed.
