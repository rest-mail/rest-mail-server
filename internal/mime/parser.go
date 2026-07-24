// Package mime provides RFC 2822 email parsing and serialization.
// It converts between raw email messages and the pipeline's EmailJSON format.
//
// MIME envelope and body parsing are delegated to github.com/emersion/go-message,
// which handles RFC 2047 encoded-words, charset conversion, transfer-encoding
// decoding, and nested multipart structures. iCalendar (text/calendar) parsing
// is not part of go-message and remains in icalendar.go.
package mime

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"strings"

	gomessage "github.com/emersion/go-message"
	// Registers a charset reader so go-message decodes non-UTF-8 text parts.
	_ "github.com/emersion/go-message/charset"
	"github.com/emersion/go-message/mail"

	"github.com/restmail/restmail/internal/pipeline"
)

// Parse converts a raw RFC 2822 message into the pipeline's EmailJSON format.
func Parse(raw []byte) (*pipeline.EmailJSON, error) {
	// go-message returns a usable *Entity even when it reports an unknown charset
	// or transfer encoding, so only a nil entity is a hard failure.
	entity, err := gomessage.Read(bytes.NewReader(raw))
	if entity == nil {
		return nil, fmt.Errorf("parse message: %w", err)
	}

	email := &pipeline.EmailJSON{
		Headers: parseHeaders(entity.Header),
	}

	body, attachments, inline, calendarEvents := parseEntity(entity)
	email.Body = body
	email.Attachments = attachments
	email.Inline = inline
	email.CalendarEvents = calendarEvents

	return email, nil
}

func parseHeaders(h gomessage.Header) pipeline.Headers {
	// Wrap the raw header to reuse go-message's RFC 5322 address and
	// encoded-word aware helpers.
	mh := mail.Header{Header: h}

	headers := pipeline.Headers{
		Date:      h.Get("Date"),
		MessageID: h.Get("Message-Id"),
		InReplyTo: h.Get("In-Reply-To"),
		Raw:       make(map[string][]string),
	}

	// Subject returns the RFC 2047-decoded value (or the raw value on error).
	// Sanitize it (and every other decoded scalar header) so an encoded-word that
	// decodes to bytes containing CR/LF cannot inject a header on re-serialization.
	subject, _ := mh.Subject()
	headers.Subject = sanitizeHeaderValue(subject)
	headers.Date = sanitizeHeaderValue(headers.Date)
	headers.MessageID = sanitizeHeaderValue(headers.MessageID)
	headers.InReplyTo = sanitizeHeaderValue(headers.InReplyTo)

	headers.From = parseAddressList(&mh, "From")
	headers.To = parseAddressList(&mh, "To")
	headers.Cc = parseAddressList(&mh, "Cc")

	// References is kept as the raw whitespace-separated list of message-ids
	// (angle brackets preserved), matching the historical output contract.
	if refs := h.Get("References"); refs != "" {
		headers.References = append(headers.References, strings.Fields(refs)...)
	}

	// Preserve all raw headers under their canonical MIME keys. go-message
	// returns the undecoded (RFC 2047-encoded) values, exactly as net/mail did,
	// which downstream DKIM/ARC/Authentication-Results logic relies on.
	//
	// EXCEPT inbound Authentication-Results: these are attacker-controlled on an
	// untrusted inbound message and MUST NOT be trusted as this server's own
	// authentication verdict. dmarc_check reads Headers.Raw["Authentication-Results"]
	// (and Extra) to decide SPF/DKIM alignment, so a forged
	// "Authentication-Results: x; spf=pass …; dkim=pass …" would otherwise bypass a
	// p=reject/quarantine policy. The local spf_check/dkim_verify/arc_verify filters
	// append their genuine verdict to this same key later in the pipeline, so
	// rehoming the inbound copy to X-Original-Authentication-Results leaves only
	// server-produced results under the trusted key while preserving the original
	// for audit. ARC-* headers are cryptographically verified by arc_verify and are
	// left untouched.
	fields := h.Fields()
	for fields.Next() {
		key := fields.Key()
		if strings.EqualFold(key, "Authentication-Results") {
			key = "X-Original-Authentication-Results"
		}
		headers.Raw[key] = append(headers.Raw[key], fields.Value())
	}

	return headers
}

func parseAddressList(h *mail.Header, key string) []pipeline.Address {
	list, err := h.AddressList(key)
	if err != nil {
		return nil
	}
	var addrs []pipeline.Address
	for _, addr := range list {
		// addr.Name is RFC 2047-decoded; strip any embedded CR/LF so a crafted
		// display name cannot inject a header when the address is re-serialized.
		addrs = append(addrs, pipeline.Address{
			Name:    sanitizeHeaderValue(addr.Name),
			Address: sanitizeHeaderValue(addr.Address),
		})
	}
	return addrs
}

// sanitizeHeaderValue removes CR and LF from a decoded header value. RFC 2047
// encoded-words can decode to arbitrary bytes, including CR/LF; if such a value is
// later re-serialized into a header (forward/reply/bounce/Sieve) the embedded line
// break would start a new header line, enabling header injection (e.g. a smuggled
// Bcc). Stripping the line breaks neutralizes the injection while preserving the
// remaining visible text. (OSI-23)
func sanitizeHeaderValue(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return headerLineBreakStripper.Replace(s)
}

var headerLineBreakStripper = strings.NewReplacer("\r", "", "\n", "")

// parseEntity converts a go-message entity (whole message or a single part)
// into the body tree, attachments, inline parts, and calendar events.
func parseEntity(e *gomessage.Entity) (pipeline.Body, []pipeline.Attachment, []pipeline.Attachment, []pipeline.CalendarEvent) {
	mediaType, _, _ := e.Header.ContentType()
	mediaType = strings.ToLower(mediaType)

	if mr := e.MultipartReader(); mr != nil {
		return parseMultipart(mr, mediaType)
	}

	// Single-part body. go-message has already decoded the transfer encoding
	// and (for text/*) converted the charset to UTF-8.
	content, _ := io.ReadAll(e.Body)
	body := pipeline.Body{
		ContentType: mediaType,
		Content:     string(content),
	}

	var calEvents []pipeline.CalendarEvent
	if mediaType == "text/calendar" {
		if events, err := ParseCalendar(string(content)); err == nil && len(events) > 0 {
			calEvents = events
		}
	}

	return body, nil, nil, calEvents
}

func parseMultipart(mr gomessage.MultipartReader, mediaType string) (pipeline.Body, []pipeline.Attachment, []pipeline.Attachment, []pipeline.CalendarEvent) {
	body := pipeline.Body{ContentType: mediaType}
	var attachments, inline []pipeline.Attachment
	var calendarEvents []pipeline.CalendarEvent

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Unknown charset/encoding still yields a readable part; anything
			// else is a malformed boundary — stop reading further parts.
			if !gomessage.IsUnknownCharset(err) && !gomessage.IsUnknownEncoding(err) {
				break
			}
		}

		partMediaType, _, _ := part.Header.ContentType()
		partMediaType = strings.ToLower(partMediaType)

		// Recurse into nested multiparts (e.g. multipart/alternative inside
		// multipart/mixed), preserving the body tree.
		if pmr := part.MultipartReader(); pmr != nil {
			subBody, subAtt, subInl, subCal := parseMultipart(pmr, partMediaType)
			body.Parts = append(body.Parts, subBody)
			attachments = append(attachments, subAtt...)
			inline = append(inline, subInl...)
			calendarEvents = append(calendarEvents, subCal...)
			continue
		}

		content, err := io.ReadAll(part.Body)
		if err != nil {
			continue
		}

		disposition, _, _ := part.Header.ContentDisposition()
		disposition = strings.ToLower(disposition)
		filename := partFilename(part.Header)

		// Calendar invite: parse events regardless of disposition, and also
		// keep the raw .ics content available as a body part.
		if partMediaType == "text/calendar" {
			if events, parseErr := ParseCalendar(string(content)); parseErr == nil && len(events) > 0 {
				calendarEvents = append(calendarEvents, events...)
			}
			body.Parts = append(body.Parts, pipeline.Body{
				ContentType: partMediaType,
				Content:     string(content),
			})
			continue
		}

		// Inline part (e.g. an embedded image referenced by Content-ID). This is
		// checked before the attachment case so an explicitly inline part with a
		// Content-ID is not misfiled as an attachment when it also has a filename
		// (which would drop the Content-ID needed for cid: rendering).
		contentID := part.Header.Get("Content-Id")
		if disposition == "inline" && contentID != "" {
			inline = append(inline, pipeline.Attachment{
				Filename:    filename,
				ContentType: partMediaType,
				Size:        int64(len(content)),
				Disposition: "inline",
				ContentID:   contentID,
				Content:     base64.StdEncoding.EncodeToString(content),
			})
			continue
		}

		// Attachment.
		if disposition == "attachment" || (filename != "" && !strings.HasPrefix(partMediaType, "text/")) {
			attachments = append(attachments, pipeline.Attachment{
				Filename:    filename,
				ContentType: partMediaType,
				Size:        int64(len(content)),
				Disposition: "attachment",
				Content:     base64.StdEncoding.EncodeToString(content),
			})
			continue
		}

		// Regular body part.
		body.Parts = append(body.Parts, pipeline.Body{
			ContentType: partMediaType,
			Content:     string(content),
		})
	}

	return body, attachments, inline, calendarEvents
}

// partFilename returns the part's filename, preferring the Content-Disposition
// "filename" parameter and falling back to the Content-Type "name" parameter.
// go-message decodes any RFC 2047 encoded-words in the parameter values.
func partFilename(h gomessage.Header) string {
	// Both the Content-Disposition "filename" and Content-Type "name" parameters are
	// RFC 2047-decoded by go-message, so sanitize them: a filename that decodes to a
	// value containing CR/LF must not be able to inject a header when the part is
	// re-serialized. (OSI-23)
	if _, params, err := h.ContentDisposition(); err == nil {
		if fn := params["filename"]; fn != "" {
			return sanitizeHeaderValue(fn)
		}
	}
	if _, params, err := h.ContentType(); err == nil {
		if fn := params["name"]; fn != "" {
			return sanitizeHeaderValue(fn)
		}
	}
	return ""
}
