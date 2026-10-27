# Recorded credential transports and safe findings

This is partial #1165/#658 evidence, continuing the decided #673 secret-scan
scope, against main `7a4c49ee666c3bea59922cd0ff98b470a2f1bac4` on 2026-10-27.
The schema/strict-binding guards and published #1173 wire-type scope are kept.
Shared named example pairs remain #625 / #1171. No API status, authorization,
validation order, request/response schema, client or generated-document switch
policy is changed.

## Findings and changes

The recorded-run scanner collected JSON secrets but missed a refresh token sent
without its original issuance, Basic/Bearer authorization, and browser session
cookies. It matched response bodies and audit entries but missed credentials
returned through headers, redirects or cookies. It also treated a push device
`token` as an issued credential despite the documented exclusion, so a normal
later device listing could fail. Finding text printed four characters of each
secret and could repeat complete values through request URLs, HAR comments,
header names, object keys or audit actions.

The scanner now collects those credential inputs and cookie-only session issuance
from both HAR cookie arrays and raw headers, using the server's exact
`restmail_access`/`restmail_refresh` cookie names. It scans all response transports,
including decoded Basic passwords, cookie quoting, escaped redirect URLs and
base64 HAR bodies. Cookie/CSRF preferences and push device tokens are not
credentials; their responses are still matched against secrets collected
elsewhere. The existing single-issuance response exception is preserved.

Documentation bodies contain example credentials and remain exempt from leak
matching. They are now also excluded from issuance collection, preventing a
specification example from becoming a supposed real credential. Documentation
headers and cookies are still checked. Findings retain method, route, transport
and JSON path, omit URL userinfo/query/fragment values, and redact every collected
secret from metadata and output without retaining a prefix.

## Controls and limits

`test_secret_transports.py` adds controls for each transport, cookie-only issuance,
ordinary push registration/listing, leaked credentials in push responses, encoded
values, audit leaks, safe CLI failure output, documentation examples and URL
metadata. The first nine test methods produced 17 failing subcases on main;
negative controls preserve redacted/short/malformed inputs and public cookies.
The original scanner tests are retained, with only the expected safe hint changed.
CI's existing `test_*.py` discovery includes both files.

These are synthetic, local HAR fixtures. No production HAR, credential, network
login, live audit read, E2E topology or full local suite is used. Vertrag replaces
its fixture admin's login values with `<redacted>` in recorded traffic, so that
marker is not evidence that the password leaked or stayed private. The known
fixture password is checked separately in the unredacted audit log. Short values
and transformations beyond the explicitly decoded transports remain outside the
existing scanner's claim. This batch does not establish completion of #1165 or
remove the wider oracle proof, conventions, clients and owner-decision work.

## Audit retrieval follow-up, 2026-10-27

On current main `67b061652bb57a5d9147a89b41c535cfdeaebeda`, the separate audit
reader followed HTTP redirects with its bearer token, treated missing cursor or
pagination data as a clean terminal page, and did not collect its own freshly
issued login tokens. Three baseline controls each returned a false clean result.

Login and audit requests now refuse redirects. Every page must contain an array
of objects and explicit boolean `has_more`; a continuing page must be nonempty
with a fresh nonempty string cursor. Cursors are URL-encoded rather than appended
to the query. A cycle or safety limit fails the scan instead of returning partial
records. Limits are 1,000 pages, 16 MiB per HTTP answer and 64 MiB of serialized
audit records. The reader's login JSON credentials and session cookies join the
known-secret set and are searched in recorded responses and audit entries.

Input, HTTP, configuration and JSON failures exit 2 with a fixed sanitized
message and no clean summary. No URL, response body, credential or exception
text is printed. `test_secret_audit_read.py` uses two loopback HTTP servers and
synthetic credentials to exercise redirects, complete/incomplete pagination,
cursor encoding/cycles/limits, malformed pages, login and cookie credentials,
network failures and safe output. An additional CLI control rejects malformed
HAR input. No production credential, live login, live audit or full topology is
used; no API schema, oracle, example or client behavior changes. Parent #1165
remains partial and the recorded owner decisions remain open.
