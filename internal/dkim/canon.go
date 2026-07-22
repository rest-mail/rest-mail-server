package dkim

import (
	"bytes"
	"crypto"
	"crypto/sha1" //nolint:gosec // rsa-sha1 is a legacy DKIM algorithm receivers must still verify
	"crypto/sha256"
	"errors"
	"net"
	"strconv"
	"strings"
)

// splitMessage normalizes line endings to CRLF and splits a raw RFC 5322
// message into ordered header fields and the body. Line-ending normalization
// (bare LF / lone CR → CRLF) reconstructs the canonical wire form the signer
// hashed, in case an intermediate stored the message with LF-only endings.
func splitMessage(raw []byte) ([]header, string) {
	s := string(raw)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\n", "\r\n")

	var headerBlock string
	body := ""
	if idx := strings.Index(s, "\r\n\r\n"); idx >= 0 {
		headerBlock = s[:idx]
		body = s[idx+4:]
	} else {
		headerBlock = strings.TrimSuffix(s, "\r\n")
	}
	return parseHeaders(headerBlock), body
}

// parseHeaders splits a header block (fields joined by CRLF, no trailing CRLF)
// into ordered header fields, folding continuation lines back into their field.
func parseHeaders(block string) []header {
	if block == "" {
		return nil
	}
	var headers []header
	for _, line := range strings.Split(block, "\r\n") {
		if line == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			// Continuation of the previous field.
			if len(headers) == 0 {
				continue
			}
			last := &headers[len(headers)-1]
			last.raw += "\r\n" + line
			last.value += "\r\n" + line
			continue
		}
		eq := strings.IndexByte(line, ':')
		if eq < 0 {
			continue // not a valid header field
		}
		headers = append(headers, header{
			name:  strings.TrimSpace(line[:eq]),
			value: line[eq+1:],
			raw:   line,
		})
	}
	return headers
}

// canonicalizeHeader canonicalizes a single header field per RFC 6376 §3.4.
// The returned string has NO trailing CRLF — the caller appends one between
// signed headers (and none after the trailing DKIM-Signature).
func canonicalizeHeader(h header, canon string) string {
	if canon == "relaxed" {
		return strings.ToLower(strings.TrimSpace(h.name)) + ":" + relaxHeaderValue(h.value)
	}
	// simple: the field exactly as it appeared.
	return h.raw
}

// relaxHeaderValue applies relaxed value canonicalization: unfold, collapse WSP
// runs to a single SP, and drop leading (post-colon) and trailing WSP.
func relaxHeaderValue(v string) string {
	v = strings.ReplaceAll(v, "\r\n", "") // unfold
	var b strings.Builder
	inWSP := false
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c == ' ' || c == '\t' {
			inWSP = true
			continue
		}
		if inWSP {
			if b.Len() > 0 { // suppress leading WSP
				b.WriteByte(' ')
			}
			inWSP = false
		}
		b.WriteByte(c)
	}
	return b.String() // trailing WSP naturally dropped (never flushed)
}

// canonicalizeBody applies simple or relaxed body canonicalization (RFC 6376
// §3.4.3 / §3.4.4). Input is a CRLF-normalized body.
func canonicalizeBody(body, canon string) string {
	if canon == "relaxed" {
		if body == "" {
			return ""
		}
		lines := strings.Split(body, "\r\n")
		for i, ln := range lines {
			lines[i] = relaxBodyLine(ln)
		}
		for len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		if len(lines) == 0 {
			return ""
		}
		return strings.Join(lines, "\r\n") + "\r\n"
	}
	// simple: strip trailing empty lines, guarantee exactly one trailing CRLF.
	for strings.HasSuffix(body, "\r\n") {
		body = body[:len(body)-2]
	}
	return body + "\r\n"
}

// relaxBodyLine collapses WSP runs within a body line to a single SP (a leading
// run collapses to a single leading SP — it is NOT removed) and strips trailing
// WSP.
func relaxBodyLine(line string) string {
	var b strings.Builder
	inWSP := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c == ' ' || c == '\t' {
			inWSP = true
			continue
		}
		if inWSP {
			b.WriteByte(' ') // leading SP preserved for body (unlike headers)
			inWSP = false
		}
		b.WriteByte(c)
	}
	return b.String()
}

// parseTagList parses a DKIM tag=value list ("k=v; k2=v2") into a map. Keys and
// the ends of values are trimmed; internal FWS in values is preserved (callers
// strip it via stripWSP where it is insignificant, e.g. b=, bh=, p=).
func parseTagList(s string) map[string]string {
	out := map[string]string{}
	for _, seg := range strings.Split(s, ";") {
		eq := strings.IndexByte(seg, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(seg[:eq])
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(seg[eq+1:])
	}
	return out
}

// removeBValue blanks the value of the b= tag in a DKIM-Signature field while
// preserving every other byte (including the "b=" itself), as required before
// canonicalizing the signature header for verification.
func removeBValue(field string) string {
	var b strings.Builder
	i, n := 0, len(field)
	for i < n {
		j := strings.IndexByte(field[i:], ';')
		var seg string
		hasDelim := false
		if j < 0 {
			seg = field[i:]
			i = n
		} else {
			seg = field[i : i+j]
			i = i + j + 1
			hasDelim = true
		}
		if eq := strings.IndexByte(seg, '='); eq >= 0 && strings.EqualFold(strings.TrimSpace(seg[:eq]), "b") {
			b.WriteString(seg[:eq+1]) // keep "b=", drop the value
		} else {
			b.WriteString(seg)
		}
		if hasDelim {
			b.WriteByte(';')
		}
	}
	return b.String()
}

// stripWSP removes all whitespace (SP, TAB, CR, LF) — used on base64 tag values
// that may be folded across lines.
func stripWSP(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\r', '\n':
			return -1
		}
		return r
	}, s)
}

func hashBytes(h crypto.Hash, data []byte) []byte {
	if h == crypto.SHA1 {
		sum := sha1.Sum(data) //nolint:gosec // legacy DKIM algorithm
		return sum[:]
	}
	sum := sha256.Sum256(data)
	return sum[:]
}

func bytesEqual(a, b []byte) bool { return bytes.Equal(a, b) }

func parseUint(s string) (int, error) { return strconv.Atoi(strings.TrimSpace(s)) }

func asDNSError(err error, target **net.DNSError) bool { return errors.As(err, target) }
