// Package rfctest is the black-box RFC conformance suite for go-imap. Its tests
// use only the public API of packages imap and server, the way a host would:
// each one drives a server.Session over net.Pipe, as a client would, and names
// the CHECKLIST.md items it covers. Tests that need the server's internals
// live beside it, in package server.
package rfctest
