package message

import (
	"fmt"
	"net/mail"
	"strings"

	"github.com/rest-mail/go-imap/pkg/imap"
	"github.com/rest-mail/go-imap/pkg/imap/internal/wire"
)

// BuildEnvelope constructs the ten ENVELOPE fields from the stored message's
// headers (RFC 3501 §7.4.2). Missing fields are NIL; only Sender and Reply-To
// default to From. INTERNALDATE and cached summary fields are not header values.
// The legacy Message fallback is retained only when no header block can be read.
func BuildEnvelope(msg imap.Message, raw string, rawOK bool) string {
	var hdr mail.Header
	if rawOK {
		// A malformed raw header block has no trustworthy envelope fields.
		hdr = mail.Header{}
		if m, err := mail.ReadMessage(strings.NewReader(raw)); err == nil {
			hdr = m.Header
		}
	}

	dateField := wire.QuoteString(msg.Date.Format("Mon, 02 Jan 2006 15:04:05 -0700"))
	subject := wire.QuoteString(msg.Subject)
	fromFallback := buildAddress(msg.From.Name, msg.From.Email)
	toFallback := buildAddressList(msg.To)
	if hdr != nil {
		dateField = envelopeHeaderString(hdr, "Date")
		subject = envelopeHeaderString(hdr, "Subject")
		fromFallback, toFallback = "NIL", "NIL"
	}
	from := headerAddressList(hdr, "From", fromFallback)
	sender := headerAddressList(hdr, "Sender", from)
	replyTo := headerAddressList(hdr, "Reply-To", from)
	to := headerAddressList(hdr, "To", toFallback)
	cc := headerAddressList(hdr, "Cc", "NIL")
	bcc := headerAddressList(hdr, "Bcc", "NIL")
	inReplyTo := envelopeHeaderString(hdr, "In-Reply-To")
	messageID := envelopeHeaderString(hdr, "Message-Id")

	return fmt.Sprintf("(%s %s %s %s %s %s %s %s %s %s)",
		dateField, subject, from, sender, replyTo, to, cc, bcc, inReplyTo, messageID)
}

// envelopeHeaderString distinguishes absent headers (NIL) from present empty
// headers (""). Reuse the astring encoder for quoted-specials and literal-only
// octets; neither Unicode nor a control character may escape into response lines.
func envelopeHeaderString(hdr mail.Header, name string) string {
	if _, present := hdr[name]; !present {
		return "NIL"
	}
	return wire.EncodeMailboxName(headerValue(hdr, name))
}

// headerAddressList renders the named address-list header as an IMAP envelope
// address field, parsing it with the same buildAddressList that backs the To
// field. It returns fallback when the header is absent (or hdr is nil because the
// raw message was unavailable), and also when the header is present but parses to
// no address. Callers choose the fallback: normally NIL, or From for the Sender
// and Reply-To defaults.
func headerAddressList(hdr mail.Header, name, fallback string) string {
	if hdr == nil {
		return fallback
	}
	v := strings.TrimSpace(hdr.Get(name))
	if v == "" {
		return fallback
	}
	if list := buildAddressList(v); list != "NIL" {
		return list
	}
	return fallback
}

// headerValue returns the trimmed value of the named header, or "" when it is
// absent or the raw message was unavailable (hdr is nil).
func headerValue(hdr mail.Header, name string) string {
	if hdr == nil {
		return ""
	}
	return strings.TrimSpace(hdr.Get(name))
}

// addressPart constructs a single IMAP address structure "(name NIL mailbox
// host)" — personal name, source-route (always NIL), mailbox name and host name
// (RFC 3501 §7.4.2). It is the inner element of the parenthesized address lists
// that make up the from/sender/reply-to/to/cc/bcc envelope fields.
func addressPart(name, email string) string {
	parts := strings.SplitN(email, "@", 2)
	user := parts[0]
	host := ""
	if len(parts) > 1 {
		host = parts[1]
	}
	return fmt.Sprintf("(%s NIL %s %s)", wire.QuoteString(name), wire.QuoteString(user), wire.QuoteString(host))
}

// buildAddress wraps a single address in the parenthesized address-list form an
// envelope field takes, e.g. ((name NIL mailbox host)). An empty address is NIL.
func buildAddress(name, email string) string {
	if email == "" {
		return "NIL"
	}
	return "(" + addressPart(name, email) + ")"
}

// buildAddressList parses an RFC 5322 address-list header value (e.g.
// "Bob <bob@example.org>, carol@example.com") into the parenthesized list of
// address structures an envelope field requires. An empty or unparseable value
// yields NIL, matching the RFC's treatment of an absent header.
func buildAddressList(list string) string {
	list = strings.TrimSpace(list)
	if list == "" {
		return "NIL"
	}
	// net/mail validates the complete grammar but flattens groups, including
	// empty ones. Scan only structural delimiters here; keep address and phrase
	// decoding with net/mail instead of implementing a second address parser.
	addrs, err := mail.ParseAddressList(list)
	if err != nil {
		return "NIL"
	}
	var b strings.Builder
	b.WriteByte('(')
	if !strings.Contains(list, ":") {
		for _, a := range addrs {
			b.WriteString(addressPart(a.Name, a.Address))
		}
	} else if !appendAddressGroups(&b, list) {
		return "NIL"
	}
	if b.Len() == 1 {
		return "NIL"
	}
	b.WriteByte(')')
	return b.String()
}

// appendAddressGroups preserves RFC 3501 §7.4.2 group-start/group-end
// structures. list has already passed mail.ParseAddressList. Delimiters inside
// quoted strings, nested comments, angle addresses and domain literals are data.
func appendAddressGroups(b *strings.Builder, list string) bool {
	appendMembers := func(value string) bool {
		if strings.TrimSpace(stripComments(value)) == "" {
			return true
		}
		members, err := mail.ParseAddressList(value)
		if err != nil {
			return false
		}
		for _, a := range members {
			b.WriteString(addressPart(a.Name, a.Address))
		}
		return true
	}
	start, comments := 0, 0
	quoted, angle, domain, escaped := false, false, false, false
	for i := 0; i < len(list); i++ {
		c := list[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && (quoted || comments > 0 || domain) {
			escaped = true
			continue
		}
		if comments > 0 {
			switch c {
			case '(':
				comments++
			case ')':
				comments--
			}
			continue
		}
		if quoted {
			if c == '"' {
				quoted = false
			}
			continue
		}
		if domain {
			if c == ']' {
				domain = false
			}
			continue
		}
		switch c {
		case '"':
			quoted = true
		case '(':
			comments++
		case '[':
			domain = true
		case '<':
			angle = true
		case '>':
			angle = false
		case ':', ',', ';':
			if angle {
				continue
			}
			if c == ':' {
				// Parse the group's display-name as a mailbox display-name so
				// comments, quoted-pairs and encoded words share one decoder.
				name, err := mail.ParseAddress(list[start:i] + " <group@invalid>")
				if err != nil {
					return false
				}
				fmt.Fprintf(b, "(NIL NIL %s NIL)", wire.EncodeMailboxName(name.Name))
			} else {
				if !appendMembers(list[start:i]) {
					return false
				}
				if c == ';' {
					b.WriteString("(NIL NIL NIL NIL)")
				}
			}
			start = i + 1
		}
	}
	return appendMembers(list[start:])
}

// buildEncapsulatedEnvelope constructs the IMAP ENVELOPE (RFC 3501 §7.4.2) of a
// MESSAGE/RFC822 part's encapsulated message directly from its own headers — date,
// subject, from, sender, reply-to, to, cc, bcc, in-reply-to and message-id — with
// sender and reply-to defaulting to from. It differs from BuildEnvelope, which
// uses the stored Message model only when headers cannot be read; an
// encapsulated message has no such model, so every field comes from the raw header
// block. A header that cannot be parsed yields ten NIL fields, keeping the
// required envelope structure well-formed.
func buildEncapsulatedEnvelope(raw string) string {
	m, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		return "(NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL)"
	}
	hdr := m.Header

	// Encapsulated envelopes follow the same header rules as FETCH ENVELOPE:
	// preserve the Date text, distinguish empty from absent scalar headers, and
	// frame literal-only octets rather than writing them in quoted strings.
	dateField := envelopeHeaderString(hdr, "Date")
	subject := envelopeHeaderString(hdr, "Subject")
	from := headerAddressList(hdr, "From", "NIL")
	sender := headerAddressList(hdr, "Sender", from)
	replyTo := headerAddressList(hdr, "Reply-To", from)
	to := headerAddressList(hdr, "To", "NIL")
	cc := headerAddressList(hdr, "Cc", "NIL")
	bcc := headerAddressList(hdr, "Bcc", "NIL")
	inReplyTo := envelopeHeaderString(hdr, "In-Reply-To")
	messageID := envelopeHeaderString(hdr, "Message-Id")

	return fmt.Sprintf("(%s %s %s %s %s %s %s %s %s %s)",
		dateField, subject, from, sender, replyTo, to, cc, bcc, inReplyTo, messageID)
}
