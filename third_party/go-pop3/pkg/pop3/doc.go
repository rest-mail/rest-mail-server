// Package pop3 holds the types a POP3 server and its mail store share:
// the Backend and Mailbox interfaces, Message values, optional backend
// interfaces, error sentinels and the per-IP Limiter.
//
// The protocol engine lives in [github.com/rest-mail/go-pop3/pkg/pop3/server].
// Both packages use only the standard library. A Backend may use a database,
// filesystem or remote API; every Mailbox method is scoped to one authenticated
// user's maildrop. Messages returns a session snapshot in oldest-first order,
// whose positions plus one become POP3 message numbers. Its UIDs must persist
// across sessions, and its sizes must count the canonical CRLF form served by
// Retrieve; [OctetCount] computes that count.
//
// Optional interfaces turn on SASL PLAIN ([SASLBackend]), UTF8 ([UTF8Backend]),
// LOGIN-DELAY ([LoginDelayBackend] and [LoginDelayMailbox]) and the informational
// IMPLEMENTATION capability ([ImplementationBackend]). A backend implementing
// only Backend keeps the base USER/PASS behavior.
package pop3
