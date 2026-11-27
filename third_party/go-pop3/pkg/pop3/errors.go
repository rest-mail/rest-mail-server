package pop3

import "errors"

// ErrInUse reports that valid credentials were accepted but the user's maildrop
// is already in use by another session (RFC 2449 §8.1.2). Backend.Authenticate or
// Mailbox.Messages may return it, directly or wrapped, only after verifying the
// credentials. The session replies with -ERR [IN-USE], stays in AUTHORIZATION
// and does not count a credential failure against the Limiter.
//
// The backend remains responsible for acquiring and releasing maildrop locks;
// the POP3 engine does not provide locking itself.
var ErrInUse = errors.New("maildrop already in use")

// ErrLoginDelay reports that valid credentials were accepted but the user
// logged in too recently to be allowed in again (RFC 2449 §8.1.1).
// Backend.Authenticate may return it, directly or wrapped, only after verifying
// the credentials. The session replies to PASS with -ERR [LOGIN-DELAY], stays in
// AUTHORIZATION and does not count a credential failure against the Limiter.
// USER never receives it, so the reply does not reveal whether an account
// exists before the password is checked.
//
// The backend remains responsible for tracking login times and choosing the
// delay; the POP3 engine does not enforce one itself.
var ErrLoginDelay = errors.New("login delay has not expired")

// ErrSystemTemporary reports a system failure likely to resolve without user
// intervention, such as an unavailable authentication service (RFC 3206 §4).
// Backend.Authenticate or Mailbox.Messages may return it directly or wrapped.
// PASS replies with -ERR [SYS/TEMP] and stays in AUTHORIZATION. An Authenticate
// failure neither records nor clears credential-failure history and does not
// check the ban list: the credentials may not have been verified. Partial
// results and private error details are never sent to the client.
var ErrSystemTemporary = errors.New("temporary system failure")

// ErrSystemPermanent reports a system failure requiring intervention, such as
// a corrupted maildrop or invalid service configuration (RFC 3206 §4). It has
// the same contract as ErrSystemTemporary but PASS replies with -ERR [SYS/PERM].
// These errors classify system failures only, never invalid credentials.
var ErrSystemPermanent = errors.New("permanent system failure")

// ErrUTF8Required reports that a message cannot be served outside UTF-8 mode
// (RFC 6856 §5): it has raw UTF-8 headers and the [Mailbox] offers no
// downgraded form. Mailbox.Retrieve may return it, directly or wrapped, to a
// session whose login reported utf8=false. When the Backend is a [UTF8Backend]
// and the client has not issued UTF8, RETR and TOP reply with -ERR [UTF8] and
// mark nothing seen; otherwise the client could not act on the code, so the
// reply is the plain retrieval failure.
var ErrUTF8Required = errors.New("message requires UTF-8 mode")
