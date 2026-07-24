// Package outbound holds the transforms every outbound message must undergo
// before it is relayed to a remote MX: Bcc-header removal (so Bcc recipients are
// never disclosed to the other destinations) and DKIM signing (so the message
// authenticates at receivers for hosted domains).
//
// These transforms previously ran only on the webmail/API send path, which
// rebuilds the message from structured fields. SMTP-submitted mail (587/465)
// enqueued its raw MUA bytes verbatim and the queue worker relayed them as-is —
// unsigned and with any MUA-supplied Bcc header intact (#171). Centralizing the
// transforms here lets the queue worker apply them to EVERY queued message,
// regardless of how it was submitted, while the API path reuses SignDKIM so the
// signing logic is not duplicated.
package outbound

import (
	"log/slog"
	"strings"
	"time"

	dkim "github.com/rest-mail/go-dkim"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// Prepare applies the outbound transforms to a raw RFC 5322 message before it is
// relayed: it strips the Bcc header, then DKIM-signs the result when the sender
// domain has a key configured and the message is not already signed.
//
// It is idempotent, which is what makes it safe to run in the shared queue
// worker over messages from both submission paths:
//   - API-originated mail is already Bcc-free and DKIM-signed at submission, so
//     Prepare leaves it untouched (StripBcc is a no-op, HasDKIMSignature short-
//     circuits the signing).
//   - SMTP-submitted mail arrives raw, so Prepare removes any Bcc header and
//     signs it here.
//
// An error is returned ONLY when the domain has a key that cannot be loaded
// (e.g. an at-rest ciphertext that fails to decrypt); the caller must fail
// closed (defer/retry) rather than relay the message unsigned. A domain with no
// key configured is not an error — the message relays unsigned, exactly as
// before.
func Prepare(db *gorm.DB, masterKey, senderDomain, raw string) (string, error) {
	raw = StripBcc(raw)
	if HasDKIMSignature(raw) {
		// Already signed upstream (API path). Re-signing would add a redundant
		// second signature, and re-canonicalizing could disturb the existing one,
		// so pass it through unchanged.
		return raw, nil
	}
	return SignDKIM(db, masterKey, senderDomain, raw)
}

// headerBodySplit returns the index just past the end of the header block (the
// position where the body begins) and the line terminator in use ("\r\n" or
// "\n"). When no blank-line separator is found the whole input is treated as
// headers. The message is assumed to use a single, consistent terminator, as
// RFC 5322 / SMTP DATA requires (CRLF); a bare-LF message is still handled.
func headerBodySplit(raw string) (bodyStart int, eol string) {
	if i := strings.Index(raw, "\r\n\r\n"); i >= 0 {
		return i + 4, "\r\n"
	}
	if i := strings.Index(raw, "\n\n"); i >= 0 {
		return i + 2, "\n"
	}
	return len(raw), "\r\n"
}

// StripBcc removes the Bcc header field (and any folded continuation lines) from
// a raw RFC 5322 message so Bcc recipients are not disclosed to destination MXs.
// The Bcc recipients still receive the message — they are delivered via the
// envelope; only the header is withheld.
//
// When the message carries no Bcc header the input is returned byte-for-byte
// unchanged, so a message that was already DKIM-signed upstream is never
// disturbed.
func StripBcc(raw string) string {
	bodyStart, eol := headerBodySplit(raw)
	header := raw[:bodyStart]
	body := raw[bodyStart:]

	lines := strings.Split(header, eol)
	out := make([]string, 0, len(lines))
	removed := false
	skipping := false
	for _, line := range lines {
		// A folded continuation line (leading space/tab) belongs to the previous
		// header field; drop it while we are skipping a Bcc field.
		if skipping && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			continue
		}
		skipping = false
		if hasHeaderName(line, "Bcc") {
			skipping = true
			removed = true
			continue
		}
		out = append(out, line)
	}
	if !removed {
		return raw
	}
	return strings.Join(out, eol) + body
}

// hasHeaderName reports whether line begins a header field with the given name
// (case-insensitive), i.e. "Name:" optionally preceded by no whitespace.
func hasHeaderName(line, name string) bool {
	colon := strings.IndexByte(line, ':')
	if colon < 0 {
		return false
	}
	return strings.EqualFold(strings.TrimRight(line[:colon], " \t"), name)
}

// HasDKIMSignature reports whether the raw message already carries a
// DKIM-Signature header field, so the outbound path can avoid adding a second
// signature to mail that was signed at submission (the API path).
func HasDKIMSignature(raw string) bool {
	bodyStart, eol := headerBodySplit(raw)
	for _, line := range strings.Split(raw[:bodyStart], eol) {
		if hasHeaderName(line, "DKIM-Signature") {
			return true
		}
	}
	return false
}

// SignDKIM prepends a DKIM-Signature computed over the ACTUAL raw bytes with the
// sender domain's key, returning the signed message. DKIM must be signed over
// what is transmitted, so this operates on the raw message directly rather than
// a reconstructed representation.
//
// If the domain has no key configured (or the key parses/signs poorly) the
// message is returned unchanged. If the domain HAS a key but it cannot be loaded
// (an encrypted-at-rest key that fails to decrypt), an error is returned so the
// caller fails closed (temp-fail) instead of relaying the message unsigned.
//
// A nil db or empty senderDomain returns the message unchanged — there is
// nothing to look a key up with.
func SignDKIM(db *gorm.DB, masterKey, senderDomain, raw string) (string, error) {
	if db == nil || senderDomain == "" {
		return raw, nil
	}
	var domain models.Domain
	if err := db.Where("name = ?", senderDomain).First(&domain).Error; err != nil ||
		domain.DKIMPrivateKey == "" || domain.DKIMSelector == "" {
		return raw, nil
	}
	// Decrypt the at-rest key, failing CLOSED. An encrypted key that cannot be
	// decrypted (wrong/missing master key or corrupt ciphertext) is a
	// transient/config fault — surface it so the send temp-fails instead of
	// silently going out unsigned. A legacy plaintext key loads as-is.
	keyPEM, err := models.LoadDKIMPrivateKey(domain.DKIMPrivateKey, masterKey)
	if err != nil {
		slog.Error("outbound dkim sign: key load failed (fail-closed)", "domain", senderDomain, "error", err)
		return "", err
	}
	priv, err := dkim.ParsePrivateKey(keyPEM)
	if err != nil {
		slog.Warn("outbound dkim sign: parse key failed", "domain", senderDomain, "error", err)
		return raw, nil
	}
	val, err := dkim.Sign([]byte(raw), dkim.SignOptions{
		Domain:     senderDomain,
		Selector:   domain.DKIMSelector,
		PrivateKey: priv,
		Time:       time.Now().Unix(),
	})
	if err != nil {
		slog.Warn("outbound dkim sign failed", "domain", senderDomain, "error", err)
		return raw, nil
	}
	return "DKIM-Signature: " + val + "\r\n" + raw, nil
}
