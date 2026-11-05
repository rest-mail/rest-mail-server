// Package mime provides RFC 2822 email parsing and serialization.
// It converts between raw email messages and the pipeline's EmailJSON format.
//
// Messages are read as github.com/emersion/go-message reads them (see entity.go),
// with its charset conversion. iCalendar (text/calendar) parsing is in icalendar.go.
package mime

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"unsafe"

	// Registers a charset reader so non-UTF-8 text parts decode.
	_ "github.com/emersion/go-message/charset"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// Parse converts a raw RFC 2822 message into the pipeline's EmailJSON format. A message nested
// deeper or holding more parts than the Limits in force is an error wrapping ErrStructureLimit,
// answered as soon as the limit is passed.
func Parse(raw []byte) (*pipeline.EmailJSON, error) {
	return ParseReader(bytes.NewReader(raw))
}

// ParseReader is Parse from a reader, for a caller that holds the message in some other form and
// has no need to copy it to parse it. What it builds is held whole, as Parse's is.
func ParseReader(source io.Reader) (*pipeline.EmailJSON, error) {
	var sourceSize int
	if sized, ok := source.(interface{ Len() int }); ok {
		sourceSize = sized.Len()
	}
	fields, br, reader, headerSize, err := readTopFields(source, sourceSize)
	if err != nil {
		return nil, fmt.Errorf("parse message: %w", err)
	}
	// The body is what's left of the source.
	return newHeaderSection(fields, reader).parseBody(br, max(sourceSize-int(headerSize), 0))
}

// HeaderSection is a message's header section, parsed once, for a caller that decides what it can
// from that before it reads the body, and then has no need to read it again. Headers is the caller's
// from then on, and Parse's answer holds the same one.
type HeaderSection struct {
	Headers pipeline.Headers
	opening partFields
	// fields holds the values of the section's fields, and has room left after them.
	fields *fieldReader
}

func newHeaderSection(fields []field, reader *fieldReader) *HeaderSection {
	return &HeaderSection{Headers: parseHeaders(fields), opening: fieldsOpening(fields), fields: reader}
}

// ReadHeaderSection parses a message's header section, which is everything before its body. It
// fails for what ParseReader fails for on the same section.
func ReadHeaderSection(section string) (*HeaderSection, error) {
	fields, _, reader, _, err := readTopFields(strings.NewReader(section), len(section))
	if err != nil {
		return nil, fmt.Errorf("parse message: %w", err)
	}
	return newHeaderSection(fields, reader), nil
}

// Parse reads the body that follows the section, whose size is known, and answers what ParseReader
// answers for the whole message.
func (h *HeaderSection) Parse(body string) (*pipeline.EmailJSON, error) {
	h.fields.sourceSize = len(body)
	return h.parseBody(strings.NewReader(body), len(body))
}

func (h *HeaderSection) parseBody(source io.Reader, size int) (*pipeline.EmailJSON, error) {
	// An unknown charset or transfer encoding still gives a readable entity.
	entity, _ := h.opening.open(source)
	budget := newPartBudget()
	budget.fields = h.fields
	body, attachments, inline, calendarEvents, err := parseEntity(entity, size, budget)
	if err != nil {
		return nil, fmt.Errorf("parse message: %w", err)
	}
	return &pipeline.EmailJSON{Headers: h.Headers, Body: body, Attachments: attachments, Inline: inline, CalendarEvents: calendarEvents}, nil
}

// fieldsOpening is what opening a message needs of its fields, which are already read.
func fieldsOpening(fields []field) partFields {
	var kept partFields
	for i := range fields {
		switch fields[i].key {
		case "Content-Type":
			if !kept.hasType {
				kept.hasType = true
				kept.mediaType, kept.params, kept.typeFailed = parseContentType(fields[i].value)
			}
		case "Content-Transfer-Encoding":
			if !kept.hasEncoding {
				kept.hasEncoding, kept.encoding = true, fields[i].value
			}
		}
	}
	return kept
}

func parseHeaders(fields []field) pipeline.Headers {
	headers := pipeline.Headers{
		Date:      firstField(fields, "Date"),
		MessageID: firstField(fields, "Message-Id"),
		InReplyTo: firstField(fields, "In-Reply-To"),
	}

	// Subject is RFC 2047-decoded (or the raw value when it can't be).
	// Sanitize it (and every other decoded scalar header) so an encoded-word that
	// decodes to bytes containing CR/LF cannot inject a header on re-serialization.
	headers.Subject = sanitizeHeaderValue(decodeWords(firstField(fields, "Subject")))
	headers.Date = sanitizeHeaderValue(headers.Date)
	headers.MessageID = sanitizeHeaderValue(headers.MessageID)
	headers.InReplyTo = sanitizeHeaderValue(headers.InReplyTo)

	headers.From = addressList(firstField(fields, "From"))
	headers.To = addressList(firstField(fields, "To"))
	headers.Cc = addressList(firstField(fields, "Cc"))

	// References is kept as the raw whitespace-separated list of message-ids
	// (angle brackets preserved), matching the historical output contract.
	if refs := firstField(fields, "References"); refs != "" {
		headers.References = append(headers.References, strings.Fields(refs)...)
	}

	// Preserve all raw headers under their canonical MIME keys, with their
	// undecoded (RFC 2047-encoded) values, exactly as net/mail did, which
	// downstream DKIM/ARC/Authentication-Results logic relies on.
	//
	// EXCEPT inbound Authentication-Results, which are rehomed to
	// X-Original-Authentication-Results: they are other servers' claims, so they
	// are kept for audit but never under the key this server's own
	// spf_check/dkim_verify/arc_verify entries are appended to. No filter reads
	// a verdict from either key; verdicts travel as pipeline.AuthResults, and
	// any field claiming our own authserv-id is removed from the raw message
	// before it is parsed (StripAuthResults). ARC-* headers are cryptographically
	// verified by arc_verify and are left untouched.
	headers.Raw = rawFields(fields)

	return headers
}

func rawFields(fields []field) map[string][]string {
	raw := make(map[string][]string)
	for i := range fields {
		key := fields[i].key
		if strings.EqualFold(key, "Authentication-Results") {
			key = "X-Original-Authentication-Results"
		}
		raw[key] = append(raw[key], fields[i].value)
	}
	return raw
}

// SourceHeaders reads the header fields of a source as a parse keeps them, stopping at the first
// blank line and never reading the body. A malformed field ends the read: what came before it still
// counts, and a signal after it is not seen, so the message stays visible.
func SourceHeaders(source io.Reader) map[string][]string {
	// The error is that malformed field, and the fields before it are returned with it.
	fields, _, _, _, _ := readTopFields(source, 0)
	return rawFields(fields)
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

// parseEntity converts an opened message into the body tree, attachments, inline
// parts, and calendar events.
func parseEntity(e *entity, sourceSize int, budget *partBudget) (pipeline.Body, []pipeline.Attachment, []pipeline.Attachment, []pipeline.CalendarEvent, error) {
	mediaType := strings.ToLower(e.mediaType)

	if mr := e.multipart(budget.fields); mr != nil {
		if err := budget.enter(1); err != nil {
			return pipeline.Body{}, nil, nil, nil, err
		}
		budget.sourceSize = sourceSize
		return parseMultipart(mr, mediaType, 1, budget)
	}

	// Single-part body. Opening it has already decoded the transfer encoding
	// and (for text/*) converted the charset to UTF-8.
	// The buffer is the source's size, which the decoded body fits unless a charset grows it, and
	// it's the body's own from here on, so the text can be the same octets.
	room := budget.fields.takeBuffer(sourceSize + bytes.MinRead)
	if room == nil {
		room = make([]byte, 0, sourceSize+bytes.MinRead)
	}
	buffer := bytes.NewBuffer(room)
	_, _ = buffer.ReadFrom(e.body)
	content := buffer.Bytes()
	body := pipeline.Body{
		ContentType: mediaType,
		Content:     unsafe.String(unsafe.SliceData(content), len(content)),
	}

	var calEvents []pipeline.CalendarEvent
	if mediaType == "text/calendar" {
		if events, err := ParseCalendar(string(content)); err == nil && len(events) > 0 {
			calEvents = events
		}
	}

	return body, nil, nil, calEvents, nil
}

// parseMultipart reads the parts of a multipart at depth. Each nested multipart wraps every
// reader above it, so reading a byte at depth d costs d boundary scans: the depth limit is what
// keeps a deeply nested message from costing the square of its depth.
func parseMultipart(mr *multipartReader, mediaType string, depth int, budget *partBudget) (pipeline.Body, []pipeline.Attachment, []pipeline.Attachment, []pipeline.CalendarEvent, error) {
	body := pipeline.Body{ContentType: mediaType}
	var attachments, inline []pipeline.Attachment
	var calendarEvents []pipeline.CalendarEvent

	for {
		part, err := mr.nextPart()
		if err == io.EOF {
			break
		}
		// Unknown charset/encoding still yields a readable part; anything
		// else is a malformed boundary — stop reading further parts.
		if err != nil && !isUnknownCoding(err) {
			break
		}

		if err := budget.part(); err != nil {
			return body, attachments, inline, calendarEvents, err
		}

		partMediaType := strings.ToLower(part.mediaType)

		// Recurse into nested multiparts (e.g. multipart/alternative inside
		// multipart/mixed), preserving the body tree.
		if pmr := part.multipart(budget.fields); pmr != nil {
			if err := budget.enter(depth + 1); err != nil {
				return body, attachments, inline, calendarEvents, err
			}
			subBody, subAtt, subInl, subCal, err := parseMultipart(pmr, partMediaType, depth+1, budget)
			if err != nil {
				return body, attachments, inline, calendarEvents, err
			}
			body.Parts = append(body.Parts, subBody)
			attachments = append(attachments, subAtt...)
			inline = append(inline, subInl...)
			calendarEvents = append(calendarEvents, subCal...)
			continue
		}

		// Every part reads into one buffer. A part that's kept stays there, and one that's encoded
		// gives the room back.
		content, err := budget.readPart(part.body)
		if err != nil {
			continue
		}
		kept := unsafe.String(unsafe.SliceData(content), len(content))

		disposition := strings.ToLower(part.disposition)
		filename := part.filename()

		// Calendar invite: parse events regardless of disposition, and also
		// keep the raw .ics content available as a body part.
		if partMediaType == "text/calendar" {
			if events, parseErr := ParseCalendar(kept); parseErr == nil && len(events) > 0 {
				calendarEvents = append(calendarEvents, events...)
			}
			body.Parts = append(body.Parts, pipeline.Body{
				ContentType: partMediaType,
				Content:     kept,
			})
			continue
		}

		// Inline part (e.g. an embedded image referenced by Content-ID). This is
		// checked before the attachment case so an explicitly inline part with a
		// Content-ID is not misfiled as an attachment when it also has a filename
		// (which would drop the Content-ID needed for cid: rendering).
		contentID := part.contentID
		if disposition == "inline" && contentID != "" {
			inline = append(inline, pipeline.Attachment{
				Filename:    filename,
				ContentType: partMediaType,
				Size:        int64(len(content)),
				Disposition: "inline",
				ContentID:   contentID,
				Content:     base64.StdEncoding.EncodeToString(content),
			})
			budget.release(content)
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
			budget.release(content)
			continue
		}

		// Regular body part.
		body.Parts = append(body.Parts, pipeline.Body{
			ContentType: partMediaType,
			Content:     kept,
		})
	}

	return body, attachments, inline, calendarEvents, nil
}

func isUnknownCoding(err error) bool {
	var unknown unknownCoding
	return errors.As(err, &unknown)
}
