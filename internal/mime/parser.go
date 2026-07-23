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
	subject, _ := mh.Subject()
	headers.Subject = subject

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
	fields := h.Fields()
	for fields.Next() {
		key := fields.Key()
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
		addrs = append(addrs, pipeline.Address{
			Name:    addr.Name,
			Address: addr.Address,
		})
	}
	return addrs
}

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
	if _, params, err := h.ContentDisposition(); err == nil {
		if fn := params["filename"]; fn != "" {
			return fn
		}
	}
	if _, params, err := h.ContentType(); err == nil {
		if fn := params["name"]; fn != "" {
			return fn
		}
	}
	return ""
}
