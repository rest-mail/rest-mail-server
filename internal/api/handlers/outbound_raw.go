package handlers

import (
	"encoding/base64"
	"strings"
	"time"

	rmime "github.com/restmail/restmail/internal/mime"
	"github.com/restmail/restmail/internal/pipeline"
)

// outboundMessage carries everything the raw RFC 5322 rendering of an outbound
// message needs.
//
// It exists so the rendering can be exercised on its own: the handler that
// produces it talks to the database and runs the outbound pipeline, neither of
// which a test of the message format should have to stand up.
type outboundMessage struct {
	FromName  string
	From      string
	To        []string
	Cc        []string
	Subject   string
	BodyText  string
	BodyHTML  string
	MessageID string
	InReplyTo string
	Date      time.Time

	// Extra holds headers added by pipeline transforms.
	Extra map[string]string

	// ICS, when set, is the iCalendar payload attached as invite.ics, and
	// ICSMethod is its method parameter (REQUEST when empty).
	ICS       []byte
	ICSMethod string
}

// buildOutboundRaw renders the message as the bytes handed to the outbound
// delivery queue and signed by DKIM.
//
// The rendering is done by internal/mime, the same serializer the pipeline
// filters use: it RFC 2047-encodes header values that need it, folds long
// headers, strips CR/LF so a transform-supplied value cannot start a header
// line of its own, and takes its multipart boundary from a random source rather
// than from the send time — which this message publishes in its own Date header
// (issue #146).
func buildOutboundRaw(m outboundMessage) (string, error) {
	email := &pipeline.EmailJSON{
		Headers: pipeline.Headers{
			From:      []pipeline.Address{{Name: m.FromName, Address: m.From}},
			To:        bareAddressList(m.To),
			Cc:        bareAddressList(m.Cc),
			Subject:   m.Subject,
			Date:      m.Date.Format(time.RFC1123Z),
			MessageID: m.MessageID,
			Extra:     transformHeaders(m.Extra),
		},
		Body: outboundBody(m.BodyText, m.BodyHTML),
	}
	if m.InReplyTo != "" {
		email.Headers.InReplyTo = "<" + m.InReplyTo + ">"
	}
	if m.ICS != nil {
		method := strings.ToUpper(m.ICSMethod)
		if method == "" {
			method = "REQUEST"
		}
		email.Attachments = []pipeline.Attachment{{
			Filename:    "invite.ics",
			ContentType: "text/calendar; charset=utf-8; method=" + method,
			Disposition: "attachment",
			Size:        int64(len(m.ICS)),
			Content:     base64.StdEncoding.EncodeToString(m.ICS),
		}}
	}

	raw, err := rmime.Serialize(email)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// outboundBody shapes the body tree from the composed text and HTML parts.
func outboundBody(text, html string) pipeline.Body {
	switch {
	case text != "" && html != "":
		return pipeline.Body{
			ContentType: "multipart/alternative",
			Parts: []pipeline.Body{
				{ContentType: "text/plain; charset=utf-8", Content: text},
				{ContentType: "text/html; charset=utf-8", Content: html},
			},
		}
	case html != "":
		return pipeline.Body{ContentType: "text/html; charset=utf-8", Content: html}
	default:
		return pipeline.Body{ContentType: "text/plain; charset=utf-8", Content: text}
	}
}

// bareAddressList turns recipient addresses into the serializer's address form.
func bareAddressList(addrs []string) []pipeline.Address {
	if len(addrs) == 0 {
		return nil
	}
	out := make([]pipeline.Address, 0, len(addrs))
	for _, addr := range addrs {
		out = append(out, pipeline.Address{Address: addr})
	}
	return out
}

// transformHeaders returns the headers a pipeline transform added, minus the
// signatures.
//
// DKIM-Signature and ARC-* are dropped deliberately: the pipeline signs a
// reconstructed EmailJSON whose header bytes do not match this rendering, so
// such a signature would never verify. DKIM is signed authoritatively over the
// finalized raw by the caller (signOutboundDKIM).
func transformHeaders(extra map[string]string) map[string]string {
	if len(extra) == 0 {
		return nil
	}
	out := make(map[string]string, len(extra))
	for name, value := range extra {
		switch {
		case strings.EqualFold(name, "DKIM-Signature"),
			strings.HasPrefix(strings.ToLower(name), "arc-"):
			continue
		}
		out[name] = value
	}
	return out
}

// calendarMethod returns the method of a composed calendar event, or "" when
// the message carries none.
func calendarMethod(event *pipeline.CalendarEvent) string {
	if event == nil {
		return ""
	}
	return event.Method
}
