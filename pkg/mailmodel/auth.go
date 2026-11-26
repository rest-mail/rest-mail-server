package mailmodel

import "strings"

// AuthMethod is an authentication method, named as in Authentication-Results.
type AuthMethod string

const (
	AuthSPF   AuthMethod = "spf"
	AuthDKIM  AuthMethod = "dkim"
	AuthARC   AuthMethod = "arc"
	AuthDMARC AuthMethod = "dmarc"
)

// The identity an AuthResult authenticated (RFC 8601 §2.7). An ARC result has
// none; its Domain is the most recent sealer, which is not written out.
const (
	PropMailFrom   = "smtp.mailfrom" // SPF, the MAIL FROM identity
	PropHelo       = "smtp.helo"     // SPF, the HELO identity
	PropHeaderD    = "header.d"      // DKIM, the signature's d=
	PropHeaderFrom = "header.from"   // DMARC, the From domain
)

// AuthResult is one verdict an authentication filter reached for the message:
// the method, its result (pass, fail, softfail, neutral, none, temperror,
// permerror), and the domain it authenticated under Property. DKIM records one
// per signature.
type AuthResult struct {
	Method   AuthMethod `json:"method" enums:"spf,dkim,arc,dmarc"`
	Result   string     `json:"result"`
	Property string     `json:"property,omitempty" enums:"smtp.mailfrom,smtp.helo,header.d,header.from"`
	Domain   string     `json:"domain,omitempty"`
	// Selector is a DKIM signature's s= tag, which a DMARC aggregate report
	// names beside its d= (RFC 9990 §3.1.1.12). It stays inside the server.
	Selector string `json:"-"`
}

// String renders r as one Authentication-Results resinfo, such as
// "spf=pass smtp.mailfrom=example.com". The property is written only when the
// domain is a plain host name, so no value can add a result of its own.
func (r AuthResult) String() string {
	s := string(r.Method) + "=" + r.Result
	if r.Property != "" && IsHostName(r.Domain) {
		s += " " + r.Property + "=" + r.Domain
	}
	return s
}

// IsHostName reports whether name is a plain host name: dot-separated labels of
// ASCII letters, digits and hyphens.
func IsHostName(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
