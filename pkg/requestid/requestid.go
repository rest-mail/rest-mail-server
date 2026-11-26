// Package requestid is the one ID that follows a message, or an IMAP/POP3
// session, through every service's logs (#1580).
//
// A protocol gateway makes one at each connection, logs it as request_id on
// every line it writes for that connection, and sends it as the X-Request-Id
// header on each call it makes to the API. The API adopts it when the call comes
// from a gateway it verified on the internal listener, and makes its own for
// any other request, so a public client's header is never taken.
package requestid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"

	chimw "github.com/go-chi/chi/v5/middleware"
)

// Header is the HTTP header the ID travels in, the one chi's request IDs use.
const Header = "X-Request-Id"

// Attr is the name of the log attribute that holds the ID in every service.
const Attr = "request_id"

// MaxLen is the longest ID the API adopts.
const MaxLen = 64

// New makes an ID: 16 random bytes, in hex.
func New() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on supported platforms; an ID is for
		// logs only, so a fixed one is better than none.
		return "unavailable"
	}
	return hex.EncodeToString(b[:])
}

// Valid reports whether id may be adopted: 1 to MaxLen characters, each a
// letter, digit, '-', '_', '.' or '/'. Nothing else reaches a log line.
func Valid(id string) bool {
	if id == "" || len(id) > MaxLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.', c == '/':
		default:
			return false
		}
	}
	return true
}

// FromContext returns the request's ID, or "" when it has none. The ID is the
// one chi's GetReqID reads, so audit rows and request lines carry the same one.
func FromContext(ctx context.Context) string {
	return chimw.GetReqID(ctx)
}

// NewContext returns ctx carrying id.
func NewContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, chimw.RequestIDKey, id)
}

// Handler adds request_id to every record logged with a context that carries
// one (slog.InfoContext and the like), so code that has the request's context
// logs its ID without naming it.
type Handler struct{ slog.Handler }

// NewHandler wraps h.
func NewHandler(h slog.Handler) *Handler { return &Handler{Handler: h} }

// Handle implements slog.Handler.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	if id := FromContext(ctx); id != "" {
		r = r.Clone()
		r.AddAttrs(slog.String(Attr, id))
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs implements slog.Handler.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{Handler: h.Handler.WithAttrs(attrs)}
}

// WithGroup implements slog.Handler.
func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{Handler: h.Handler.WithGroup(name)}
}
