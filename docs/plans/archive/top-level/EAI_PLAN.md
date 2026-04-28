# rest-mail — Internationalized Email (EAI) Support Plan

_This is a separate plan for supporting internationalized email addresses and headers. It is not part of the initial release but is documented here for future implementation._

## Overview

**EAI (Email Address Internationalization)** allows email addresses and headers to use non-ASCII characters — enabling addresses like `用户@例え.jp` or `münchen@städte.de`. This is defined by a set of RFCs that extend SMTP, IMAP, POP3, and the email message format.

## Relevant RFCs

| RFC | Title | What It Covers |
|-----|-------|---------------|
| **RFC 6530** | Overview and Framework for EAI | Architecture overview |
| **RFC 6531** | SMTP Extension for Internationalized Email (SMTPUTF8) | SMTP `SMTPUTF8` extension |
| **RFC 6532** | Internationalized Email Headers | UTF-8 in headers (From, To, Subject, etc.) |
| **RFC 6533** | Internationalized Delivery Status Notifications | UTF-8 DSN/bounce messages |
| **RFC 6855** | IMAP Support for UTF-8 | IMAP `UTF8=ACCEPT` capability |
| **RFC 6856** | POP3 Support for UTF-8 | POP3 `UTF8` capability |
| **RFC 6857** | Post-Delivery Message Downgrading | Fallback for non-EAI systems |
| **RFC 5891** | Internationalized Domain Names (IDNA 2008) | Punycode domain encoding |

## Impact on rest-mail Components

### 1. SMTP Gateway (Inbound)

The gateway must advertise `SMTPUTF8` in its EHLO capabilities:

```
S: 250-mail3.test
S: 250-SMTPUTF8
S: 250-RESTMAIL https://mail3.test/restmail
S: 250 8BITMIME
```

When a sending server includes `SMTPUTF8` in its `MAIL FROM` command, the gateway must accept UTF-8 in:
- `MAIL FROM:<用户@例え.jp> SMTPUTF8`
- `RCPT TO:<münchen@städte.de>`
- Message headers (From, To, Cc, Subject, etc.)

**Key change:** The SMTP parser must handle UTF-8 in envelope addresses and not reject them as malformed.

### 2. SMTP Gateway (Outbound)

When sending to a remote server:

1. Check if remote server advertises `SMTPUTF8` in its EHLO response
2. If **yes**: send with UTF-8 addresses and `SMTPUTF8` parameter
3. If **no**: **downgrade** the message:
   - Convert internationalized domain parts to Punycode (IDNA 2008): `例え.jp` → `xn--r8jz45g.jp`
   - If the local part has non-ASCII characters and the remote doesn't support SMTPUTF8, the message **cannot be delivered** — generate a bounce (RFC 6533)

### 3. IMAP Gateway

Advertise `UTF8=ACCEPT` capability (RFC 6855):

```
* OK [CAPABILITY IMAP4rev1 UTF8=ACCEPT] ready
```

When the client enables UTF-8 mode, the gateway returns headers and folder names in UTF-8 without RFC 2047 encoding.

### 4. POP3 Gateway

Advertise `UTF8` capability (RFC 6856). Similar to IMAP — return messages with UTF-8 headers when enabled.

### 5. REST API

The API already uses JSON (UTF-8 natively), so most of the API layer doesn't need changes. However:

- **Email address validation:** Must accept UTF-8 local parts (not just `[a-zA-Z0-9._%+-]`)
- **Domain handling:** Must support IDN (Internationalized Domain Names) — store as Unicode, convert to Punycode for DNS/SMTP
- **Search:** Full-text search must handle Unicode normalization (NFC vs NFD)

### 6. Database

PostgreSQL already supports UTF-8 (`ENCODING = 'UTF8'`). Columns storing email addresses and headers are `VARCHAR` or `TEXT`, which handle UTF-8 natively. No schema changes needed, but:

- **Collation:** Consider using ICU collation for correct Unicode sorting
- **Indexes:** Ensure `LOWER()` indexes work correctly with Unicode (may need `pg_trgm` or ICU-aware `LOWER()`)

### 7. MIME Parser

The MIME parser (`internal/mime/`) already handles RFC 2047 encoded headers. For EAI:

- **RFC 6532 headers:** Accept raw UTF-8 in headers (no RFC 2047 encoding needed)
- **Mixed mode:** Some headers may use RFC 2047, others raw UTF-8 — handle both
- **Downgrading (RFC 6857):** When forwarding to non-EAI systems, convert UTF-8 headers back to RFC 2047

### 8. Webmail

The React frontend already handles UTF-8 natively (JavaScript strings are UTF-16). Display of international email addresses should work without changes. Input validation needs updating to accept non-ASCII in email address fields.

## Implementation Phases

### Phase A: Domain Internationalization (IDN)

_Lower risk, higher value — many domains are already internationalized._

1. Add IDNA 2008 library (`golang.org/x/net/idna`)
2. Store domains as Unicode in database, convert to Punycode for DNS/SMTP operations
3. Accept IDN domains in API domain creation endpoints
4. Display Unicode domain names in webmail, convert to Punycode in protocol layer

### Phase B: SMTPUTF8 Support (Gateway)

_Core protocol change — enables receiving EAI mail._

1. Advertise `SMTPUTF8` in EHLO response
2. Accept UTF-8 in MAIL FROM / RCPT TO when `SMTPUTF8` parameter is present
3. Accept UTF-8 headers in DATA phase
4. Update MIME parser for RFC 6532 (raw UTF-8 headers)
5. Check remote SMTPUTF8 support before outbound delivery
6. Implement message downgrading (RFC 6857) for non-EAI recipients

### Phase C: IMAP/POP3 UTF-8 Support

1. Advertise `UTF8=ACCEPT` (IMAP) and `UTF8` (POP3)
2. Return UTF-8 headers when client enables the capability
3. Support UTF-8 folder names

### Phase D: Full Stack Validation

1. Update email address validation throughout API and webmail
2. Add Unicode normalization (NFC) for consistent storage
3. Add EAI-specific test cases to the test suite
4. Test interop with Gmail, Outlook.com, and other major providers that support SMTPUTF8

## Risks & Challenges

| Challenge | Impact | Mitigation |
|-----------|--------|------------|
| Many mail servers don't support SMTPUTF8 | Delivery failures for pure-EAI addresses | Downgrading (RFC 6857) + bounce on failure |
| SPF/DKIM/DMARC with internationalized domains | Standards may not fully cover IDN edge cases | Use Punycode for all DNS-based checks |
| Client compatibility | Older IMAP/POP3 clients may not support UTF8 | Only enable UTF-8 mode when client requests it |
| Testing complexity | Difficult to test with real-world EAI addresses | Create test accounts with IDN domains in dev environment |
| Unicode normalization | Same visual address could have different byte representations | Normalize to NFC before comparison/storage |

## Dependencies

- `golang.org/x/net/idna` — IDNA 2008 Punycode conversion
- `golang.org/x/text/unicode/norm` — Unicode normalization (NFC)
- PostgreSQL ICU collation (optional, for correct Unicode sorting)

## TODO

- [ ] Research current SMTPUTF8 adoption rates (Gmail, Outlook.com, Yahoo, etc.)
- [ ] Add `golang.org/x/net/idna` dependency
- [ ] Implement IDN domain support (Phase A — lowest risk, do first)
- [ ] Update email address validation to accept UTF-8 local parts
- [ ] Implement SMTPUTF8 in gateway SMTP handler (Phase B)
- [ ] Implement message downgrading (RFC 6857) for non-EAI outbound
- [ ] Implement IMAP `UTF8=ACCEPT` and POP3 `UTF8` capabilities (Phase C)
- [ ] Add EAI test cases to test suite (Phase D)
- [ ] Test interop with major EAI-capable providers

## Open Questions

- Should we support EAI for the traditional mail servers (mail1/mail2) in the dev environment, or only for mail3 (rest-mail)?
- What percentage of our target users (small businesses) would benefit from EAI support?
- Should IDN domain support (Phase A) be included in the initial release since it's relatively low-risk?
