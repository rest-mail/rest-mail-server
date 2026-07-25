package mail

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// ReceivedInfo carries the fields for one RFC 5321 §4.4 / RFC 5322 "Received:"
// trace header. The receiving MTA prepends such a header to every message it
// accepts for delivery or further processing, recording the hop so a
// Received-chain analysis, DMARC/ARC alignment, and loop detection (RFC 5321
// §6.3) can see this server in the path.
type ReceivedInfo struct {
	// From is the remote client's HELO/EHLO name (the "from" clause). It is
	// remote-controlled, so BuildReceivedHeader sanitizes it against header
	// injection. Empty becomes "unknown".
	From string
	// RemoteIP is the source IP literal recorded alongside From, e.g.
	// "192.0.2.1". Empty omits the bracketed address.
	RemoteIP string
	// By is this server's own host name (the "by" clause).
	By string
	// With is the RFC 3848 protocol keyword: ESMTP, ESMTPS (TLS), ESMTPA
	// (authenticated), or ESMTPSA (TLS + authenticated).
	With string
	// ID is the queue/message id assigned to this reception (the "id" clause).
	ID string
	// For is the single envelope recipient (the optional "for" clause). It is
	// only emitted when exactly one recipient is known — RFC 5321 §4.4 advises
	// omitting it for multi-recipient transactions so a Bcc recipient is not
	// disclosed to the others. Empty omits the clause.
	For string
	// Timestamp is when the message was received; formatted as an RFC 5322
	// date-time. Zero means "now".
	Timestamp time.Time
}

// BuildReceivedHeader formats info as a single RFC 5321 §4.4 / RFC 5322
// "Received:" header terminated with CRLF, ready to prepend to the top of a
// message's header block (above any DKIM-Signature — a leading Received does not
// break existing signatures). Continuation lines are folded with CRLF + TAB.
//
// The remote-controlled From and For values are stripped of control characters
// (CR/LF included) so a hostile HELO name or recipient can never inject a
// forged header or split the trace line.
func BuildReceivedHeader(info ReceivedInfo) string {
	from := sanitizeHeaderToken(info.From)
	if from == "" {
		from = "unknown"
	}
	by := sanitizeHeaderToken(info.By)
	if by == "" {
		by = "localhost"
	}
	with := sanitizeHeaderToken(info.With)
	if with == "" {
		with = "ESMTP"
	}
	id := sanitizeHeaderToken(info.ID)
	if id == "" {
		id = GenerateQueueID()
	}

	ts := info.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}

	var b strings.Builder
	b.WriteString("Received: from ")
	b.WriteString(from)
	if ip := sanitizeHeaderToken(info.RemoteIP); ip != "" {
		b.WriteString(" ([")
		b.WriteString(ip)
		b.WriteString("])")
	}
	b.WriteString("\r\n\tby ")
	b.WriteString(by)
	b.WriteString(" (RESTMAIL) with ")
	b.WriteString(with)
	b.WriteString(" id ")
	b.WriteString(id)
	if forAddr := sanitizeHeaderToken(info.For); forAddr != "" {
		b.WriteString("\r\n\tfor <")
		b.WriteString(forAddr)
		b.WriteString(">")
	}
	b.WriteString("; ")
	// RFC 5322 date-time (day-of-week, day month year, time, numeric zone).
	b.WriteString(ts.Format("Mon, 02 Jan 2006 15:04:05 -0700"))
	b.WriteString("\r\n")
	return b.String()
}

// GenerateQueueID returns a short, unique-enough uppercase-hex identifier for a
// message reception, used as the "id" clause of a Received header. It never
// panics on the mail-accept path: a (practically impossible) crypto/rand failure
// falls back to a timestamp-derived id rather than dropping the message.
func GenerateQueueID() string {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return strings.ToUpper(hex.EncodeToString(b))
}

// sanitizeHeaderToken strips ASCII control characters (any byte < 0x20 —
// including CR and LF — and DEL 0x7f) from a value destined for a header line,
// then trims surrounding spaces. It is the header-injection defense for the
// remote-controlled fields of a Received header.
func sanitizeHeaderToken(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return strings.TrimSpace(s)
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
