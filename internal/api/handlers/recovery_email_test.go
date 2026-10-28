package handlers

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	"gorm.io/gorm"
)

func emailRecoveryRoutes(h *RecoveryEmailHandler, id uint) http.Handler {
	r := chi.NewRouter()
	h.RegisterPublic(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, passingSignInChecks)
	r.Group(func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if id != 0 {
					r = r.WithContext(context.WithValue(r.Context(), middleware.ClaimsKey, &auth.Claims{AccountID: id}))
				}
				next.ServeHTTP(w, r)
			})
		})
		h.RegisterOwn(endpoint.Routes{Router: r, Registry: endpoint.NewRegistry()}, passingSignInChecks)
	})
	return r
}

func emailRecoveryCall(h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rr, req)
	return rr
}

func TestRecoveryEmailBindingAndAuthentication(t *testing.T) {
	h := NewRecoveryEmailHandler(nil, NewAuthHandler(nil, nil, "", nil), "https://mail.example.test")
	for _, tc := range []struct {
		path   string
		body   any
		status int
	}{
		{"/api/v1/auth/recovery-email", map[string]string{"email": "backup@external.test", "current_password": "password"}, 401},
		{"/api/v1/auth/recover/email", map[string]string{"email": "invalid"}, 422},
		{"/api/v1/auth/recovery-email/verify", map[string]string{"token": ""}, 422},
		{"/api/v1/auth/recover/email/redeem", map[string]string{"token": "unknown", "new_password": "short"}, 422},
	} {
		t.Run(tc.path, func(t *testing.T) {
			rr := emailRecoveryCall(emailRecoveryRoutes(h, 0), "POST", tc.path, tc.body)
			if rr.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rr.Code, tc.status, rr.Body.String())
			}
		})
	}
}

func TestRecoveryEmailLinkIsPrivateAndUsableWithSecurityHeaders(t *testing.T) {
	h := NewRecoveryEmailHandler(nil, nil, "https://mail.example.test")
	routes := middleware.SecurityHeaders(middleware.SecurityHeadersConfig{})(emailRecoveryRoutes(h, 0))
	rr := emailRecoveryCall(routes, "GET", "/api/v1/auth/recovery-email/link?mode=verify", nil)
	if rr.Code != 200 || rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("page: %d %v", rr.Code, rr.Header())
	}
	for _, policy := range rr.Header().Values("Content-Security-Policy") {
		if !strings.Contains(policy, "script-src") || strings.Contains(policy, "script-src 'none'") {
			t.Fatalf("CSP blocks private link UI: %s", policy)
		}
	}
	if !strings.Contains(rr.Body.String(), "history.replaceState") || !strings.Contains(rr.Body.String(), "credentials: 'omit'") {
		t.Fatal("page must remove fragment and use no session")
	}
}

func TestRecoveryEmailUnknownAndDependencyFailureAreUniformAndThrottled(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(strconv.FormatBool(fail), func(t *testing.T) {
			d := &domainPolicyFaultDB{steps: []domainPolicySQLStep{}}
			for i := 0; i < 5; i++ {
				d.steps = append(d.steps, domainPolicySQLStep{match: `FROM "mailboxes"`, columns: []string{"id"}, fail: fail})
			}
			db := domainPolicyFaultGorm(t, d)
			h := NewRecoveryEmailHandler(db, NewAuthHandler(db, nil, "", nil), "https://mail.example.test")
			routes := emailRecoveryRoutes(h, 0)
			var answer string
			for i := 0; i < 6; i++ {
				rr := emailRecoveryCall(routes, "POST", "/api/v1/auth/recover/email", map[string]string{"email": "UNKNOWN@Example.test"})
				if i == 5 {
					if rr.Code != 429 || rr.Header().Get("Retry-After") == "" {
						t.Fatalf("budget %d %s", rr.Code, rr.Body)
					}
					continue
				}
				if rr.Code != 200 {
					t.Fatalf("request %d %s", rr.Code, rr.Body)
				}
				if i == 0 {
					answer = rr.Body.String()
				} else if answer != rr.Body.String() {
					t.Fatal("responses differ")
				}
			}
			if d.calls.Load() != 5 || d.commits.Load() != 0 || d.rollbacks.Load() != 5 {
				t.Fatalf("unknown/failing lookups must not commit: calls=%d commits=%d rollbacks=%d", d.calls.Load(), d.commits.Load(), d.rollbacks.Load())
			}
		})
	}
}

func TestRecoveryEmailWrongPurposeOrExpiredNeverChangesPassword(t *testing.T) {
	d := &domainPolicyFaultDB{steps: []domainPolicySQLStep{{match: `FROM "one_time_tokens"`, columns: []string{"id"}}}}
	db := domainPolicyFaultGorm(t, d)
	h := NewRecoveryEmailHandler(db, NewAuthHandler(db, nil, "", nil), "https://mail.example.test")
	rr := emailRecoveryCall(emailRecoveryRoutes(h, 0), "POST", "/api/v1/auth/recover/email/redeem", map[string]string{"token": "wrong-purpose", "new_password": "new password chosen"})
	if rr.Code != 401 || d.inserts.Load() != 0 || d.commits.Load() != 0 || d.rollbacks.Load() != 1 {
		t.Fatalf("invalid token wrote credentials: %d %s", rr.Code, rr.Body)
	}
}

func TestRecoveryEmailExternalAddressChecksHostedDomain(t *testing.T) {
	d := &domainPolicyFaultDB{steps: []domainPolicySQLStep{{match: `FROM "domains"`, columns: []string{"count"}, values: []driver.Value{int64(1)}}}}
	db := domainPolicyFaultGorm(t, d)
	external, err := bootstrap.RecoveryEmailExternal(db, "backup@hosted.test")
	if err != nil || external {
		t.Fatalf("hosted address is not external: %v %v", external, err)
	}
}

func recoveryEmailFixture(t *testing.T) (*gorm.DB, *RecoveryEmailHandler, models.Mailbox, http.Handler) {
	t.Helper()
	db := openCustomFilterCreateScratchDB(t)
	tables := append([]interface{}{}, accountModels...)
	tables = append(tables, &models.OneTimeToken{}, &models.RecoveryEmail{}, &models.OutboundQueue{}, &models.AuditEntry{})
	if err := db.AutoMigrate(tables...); err != nil {
		t.Fatal(err)
	}
	address := seedLoginMailbox(t, db, "original password chosen")
	var account models.Mailbox
	if err := db.Where("address = ?", address).First(&account).Error; err != nil {
		t.Fatal(err)
	}
	h := NewRecoveryEmailHandler(db, NewAuthHandler(db, auth.NewJWTService("email-recovery-tests", time.Minute, time.Hour), "", nil), "https://mail.example.test")
	return db, h, account, emailRecoveryRoutes(h, account.ID)
}

func recoveryEmailToken(t *testing.T, db *gorm.DB, recipient string) string {
	t.Helper()
	var row models.OutboundQueue
	if err := db.Where("recipient = ?", recipient).Order("id DESC").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	start := strings.Index(row.RawMessage, "#")
	if start < 0 {
		t.Fatalf("no private fragment link: %s", row.RawMessage)
	}
	token := strings.Fields(row.RawMessage[start+1:])[0]
	if len(token) != 43 {
		t.Fatalf("unexpected token %q", token)
	}
	return token
}

func TestRecoveryEmailVerifiedSingleUsePasswordReset(t *testing.T) {
	db, _, account, routes := recoveryEmailFixture(t)
	const backup = "backup@external.test"
	register := func() *httptest.ResponseRecorder {
		return emailRecoveryCall(routes, "POST", "/api/v1/auth/recovery-email", map[string]string{"email": backup, "current_password": "original password chosen"})
	}
	if rr := register(); rr.Code != 200 {
		t.Fatalf("register %d: %s", rr.Code, rr.Body.String())
	}
	verify := recoveryEmailToken(t, db, backup)
	// Unverified and unknown account addresses receive the identical answer and no recovery mail.
	request := func(address string) *httptest.ResponseRecorder {
		return emailRecoveryCall(routes, "POST", "/api/v1/auth/recover/email", map[string]string{"email": address})
	}
	known, unknown := request(account.Address), request("unknown@example.test")
	if known.Code != 200 || known.Body.String() != unknown.Body.String() {
		t.Fatalf("enumerating response: %s / %s", known.Body, unknown.Body)
	}
	if got := recoveryEmailToken(t, db, backup); got != verify {
		t.Fatal("unverified address received recovery token")
	}
	verified := emailRecoveryCall(routes, "POST", "/api/v1/auth/recovery-email/verify", map[string]string{"token": verify})
	if verified.Code != 200 {
		t.Fatalf("verify %d: %s", verified.Code, verified.Body)
	}
	if rr := emailRecoveryCall(routes, "POST", "/api/v1/auth/recovery-email/verify", map[string]string{"token": verify}); rr.Code != 401 {
		t.Fatalf("verification replay %d", rr.Code)
	}
	if rr := request(account.Address); rr.Code != 200 {
		t.Fatalf("request %d: %s", rr.Code, rr.Body)
	}
	token := recoveryEmailToken(t, db, backup)
	if token == verify {
		t.Fatal("verification token reused for recovery")
	}
	redeem := func(tok string) *httptest.ResponseRecorder {
		return emailRecoveryCall(routes, "POST", "/api/v1/auth/recover/email/redeem", map[string]string{"token": tok, "new_password": "newly chosen password"})
	}
	if rr := redeem(verify); rr.Code != 401 {
		t.Fatalf("wrong-purpose token %d", rr.Code)
	}
	if rr := redeem(token); rr.Code != 200 || strings.Contains(rr.Body.String(), "access_token") || len(rr.Result().Cookies()) != 0 {
		t.Fatalf("reset must require normal sign-in: %d %s", rr.Code, rr.Body)
	}
	if rr := redeem(token); rr.Code != 401 {
		t.Fatalf("replay %d", rr.Code)
	}
	var after models.Mailbox
	db.First(&after, account.ID)
	if after.CredentialEpoch <= account.CredentialEpoch {
		t.Fatal("old sessions remain valid")
	}
	cred, err := repositories.NewCredentialRepository(db).MatchPassword(account.ID, "newly chosen password", time.Now())
	if err != nil || cred == nil {
		t.Fatalf("new password missing: %v", err)
	}
	// Reset freezes recovery address changes for three days.
	if rr := register(); rr.Code != 403 {
		t.Fatalf("recovery method not frozen: %d %s", rr.Code, rr.Body)
	}
}

func TestRecoveryEmailExpiryReplacementAndDeliveryRollback(t *testing.T) {
	db, _, account, routes := recoveryEmailFixture(t)
	register := func(email string) *httptest.ResponseRecorder {
		return emailRecoveryCall(routes, "POST", "/api/v1/auth/recovery-email", map[string]string{"email": email, "current_password": "original password chosen"})
	}
	if rr := register(account.Address); rr.Code != 422 {
		t.Fatalf("hosted address accepted: %d", rr.Code)
	}
	if rr := register("backup@external.test"); rr.Code != 200 {
		t.Fatalf("register %d %s", rr.Code, rr.Body)
	}
	first := recoveryEmailToken(t, db, "backup@external.test")
	if rr := register("replacement@external.test"); rr.Code != 200 {
		t.Fatalf("replace %d %s", rr.Code, rr.Body)
	}
	if rr := emailRecoveryCall(routes, "POST", "/api/v1/auth/recovery-email/verify", map[string]string{"token": first}); rr.Code != 401 {
		t.Fatalf("replaced token %d", rr.Code)
	}
	second := recoveryEmailToken(t, db, "replacement@external.test")
	db.Model(&models.OneTimeToken{}).Where("account_id = ?", account.ID).Update("expires_at", time.Now().Add(-time.Minute))
	if rr := emailRecoveryCall(routes, "POST", "/api/v1/auth/recovery-email/verify", map[string]string{"token": second}); rr.Code != 401 {
		t.Fatalf("expired token %d", rr.Code)
	}
	if err := db.Callback().Create().Before("gorm:create").Register("fail_recovery_mail", func(tx *gorm.DB) {
		if tx.Statement.Table == "outbound_queue" {
			_ = tx.AddError(gorm.ErrInvalidData)
		}
	}); err != nil {
		t.Fatal(err)
	}
	rr := register("undelivered@external.test")
	if err := db.Callback().Create().Remove("fail_recovery_mail"); err != nil {
		t.Fatal(err)
	}
	if rr.Code != 500 {
		t.Fatalf("queue failure %d", rr.Code)
	}
	var method models.RecoveryEmail
	db.First(&method, "account_id = ?", account.ID)
	if method.Address != "replacement@external.test" {
		t.Fatalf("delivery failure changed method: %+v", method)
	}
}
