// Package message reads a stored RFC 5322 message for the server: its header
// and text sections, numeric MIME section paths, BODY and BODYSTRUCTURE and
// ENVELOPE (RFC 3501 §7.4.2), BINARY sections with their transfer encoding
// removed (RFC 3516), and the decoded text SEARCH matches against. MIME
// nesting is bounded at depth 64.
package message
