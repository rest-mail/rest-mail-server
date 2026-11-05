// Package outbound holds the transforms every outbound message must undergo
// before it is relayed to a remote MX: Bcc-header removal (so Bcc recipients are
// never disclosed to the other destinations) and DKIM signing (so the message
// authenticates at receivers for hosted domains).
//
// These transforms previously ran only on the webmail/API send path, which
// rebuilds the message from structured fields. SMTP-submitted mail (465)
// enqueued its raw MUA bytes verbatim and the queue worker relayed them as-is —
// unsigned and with any MUA-supplied Bcc header intact (#171). Centralizing the
// transforms here lets the queue worker apply them to EVERY queued message,
// regardless of how it was submitted, while the API path reuses SignDKIM so the
// signing logic is not duplicated.
package outbound

import (
	"context"
	"crypto"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	dkim "github.com/rest-mail/go-dkim/pkg/dkim"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
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
// An error is returned ONLY when the domain has a key that cannot sign (see
// SignDKIM); the caller must fail closed (defer/retry) rather than relay the
// message unsigned. A domain with no key configured is not an error — the
// message relays unsigned.
func Prepare(ctx context.Context, db *gorm.DB, masterKey, senderDomain, raw string) (string, error) {
	raw = StripBcc(raw)
	if HasDKIMSignature(raw) {
		// Already signed upstream (API path). Re-signing would add a redundant
		// second signature, and re-canonicalizing could disturb the existing one,
		// so pass it through unchanged.
		return raw, nil
	}
	return SignDKIM(ctx, db, masterKey, senderDomain, raw)
}

// StripBcc removes every Bcc header field (and its folded continuation lines) from
// a raw RFC 5322 message so Bcc recipients are not disclosed to destination MXs.
// The Bcc recipients still receive the message — they are delivered via the
// envelope; only the header is withheld. rmail.HeadFields says what is in the head,
// for any mix of line ends.
//
// When the message carries no Bcc header the input is returned byte-for-byte
// unchanged, so a message that was already DKIM-signed upstream is never
// disturbed.
func StripBcc(raw string) string {
	return rmail.RemoveHeadField(raw, "Bcc")
}

// HasDKIMSignature reports whether the raw message already carries a
// DKIM-Signature header field, so the outbound path can avoid adding a second
// signature to mail that was signed at submission (the API path).
func HasDKIMSignature(raw string) bool {
	for field := range rmail.HeadFields(raw) {
		if field.Is("DKIM-Signature") {
			return true
		}
	}
	return false
}

// TLSOptional reports whether a message carries "TLS-Required: No": its
// sender asks that the recipient domain's TLS policy (MTA-STS, DANE) be
// ignored for it, while STARTTLS is still attempted (RFC 8689 §3, §4.2.2).
// The field may appear only once, so a message with more than one, or with
// any other value, keeps the policy. The name and "No" match in any case
// (RFC 5234 §2.3), and the value may be folded.
func TLSOptional(raw string) bool {
	var values []string
	for field := range rmail.HeadFields(raw) {
		if field.Is("TLS-Required") {
			values = append(values, field.Value())
		}
	}
	return len(values) == 1 && strings.EqualFold(values[0], "No")
}

// signedHeaders is the h= set SignDKIM signs: go-dkim's default set, the
// RFC 8058 one-click field, so a one-click claim is covered alongside the
// List-Unsubscribe target it applies to, and the TLS-RPT report fields, which
// RFC 8460 §5.3 says a report's signature should cover. A field a message
// lacks is not signed.
var signedHeaders = append(append([]string{}, dkim.DefaultSignedHeaders...),
	"list-unsubscribe-post", "tls-report-domain", "tls-report-submitter")

// HasDKIMKey reports whether SignDKIM would sign mail from senderDomain: the
// domain is hosted here with a selector and a key configured. Whether the key
// loads is not checked; SignDKIM fails closed when it does not.
func HasDKIMKey(db *gorm.DB, senderDomain string) (bool, error) {
	if db == nil || senderDomain == "" {
		return false, nil
	}
	var domain models.Domain
	err := db.Where("name = ?", senderDomain).First(&domain).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return domain.DKIMPrivateKey != "" && domain.DKIMSelector != "", nil
}

// SignDKIM prepends a DKIM-Signature computed over the ACTUAL raw bytes with the
// sender domain's key, returning the signed message. DKIM must be signed over
// what is transmitted, so this operates on the raw message directly rather than
// a reconstructed representation. It is the only DKIM signer: the API send path,
// SMTP submission and the queue worker all reach it.
//
// A domain with no key configured is not an error — the message is returned
// unchanged. A domain WITH a key either signs or fails CLOSED: a key that cannot
// be loaded (wrong or missing MASTER_KEY, corrupt ciphertext), parsed or used
// returns an error, so the caller temp-fails instead of relaying unsigned.
//
// A nil db or empty senderDomain returns the message unchanged — there is
// nothing to look a key up with.
func SignDKIM(ctx context.Context, db *gorm.DB, masterKey, senderDomain, raw string) (string, error) {
	domain, priv, asciiDomain, err := domainKey(ctx, db, masterKey, senderDomain, "dkim sign")
	if err != nil {
		return "", err
	}
	if priv == nil {
		return raw, nil
	}
	// go-dkim's defaults sign the RFC 6376 §5.4.1 set and oversign the fields
	// a reader sees, so a From, Subject or Reply-To added after signing breaks
	// the signature (#128). Naming Headers switches the defaults off, so both
	// lists are given in full: the defaults plus List-Unsubscribe-Post, which
	// RFC 8058 §4 requires signed and RFC 6376 predates (#144). A field the
	// message lacks is not named, so plain mail signs exactly as before.
	val, err := dkim.Sign([]byte(raw), dkim.SignOptions{
		Domain:     asciiDomain,
		Selector:   domain.DKIMSelector,
		PrivateKey: priv,
		Time:       time.Now().Unix(),
		Headers:    signedHeaders,
		Oversign:   dkim.DefaultOversignedHeaders,
	})
	if err != nil {
		slog.ErrorContext(ctx, "outbound dkim sign failed (fail-closed)", "domain", senderDomain, "error", err)
		return "", fmt.Errorf("dkim: sign for %s: %w", senderDomain, err)
	}
	return "DKIM-Signature: " + val + "\r\n" + raw, nil
}

// domainKey loads the key a hosted domain signs its mail with, and the domain's A-label
// name, the signing identity (RFC 8616 §5). A domain that is not hosted here or has no key
// gives a nil key and no error: there is nothing to sign with. A key that cannot be loaded
// (wrong or missing MASTER_KEY, corrupt ciphertext) or parsed is an error, so the caller
// fails closed. what names the signing in the log.
func domainKey(ctx context.Context, db *gorm.DB, masterKey, senderDomain, what string) (models.Domain, crypto.Signer, string, error) {
	var domain models.Domain
	if db == nil || senderDomain == "" {
		return domain, nil, "", nil
	}
	if err := db.Where("name = ?", senderDomain).First(&domain).Error; err != nil ||
		domain.DKIMPrivateKey == "" || domain.DKIMSelector == "" {
		return domain, nil, "", nil
	}
	keyPEM, err := models.LoadDKIMPrivateKey(domain.DKIMPrivateKey, masterKey)
	if err != nil {
		slog.ErrorContext(ctx, "outbound "+what+": key load failed (fail-closed)", "domain", senderDomain, "error", err)
		return domain, nil, "", err
	}
	priv, err := ParseDKIMKey(keyPEM)
	if err != nil {
		slog.ErrorContext(ctx, "outbound "+what+": parse key failed (fail-closed)", "domain", senderDomain, "error", err)
		return domain, nil, "", fmt.Errorf("dkim: the key for %s %w", senderDomain, err)
	}
	// Use an A-label signing identity, while hashing the exact original UTF-8 header
	// and body bytes. Unicode domains stay in storage.
	asciiDomain, err := rmail.DomainASCII(domain.Name)
	if err != nil {
		return domain, nil, "", fmt.Errorf("dkim: sign for %s: %w", senderDomain, err)
	}
	return domain, priv, asciiDomain, nil
}
