// Package mime provides RFC 2822 email parsing and serialization.
// It converts between raw email messages and the pipeline's EmailJSON format.
package mime

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"

	"github.com/restmail/restmail/internal/pipeline"
)

// Parse converts a raw RFC 2822 message into the pipeline's EmailJSON format.
func Parse(raw []byte) (*pipeline.EmailJSON, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse message: %w", err)
	}

	email := &pipeline.EmailJSON{
		Headers: parseHeaders(msg.Header),
	}

	// Parse body
	contentType := msg.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "text/plain"
	}

	body, attachments, inline, calendarEvents, err := parseBody(msg.Body, contentType)
	if err != nil {
		return nil, fmt.Errorf("parse body: %w", err)
	}

	email.Body = body
	email.Attachments = attachments
	email.Inline = inline
	email.CalendarEvents = calendarEvents

	return email, nil
}

func parseHeaders(h mail.Header) pipeline.Headers {
	headers := pipeline.Headers{
		Subject:   decodeHeader(h.Get("Subject")),
		Date:      h.Get("Date"),
		MessageID: h.Get("Message-Id"),
		InReplyTo: h.Get("In-Reply-To"),
		Raw:       make(map[string][]string),
	}

	// Parse From addresses
	if fromList, err := h.AddressList("From"); err == nil {
		for _, addr := range fromList {
			headers.From = append(headers.From, pipeline.Address{
				Name:    addr.Name,
				Address: addr.Address,
			})
		}
	}

	// Parse To addresses
	if toList, err := h.AddressList("To"); err == nil {
		for _, addr := range toList {
			headers.To = append(headers.To, pipeline.Address{
				Name:    addr.Name,
				Address: addr.Address,
			})
		}
	}

	// Parse Cc addresses
	if ccList, err := h.AddressList("Cc"); err == nil {
		for _, addr := range ccList {
			headers.Cc = append(headers.Cc, pipeline.Address{
				Name:    addr.Name,
				Address: addr.Address,
			})
		}
	}

	// Parse References
	if refs := h.Get("References"); refs != "" {
		headers.References = append(headers.References, strings.Fields(refs)...)
	}

	// Preserve all raw headers
	for key, values := range h {
		headers.Raw[key] = values
	}

	return headers
}

func parseBody(reader io.Reader, contentType string) (pipeline.Body, []pipeline.Attachment, []pipeline.Attachment, []pipeline.CalendarEvent, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		// Fallback: treat as plain text
		bodyBytes, _ := io.ReadAll(reader)
		return pipeline.Body{
			ContentType: "text/plain",
			Content:     string(bodyBytes),
		}, nil, nil, nil, nil
	}

	// Handle multipart
	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			bodyBytes, _ := io.ReadAll(reader)
			return pipeline.Body{
				ContentType: mediaType,
				Content:     string(bodyBytes),
			}, nil, nil, nil, nil
		}
		return parseMultipart(reader, mediaType, boundary)
	}

	// Single-part body
	bodyBytes, err := io.ReadAll(reader)
	if err != nil {
		return pipeline.Body{}, nil, nil, nil, err
	}

	// Check if this single-part message is a calendar invite
	var calEvents []pipeline.CalendarEvent
	if mediaType == "text/calendar" {
		if events, parseErr := ParseCalendar(string(bodyBytes)); parseErr == nil && len(events) > 0 {
			calEvents = events
		}
	}

	return pipeline.Body{
		ContentType: mediaType,
		Content:     string(bodyBytes),
	}, nil, nil, calEvents, nil
}

func parseMultipart(reader io.Reader, mediaType, boundary string) (pipeline.Body, []pipeline.Attachment, []pipeline.Attachment, []pipeline.CalendarEvent, error) {
	mr := multipart.NewReader(reader, boundary)
	body := pipeline.Body{
		ContentType: mediaType,
	}
	var attachments, inline []pipeline.Attachment
	var calendarEvents []pipeline.CalendarEvent

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return body, attachments, inline, calendarEvents, fmt.Errorf("read part: %w", err)
		}

		partCT := part.Header.Get("Content-Type")
		if partCT == "" {
			partCT = "text/plain"
		}
		partMediaType, partParams, _ := mime.ParseMediaType(partCT)

		disposition := part.Header.Get("Content-Disposition")
		filename := part.FileName()

		// Decode content transfer encoding
		partReader := decodeTransferEncoding(part, part.Header.Get("Content-Transfer-Encoding"))

		// Recursive multipart
		if strings.HasPrefix(partMediaType, "multipart/") {
			subBoundary := partParams["boundary"]
			if subBoundary != "" {
				subBody, subAtt, subInl, subCal, err := parseMultipart(partReader, partMediaType, subBoundary)
				if err != nil {
					continue
				}
				body.Parts = append(body.Parts, subBody)
				attachments = append(attachments, subAtt...)
				inline = append(inline, subInl...)
				calendarEvents = append(calendarEvents, subCal...)
				continue
			}
		}

		content, err := io.ReadAll(partReader)
		if err != nil {
			continue
		}

		// Is this a calendar invite? Parse it regardless of disposition.
		if partMediaType == "text/calendar" {
			if events, parseErr := ParseCalendar(string(content)); parseErr == nil && len(events) > 0 {
				calendarEvents = append(calendarEvents, events...)
			}
			// Also store as a body part so the raw .ics content is available
			body.Parts = append(body.Parts, pipeline.Body{
				ContentType: partMediaType,
				Content:     string(content),
			})
			continue
		}

		// Is this an attachment?
		if strings.Contains(disposition, "attachment") || (filename != "" && !strings.HasPrefix(partMediaType, "text/")) {
			att := pipeline.Attachment{
				Filename:    filename,
				ContentType: partMediaType,
				Size:        int64(len(content)),
				Disposition: "attachment",
				Content:     base64.StdEncoding.EncodeToString(content),
			}
			attachments = append(attachments, att)
			continue
		}

		// Is this an inline image?
		contentID := part.Header.Get("Content-Id")
		if strings.Contains(disposition, "inline") && contentID != "" {
			att := pipeline.Attachment{
				Filename:    filename,
				ContentType: partMediaType,
				Size:        int64(len(content)),
				Disposition: "inline",
				ContentID:   contentID,
				Content:     base64.StdEncoding.EncodeToString(content),
			}
			inline = append(inline, att)
			continue
		}

		// Regular body part
		body.Parts = append(body.Parts, pipeline.Body{
			ContentType: partMediaType,
			Content:     string(content),
		})
	}

	return body, attachments, inline, calendarEvents, nil
}

func decodeTransferEncoding(reader io.Reader, encoding string) io.Reader {
	switch strings.ToLower(encoding) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, reader)
	case "quoted-printable":
		return quotedprintable.NewReader(reader)
	default:
		return reader
	}
}

func decodeHeader(value string) string {
	decoder := &mime.WordDecoder{}
	decoded, err := decoder.DecodeHeader(value)
	if err != nil {
		return value // Return as-is if decoding fails
	}
	return decoded
}
