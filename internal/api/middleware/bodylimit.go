package middleware

import "net/http"

// MaxBodyBytes returns middleware that caps the request body at limit bytes,
// bounding how much a handler can buffer. It is used both by the message-delivery
// routes (OSI-7, config.InternalDeliveryBodyLimit) and across the authenticated
// API surface (#184, config.APIMaxBodyBytes) so an unbounded JSON upload cannot
// exhaust memory. The delivery limit is a multiple of the configured
// SMTP_MAX_MESSAGE_SIZE plus fixed scaffolding headroom, so the cap is always
// ABOVE a legitimate max-size message (which is never rejected) while a
// runaway/unbounded upload cannot buffer without limit.
//
// Enforcement is two-layered:
//
//   - A declared Content-Length over the limit is a fast-fail: the request is
//     rejected with 413 before any body byte is read. A client may only make its
//     situation worse by over-declaring Content-Length, so trusting an
//     over-the-limit value here is safe.
//   - The body is then wrapped in http.MaxBytesReader, which enforces on the
//     ACTUAL bytes read. This is the real guard: a lying (short or absent)
//     Content-Length cannot bypass it — the next Read past the limit fails and
//     the handler surfaces that as a 4xx rejection.
//
// limit <= 0 disables the cap (leaves the body untouched).
func MaxBodyBytes(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if limit > 0 {
				if r.ContentLength > limit {
					writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "Request body too large")
					return
				}
				if r.Body != nil {
					r.Body = http.MaxBytesReader(w, r.Body, limit)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
