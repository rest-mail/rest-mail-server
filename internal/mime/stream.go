package mime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	gomessage "github.com/emersion/go-message"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// MetadataBodyTruncated is set on a parsed message when a text part went past
// StreamOptions.MaxText. The whole text is in the stored message.
const MetadataBodyTruncated = "body_truncated"

// StoredPart is where a part went, and how to check it.
type StoredPart struct {
	Storage  string // "filesystem" or "s3"
	Ref      string
	Checksum string
	Size     int64
}

// StreamOptions says how ParseStream handles what it does not keep in memory.
type StreamOptions struct {
	// Store takes an attachment or inline part as it is decoded.
	Store func(ctx context.Context, part io.Reader) (StoredPart, error)
	// MaxText is how much of each text part stays in memory. The rest is passed over.
	MaxText int64
}

// ParseHeaders reads a message's header block from source and nothing past it, for a caller that
// can decide something about a message before reading its parts. It fails for what Parse and
// ParseStream fail for, because those can only fail on the header block.
func ParseHeaders(source io.Reader) (pipeline.Headers, error) {
	entity, err := gomessage.Read(source)
	if entity == nil {
		return pipeline.Headers{}, fmt.Errorf("parse message: %w", err)
	}
	return parseHeaders(entity.Header), nil
}

// ParseStream decomposes a message read from source, and keeps in memory only headers,
// structure and up to MaxText of each text part: attachments and inline parts go straight to
// Store as they are decoded. A message is untrusted text, and its size is the sender's choice.
//
// A part that can't be decoded is left out, as Parse leaves it out. A failure to store one is an
// error: the message must not be taken without it.
func ParseStream(ctx context.Context, source io.Reader, options StreamOptions) (*pipeline.EmailJSON, error) {
	if options.Store == nil || options.MaxText <= 0 {
		return nil, errors.New("parse message: a store and a text limit are required")
	}
	entity, err := gomessage.Read(source)
	if entity == nil {
		return nil, fmt.Errorf("parse message: %w", err)
	}
	s := &streamParse{ctx: ctx, options: options, budget: newPartBudget(), email: &pipeline.EmailJSON{Headers: parseHeaders(entity.Header)}}
	mediaType, _, _ := entity.Header.ContentType()
	mediaType = strings.ToLower(mediaType)
	if mr := entity.MultipartReader(); mr != nil {
		if s.failed = s.budget.enter(1); s.failed == nil {
			s.email.Body = s.walk(mr, mediaType, 1)
		}
	} else {
		content, _ := s.readText(entity.Body)
		s.email.Body = pipeline.Body{ContentType: mediaType, Content: content}
		if mediaType == "text/calendar" {
			s.calendar(content)
		}
	}
	if s.failed != nil {
		if errors.Is(s.failed, ErrStructureLimit) {
			return nil, fmt.Errorf("parse message: %w", s.failed)
		}
		return nil, s.failed
	}
	return s.email, nil
}

type streamParse struct {
	ctx     context.Context
	options StreamOptions
	budget  *partBudget
	email   *pipeline.EmailJSON
	failed  error
}

// walk reads the parts of a multipart at depth, which the Limits in force bound as Parse's
// parseMultipart bounds them.
func (s *streamParse) walk(mr gomessage.MultipartReader, mediaType string, depth int) pipeline.Body {
	body := pipeline.Body{ContentType: mediaType}
	for s.failed == nil {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil && !gomessage.IsUnknownCharset(err) && !gomessage.IsUnknownEncoding(err) {
			break // a malformed boundary: nothing further can be read
		}
		if s.failed = s.budget.part(); s.failed != nil {
			break
		}
		partType, _, _ := part.Header.ContentType()
		partType = strings.ToLower(partType)
		if nested := part.MultipartReader(); nested != nil {
			if s.failed = s.budget.enter(depth + 1); s.failed != nil {
				break
			}
			body.Parts = append(body.Parts, s.walk(nested, partType, depth+1))
			continue
		}
		disposition, _, _ := part.Header.ContentDisposition()
		disposition = strings.ToLower(disposition)
		filename := partFilename(part.Header)
		contentID := part.Header.Get("Content-Id")
		switch {
		case partType == "text/calendar":
			content, err := s.readText(part.Body)
			if err != nil {
				continue
			}
			s.calendar(content)
			body.Parts = append(body.Parts, pipeline.Body{ContentType: partType, Content: content})
		case disposition == "inline" && contentID != "":
			if stored, ok := s.store(part.Body); ok {
				s.email.Inline = append(s.email.Inline, attachmentOf(filename, partType, "inline", contentID, stored))
			}
		case disposition == "attachment" || (filename != "" && !strings.HasPrefix(partType, "text/")):
			if stored, ok := s.store(part.Body); ok {
				s.email.Attachments = append(s.email.Attachments, attachmentOf(filename, partType, "attachment", "", stored))
			}
		default:
			content, err := s.readText(part.Body)
			if err != nil {
				continue
			}
			body.Parts = append(body.Parts, pipeline.Body{ContentType: partType, Content: content})
		}
	}
	return body
}

func attachmentOf(filename, contentType, disposition, contentID string, stored StoredPart) pipeline.Attachment {
	return pipeline.Attachment{
		Filename: filename, ContentType: contentType, Size: stored.Size, Disposition: disposition, ContentID: contentID,
		Storage: stored.Storage, Ref: stored.Ref, Checksum: stored.Checksum,
	}
}

// readText reads up to MaxText of a text part and passes over the rest, which still has to be
// read for the next part to be found. When reading fails it answers what it read as well as the
// error: a message's only part keeps what could be decoded, as Parse keeps it.
func (s *streamParse) readText(part io.Reader) (string, error) {
	var text strings.Builder
	if _, err := io.Copy(&text, io.LimitReader(part, s.options.MaxText)); err != nil {
		return text.String(), err
	}
	rest, err := io.Copy(io.Discard, part)
	if err != nil {
		return text.String(), err
	}
	if rest > 0 {
		if s.email.Metadata == nil {
			s.email.Metadata = map[string]string{}
		}
		s.email.Metadata[MetadataBodyTruncated] = strconv.FormatBool(true)
	}
	return text.String(), nil
}

func (s *streamParse) calendar(content string) {
	if events, err := ParseCalendar(content); err == nil && len(events) > 0 {
		s.email.CalendarEvents = append(s.email.CalendarEvents, events...)
	}
}

// readProblem remembers that reading a part failed, which is the message's fault, as distinct
// from storing it failing, which is ours.
type readProblem struct {
	source io.Reader
	err    error
}

func (r *readProblem) Read(p []byte) (int, error) {
	n, err := r.source.Read(p)
	if err != nil && err != io.EOF {
		r.err = err
	}
	return n, err
}

func (s *streamParse) store(part io.Reader) (StoredPart, bool) {
	reading := &readProblem{source: part}
	stored, err := s.options.Store(s.ctx, reading)
	if reading.err != nil {
		return StoredPart{}, false
	}
	if err != nil {
		s.failed = fmt.Errorf("store a message part: %w", err)
		return StoredPart{}, false
	}
	return stored, true
}
