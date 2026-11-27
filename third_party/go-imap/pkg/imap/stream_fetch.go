package imap

import "io"

// StreamFetcher is the optional interface a [Mailbox] may also implement so that
// FETCH can send a message without holding it whole (#2107). When the concrete
// Mailbox satisfies it, a FETCH whose body items are all the whole message, its
// header or its text — BODY[], BODY[HEADER], BODY[TEXT], RFC822, RFC822.HEADER,
// RFC822.TEXT and BINARY[], with their PEEK and partial forms, and BINARY.SIZE[] —
// calls FetchStream instead of [Mailbox.Fetch] and copies each literal from the
// reader. BODY and RFC822 items are copied only for a message with no 8-bit
// octet and no NUL, which FETCH sends as stored; any other message FETCH sends
// converted (see package wireform), so the server reads the stream back whole
// for them, as Fetch would have returned it. BINARY[] sends the stored octets
// and is always copied. A FETCH that also asks for ENVELOPE, BODYSTRUCTURE, a
// numeric section or a header field list calls Fetch as before. No octet on the
// wire differs from what Fetch would have sent.
type StreamFetcher interface {
	// FetchStream opens the octets [Mailbox.Fetch] returns for uid, exactly those.
	// The server reads the message at any offset, any number of times, until it
	// calls Close, which it always does, however the command ends; Close releases
	// whatever the message holds, such as a temporary file.
	FetchStream(uid uint32) (FetchedMessage, error)
}

// FetchedMessage is a message [StreamFetcher.FetchStream] opened: Size octets
// readable at any offset.
type FetchedMessage interface {
	io.ReaderAt
	io.Closer
	// Size is the message's length in octets.
	Size() int64
}
