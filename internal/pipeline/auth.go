package pipeline

import (
	"context"
	"strings"
	"sync"
)

// AuthServID is the authserv-id (RFC 8601 §2.5) of every Authentication-Results
// header this server writes. Incoming Authentication-Results that claim it are
// removed before the pipeline runs (RFC 8601 §5), so a header carrying it in a
// stored message was written by this server.
const AuthServID = "restmail"

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
	Method   AuthMethod `json:"method"`
	Result   string     `json:"result"`
	Property string     `json:"property,omitempty"`
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

// AuthResults are the verdicts the authentication filters reach for one
// message, in the order they were recorded. Engine.Execute puts a fresh one on
// the context of every run; filters record into it and read it back. It is the
// only place a verdict travels: no filter reads another's verdict from
// Authentication-Results text. A nil *AuthResults records nothing and holds
// nothing. Safe for concurrent use, since each filter runs on its own goroutine.
type AuthResults struct {
	mu      sync.Mutex
	results []AuthResult
}

// Add records verdicts.
func (a *AuthResults) Add(rs ...AuthResult) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.results = append(a.results, rs...)
}

// List returns a copy of every verdict recorded so far.
func (a *AuthResults) List() []AuthResult {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]AuthResult(nil), a.results...)
}

// Find returns the verdicts recorded for method with the given property.
func (a *AuthResults) Find(method AuthMethod, property string) []AuthResult {
	var out []AuthResult
	for _, r := range a.List() {
		if r.Method == method && r.Property == property {
			out = append(out, r)
		}
	}
	return out
}

const authResultsContextKey contextKey = "pipeline_auth_results"

// WithAuthResults returns a context carrying a.
func WithAuthResults(ctx context.Context, a *AuthResults) context.Context {
	return context.WithValue(ctx, authResultsContextKey, a)
}

// AuthResultsFromContext returns the verdicts of the pipeline run ctx belongs
// to, or nil outside a run.
func AuthResultsFromContext(ctx context.Context) *AuthResults {
	a, _ := ctx.Value(authResultsContextKey).(*AuthResults)
	return a
}

// AuthResultsValue renders verdicts as one Authentication-Results value:
// "restmail; spf=pass smtp.mailfrom=example.com; dkim=pass header.d=example.com".
// With no verdicts it is "restmail; none" (RFC 8601 §2.2).
func AuthResultsValue(rs []AuthResult) string {
	if len(rs) == 0 {
		return AuthServID + "; none"
	}
	parts := make([]string, len(rs))
	for i, r := range rs {
		parts[i] = r.String()
	}
	return AuthServID + "; " + strings.Join(parts, "; ")
}

// AuthResultsHeader renders verdicts as the Authentication-Results header field
// prepended to a delivered message, folded one result to a line and ending in
// CRLF. It is "" when there are no verdicts.
func AuthResultsHeader(rs []AuthResult) string {
	if len(rs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Authentication-Results: " + AuthServID)
	for _, r := range rs {
		b.WriteString(";\r\n\t" + r.String())
	}
	b.WriteString("\r\n")
	return b.String()
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
