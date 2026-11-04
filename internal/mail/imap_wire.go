package mail

import "github.com/rest-mail/go-imap/pkg/imap/wireform"

// IMAPWireSize is the octet count an IMAP FETCH of the whole message sends, which
// RFC822.SIZE must report (RFC 3501 section 2.3.4). A session that has not enabled
// UTF-8 is sent the downgraded headers and the engine's 7-bit conversion of what
// remains (section 7.4.2); one that has is sent the conversion of the stored raw.
// A message that is already 7-bit measures len(raw).
func IMAPWireSize(raw string, utf8Session bool) int {
	if !utf8Session {
		raw = DowngradeHeaders(raw)
	}
	return len(wireform.Convert(raw, utf8Session))
}
