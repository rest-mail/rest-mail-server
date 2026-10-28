package handlers

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/examplepairs"
	"gorm.io/gorm"
)

func TestRecoveryEmailCanonicalBindingExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{"registerRecoveryEmail": "/api/v1/auth/recovery-email", "verifyRecoveryEmail": "/api/v1/auth/recovery-email/verify", "requestRecoveryEmail": "/api/v1/auth/recover/email", "redeemRecoveryEmail": "/api/v1/auth/recover/email/redeem"}
	for op, path := range paths {
		for _, name := range []string{"body_array", "invalid_field", "request_too_large"} {
			t.Run(op+"/"+name, func(t *testing.T) {
				pair := pairs[op][name]
				h := NewRecoveryEmailHandler(nil, NewAuthHandler(nil, nil, "", nil), "https://mail.example.test")
				routes := emailRecoveryRoutes(h, 0)
				if name == "request_too_large" {
					routes = middleware.MaxBodyBytes(1)(routes)
				}
				rr := httptest.NewRecorder()
				req := httptest.NewRequest("POST", path, bytes.NewReader(pair.Request.Body))
				req.Header.Set("Content-Type", "application/json")
				routes.ServeHTTP(rr, req)
				var got, want any
				if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(pair.Response.Body, &want); err != nil {
					t.Fatal(err)
				}
				if rr.Code != pair.Response.Status || !reflect.DeepEqual(got, want) {
					t.Fatalf("canonical pair differs: status=%d body=%s, want %d %s", rr.Code, rr.Body, pair.Response.Status, pair.Response.Body)
				}
			})
		}
	}
}

func TestRecoveryEmailCanonicalPage(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	var want string
	if err := json.Unmarshal(pairs["recoveryEmailLink"]["page"].Response.Body, &want); err != nil {
		t.Fatal(err)
	}
	rr := emailRecoveryCall(emailRecoveryRoutes(NewRecoveryEmailHandler(nil, nil, ""), 0), "GET", "/api/v1/auth/recovery-email/link", nil)
	if rr.Code != 200 || rr.Body.String() != want {
		t.Fatal("documented recovery link page differs from its registered response")
	}
}

func TestRecoveryEmailRegistrationChecksCurrentPasswordBeforeWriting(t *testing.T) {
	d := &domainPolicyFaultDB{steps: []domainPolicySQLStep{
		{match: "FOR UPDATE", columns: []string{"id", "address", "active"}, values: []driver.Value{int64(7), "person@hosted.test", true}},
		{match: `FROM "credentials"`, columns: []string{"id"}},
	}}
	db := domainPolicyFaultGorm(t, d)
	h := NewRecoveryEmailHandler(db, NewAuthHandler(db, nil, "", nil), "https://mail.example.test")
	rr := emailRecoveryCall(emailRecoveryRoutes(h, 7), "POST", "/api/v1/auth/recovery-email", map[string]string{"email": "backup@external.test", "current_password": "incorrect password"})
	if rr.Code != 403 || errorCode(rr) != "invalid_password" || d.inserts.Load() != 0 || d.commits.Load() != 0 || d.rollbacks.Load() != 1 {
		t.Fatalf("incorrect password registered a method: %d %s", rr.Code, rr.Body)
	}
}

func TestRecoveryEmailQueuesPrivateLinkWithoutTokenInHeaders(t *testing.T) {
	d := &domainPolicyFaultDB{steps: []domainPolicySQLStep{domainPolicyInsert("outbound_queue")}}
	db := domainPolicyFaultGorm(t, d)
	var queued models.OutboundQueue
	if err := db.Callback().Create().Before("gorm:create").Register("capture_recovery_mail", func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*models.OutboundQueue); ok {
			queued = *row
		}
	}); err != nil {
		t.Fatal(err)
	}
	h := NewRecoveryEmailHandler(db, nil, "https://trusted.example.test")
	token := strings.Repeat("S", 43)
	if err := h.queue(db, &models.Mailbox{Address: "person@hosted.test"}, "backup@external.test", token, "recover", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	headers, body, _ := strings.Cut(queued.RawMessage, "\r\n\r\n")
	if strings.Contains(headers, token) || !strings.Contains(body, "https://trusted.example.test/api/v1/auth/recovery-email/link?mode=recover#"+token) || queued.Recipient != "backup@external.test" {
		t.Fatalf("unsafe delivery: %+v", queued)
	}
}

func TestRecoveryEmailAuditDistinguishesAnonymousIssueFromProof(t *testing.T) {
	for _, action := range []string{"register", "verify", "issue", "redeem"} {
		t.Run(action, func(t *testing.T) {
			d := &domainPolicyFaultDB{steps: []domainPolicySQLStep{
				{match: "pg_advisory_xact_lock", affected: 1},
				{match: "SELECT seq, hash FROM audit_log", columns: []string{"seq", "hash"}, values: []driver.Value{int64(0), ""}},
				{match: `INSERT INTO "audit_log"`, affected: 1},
			}}
			db := domainPolicyFaultGorm(t, d)
			var recorded models.AuditEntry
			if err := db.Callback().Create().Before("gorm:create").Register("capture_recovery_audit", func(tx *gorm.DB) {
				if e, ok := tx.Statement.Dest.(*models.AuditEntry); ok {
					recorded = *e
				}
			}); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/recover/email", nil)
			if err := emailRecoveryAudit(db, req, "auth.recovery_email."+action, &models.Mailbox{ID: 7, Address: "person@hosted.test"}); err != nil {
				t.Fatal(err)
			}
			if recorded.Target["account_id"] != "7" || recorded.Action != "auth.recovery_email."+action || recorded.Outcome != models.AuditOutcomeSuccess {
				t.Fatalf("audit identity or outcome missing: %+v", recorded)
			}
			if action == "issue" {
				if recorded.ActorKind != models.AuditActorAnonymous || recorded.ActorID != nil {
					t.Fatalf("issuing mail proves no identity: %+v", recorded)
				}
			} else if recorded.ActorID == nil || *recorded.ActorID != 7 {
				t.Fatalf("verified actor missing: %+v", recorded)
			}
		})
	}
}
