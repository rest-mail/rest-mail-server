package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/rest-mail/rest-mail-server/internal/api/limittest"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

const testAppPassword = "rm_AAAAAAAAAAAA_secretsecretsecretsecretsecretsecretsecretsec"

// fakeAppPasswords verifies one app password for one address, as credential 7
// of mailbox 10, and refuses everything else as a wrong secret.
type fakeAppPasswords struct {
	calls   int
	refusal *auth.CredentialRefusal // when set, what every check returns
	err     error                   // when set, the check fails outright
}

func (f *fakeAppPasswords) verify(address, password string) (*auth.Claims, error) {
	f.calls++
	switch {
	case f.err != nil:
		return nil, f.err
	case f.refusal != nil:
		return nil, f.refusal
	case address == "user@example.com" && password == testAppPassword:
		return &auth.Claims{Address: address, AccountID: 10, TokenType: "access", CredentialID: 7, CredentialKind: models.CredentialKindAppPassword}, nil
	}
	return nil, &auth.CredentialRefusal{Reason: auth.CredentialWrongSecret, PublicID: "AAAAAAAAAAAA"}
}

// appPasswordRouter serves POST /api/v1/gateway-sessions behind
// SessionPasswords, answering with the claims it saw and the body it was left.
func appPasswordRouter(f *fakeAppPasswords, sink *auditSink, failures *RateLimitConfig) (http.Handler, *auth.JWTService) {
	jwtSvc := auth.NewJWTService("app-password-test", time.Minute, time.Hour)
	a := NewSessionPasswords(f.verify, sink.auditor(nil).CredentialRefused, failures)
	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.With(a.Authenticate()).Post("/api/v1/gateway-sessions", func(w http.ResponseWriter, r *http.Request) {
		claims := GetClaims(r)
		var body struct {
			Protocol string `json:"protocol"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("X-Credential", strings.Repeat("c", int(claims.CredentialID)))
		w.Header().Set("X-Email", claims.Address)
		w.Header().Set("X-Protocol", body.Protocol)
		w.WriteHeader(http.StatusOK)
	})
	return r, jwtSvc
}

// passwordRequest opens a session with address and password in the body.
func passwordRequest(address, password, ip string) *http.Request {
	b, _ := json.Marshal(map[string]string{"protocol": "imap", "address": address, "password": password})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/gateway-sessions", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":40000"
	req.Header.Set("User-Agent", "Thunderbird/128")
	return req
}

func TestSessionPasswords_RunsAsTheCredential(t *testing.T) {
	f, sink := &fakeAppPasswords{}, &auditSink{}
	router, _ := appPasswordRouter(f, sink, nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, passwordRequest("user@example.com", testAppPassword, "203.0.113.1"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("X-Credential") != strings.Repeat("c", 7) || rr.Header().Get("X-Email") != "user@example.com" {
		t.Errorf("the handler ran without the credential's claims")
	}
	if rr.Header().Get("X-Protocol") != "imap" {
		t.Errorf("the handler was not left the body: protocol %q", rr.Header().Get("X-Protocol"))
	}
	if len(sink.entries) != 0 {
		t.Errorf("a request that succeeded was recorded: %+v", sink.entries)
	}
}

func TestSessionPasswords_RefusalIsRecordedAndUniform(t *testing.T) {
	credentialID := uint(7)
	for _, refusal := range []*auth.CredentialRefusal{
		{Reason: auth.CredentialWrongSecret, PublicID: "AAAAAAAAAAAA"},
		{Reason: auth.CredentialRevoked, PublicID: "AAAAAAAAAAAA", CredentialID: &credentialID},
		{Reason: auth.CredentialWrongPassword},
	} {
		t.Run(refusal.Reason, func(t *testing.T) {
			f, sink := &fakeAppPasswords{refusal: refusal}, &auditSink{}
			router, _ := appPasswordRouter(f, sink, nil)
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, passwordRequest("user@example.com", testAppPassword, "203.0.113.1"))

			// Whatever the reason, the answer is the same, and never asks a
			// browser for Basic credentials.
			if rr.Code != http.StatusUnauthorized || errorCode(t, rr) != "unauthorized" {
				t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), "Invalid address or password") || strings.Contains(rr.Body.String(), refusal.Reason) {
				t.Errorf("the answer says more than that the credentials are wrong: %s", rr.Body.String())
			}
			if rr.Header().Get("WWW-Authenticate") != "" {
				t.Error("the answer carries WWW-Authenticate")
			}

			e := sink.only(t)
			if e.Action != AuditActionCredentialRefused || e.Outcome != models.AuditOutcomeDenied || e.Status != http.StatusUnauthorized {
				t.Errorf("entry: %+v", e)
			}
			if e.ActorKind != models.AuditActorAccount || e.ActorName != "user@example.com" || e.ActorID != nil {
				t.Errorf("actor %s/%q/%v", e.ActorKind, e.ActorName, e.ActorID)
			}
			if e.Target["reason"] != refusal.Reason || e.Target["public_id"] != refusal.PublicID {
				t.Errorf("target %v", e.Target)
			}
			if (e.CredentialID == nil) != (refusal.CredentialID == nil) {
				t.Errorf("credential id %v, want %v", e.CredentialID, refusal.CredentialID)
			}
			if e.Route != "/api/v1/gateway-sessions" || e.IP != "203.0.113.1" || e.UserAgent != "Thunderbird/128" || e.RequestID == "" {
				t.Errorf("request context: %+v", e)
			}
			assertNoSecret(t, e, testAppPassword, "secretsecret")
		})
	}
}

func TestSessionPasswords_CheckFailureIsUnavailable(t *testing.T) {
	f, sink := &fakeAppPasswords{err: errors.New("database down")}, &auditSink{}
	router, _ := appPasswordRouter(f, sink, nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, passwordRequest("user@example.com", testAppPassword, "203.0.113.1"))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rr.Code)
	}
	if len(sink.entries) != 0 {
		t.Errorf("an unchecked request was recorded as refused")
	}
}

func TestSessionPasswords_NoCredentialNoCheck(t *testing.T) {
	f, sink := &fakeAppPasswords{}, &auditSink{}
	router, _ := appPasswordRouter(f, sink, nil)
	for name, req := range map[string]*http.Request{
		"no address or password": httptest.NewRequest(http.MethodPost, "/api/v1/gateway-sessions", strings.NewReader(`{"protocol":"imap"}`)),
		"no password": httptest.NewRequest(http.MethodPost, "/api/v1/gateway-sessions",
			strings.NewReader(`{"protocol":"imap","address":"user@example.com"}`)),
		"HTTP Basic instead": func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/gateway-sessions", strings.NewReader(`{"protocol":"imap"}`))
			r.SetBasicAuth("user@example.com", testAppPassword)
			return r
		}(),
	} {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "credential_required") {
			t.Errorf("%s: status %d, body %s", name, rr.Code, rr.Body.String())
		}
	}
	if f.calls != 0 {
		t.Errorf("a request without a password in its body was checked")
	}
}

func TestSessionPasswords_GuessesAreThrottledPerIP(t *testing.T) {
	f, sink := &fakeAppPasswords{}, &auditSink{}
	router, _ := appPasswordRouter(f, sink, &RateLimitConfig{RPS: 0.001, Burst: 3})

	for i := 0; i < 3; i++ {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, passwordRequest("user@example.com", "rm_wrong", "203.0.113.1"))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("guess %d: status %d", i+1, rr.Code)
		}
	}
	// Budget spent: answered unchecked, and not recorded.
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, passwordRequest("user@example.com", testAppPassword, "203.0.113.1"))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("over budget: status %d", rr.Code)
	}
	limittest.RetryAfter(t, rr.Header())
	if f.calls != 3 || len(sink.entries) != 3 {
		t.Errorf("%d checks and %d entries, want 3 of each", f.calls, len(sink.entries))
	}
	// Another IP has its own budget.
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, passwordRequest("user@example.com", testAppPassword, "203.0.113.2"))
	if rr.Code != http.StatusOK {
		t.Fatalf("another IP: status %d", rr.Code)
	}
}

func TestSessionPasswords_SuccessesCostNothing(t *testing.T) {
	f, sink := &fakeAppPasswords{}, &auditSink{}
	router, _ := appPasswordRouter(f, sink, &RateLimitConfig{RPS: 0.001, Burst: 2})
	for i := 0; i < 20; i++ {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, passwordRequest("user@example.com", testAppPassword, "203.0.113.1"))
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: status %d", i+1, rr.Code)
		}
	}
}

// A mail client still using an app password its owner revoked must not use up
// the budget of the address it arrives from: a protocol gateway's, which every
// other user of the gateway shares.
func TestSessionPasswords_RevokedCredentialIsChargedToItself(t *testing.T) {
	credentialID := uint(9)
	f, sink := &fakeAppPasswords{}, &auditSink{}
	router, _ := appPasswordRouter(f, sink, &RateLimitConfig{RPS: 0.001, Burst: 2})

	f.refusal = &auth.CredentialRefusal{Reason: auth.CredentialRevoked, PublicID: "BBBBBBBBBBBB", CredentialID: &credentialID}
	codes := []int{}
	for i := 0; i < 4; i++ {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, passwordRequest("other@example.com", "rm_revoked", "10.0.0.5"))
		codes = append(codes, rr.Code)
	}
	if codes[0] != 401 || codes[1] != 401 || codes[2] != 429 || codes[3] != 429 {
		t.Fatalf("revoked credential: statuses %v, want 401 401 429 429", codes)
	}
	if len(sink.entries) != 2 {
		t.Errorf("%d entries, want the 2 within budget", len(sink.entries))
	}

	// The gateway's address still has all its budget for everyone else.
	f.refusal = nil
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, passwordRequest("user@example.com", testAppPassword, "10.0.0.5"))
	if rr.Code != http.StatusOK {
		t.Fatalf("another user through the same address: status %d", rr.Code)
	}
}

func TestAudit_RecordsTheCredentialOfTheRequest(t *testing.T) {
	sink := &auditSink{}
	h := sink.auditor(nil).Audit("auth.protocol_sign_in", AuditBodyFields("protocol"))(okHandler)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/protocol-sign-in", strings.NewReader(`{"protocol":"imap"}`))
	claims := &auth.Claims{Address: "user@example.com", AccountID: 10, CredentialID: 7, CredentialKind: models.CredentialKindAppPassword}
	req = req.WithContext(withClaims(req, claims))
	h.ServeHTTP(httptest.NewRecorder(), req)

	e := sink.only(t)
	if e.CredentialID == nil || *e.CredentialID != 7 || e.ActorID == nil || *e.ActorID != 10 || e.ActorName != "user@example.com" {
		t.Errorf("entry: actor %v %q, credential %v", e.ActorID, e.ActorName, e.CredentialID)
	}
	if e.Target["protocol"] != "imap" {
		t.Errorf("target %v", e.Target)
	}

	// A session has no credential.
	sink.entries = nil
	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/protocol-sign-in", strings.NewReader(`{}`))
	req = req.WithContext(withClaims(req, &auth.Claims{Address: "user@example.com", AccountID: 10}))
	h.ServeHTTP(httptest.NewRecorder(), req)
	if e := sink.only(t); e.CredentialID != nil {
		t.Errorf("a session's entry names credential %d", *e.CredentialID)
	}
}

func TestAudit_ResponseFieldNamesWhatWasCreated(t *testing.T) {
	sink := &auditSink{}
	created := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":12,"public_id":"AAAAAAAAAAAA","password":"rm_AAAAAAAAAAAA_topsecret"}}`))
	}
	r := chi.NewRouter()
	r.With(sink.auditor(nil).Audit("credentials.create",
		AuditResponseField("id", "credential_id"), AuditResponseField("public_id", "public_id"))).
		Post("/api/v1/accounts/{id}/credentials", created)
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/accounts/3/credentials", strings.NewReader(`{}`)))

	e := sink.only(t)
	if e.Target["credential_id"] != "12" || e.Target["public_id"] != "AAAAAAAAAAAA" {
		t.Errorf("target %v", e.Target)
	}
	assertNoSecret(t, e, "topsecret")
}

func errorCode(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var resp ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", rr.Body.String(), err)
	}
	return resp.Error.Code
}

func withClaims(r *http.Request, claims *auth.Claims) context.Context {
	return context.WithValue(r.Context(), ClaimsKey, claims)
}
