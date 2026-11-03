// Package server implements an IMAP4rev1 ([RFC 3501]) server engine with zero
// external dependencies (standard library only).
//
// A host supplies an [imap.Backend] that authenticates users and returns an
// [imap.Mailbox] view over their folders and messages, expressed as neutral
// [imap.Message] values; this package speaks the wire protocol. The types the
// host implements live in package [github.com/rest-mail/go-imap/pkg/imap],
// together with the optional interfaces that turn extensions on.
//
// The commands the engine answers include CAPABILITY, STARTTLS, LOGIN,
// AUTHENTICATE PLAIN, LIST/LSUB, SELECT/EXAMINE, STATUS, FETCH, UID FETCH,
// SEARCH, STORE, COPY, MOVE, EXPUNGE, UID EXPUNGE, CLOSE, UNSELECT ([RFC 3691]),
// ENABLE ([RFC 5161]), APPEND, IDLE ([RFC 2177]) and QUOTA ([RFC 2087]).
//
// # TLS and authentication
//
// A non-nil *tls.Config enables both STARTTLS on the cleartext port and an
// implicit-TLS listener. When TLS is configured, LOGIN is refused on a cleartext
// connection until the client issues STARTTLS, and AUTH=PLAIN is advertised only
// once the connection is protected; with no TLS config at all the server offers
// AUTH=PLAIN in the clear, for development. LOGIN and AUTHENTICATE PLAIN share one
// credential check against [imap.Backend.Authenticate], and each failure is
// reported to the [imap.Limiter] — the per-IP connection and auth-failure guard
// the Server consults. Pass [imap.NopLimiter] (or nil) to impose no limits.
//
// # Running a server
//
// [New] builds a Server; [Server.ListenAndServe] opens the configured
// [Ports] and serves in the background. [Server.Shutdown] stops accepting and
// then blocks until the in-flight sessions drain (or its context deadline
// passes), while [Server.Close] stops at once, force-closing live connections.
// To drive a single already-accepted connection behind your own listener,
// construct a [Session] with [NewSession] and call [Session.Handle].
//
// [RFC 3501]: https://www.rfc-editor.org/rfc/rfc3501
// [RFC 2087]: https://www.rfc-editor.org/rfc/rfc2087
// [RFC 2177]: https://www.rfc-editor.org/rfc/rfc2177
// [RFC 3691]: https://www.rfc-editor.org/rfc/rfc3691
// [RFC 5161]: https://www.rfc-editor.org/rfc/rfc5161
package server
