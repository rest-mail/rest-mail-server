# 03 — Feature Gaps

What's missing that mature mail-server products ship. Ranked by impact-to-the-user.

Note: I'm scoring against "what a small but real-world mail provider needs," not "what Gmail has." A homegrown product that solves the first 80% beats a clone of Gmail that's 5% complete.

## Tier 1 — block real customers (do these)

### A. Per-recipient queue state

Currently a queued outbound message has one delivery state. Real mail has N recipients per message; one might bounce permanently while four others go through. The product needs:

- `queue_recipient` table linking (queue_id, recipient_address) → state, last_error, attempts
- API: `GET /messages/<id>/recipients` returns per-recipient delivery status
- Webmail: when a sent message has a partial bounce, surface it in the conversation thread

Without this, "did my message reach Bob?" is a debug-by-grep operation. Real mail clients display this.

### B. Suppression list (hard-bounce blocklist)

When `bob@example.com` returns 5xx, the product should refuse to retry sending to that address for 90 days (configurable). Today the queue keeps trying.

- `suppression` table: (recipient_address, reason, expires_at, source_message_id)
- Reasons: `hard_bounce`, `complaint` (DMARC abuse report), `manual` (admin-added)
- API: list/clear suppressions per-domain
- Webmail prefers suppressed-aware "to" autocomplete: hide already-suppressed addresses

This is *the* difference between "small mail server that gets blocklisted in 2 weeks" and "mail server that maintains a sane reputation."

### C. Per-domain DKIM key rotation

The model `dkim.go` exists; the API endpoint exists. What's missing:

- A scheduled rotation: every N days, generate a new key with selector `restmail-N+1`, publish to DNS, wait for TTL, switch signing to the new key, retire the old after another TTL.
- Verification reports: did the new selector propagate to my own resolver before I started signing with it?
- Rollback: if signing breaks, switch back to the old selector quickly.

Manual rotation is fine for one domain. With 50 customer domains it's a Sunday afternoon you don't have.

### D. Bounce categorization

Today: bounce = "delivery failed". Tomorrow: bounce = `(category, subcategory, raw_dsn)`. Categories:

- Hard bounce / mailbox does not exist
- Hard bounce / domain does not exist
- Soft bounce / mailbox full
- Soft bounce / temporary failure
- Reputation block (e.g., "blocked by spam filter")
- Rate limit ("too many connections")
- TLS-required ("TLS handshake failed; sender insists on TLS")

The webmail surface for "your message bounced" should show a human-readable reason, not "delivery failed." The queue retry logic should branch on category (don't retry hard bounces; retry rate limits with longer backoff; etc.).

### E. Plus-addressing routing

`bob+filter1@example.com`, `bob+amazon@example.com`. The plus suffix is part of RFC 5233 and customers expect it. The current routing logic appears to be "find mailbox where address = recipient_address", which won't strip the plus. Need:

- Lookup falls back from `bob+amazon@x` to `bob@x` if the former isn't an explicit alias
- The original `+amazon` part is preserved in the message headers (as the "Delivered-To") so Sieve filters can match on it

Once landed, this is one of those features users use without naming. Without it, they'll think the server's broken when their Amazon-only address doesn't work.

### F. ARC reception trust chain

You sign ARC on outbound; you verify ARC on inbound. The next step is *trust* — when a forwarder (mailing list) breaks DKIM, the inbound message still has a valid ARC chain from a trusted forwarder. Currently I see ARC verify in the pipeline but I don't see a "trusted forwarders list" with which to authorize ARC-only-validated messages.

- `trusted_arc_signers` table: (domain, added_by, expires_at)
- Pipeline filter `arc_trust`: if DKIM/SPF fail but ARC has a valid signature from a trusted signer, treat the message as if SPF/DKIM had passed
- Default trusted list: the major mailing-list providers (groups.google.com, lists.google.com, github.com, mailgun.com, etc.)

### G. Sieve script management UI

The model exists, the runtime exists, the API endpoints exist. The webmail UI for editing scripts is the gap. Most users won't write Sieve by hand — they want a "rule builder" UI that compiles down to Sieve.

- Webmail page: visual rule builder ("if subject contains X, move to folder Y")
- Backend translates rules to Sieve and stores both representations
- Power users: raw Sieve editor with syntax highlighting

## Tier 2 — competitive parity

### H. Calendar invites round-trip

You ship `internal/api/handlers/messages.go` with calendar response handling and `webmail/src/components/mail/CalendarInvite.tsx` (which I just fixed a bug in). The next gaps:

- Outbound invitation creation (compose a meeting invite, send to attendees)
- Free/busy availability lookup
- Per-mailbox calendar viewer beyond inline display in messages

The model `calendar.go` suggests this is partially built; the API surface I'd expect (`GET /calendars`, `POST /events`) needs verification.

### I. Contacts (CardDAV-ish or just an API)

`internal/api/handlers/contacts.go` exists. What's missing for parity with Gmail/Outlook:

- "Auto-add contact on first reply": when user replies to a new sender, capture them
- Smart suggestions in the to: field (pulled from previously-emailed addresses)
- VCard import/export
- Possibly CardDAV server if you want native Apple/Outlook contact sync

CardDAV is heavy; the import/export + auto-add are much lower hanging fruit.

### J. Webmail attachment preview

I see attachment APIs but no preview generation. For a mail product:

- Image attachments: thumbnail in message list, full preview inline
- PDF: page-1 thumbnail
- Office docs: convert to PDF for preview (libreoffice in a sidecar — or punt to a third-party preview service)

This is the kind of feature users notice the absence of when they switch from Gmail.

### K. Push notifications

IMAP IDLE works (test stage 13 verifies). Webmail uses SSE (`internal/api/handlers/events.go`). What's missing:

- Mobile push (APNS/FCM) — requires a service worker registration flow + backend per-user push tokens
- Desktop push via Web Push API (similar)

Punt this until you have a mobile app.

### L. Two-factor authentication

`internal/auth/auth.go` has password + JWT. Mature mail systems offer:

- TOTP (Google Authenticator)
- WebAuthn (passkeys) — **growing fast in 2026, worth doing**
- Backup codes
- App-specific passwords (for IMAP clients that don't speak modern OAuth)

The "app-specific passwords" piece is the bridge: it's how you offer 2FA without breaking IMAP/SMTP clients that expect `LOGIN`.

### M. SMTP auth via OAuth (XOAUTH2)

If you go down the WebAuthn / passkey route, you'll want OAuth-based SMTP auth so users don't have to maintain app passwords forever. RFC 7628. Not trivial to implement (requires an OAuth issuer, scopes, refresh-token plumbing) but where the industry's heading.

### N. MIME composition is a known weak point

I see `internal/mime/parser.go` (parsing) but the compose path (in messages.go's various send handlers) does string concatenation:

```go
b.WriteString("Subject: " + req.Subject + "\r\n")
```

This will break on:
- Subject lines with non-ASCII characters (need RFC 2047 encoding)
- Subject lines with line-folding triggers (long subjects)
- HTML bodies that contain `--boundary` accidentally
- Attachments with filename that have special chars

Use `net/mail` + `mime/multipart` (stdlib) properly. Currently the code makes assumptions that will fail in production for international users. **High-value, easy to fix.**

## Tier 3 — features that nobody knows they want until they see them

### O. Send-later / scheduled send

User clicks "send" with a "send at 9am Monday" option. Backed by a scheduled queue table.

- Easy to implement (already have a queue runner; just add a `not_before` column)
- Big perceived value
- ~200 lines of code

### P. Snooze

User opens a message; "snooze until tomorrow morning". Message disappears from inbox, reappears at the chosen time.

- Snooze table: (message_id, reappear_at, original_folder)
- Worker that promotes snoozed→inbox on schedule
- Webmail UI: snooze button in message viewer

Modern mail clients all do this. ~300 lines.

### Q. Undo send

User clicks send; gets a 30-second window to undo before the message actually goes out.

- The web UI delays sending by 30s, optimistically shows "Sent (undo)"
- API exposes `POST /messages/<draft-id>/cancel-send` valid for 30s
- Backend respects the cancellation window before kicking off SMTP

Implementation hint: the queue worker doesn't pick up messages with `not_before > now()`; setting `not_before = now() + 30s` on send and clearing it on cancel-send is the whole feature.

### R. Mute / "this conversation only" filters

User mutes a thread; future messages on the same thread auto-archive. Currently I see threads via `thread_id` on the message model — half the work is done.

- Add `muted_threads` table
- Pipeline filter `mute_check` on inbound: if message's thread_id is muted, skip inbox
- Webmail "mute this thread" action

### S. Smart unsubscribe (List-Unsubscribe header)

When the inbound message has a `List-Unsubscribe` header, surface a "unsubscribe" button in the webmail UI that:

- Sends the unsubscribe email (mailto:) on the user's behalf, OR
- POSTs to the unsubscribe URL (https:) per RFC 8058

Adds a nice quality-of-life feature without much code. Modern Gmail / Apple Mail do this.

### T. Auto-tagging by sender pattern

Beyond Sieve scripts: a managed "type" classifier that auto-assigns labels.

- "shipping notification" detection (Amazon, FedEx, UPS senders)
- "newsletter" detection (List-Id present, marketing-style HTML)
- "important from a contact" surfacing

Maybe ML-shaped but a heuristic version (regex on sender + headers) gets you 70% of the value.

## Tier 4 — operational features (not user-facing)

### U. Domain-level pipeline templating

A new domain shouldn't need a full pipeline config from scratch. Provide:

- "starter" pipeline templates (basic, strict, lenient)
- "extends" syntax in a domain's pipeline config to inherit from a template

### V. Shared blocklists

Multiple domains in one deployment likely share threat intelligence. A platform admin should be able to define blocklists at the platform level that all domains inherit.

### W. Vacation responses with smart deduplication

`internal/pipeline/filters/vacation.go` exists. Make sure it doesn't:
- Reply to mailing lists (List-Id header set)
- Reply to no-reply senders
- Reply more than once per N days to the same sender
- Reply to its own replies (loop detection)

Standard vacation-responder stuff; some libraries get this wrong.

### X. Bulk import / migration tools

A new customer migrating *to* the platform from another provider:

- IMAP migration tool: log into source IMAP, pull all messages, push to RESTMAIL via API
- Mbox import
- Maildir import
- PST/Outlook import (rough, requires a parser library)

The IMAP one is the highest-value; many providers offer it as a paid service.

### Y. Bulk export

Required for GDPR-style "give me my data". Per-mailbox export to a tarball of mbox + attachments + metadata.

### Z. Data retention policies

Per-domain, per-folder retention rules: "delete trash after 30 days, spam after 90 days, leave inbox forever." Currently I don't see retention enforcement in the codebase.

## Per-feature notes on what's already there

Spotted while writing this — features that are *already* present (full or partial) and worth highlighting:

- **MTA-STS**: handler exists (`internal/api/handlers/mtasts.go`). Great — most servers don't bother. **But** the handler hardcodes mail3.test ([02-PARAMETERIZATION.md](02-PARAMETERIZATION.md)).
- **TLS-RPT**: model exists (`internal/db/models/tlsrpt.go`). I didn't trace whether the report-receiving endpoint is wired up. If it is — kudos. If not — easy to finish.
- **Quota**: model + API + IMAP GETQUOTA all in place. Solid.
- **Aliases**: handler + model exist. Make sure plus-addressing (Tier 1 E) works alongside.
- **Sender rules**: per-account "always send from this name/address" — present.
- **Activity log**: model exists. Verify it's actually being written from auth events, admin actions, etc.

## What I'd cut from "we should add this"

A few things that look like obvious features but actually cost more than they pay:

- **Custom HTML email signatures with images**: The image-hosting story (CID? remote? proxy?) is a rabbit hole.
- **Threaded replies via References / In-Reply-To**: stdlib MIME parsing supports it, threading UIs are fiddly. Worth doing eventually but not soon.
- **Per-message encryption (S/MIME, PGP)**: The audience for "I want to use PGP through a webmail" is approximately zero. Punt.
- **Calendar full implementation (CalDAV)**: heavy lift; stick with iCal invitation handling for now.
- **Mailing list management**: Mailman is a project. Don't implement a Mailman-clone.
