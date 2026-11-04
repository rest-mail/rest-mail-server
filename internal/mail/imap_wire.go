package mail

import "github.com/rest-mail/go-imap/pkg/imap/wireform"

// IMAPWireSize is the octet count an IMAP FETCH of the whole message sends, which
// RFC822.SIZE must report (RFC 3501 section 2.3.4). A session that has not enabled
// UTF-8 is sent the downgraded headers and the engine's 7-bit conversion of what
// remains (section 7.4.2); one that has is sent the conversion of the stored raw.
// A message that is already 7-bit measures len(raw). Nothing here builds the
// message: the conversion runs into a counter.
func IMAPWireSize(raw string, utf8Session bool) int {
	if utf8Session {
		return wireform.Size(raw, true)
	}
	header, sep, body := splitHeader(raw)
	if !containsNonASCII(header) {
		return wireform.Size(raw, false)
	}
	return downgradedWireSize(header, sep, body)
}

// downgradedWireSize is the conversion's count of the downgraded header and the body.
func downgradedWireSize(header, sep, body string) int {
	block, dropped, ok := measuredBlock(header, sep)
	if !ok {
		return wireform.JoinedSize(downgradedBlock(header, sep), body, false)
	}
	return wireform.JoinedSize(block, body, false) + dropped
}

// WireSizes are the octet counts a gateway reports for a stored message.
type WireSizes struct {
	// Downgraded is DowngradedSize, and POP3UTF8 is POP3WireSize.
	Downgraded, POP3UTF8 int
	// IMAP and IMAPUTF8 are IMAPWireSize without and with UTF-8.
	IMAP, IMAPUTF8 int
}

// MeasureWireSizes is every count at once.
func MeasureWireSizes(raw string) WireSizes {
	return WireSizes{
		Downgraded: DowngradedSize(raw),
		POP3UTF8:   POP3WireSize(raw),
		IMAP:       IMAPWireSize(raw, false),
		IMAPUTF8:   IMAPWireSize(raw, true),
	}
}
