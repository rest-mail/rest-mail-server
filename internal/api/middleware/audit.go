package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
)

// auditTeeLimit is how much of a response the audit middleware keeps, to read
// the error code (or, after a reset, whose session started).
const auditTeeLimit = 4 << 10

// auditBodyLimit is how much of a request body the audit middleware reads for
// the fields it records. A larger body is passed on untouched and unread.
const auditBodyLimit = 16 << 10

// AuditAppend writes one entry to the audit log: audit.Append, in production.
type AuditAppend func(*models.AuditEntry) error

// AuditLookup finds the id of the account a sign-in named, by kind and name:
// audit.ActorID, in production. Nil when there is none.
type AuditLookup func(kind, name string) (*uint, error)

// Auditor records requests in the audit log, at the edge: after the handler
// has run it takes the route, the URL parameters, the status and the error
// code from the response, and the actor from the token's claims, so handlers
// need not know they are audited.
//
// It never records a password or a token. Where a route's body tells who is
// acting, it reads only the fields named below, and leaves the body for the
// handler as it found it.
//
// If writing the entry fails, it logs the whole entry with slog.Error and the
// request's result stands.
type Auditor struct {
	appendEntry AuditAppend
	lookup      AuditLookup
	tokens      *auth.JWTService
}

// NewAuditor builds an Auditor. tokens identifies the actor of a logout or a
// refresh from the refresh token presented.
func NewAuditor(appendEntry AuditAppend, lookup AuditLookup, tokens *auth.JWTService) *Auditor {
	return &Auditor{appendEntry: appendEntry, lookup: lookup, tokens: tokens}
}

// AuditOption adjusts what Audit records for a route.
type AuditOption func(*auditConfig)

type auditConfig struct {
	writesOnly     bool
	failuresOnly   bool
	signIn         bool
	refreshToken   bool
	startedSession bool
	identifiedOnly bool
	bodyFields     []string
	responseFields []responseField
}

type responseField struct{ field, key string }

func (c *auditConfig) readsBody() bool {
	return c.signIn || c.refreshToken || len(c.bodyFields) > 0
}

// AuditWritesOnly passes GET, HEAD and OPTIONS requests through unrecorded.
func AuditWritesOnly() AuditOption { return func(c *auditConfig) { c.writesOnly = true } }

// AuditFailuresOnly records only requests that do not succeed.
func AuditFailuresOnly() AuditOption { return func(c *auditConfig) { c.failuresOnly = true } }

// AuditSignIn takes the actor from the body's username (an admin) or email (a
// mailbox), and nothing else from it; on success it looks up the account's id.
func AuditSignIn() AuditOption { return func(c *auditConfig) { c.signIn = true } }

// AuditRefreshToken takes the actor from the refresh token presented, in the
// restmail_refresh cookie or the body's refresh_token.
func AuditRefreshToken() AuditOption { return func(c *auditConfig) { c.refreshToken = true } }

// AuditStartedSession takes the actor, on success, from the admin session the
// response started (a reset names no account until it succeeds).
func AuditStartedSession() AuditOption { return func(c *auditConfig) { c.startedSession = true } }

// AuditIdentifiedOnly records nothing when the actor is unknown: for a route
// anyone may call, unthrottled, that changes nothing unless they are someone.
func AuditIdentifiedOnly() AuditOption { return func(c *auditConfig) { c.identifiedOnly = true } }

// AuditBodyFields adds the named string fields of the body to the target.
func AuditBodyFields(names ...string) AuditOption {
	return func(c *auditConfig) { c.bodyFields = append(c.bodyFields, names...) }
}

// AuditResponseField adds field, a string or a number of a successful
// response's data, to the target as key: the id of what a request created.
// Never name a secret: the target is recorded as it is.
func AuditResponseField(field, key string) AuditOption {
	return func(c *auditConfig) { c.responseFields = append(c.responseFields, responseField{field, key}) }
}

// Audit records each request to the route it wraps as one entry with the
// given action. Put it after the JWT middleware, so it sees the claims, and on
// a route anyone may call, after its rate limit, so nobody can write to the
// log faster than that.
func (a *Auditor) Audit(action string, opts ...AuditOption) func(http.Handler) http.Handler {
	var cfg auditConfig
	for _, o := range opts {
		o(&cfg)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfg.writesOnly && (r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions) {
				next.ServeHTTP(w, r)
				return
			}
			started := time.Now()
			var body []byte
			if cfg.readsBody() {
				body = peekBody(r, auditBodyLimit)
			}
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			tee := &cappedBuffer{limit: auditTeeLimit}
			ww.Tee(tee)
			handled := false
			defer func() {
				status := ww.Status()
				if !handled {
					// The handler panicked; Recoverer answers 500.
					status = http.StatusInternalServerError
				}
				a.record(action, &cfg, r, status, tee.buf.Bytes(), body, started)
			}()
			next.ServeHTTP(ww, r)
			handled = true
		})
	}
}

// newEntry starts the entry for request r, answered with status: the action,
// the outcome, the route and its URL parameters, and where it came from.
func newEntry(action string, r *http.Request, status int, started time.Time) *models.AuditEntry {
	e := &models.AuditEntry{
		OccurredAt: started,
		ActorKind:  models.AuditActorAnonymous,
		Action:     action,
		Outcome:    auditOutcome(status),
		Method:     r.Method,
		Status:     status,
		Target:     models.AuditTarget{},
		IP:         clientIP(r),
		UserAgent:  r.UserAgent(),
		RequestID:  chimw.GetReqID(r.Context()),
	}
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		e.Route = rctx.RoutePattern()
		for i, k := range rctx.URLParams.Keys {
			if k != "" && k != "*" {
				e.Target[k] = rctx.URLParams.Values[i]
			}
		}
	}
	return e
}

func (a *Auditor) record(action string, cfg *auditConfig, r *http.Request, status int, resp, body []byte, started time.Time) {
	if status == 0 {
		status = http.StatusOK // nothing written: net/http sends 200
	}
	outcome := auditOutcome(status)
	if cfg.failuresOnly && outcome == models.AuditOutcomeSuccess {
		return
	}
	e := newEntry(action, r, status, started)
	if status >= 400 {
		var er ErrorResponse
		if json.Unmarshal(resp, &er) == nil {
			e.ErrorCode = er.Error.Code
		}
	}

	if claims := GetClaims(r); claims != nil {
		actorFromClaims(e, claims)
	}
	switch {
	case cfg.signIn:
		var who struct {
			Email    string `json:"email"`
			Username string `json:"username"`
		}
		decodeFirst(body, &who)
		// The username wins, as it does at login.
		if who.Username != "" {
			a.namedActor(e, models.AuditActorAdmin, who.Username)
		} else if who.Email != "" {
			a.namedActor(e, models.AuditActorMailbox, who.Email)
		}
	case cfg.refreshToken:
		if claims := a.refreshClaims(r, body); claims != nil {
			actorFromClaims(e, claims)
		}
	case cfg.startedSession:
		if outcome == models.AuditOutcomeSuccess {
			var session struct {
				Data struct {
					User struct {
						Email string `json:"email"` // an admin's username
					} `json:"user"`
				} `json:"data"`
			}
			if json.Unmarshal(resp, &session) == nil && session.Data.User.Email != "" {
				a.namedActor(e, models.AuditActorAdmin, session.Data.User.Email)
			}
		}
	}
	if len(cfg.bodyFields) > 0 {
		var fields map[string]json.RawMessage
		decodeFirst(body, &fields)
		for _, name := range cfg.bodyFields {
			var v string
			if _, taken := e.Target[name]; !taken && json.Unmarshal(fields[name], &v) == nil {
				e.Target[name] = v
			}
		}
	}
	if len(cfg.responseFields) > 0 && outcome == models.AuditOutcomeSuccess {
		var created struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		decodeFirst(resp, &created)
		for _, f := range cfg.responseFields {
			if _, taken := e.Target[f.key]; taken {
				continue
			}
			var v any
			if json.Unmarshal(created.Data[f.field], &v) != nil {
				continue
			}
			switch v := v.(type) {
			case string:
				e.Target[f.key] = v
			case float64:
				e.Target[f.key] = strconv.FormatFloat(v, 'f', -1, 64)
			}
		}
	}
	if cfg.identifiedOnly && e.ActorKind == models.AuditActorAnonymous {
		return
	}

	a.write(e)
}

// write appends e; if that fails it logs the whole entry with slog.Error.
func (a *Auditor) write(e *models.AuditEntry) {
	if err := a.appendEntry(e); err != nil {
		slog.Error("failed to write an audit entry", "error", err, "entry", e)
	}
}

// AppPasswordRefused records an app password AppPasswords refused, as one
// auth.app_password entry: the address given, why it was refused, the public
// id presented, and the credential when its secret matched. It never records
// the password.
func (a *Auditor) AppPasswordRefused(r *http.Request, address string, refusal *auth.AppPasswordRefusal) {
	e := newEntry(AuditActionAppPassword, r, http.StatusUnauthorized, time.Now())
	e.ErrorCode = "unauthorized"
	e.ActorKind, e.ActorName = models.AuditActorMailbox, address
	e.CredentialID = refusal.CredentialID
	e.Target["reason"] = refusal.Reason
	if refusal.PublicID != "" {
		e.Target["public_id"] = refusal.PublicID
	}
	a.write(e)
}

// AuditActionAppPassword is the action of a refused app password.
const AuditActionAppPassword = "auth.app_password"

// actorFromClaims makes the token's account the actor, and its jti the session.
func actorFromClaims(e *models.AuditEntry, claims *auth.Claims) {
	var id uint
	if claims.UserType == "admin" {
		e.ActorKind, e.ActorName, id = models.AuditActorAdmin, claims.Username, claims.AdminUserID
	} else {
		e.ActorKind, e.ActorName, id = models.AuditActorMailbox, claims.Email, claims.MailboxID
	}
	e.ActorID = &id
	e.SessionID = claims.ID
	if claims.CredentialID != 0 {
		credentialID := claims.CredentialID
		e.CredentialID = &credentialID
	}
}

// namedActor makes the account a request names the actor; its id is looked up
// only once the request has succeeded, which proves it.
func (a *Auditor) namedActor(e *models.AuditEntry, kind, name string) {
	e.ActorKind, e.ActorName = kind, name
	if e.Outcome != models.AuditOutcomeSuccess || a.lookup == nil {
		return
	}
	id, err := a.lookup(kind, name)
	if err != nil {
		slog.Warn("audit: could not look up the actor", "kind", kind, "name", name, "error", err)
		return
	}
	e.ActorID = id
}

// refreshClaims reads the refresh token presented the way the auth handler
// does (the cookie wins over the body) and returns its claims if it is valid.
func (a *Auditor) refreshClaims(r *http.Request, body []byte) *auth.Claims {
	if a.tokens == nil {
		return nil
	}
	token := ""
	if c, err := r.Cookie(auth.RefreshCookieName); err == nil && c.Value != "" {
		token = c.Value
	} else {
		var b struct {
			RefreshToken string `json:"refresh_token"`
		}
		decodeFirst(body, &b)
		token = b.RefreshToken
	}
	if token == "" {
		return nil
	}
	claims, err := a.tokens.ValidateRefreshToken(token)
	if err != nil {
		return nil
	}
	return claims
}

// auditOutcome maps a status to an outcome: 2xx success; 401, 403 and 429
// denied; any other 4xx failed; 5xx error.
func auditOutcome(status int) string {
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden, status == http.StatusTooManyRequests:
		return models.AuditOutcomeDenied
	case status >= 500:
		return models.AuditOutcomeError
	case status >= 400:
		return models.AuditOutcomeFailed
	default:
		return models.AuditOutcomeSuccess
	}
}

// decodeFirst decodes the first JSON value in b into v, as the handlers'
// json.Decoder does; anything else in b is ignored, and so is a failure.
func decodeFirst(b []byte, v any) {
	if len(b) > 0 {
		_ = json.NewDecoder(bytes.NewReader(b)).Decode(v)
	}
}

// peekBody reads up to limit bytes of the request body and puts them back in
// front of the rest, so the handler reads the body it would have. It returns
// what it read, or nil if the body is larger than limit or could not be read.
func peekBody(r *http.Request, limit int64) []byte {
	if r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	r.Body = peekedBody{Reader: io.MultiReader(bytes.NewReader(buf), r.Body), Closer: r.Body}
	if err != nil || int64(len(buf)) > limit {
		return nil
	}
	return buf
}

type peekedBody struct {
	io.Reader
	io.Closer
}

// cappedBuffer keeps the first limit bytes written to it and drops the rest,
// never failing a write: it is a response tee.
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.limit - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}
