// Package limittest checks the rate-limit refusals the API serves. It is a
// normal (importable) package, like net/http/httptest, so every package that
// drives a 429 holds it to the one documented header instead of its own
// reading of it.
package limittest

import (
	"net/http"
	"strconv"
	"testing"
)

// RetryAfter fails t unless h carries the Retry-After every 429 documents: a
// whole, positive number of seconds (not an HTTP date, a fraction or zero). It
// returns the seconds.
func RetryAfter(t testing.TB, h http.Header) int {
	t.Helper()
	header := h.Get("Retry-After")
	secs, err := strconv.Atoi(header)
	if err != nil || secs < 1 {
		t.Fatalf("Retry-After %q, want whole seconds of at least 1", header)
	}
	return secs
}
