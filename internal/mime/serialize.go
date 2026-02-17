package mime

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/restmail/restmail/internal/pipeline"
)

// Serialize converts an EmailJSON back to a raw RFC 2822 message.
func Serialize(email *pipeline.EmailJSON) ([]byte, error) {
	var b strings.Builder

	// Headers
	if len(email.Headers.From) > 0 {
		b.WriteString("From: " + formatAddresses(email.Headers.From) + "\r\n")
	}
	if len(email.Headers.To) > 0 {
		b.WriteString("To: " + formatAddresses(email.Headers.To) + "\r\n")
	}
	if len(email.Headers.Cc) > 0 {
		b.WriteString("Cc: " + formatAddresses(email.Headers.Cc) + "\r\n")
	}
	if email.Headers.Subject != "" {
		b.WriteString("Subject: " + email.Headers.Subject + "\r\n")
	}
	if email.Headers.Date != "" {
		b.WriteString("Date: " + email.Headers.Date + "\r\n")
	} else {
		b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	}
	if email.Headers.MessageID != "" {
		b.WriteString("Message-ID: " + email.Headers.MessageID + "\r\n")
	}
	if email.Headers.InReplyTo != "" {
		b.WriteString("In-Reply-To: " + email.Headers.InReplyTo + "\r\n")
	}
	if len(email.Headers.References) > 0 {
		b.WriteString("References: " + strings.Join(email.Headers.References, " ") + "\r\n")
	}

	// Extra headers
	for k, v := range email.Headers.Extra {
		b.WriteString(k + ": " + v + "\r\n")
	}

	// MIME version
	b.WriteString("MIME-Version: 1.0\r\n")

	// Body
	hasAttachments := len(email.Attachments) > 0 || len(email.Inline) > 0
	hasMultipleBodyParts := len(email.Body.Parts) > 0

	if hasAttachments {
		boundary := generateBoundary()
		b.WriteString("Content-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n")
		b.WriteString("\r\n")

		// Body part
		b.WriteString("--" + boundary + "\r\n")
		if hasMultipleBodyParts {
			altBoundary := generateBoundary()
			b.WriteString("Content-Type: multipart/alternative; boundary=\"" + altBoundary + "\"\r\n\r\n")
			for _, part := range email.Body.Parts {
				b.WriteString("--" + altBoundary + "\r\n")
				b.WriteString("Content-Type: " + part.ContentType + "\r\n\r\n")
				b.WriteString(part.Content + "\r\n")
			}
			b.WriteString("--" + altBoundary + "--\r\n")
		} else {
			ct := email.Body.ContentType
			if ct == "" {
				ct = "text/plain; charset=utf-8"
			}
			b.WriteString("Content-Type: " + ct + "\r\n\r\n")
			b.WriteString(email.Body.Content + "\r\n")
		}

		// Inline images
		for _, att := range email.Inline {
			b.WriteString("--" + boundary + "\r\n")
			writeAttachmentPart(&b, att)
		}

		// Attachments
		for _, att := range email.Attachments {
			b.WriteString("--" + boundary + "\r\n")
			writeAttachmentPart(&b, att)
		}

		b.WriteString("--" + boundary + "--\r\n")
	} else if hasMultipleBodyParts {
		boundary := generateBoundary()
		b.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n")
		b.WriteString("\r\n")
		for _, part := range email.Body.Parts {
			b.WriteString("--" + boundary + "\r\n")
			b.WriteString("Content-Type: " + part.ContentType + "\r\n\r\n")
			b.WriteString(part.Content + "\r\n")
		}
		b.WriteString("--" + boundary + "--\r\n")
	} else {
		ct := email.Body.ContentType
		if ct == "" {
			ct = "text/plain; charset=utf-8"
		}
		b.WriteString("Content-Type: " + ct + "\r\n")
		b.WriteString("\r\n")
		b.WriteString(email.Body.Content + "\r\n")
	}

	return []byte(b.String()), nil
}

func formatAddresses(addrs []pipeline.Address) string {
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		if a.Name != "" {
			parts[i] = fmt.Sprintf("%q <%s>", a.Name, a.Address)
		} else {
			parts[i] = a.Address
		}
	}
	return strings.Join(parts, ", ")
}

func writeAttachmentPart(b *strings.Builder, att pipeline.Attachment) {
	disp := att.Disposition
	if disp == "" {
		disp = "attachment"
	}
	b.WriteString("Content-Type: " + att.ContentType + "\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	if att.Filename != "" {
		b.WriteString(fmt.Sprintf("Content-Disposition: %s; filename=%q\r\n", disp, att.Filename))
	} else {
		b.WriteString("Content-Disposition: " + disp + "\r\n")
	}
	if att.ContentID != "" {
		b.WriteString("Content-ID: " + att.ContentID + "\r\n")
	}
	b.WriteString("\r\n")

	// Write base64 content with line wrapping
	content := att.Content
	for len(content) > 76 {
		b.WriteString(content[:76] + "\r\n")
		content = content[76:]
	}
	if len(content) > 0 {
		b.WriteString(content + "\r\n")
	}
}

var boundaryCounter int

func generateBoundary() string {
	boundaryCounter++
	return fmt.Sprintf("=_restmail_%d_%d", time.Now().UnixNano(), boundaryCounter)
}

// EnvelopeFromEmail extracts envelope information from the email headers.
func EnvelopeFromEmail(email *pipeline.EmailJSON) pipeline.Envelope {
	env := email.Envelope
	if env.MailFrom == "" && len(email.Headers.From) > 0 {
		env.MailFrom = email.Headers.From[0].Address
	}
	if len(env.RcptTo) == 0 {
		for _, to := range email.Headers.To {
			env.RcptTo = append(env.RcptTo, to.Address)
		}
		for _, cc := range email.Headers.Cc {
			env.RcptTo = append(env.RcptTo, cc.Address)
		}
	}
	return env
}

// EstimateSize returns a rough estimate of the message size in bytes.
func EstimateSize(email *pipeline.EmailJSON) int64 {
	var size int64
	// Headers
	for _, from := range email.Headers.From {
		size += int64(len(from.Address) + len(from.Name) + 20)
	}
	size += int64(len(email.Headers.Subject) + 20)
	// Body
	size += int64(len(email.Body.Content))
	for _, part := range email.Body.Parts {
		size += int64(len(part.Content))
	}
	// Attachments
	for _, att := range email.Attachments {
		if att.Content != "" {
			decoded, _ := base64.StdEncoding.DecodeString(att.Content)
			size += int64(len(decoded))
		} else {
			size += att.Size
		}
	}
	for _, att := range email.Inline {
		if att.Content != "" {
			decoded, _ := base64.StdEncoding.DecodeString(att.Content)
			size += int64(len(decoded))
		} else {
			size += att.Size
		}
	}
	return size
}
