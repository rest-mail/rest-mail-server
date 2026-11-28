package filters

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// errOverRequestLimit says a request would be larger than its filter sends.
var errOverRequestLimit = errors.New("request is over the limit")

// referencedPart is a part whose bytes the pipeline's caller holds, opened when it is rendered.
type referencedPart struct {
	ctx  context.Context
	open pipeline.AttachmentOpener
	att  pipeline.Attachment
}

func (p referencedPart) Open() (io.ReadCloser, error) {
	return p.open.Open(p.ctx, p.att)
}

// referencedSources are the sources of the parts the context's opener holds, one per part, nil
// for a part it does not hold, or nil when it holds none.
func referencedSources(ctx context.Context, parts []pipeline.Attachment) []rmime.AttachmentSource {
	open := pipeline.AttachmentOpenerFromContext(ctx)
	if open == nil {
		return nil
	}
	var sources []rmime.AttachmentSource
	for i, att := range parts {
		// A part as content is sent as it is, and so is a reference nobody handed in.
		if att.Ref == "" || att.Content != "" || !open.Holds(att) {
			continue
		}
		if sources == nil {
			sources = make([]rmime.AttachmentSource, len(parts))
		}
		sources[i] = referencedPart{ctx: ctx, open: open, att: att}
	}
	return sources
}

// withReferencedContent is email as a JSON filter is sent it: a part handed to the pipeline by
// reference carries its bytes as base64 content and no reference, as it did when it was handed
// in as content. It refuses, before reading the part, content that would pass limit octets.
func withReferencedContent(ctx context.Context, email *pipeline.EmailJSON, limit int64) (*pipeline.EmailJSON, error) {
	attachments, inline := referencedSources(ctx, email.Attachments), referencedSources(ctx, email.Inline)
	if attachments == nil && inline == nil {
		return email, nil
	}
	remaining := limit
	fill := func(parts []pipeline.Attachment, sources []rmime.AttachmentSource) ([]pipeline.Attachment, error) {
		if sources == nil {
			return parts, nil
		}
		out := append([]pipeline.Attachment(nil), parts...)
		for i, source := range sources {
			if source == nil {
				continue
			}
			size := int64(base64.StdEncoding.EncodedLen(int(out[i].Size)))
			if out[i].Size < 0 || size > remaining {
				return nil, errOverRequestLimit
			}
			remaining -= size
			content, err := encodeSource(source, size)
			if err != nil {
				return nil, err
			}
			out[i].Content, out[i].Storage, out[i].Ref = content, "", ""
		}
		return out, nil
	}
	copied := *email
	var err error
	if copied.Attachments, err = fill(email.Attachments, attachments); err != nil {
		return nil, err
	}
	if copied.Inline, err = fill(email.Inline, inline); err != nil {
		return nil, err
	}
	return &copied, nil
}

func encodeSource(source rmime.AttachmentSource, size int64) (content string, err error) {
	r, err := source.Open()
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := r.Close(); err == nil {
			err = closeErr
		}
	}()
	var b strings.Builder
	b.Grow(int(size))
	encoder := base64.NewEncoder(base64.StdEncoding, &b)
	if _, err := io.Copy(encoder, struct{ io.Reader }{r}); err != nil {
		return "", err
	}
	if err := encoder.Close(); err != nil {
		return "", err
	}
	return b.String(), nil
}

// mimeRequest is a message as the scanners are sent it, rendered as it is sent with the parts
// handed in by reference read from where they are stored. It is rendered once to learn its
// length, and again, with the same boundaries, as the request body.
type mimeRequest struct {
	email      *pipeline.EmailJSON
	sources    rmime.SerializeOptions
	boundaries []string
	length     int64
}

// newMIMERequest measures email's rendering, refusing it once it passes limit octets.
func newMIMERequest(ctx context.Context, email *pipeline.EmailJSON, limit int64) (*mimeRequest, error) {
	m := &mimeRequest{email: email, sources: rmime.SerializeOptions{
		AttachmentSources: referencedSources(ctx, email.Attachments),
		InlineSources:     referencedSources(ctx, email.Inline),
	}}
	opts := m.sources
	opts.Boundary = func() string {
		var random [30]byte
		_, _ = rand.Read(random[:])
		boundary := hex.EncodeToString(random[:])
		m.boundaries = append(m.boundaries, boundary)
		return boundary
	}
	counter := &limitedCounter{limit: limit}
	if err := rmime.SerializeTo(counter, email, opts); err != nil {
		if errors.Is(err, errOverRequestLimit) {
			return nil, fmt.Errorf("request of more than %d bytes is over the limit of %d bytes", limit, limit)
		}
		return nil, err
	}
	m.length = counter.n
	return m, nil
}

// body renders the message again as it was measured. A part that changed since is a failed
// request, as its length no longer matches.
func (m *mimeRequest) body() io.ReadCloser {
	pr, pw := io.Pipe()
	opts := m.sources
	next := 0
	opts.Boundary = func() string {
		boundary := ""
		if next < len(m.boundaries) {
			boundary = m.boundaries[next]
		}
		next++
		return boundary
	}
	go func() {
		pw.CloseWithError(rmime.SerializeTo(pw, m.email, opts))
	}()
	return pr
}

type limitedCounter struct {
	n, limit int64
}

func (c *limitedCounter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	if c.n > c.limit {
		return 0, errOverRequestLimit
	}
	return len(p), nil
}
