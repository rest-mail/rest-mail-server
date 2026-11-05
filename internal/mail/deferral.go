package mail

// The error codes of a delivery refusal that says why the inbound pipeline held the
// message back. The deliver route answers them with a 451, and the gateway turns that
// into the SMTP reply that says so. /restmail/messages answers them with a 503, and the
// sending queue classifies by them. Any other deferral's code is "deferred".
const (
	// GreylistedRefusal is a message the greylist step held back.
	GreylistedRefusal = "greylisted"
	// RateLimitedRefusal is a message the rate_limit step held back.
	RateLimitedRefusal = "rate_limited"
)
