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
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
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

// appPasswordRouter serves GET /api/v1/accounts/{id}/folders behind
// BasicCredentials, answering with the claims it saw.
func appPasswordRouter(f *fakeAppPasswords, sink *auditSink, failures *RateLimitConfig) (http.Handler, *auth.JWTService) {
	jwtSvc := auth.NewJWTService("app-password-test", time.Minute, time.Hour)
	a := NewBasicCredentials(f.verify, sink.auditor(nil).BasicRefused, failures)
	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.With(a.Authenticate(jwtSvc)).Get("/api/v1/accounts/{id}/folders", func(w http.ResponseWriter, r *http.Request) {
		claims := GetClaims(r)
		w.Header().Set("X-Credential", strings.Repeat("c", int(claims.CredentialID)))
		w.Header().Set("X-Email", claims.Address)
		w.WriteHeader(http.StatusOK)
	})
	return r, jwtSvc
}

func basicRequest(address, password, ip string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/3/folders", nil)
	req.SetBasicAuth(address, password)
	req.RemoteAddr = ip + ":40000"
	req.Header.Set("User-Agent", "Thunderbird/128")
	return req
}

func TestBasicCredentials_BasicRunsAsTheCredential(t *testing.T) {
	f, sink := &fakeAppPasswords{}, &auditSink{}
	router, _ := appPasswordRouter(f, sink, nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, basicRequest("user@example.com", testAppPassword, "203.0.113.1"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("X-Credential") != strings.Repeat("c", 7) || rr.Header().Get("X-Email") != "user@example.com" {
		t.Errorf("the handler ran without the credential's claims")
	}
	if len(sink.entries) != 0 {
		t.Errorf("a request that succeeded was recorded: %+v", sink.entries)
	}
}

func TestBasicCredentials_RefusalIsRecordedAndUniform(t *testing.T) {
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
			router.ServeHTTP(rr, basicRequest("user@example.com", testAppPassword, "203.0.113.1"))

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
			if e.Action != AuditActionBasicRefused || e.Outcome != models.AuditOutcomeDenied || e.Status != http.StatusUnauthorized {
				t.Errorf("entry: %+v", e)
			}
			if e.ActorKind != models.AuditActorAccount || e.ActorName != "user@example.com" || e.ActorID != nil {
				t.Errorf("actor %s/%q/%v", e.ActorKind, e.ActorName, e.ActorID)
			}
			if e.Target["reason"] != refusal.Reason || e.Target["public_id"] != refusal.PublicID || e.Target["id"] != "3" {
				t.Errorf("target %v", e.Target)
			}
			if (e.CredentialID == nil) != (refusal.CredentialID == nil) {
				t.Errorf("credential id %v, want %v", e.CredentialID, refusal.CredentialID)
			}
			if e.Route != "/api/v1/accounts/{id}/folders" || e.IP != "203.0.113.1" || e.UserAgent != "Thunderbird/128" || e.RequestID == "" {
				t.Errorf("request context: %+v", e)
			}
			assertNoSecret(t, e, testAppPassword, "secretsecret")
		})
	}
}

func TestBasicCredentials_CheckFailureIsUnavailable(t *testing.T) {
	f, sink := &fakeAppPasswords{err: errors.New("database down")}, &auditSink{}
	router, _ := appPasswordRouter(f, sink, nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, basicRequest("user@example.com", testAppPassword, "203.0.113.1"))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rr.Code)
	}
	if len(sink.entries) != 0 {
		t.Errorf("an unchecked request was recorded as refused")
	}
}

func TestBasicCredentials_SessionsStillWork(t *testing.T) {
	f, sink := &fakeAppPasswords{}, &auditSink{}
	router, jwtSvc := appPasswordRouter(f, sink, nil)
	pair, err := jwtSvc.GenerateSession(10, "user@example.com", 0, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/3/folders", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || rr.Header().Get("X-Credential") != "" {
		t.Fatalf("a session: status %d, credential %q", rr.Code, rr.Header().Get("X-Credential"))
	}

	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/accounts/3/folders", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no credentials: status %d", rr.Code)
	}
	if f.calls != 0 {
		t.Errorf("a request without Basic was checked as an app password")
	}
}

func TestBasicCredentials_GuessesAreThrottledPerIP(t *testing.T) {
	f, sink := &fakeAppPasswords{}, &auditSink{}
	router, _ := appPasswordRouter(f, sink, &RateLimitConfig{RPS: 0.001, Burst: 3})

	for i := 0; i < 3; i++ {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, basicRequest("user@example.com", "rm_wrong", "203.0.113.1"))
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("guess %d: status %d", i+1, rr.Code)
		}
	}
	// Budget spent: answered unchecked, and not recorded.
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, basicRequest("user@example.com", testAppPassword, "203.0.113.1"))
	if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") == "" {
		t.Fatalf("over budget: status %d", rr.Code)
	}
	if f.calls != 3 || len(sink.entries) != 3 {
		t.Errorf("%d checks and %d entries, want 3 of each", f.calls, len(sink.entries))
	}
	// Another IP has its own budget.
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, basicRequest("user@example.com", testAppPassword, "203.0.113.2"))
	if rr.Code != http.StatusOK {
		t.Fatalf("another IP: status %d", rr.Code)
	}
}

func TestBasicCredentials_SuccessesCostNothing(t *testing.T) {
	f, sink := &fakeAppPasswords{}, &auditSink{}
	router, _ := appPasswordRouter(f, sink, &RateLimitConfig{RPS: 0.001, Burst: 2})
	for i := 0; i < 20; i++ {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, basicRequest("user@example.com", testAppPassword, "203.0.113.1"))
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: status %d", i+1, rr.Code)
		}
	}
}

// A mail client still using an app password its owner revoked must not use up
// the budget of the address it arrives from: a protocol gateway's, which every
// other user of the gateway shares.
func TestBasicCredentials_RevokedCredentialIsChargedToItself(t *testing.T) {
	credentialID := uint(9)
	f, sink := &fakeAppPasswords{}, &auditSink{}
	router, _ := appPasswordRouter(f, sink, &RateLimitConfig{RPS: 0.001, Burst: 2})

	f.refusal = &auth.CredentialRefusal{Reason: auth.CredentialRevoked, PublicID: "BBBBBBBBBBBB", CredentialID: &credentialID}
	codes := []int{}
	for i := 0; i < 4; i++ {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, basicRequest("other@example.com", "rm_revoked", "10.0.0.5"))
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
	router.ServeHTTP(rr, basicRequest("user@example.com", testAppPassword, "10.0.0.5"))
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
	if e.Target["credential_id"] != "12" || e.Target["public_id"] != "AAAAAAAAAAAA" || e.Target["id"] != "3" {
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
