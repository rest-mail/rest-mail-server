package middleware

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
)

// auditSink collects the entries an Auditor writes, and the lookups it makes.
type auditSink struct {
	entries []*models.AuditEntry
	lookups []string
}

func (s *auditSink) auditor(tokens *auth.JWTService) *Auditor {
	return NewAuditor(
		func(e *models.AuditEntry) error {
			s.entries = append(s.entries, e)
			return nil
		},
		func(kind, name string) (*uint, error) {
			s.lookups = append(s.lookups, kind+":"+name)
			id := uint(42)
			return &id, nil
		},
		tokens,
	)
}

func (s *auditSink) only(t *testing.T) *models.AuditEntry {
	t.Helper()
	if len(s.entries) != 1 {
		t.Fatalf("%d audit entries, want exactly one", len(s.entries))
	}
	return s.entries[0]
}

// errorWith answers with the API's standard error body.
func errorWith(status int, code string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeError(w, status, code, "refused")
	}
}

// assertNoSecret fails if a secret appears anywhere in an entry.
func assertNoSecret(t *testing.T, e *models.AuditEntry, secrets ...string) {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range secrets {
		if strings.Contains(string(b), s) {
			t.Fatalf("the entry records a secret %q: %s", s, b)
		}
	}
}

func TestAudit_FailedSignInRecordsTheIdentifierAndNoPassword(t *testing.T) {
	sink := &auditSink{}
	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	var handlerSaw string
	r.With(sink.auditor(nil).Audit("auth.sign_in", AuditSignIn())).Post("/api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		handlerSaw = string(b)
		errorWith(http.StatusUnauthorized, "unauthorized")(w, r)
	})

	body := `{"username":"admin","password":"hunter2-correct-horse","totp_code":"123456"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	req.Header.Set("User-Agent", "restmail-cli/1.0")
	req.RemoteAddr = "198.51.100.7:51000"
	r.ServeHTTP(httptest.NewRecorder(), req)

	if handlerSaw != body {
		t.Fatalf("the handler read %q, want the whole body", handlerSaw)
	}
	e := sink.only(t)
	if e.Action != "auth.sign_in" || e.Outcome != models.AuditOutcomeDenied || e.Status != http.StatusUnauthorized || e.ErrorCode != "unauthorized" {
		t.Errorf("entry: %+v", e)
	}
	if e.ActorKind != models.AuditActorAdmin || e.ActorName != "admin" || e.ActorID != nil {
		t.Errorf("actor = %s/%v/%q, want admin, no id, \"admin\"", e.ActorKind, e.ActorID, e.ActorName)
	}
	if len(sink.lookups) != 0 {
		t.Errorf("a failed sign-in looked up %v", sink.lookups)
	}
	if e.Route != "/api/v1/auth/login" || e.Method != http.MethodPost || e.IP != "198.51.100.7" ||
		e.UserAgent != "restmail-cli/1.0" || e.RequestID == "" {
		t.Errorf("request context: %+v", e)
	}
	assertNoSecret(t, e, "hunter2-correct-horse", "123456")
}

func TestAudit_SuccessfulSignInLooksUpTheMailbox(t *testing.T) {
	sink := &auditSink{}
	h := sink.auditor(nil).Audit("auth.sign_in", AuditSignIn())(okHandler)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
		strings.NewReader(`{"email":"chris@example.com","password":"secret-pass"}`))
	h.ServeHTTP(httptest.NewRecorder(), req)

	e := sink.only(t)
	if e.Outcome != models.AuditOutcomeSuccess || e.ActorKind != models.AuditActorMailbox ||
		e.ActorName != "chris@example.com" || e.ActorID == nil || *e.ActorID != 42 {
		t.Errorf("entry: %+v", e)
	}
	if len(sink.lookups) != 1 || sink.lookups[0] != "mailbox:chris@example.com" {
		t.Errorf("lookups = %v", sink.lookups)
	}
	assertNoSecret(t, e, "secret-pass")
}

// newAdminGroup is the admin route group as routes.go builds it: the JWT
// middleware, then the audit of every change.
func newAdminGroup(sink *auditSink, jwtSvc *auth.JWTService) http.Handler {
	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Group(func(r chi.Router) {
		r.Use(JWTMiddleware(jwtSvc))
		r.Use(sink.auditor(jwtSvc).Audit("admin.change", AuditWritesOnly()))
		r.Get("/api/v1/admin/domains", okHandler)
		r.Delete("/api/v1/admin/domains/{id}/allowlist/{eid}", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
		r.Patch("/api/v1/admin/domains/{id}", errorWith(http.StatusForbidden, "forbidden"))
	})
	return r
}

func TestAudit_AdminChangeRecordsRouteParamsAndOutcome(t *testing.T) {
	jwtSvc := newTestJWTService(5 * time.Minute)
	pair, err := jwtSvc.GenerateAdminTokenPair(3, "ops", []string{"domains:write"})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := jwtSvc.ValidateAccessToken(pair.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	sink := &auditSink{}
	router := newAdminGroup(sink, jwtSvc)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/domains/7/allowlist/19", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	router.ServeHTTP(httptest.NewRecorder(), req)

	e := sink.only(t)
	if e.Action != "admin.change" || e.Route != "/api/v1/admin/domains/{id}/allowlist/{eid}" || e.Method != http.MethodDelete {
		t.Errorf("entry: %+v", e)
	}
	if len(e.Target) != 2 || e.Target["id"] != "7" || e.Target["eid"] != "19" {
		t.Errorf("target = %v, want the route's params", e.Target)
	}
	if e.Outcome != models.AuditOutcomeSuccess || e.Status != http.StatusNoContent || e.ErrorCode != "" {
		t.Errorf("outcome %s, status %d, error %q", e.Outcome, e.Status, e.ErrorCode)
	}
	if e.ActorKind != models.AuditActorAdmin || e.ActorID == nil || *e.ActorID != 3 || e.ActorName != "ops" {
		t.Errorf("actor = %s/%v/%q", e.ActorKind, e.ActorID, e.ActorName)
	}
	if claims.ID == "" || e.SessionID != claims.ID {
		t.Errorf("session = %q, want the access token's jti %q", e.SessionID, claims.ID)
	}
	assertNoSecret(t, e, pair.AccessToken)

	// A refused change is recorded too, with its error code.
	sink.entries = nil
	req = httptest.NewRequest(http.MethodPatch, "/api/v1/admin/domains/7", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	router.ServeHTTP(httptest.NewRecorder(), req)
	e = sink.only(t)
	if e.Outcome != models.AuditOutcomeDenied || e.Status != http.StatusForbidden || e.ErrorCode != "forbidden" || e.Target["id"] != "7" {
		t.Errorf("refused change: %+v", e)
	}
}

func TestAudit_AdminGetIsNotRecorded(t *testing.T) {
	jwtSvc := newTestJWTService(5 * time.Minute)
	pair, err := jwtSvc.GenerateAdminTokenPair(3, "ops", []string{"domains:read"})
	if err != nil {
		t.Fatal(err)
	}
	sink := &auditSink{}
	router := newAdminGroup(sink, jwtSvc)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/domains", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	if len(sink.entries) != 0 {
		t.Fatalf("a GET was recorded: %+v", sink.entries[0])
	}
}

func TestAudit_LogoutAndRefreshIdentifyTheRefreshToken(t *testing.T) {
	jwtSvc := newTestJWTService(5 * time.Minute)
	pair, err := jwtSvc.GenerateTokenPair(12, "chris@example.com", 4)
	if err != nil {
		t.Fatal(err)
	}
	sink := &auditSink{}
	a := sink.auditor(jwtSvc)
	logout := a.Audit("auth.sign_out", AuditRefreshToken(), AuditIdentifiedOnly())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))

	// A native client sends its refresh token in the body.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout",
		strings.NewReader(`{"refresh_token":"`+pair.RefreshToken+`"}`))
	logout.ServeHTTP(httptest.NewRecorder(), req)
	e := sink.only(t)
	if e.ActorKind != models.AuditActorMailbox || e.ActorID == nil || *e.ActorID != 12 ||
		e.ActorName != "chris@example.com" || e.SessionID != pair.RefreshJTI {
		t.Errorf("logout entry: %+v", e)
	}
	assertNoSecret(t, e, pair.RefreshToken)

	// A browser sends it as the cookie.
	sink.entries = nil
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: auth.RefreshCookieName, Value: pair.RefreshToken})
	logout.ServeHTTP(httptest.NewRecorder(), req)
	if e := sink.only(t); e.SessionID != pair.RefreshJTI {
		t.Errorf("cookie logout entry: %+v", e)
	}

	// A logout naming no session changes nothing and is not recorded.
	sink.entries = nil
	logout.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil))
	logout.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout",
		strings.NewReader(`{"refresh_token":"not-a-token"}`)))
	if len(sink.entries) != 0 {
		t.Fatalf("an anonymous logout was recorded: %+v", sink.entries[0])
	}

	// A refresh is recorded only when it fails.
	sink.entries = nil
	refresh := func(status int) http.Handler {
		return a.Audit("auth.refresh", AuditRefreshToken(), AuditFailuresOnly())(errorWith(status, "unauthorized"))
	}
	refreshReq := func() *http.Request {
		return httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh",
			strings.NewReader(`{"refresh_token":"`+pair.RefreshToken+`"}`))
	}
	refresh(http.StatusOK).ServeHTTP(httptest.NewRecorder(), refreshReq())
	if len(sink.entries) != 0 {
		t.Fatalf("a successful refresh was recorded: %+v", sink.entries[0])
	}
	refresh(http.StatusUnauthorized).ServeHTTP(httptest.NewRecorder(), refreshReq())
	if e := sink.only(t); e.Outcome != models.AuditOutcomeDenied || e.ActorID == nil || *e.ActorID != 12 {
		t.Errorf("failed refresh entry: %+v", e)
	}
}

func TestAudit_BodyFieldsGoToTheTarget(t *testing.T) {
	sink := &auditSink{}
	r := chi.NewRouter()
	r.With(sink.auditor(nil).Audit("accounts.link", AuditBodyFields("address"))).Post("/api/v1/accounts", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Address, Password string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Password != "linked-secret" {
			t.Errorf("the handler did not get the body: %v %+v", err, req)
		}
		w.WriteHeader(http.StatusCreated)
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/accounts",
		strings.NewReader(`{"address":"other@example.com","password":"linked-secret"}`))
	r.ServeHTTP(httptest.NewRecorder(), req)

	e := sink.only(t)
	if len(e.Target) != 1 || e.Target["address"] != "other@example.com" {
		t.Errorf("target = %v", e.Target)
	}
	assertNoSecret(t, e, "linked-secret")
}

func TestAudit_PanicIsRecordedAsAnError(t *testing.T) {
	sink := &auditSink{}
	h := chimw.Recoverer(sink.auditor(nil).Audit("admin.change")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/x", nil))
	if e := sink.only(t); e.Outcome != models.AuditOutcomeError || e.Status != http.StatusInternalServerError {
		t.Errorf("entry: %+v", e)
	}
}

// When the entry cannot be written, the request's result stands.
func TestAudit_WriteFailureLeavesTheResponse(t *testing.T) {
	a := NewAuditor(func(*models.AuditEntry) error { return errors.New("database down") }, nil, nil)
	rr := httptest.NewRecorder()
	a.Audit("admin.change")(okHandler).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/x", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "ok" {
		t.Fatalf("response %d %q", rr.Code, rr.Body.String())
	}
}

func TestAuditOutcome(t *testing.T) {
	for status, want := range map[int]string{
		200: models.AuditOutcomeSuccess, 201: models.AuditOutcomeSuccess, 204: models.AuditOutcomeSuccess,
		401: models.AuditOutcomeDenied, 403: models.AuditOutcomeDenied, 429: models.AuditOutcomeDenied,
		400: models.AuditOutcomeFailed, 404: models.AuditOutcomeFailed, 409: models.AuditOutcomeFailed, 422: models.AuditOutcomeFailed,
		500: models.AuditOutcomeError, 503: models.AuditOutcomeError,
	} {
		if got := auditOutcome(status); got != want {
			t.Errorf("%d: %s, want %s", status, got, want)
		}
	}
}

// The body is put back whole even when it is too large to read for fields.
func TestPeekBody_LargeBodyPassesThroughUnread(t *testing.T) {
	big := strings.Repeat("x", auditBodyLimit+100)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(big))
	if got := peekBody(req, auditBodyLimit); got != nil {
		t.Fatalf("peeked %d bytes of an oversized body", len(got))
	}
	rest, _ := io.ReadAll(req.Body)
	if string(rest) != big {
		t.Fatalf("the handler would read %d bytes, want %d", len(rest), len(big))
	}
}
