// Package mutf7 encodes and decodes modified UTF-7, the 7-bit form of
// IMAP4rev1 mailbox names (RFC 3501 §5.1.3). A Mailbox names its folders in
// UTF-8, and the server converts at the wire: until the client enables
// UTF8=ACCEPT, names travel in modified UTF-7; after, they are UTF-8
// (RFC 6855 §3).
package mutf7
