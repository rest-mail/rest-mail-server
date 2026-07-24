package middleware

import "net/http"

// MaxBodyBytes returns middleware that caps the request body at limit bytes with
// http.MaxBytesReader, bounding how much a message-delivery handler can buffer
// (OSI-7). The caller passes config.InternalDeliveryBodyLimit, a multiple of the
// configured SMTP_MAX_MESSAGE_SIZE plus fixed scaffolding headroom, so the cap is
// always ABOVE a legitimate max-size message (which is never rejected) while a
// runaway/unbounded upload cannot buffer without limit. A body over the limit
// fails the next Read; the delivery handler surfaces that as a 4xx rejection.
//
// limit <= 0 disables the cap (leaves the body untouched). Content-Length is not
// trusted — MaxBytesReader enforces on the actual bytes read, so a lying header
// cannot bypass it.
func MaxBodyBytes(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if limit > 0 && r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}
