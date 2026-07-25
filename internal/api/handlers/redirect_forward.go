package handlers

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/restmail/restmail/internal/db/models"
	rmail "github.com/restmail/restmail/internal/mail"
	"github.com/restmail/restmail/internal/pipeline"
)

// The inbound Sieve filter records `redirect` targets in the redirect_to metadata
// (a JSON array) and, for a bare redirect that leaves no local copy, sets
// redirect_suppress_keep. The delivery path is the consumer: it forwards the
// original message to each target via the outbound queue (RFC 5228 §4.2), and
// when the keep was cancelled it stores no local copy.

// redirectMetaTargets is the metadata key the sieve filter writes redirect
// targets to (JSON array of addresses).
const redirectMetaTargets = "redirect_to"

// redirectMetaSuppressKeep is set by the sieve filter when a bare redirect
// cancelled the implicit keep, so the delivery path forwards WITHOUT also storing
// a local copy (RFC 5228 §4.2). A `redirect :copy` does not set it.
const redirectMetaSuppressKeep = "redirect_suppress_keep"

// redirectSuppressesKeep reports whether the Sieve evaluation forwarded the
// message with no local copy, so the delivery path must not store it to a mailbox.
func redirectSuppressesKeep(finalEmail *pipeline.EmailJSON) bool {
	return finalEmail != nil && finalEmail.Metadata != nil &&
		finalEmail.Metadata[redirectMetaSuppressKeep] == "true"
}

// buildRedirectForwards turns the Sieve redirect targets recorded on finalEmail
// into outbound_queue rows that forward the ORIGINAL message onward, one per
// target. It reuses the outbound queue the SMTP/vacation paths use — the queue
// worker applies the standard outbound transforms (Bcc strip, DKIM) and relays.
//
// Semantics (RFC 5228 §4.2, like a .forward):
//   - The envelope sender is preserved (the original sender), so a DSN for a
//     failed forward returns to the original sender, not the mailbox owner. When
//     the original sender is empty (a null reverse-path bounce) it is preserved.
//   - Each forwarded copy gains a Delivered-To header for its target, both to
//     record the delivery and so a downstream hop can detect a longer loop.
//   - Loop suppression: a target already in the message's Delivered-To chain, or
//     among the current envelope recipients (a self-redirect), is skipped — a
//     second line of defense; the sieve filter already suppresses these, but the
//     locally-stamped Delivered-To (added at storage time) is only visible here.
//
// It performs no I/O; the caller persists the returned rows. Returns nil when
// there is nothing to forward.
func buildRedirectForwards(finalEmail *pipeline.EmailJSON, originalRaw, sender string, recipients []string) []models.OutboundQueue {
	if finalEmail == nil || finalEmail.Metadata == nil {
		return nil
	}
	// Without the original bytes there is nothing to forward. Returning nil here
	// (and gating the keep suppression on a forward actually being produced) means
	// a redirect that cannot be honoured falls back to keeping the message locally
	// rather than losing it.
	if strings.TrimSpace(originalRaw) == "" {
		return nil
	}
	targets := decodeRedirectTargets(finalEmail.Metadata[redirectMetaTargets])
	if len(targets) == 0 {
		return nil
	}

	seen := make(map[string]struct{})
	for _, dt := range deliveredToChain(originalRaw) {
		seen[normalizeRedirectAddr(dt)] = struct{}{}
	}
	for _, r := range recipients {
		seen[normalizeRedirectAddr(r)] = struct{}{}
	}

	var forwards []models.OutboundQueue
	emitted := make(map[string]struct{})
	for _, target := range targets {
		key := normalizeRedirectAddr(target)
		if key == "" {
			continue
		}
		if _, dup := emitted[key]; dup {
			continue
		}
		if _, loop := seen[key]; loop {
			slog.Warn("delivery: sieve redirect suppressed to avoid a mail loop",
				"target", target)
			continue
		}
		emitted[key] = struct{}{}

		domain := rmail.DomainFromAddress(target)
		if domain == "" {
			continue
		}
		forwardedRaw := originalRaw
		if forwardedRaw != "" {
			forwardedRaw = "Delivered-To: " + target + "\r\n" + forwardedRaw
		}
		forwards = append(forwards, models.OutboundQueue{
			Sender:     sender,
			Recipient:  target,
			Domain:     domain,
			RawMessage: forwardedRaw,
			Status:     "pending",
		})
	}
	return forwards
}

// decodeRedirectTargets parses the JSON-array redirect_to metadata. A malformed
// or empty value yields no targets.
func decodeRedirectTargets(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// deliveredToChain returns every Delivered-To address in the raw message's header
// block (case-insensitive), used to detect a forwarding loop.
func deliveredToChain(raw string) []string {
	if raw == "" {
		return nil
	}
	// Only scan the header block (up to the first blank line).
	if i := strings.Index(raw, "\r\n\r\n"); i >= 0 {
		raw = raw[:i]
	} else if i := strings.Index(raw, "\n\n"); i >= 0 {
		raw = raw[:i]
	}
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		// Header field, not a folded continuation line.
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(line[:colon]), "Delivered-To") {
			out = append(out, strings.TrimSpace(line[colon+1:]))
		}
	}
	return out
}

// normalizeRedirectAddr lower-cases an address and strips surrounding angle
// brackets and whitespace, so loop comparisons ignore formatting differences.
func normalizeRedirectAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	addr = strings.TrimPrefix(addr, "<")
	addr = strings.TrimSuffix(addr, ">")
	return strings.ToLower(strings.TrimSpace(addr))
}
