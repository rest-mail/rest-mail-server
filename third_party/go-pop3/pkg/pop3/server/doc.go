// Package server implements a POP3 (RFC 1939) server engine with no external
// dependencies. The host implements [pop3.Backend] and [pop3.Mailbox] from
// [github.com/rest-mail/go-pop3/pkg/pop3]; this package speaks the protocol.
//
// # Running a server
//
// [New] builds a [Server]. [Server.ListenAndServe] opens the configured [Ports]
// and serves in the background. [Server.Shutdown] stops accepting and waits for
// sessions to drain, bounded by its context; [Server.Close] immediately closes
// live connections. [NewSession] and [Session.Handle] serve a connection the
// host accepted itself.
//
// # TLS and authentication
//
// A non-nil TLS config enables STLS (RFC 2595) and refuses USER/PASS and AUTH
// PLAIN until TLS is active. Implicit TLS requires a config, and ListenAndServe
// checks for a certificate source before opening listeners (RFC 8314). Without
// a config, plaintext authentication is available for local development.
// A nil [pop3.Limiter] becomes [pop3.NopLimiter]; hosts can supply connection
// caps and authentication-failure bans through that interface.
//
// # Messages and capabilities
//
// The engine handles CAPA, STLS, USER/PASS, STAT, LIST, UIDL, RETR, TOP, DELE,
// RSET, NOOP and QUIT, plus optional AUTH PLAIN and UTF8. CAPA (RFC 2449)
// reflects TLS state and optional backend interfaces. RETR and TOP canonicalize
// line endings to CRLF and dot-stuff on the wire. RETR marks unseen mail read;
// TOP never does. DELE marks messages within the session, RSET clears marks,
// and only QUIT commits deletions, as RFC 1939's UPDATE state requires.
package server
