package smtp

// receivedWith returns the RFC 3848 "with" protocol keyword recorded in the
// Received header for a reception, given the connection's transport security and
// whether the client authenticated. go-smtp always speaks the extended (EHLO)
// protocol, so the base keyword is ESMTP; the S and A suffixes are added for a
// TLS-protected and/or authenticated session:
//
//	ESMTP    plain, unauthenticated (inbound MX, no STARTTLS)
//	ESMTPS   TLS, unauthenticated
//	ESMTPA   authenticated, no TLS
//	ESMTPSA  TLS + authenticated (typical submission)
func receivedWith(isTLS, authenticated bool) string {
	switch {
	case isTLS && authenticated:
		return "ESMTPSA"
	case isTLS:
		return "ESMTPS"
	case authenticated:
		return "ESMTPA"
	default:
		return "ESMTP"
	}
}

// singleRecipient returns the sole recipient of a transaction, or "" when there
// is not exactly one. A Received header's "for" clause is only safe to emit for
// a single-recipient transaction (RFC 5321 §4.4): emitting it for several
// recipients would disclose a Bcc recipient to the others.
func singleRecipient(rcpts []string) string {
	if len(rcpts) == 1 {
		return rcpts[0]
	}
	return ""
}
