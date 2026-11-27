package pop3

import "io"

// StreamMailbox is an optional [Mailbox] extension that lets RETR and TOP send a
// message without holding it whole. When the Mailbox implements it, RETR and TOP
// call OpenMessage instead of [Mailbox.Retrieve] and read the message as they
// send it, with the same line-ending canonicalization, byte-stuffing and "."
// terminator, so no octet on the wire differs from what Retrieve's bytes would
// have produced. RETR reads the message twice: once to count the octets its
// "+OK <n> octets" reply advertises, then to send them. TOP reads it once, and
// stops after the last body line it sends.
type StreamMailbox interface {
	Mailbox
	// OpenMessage opens the octets [Mailbox.Retrieve] returns for uid, exactly
	// those. Errors mean what they mean for Retrieve: [ErrUTF8Required] gets
	// [UTF8], and any other error "-ERR Failed to retrieve message". The server
	// calls Close however the command ends, including when the client goes away
	// or a handler panics; Close releases whatever the message holds, such as a
	// temporary file.
	OpenMessage(uid string) (StoredMessage, error)
}

// StoredMessage is a message [StreamMailbox.OpenMessage] opened: Size octets
// readable at any offset, any number of times, until Close.
type StoredMessage interface {
	io.ReaderAt
	io.Closer
	// Size is the message's length in octets as stored, before RETR's
	// canonicalization; it is not the RFC 1939 §11 count.
	Size() int64
}
