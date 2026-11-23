package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api"
	"github.com/rest-mail/rest-mail-server/internal/audit"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/crypto"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
	"github.com/rest-mail/rest-mail-server/internal/grants"
)

const operatorMasterKey = "0102030405060708091011121314151617181920212223242526272829303132"

func TestAC03PublicStatusOnlyReportsAvailability(t *testing.T) {
	db := migrationDatabase(t)
	domain := models.Domain{Name: "private-status-domain.example.test", Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	account := models.Mailbox{DomainID: domain.ID, LocalPart: "private-status-account", Address: "private-status-account@" + domain.Name, Active: true}
	if err := db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	queue := models.OutboundQueue{
		Sender: account.Address, Recipient: "private-status-recipient@example.test", Domain: "example.test",
		RawMessage: "Subject: private-status-subject\r\n\r\nprivate-status-body\r\n", Status: "deferred",
		LastError: "private-status-queue-diagnostic", NextAttempt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := db.Create(&queue).Error; err != nil {
		t.Fatal(err)
	}
	cert := models.Certificate{
		DomainID: domain.ID, CertPEM: "private-status-cert", KeyPEM: "private-status-key", Issuer: "private-status-issuer",
		NotBefore: time.Now().Add(-2 * time.Hour), NotAfter: time.Now().Add(-time.Hour),
	}
	if err := db.Create(&cert).Error; err != nil {
		t.Fatal(err)
	}

	jwt := auth.NewJWTService("public-status-test-signing-key", 5*time.Minute, time.Hour)
	cfg := &config.Config{Environment: "production", GatewayHostname: "private-status-gateway.example.test"}
	routers := api.NewRouters(db, jwt, cfg, nil)
	t.Cleanup(routers.Close)
	server := httptest.NewServer(routers.Public)
	t.Cleanup(server.Close)
	probe := func(status int, body string, conditional bool) string {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/api/v1/status", nil)
		if err != nil {
			t.Fatal(err)
		}
		if conditional {
			req.Header.Set("If-None-Match", "*")
			req.Header.Set("If-Modified-Since", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		got, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != status || string(got) != body+"\n" {
			t.Errorf("conditional=%v: got %d %q, want %d %q", conditional, res.StatusCode, got, status, body+"\n")
		}
		if res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("ETag") != "" || res.Header.Get("Last-Modified") != "" {
			t.Errorf("conditional=%v: unexpected cache headers %v", conditional, res.Header)
		}
		return string(got) + fmt.Sprint(res.Header)
	}
	for _, conditional := range []bool{false, true} {
		if got := probe(http.StatusOK, `{"data":{"status":"operational"}}`, conditional); strings.Contains(got, "private-status-") {
			t.Error("healthy status disclosed private fixture data")
		}
	}

	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	for _, conditional := range []bool{true, false} {
		if got := probe(http.StatusServiceUnavailable, `{"data":{"status":"degraded"}}`, conditional); strings.Contains(got, "private-status-") {
			t.Error("degraded status disclosed private fixture data")
		}
	}
}

func TestAC04ScopedOperatorDiagnostics(t *testing.T) {
	s := newQueueServer(t)
	retryBase := "/api/v1/admin/queue/{id}/retry"
	resetPath := "/api/v1/admin/database/statements/reset"
	operator := operatorMFASession(t, s)

	// Exercise the actual executable with only its API credential in the
	// environment. The CLI has no database address or credentials.
	bin := filepath.Join(t.TempDir(), "restmail")
	if out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build console: %v\n%s", err, out)
	}
	run := func(t *testing.T, wantExit int, wantError, token, command, action string, flags ...string) string {
		t.Helper()
		args := append([]string{command, action, "--server", s.url, "--json"}, flags...)
		cmd := exec.CommandContext(t.Context(), bin, args...)
		cmd.Env = []string{"RESTMAIL_API_KEY=" + token}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		code := 0
		if err := cmd.Run(); err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatal(err)
			}
			code = exit.ExitCode()
		}
		if code != wantExit || (wantError == "" && stderr.Len() != 0) ||
			(wantError != "" && (stdout.Len() != 0 || !strings.Contains(stderr.String(), `"code":"`+wantError+`"`))) {
			t.Errorf("%v: exit %d, stdout %q, stderr %q, want exit %d/error %q", args, code, stdout.String(), stderr.String(), wantExit, wantError)
		}
		return stdout.String()
	}

	wrongScope, _ := s.key(t, "domain-only", grants.Domain(migrationDomain), "queue:read", "queue:manage", "database:read", "database:manage")
	diagnose, diagnosticID := s.key(t, "diagnose", grants.All, "database:read")
	r := s.seed(t)
	rowID := strconv.FormatUint(uint64(r.refused), 10)
	const queueSecret = "operator-private-queued-content"
	if err := s.db.Model(&models.OutboundQueue{}).Where("id = ?", r.refused).Update("raw_message", queueSecret).Error; err != nil {
		t.Fatal(err)
	}

	t.Run("queue_inspection_pagination_and_persisted_retry", func(t *testing.T) {
		for offset, id := range []uint{r.refused, r.greylisted} {
			out := run(t, exitOK, "", s.read, "mail-queue", "list", "--status", "deferred", "--limit", "1", "--offset", strconv.Itoa(offset))
			var page queuePage
			if err := json.Unmarshal([]byte(out), &page); err != nil {
				t.Fatalf("page: %v: %s", err, out)
			}
			if page.Total != 2 || page.Limit != 1 || page.Offset != offset || len(page.Items) != 1 || page.Items[0].ID != id {
				t.Fatalf("page %d: %+v (want row %d)", offset, page, id)
			}
		}
		for _, path := range []string{"/api/v1/admin/queue/" + rowID, "/api/v1/admin/queue?status=deferred"} {
			body := operatorAPIGet(t, s, s.read, path, http.StatusOK)
			if !bytes.Contains(body, []byte("connection refused")) || bytes.Contains(body, []byte(queueSecret)) ||
				bytes.Contains(body, []byte(`"raw_message"`)) {
				t.Errorf("%s: queue diagnostics missing or raw mail disclosed", path)
			}
		}
		run(t, exitFailed, "not_found", operator, "mail-queue", "show", "4294967295")
		run(t, exitFailed, "unauthorized", "invalid-operator-key", "mail-queue", "stats")
		run(t, exitFailed, "forbidden", wrongScope, "mail-queue", "list")
		run(t, exitFailed, "forbidden", s.read, "mail-queue", "retry", rowID)
		run(t, exitFailed, "forbidden", wrongScope, "mail-queue", "retry", rowID)
		s.wantStatuses(t, r.statuses())
		run(t, exitOK, "", operator, "mail-queue", "retry", rowID)
		s.wantStatuses(t, r.with(map[uint]string{r.refused: "pending"}))
		var row models.OutboundQueue
		if err := s.db.First(&row, r.refused).Error; err != nil {
			t.Fatal(err)
		}
		if row.NextAttempt.After(time.Now()) || row.RawMessage != queueSecret {
			t.Errorf("retry did not make the intact message due: next attempt %s", row.NextAttempt)
		}
	})

	t.Run("slow_query_authorization_redaction_and_reset", func(t *testing.T) {
		const (
			commentSecret = "operator-private-query-comment"
			utilitySecret = "operator-private-utility-value"
			literalSecret = "operator-private-query-literal"
		)
		for _, stmt := range []string{
			`SELECT pg_sleep(0.01), length('` + literalSecret + `')`,
			`SELECT /* ` + commentSecret + ` */ length('value'), 2117`,
			`DO $body$ BEGIN PERFORM length('` + utilitySecret + `'); END; $body$;`,
		} {
			if err := s.db.Exec(stmt).Error; err != nil {
				t.Fatal(err)
			}
		}
		var queries []struct {
			Query string
			ID    int64
		}
		if err := s.db.Raw(`SELECT query, queryid AS id FROM pg_stat_statements
			WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&queries).Error; err != nil {
			t.Fatal(err)
		}
		redacted := make(map[string]bool)
		for _, q := range queries {
			if strings.Contains(q.Query, commentSecret) || strings.Contains(q.Query, utilitySecret) {
				redacted[fmt.Sprintf("[redacted query %d]", q.ID)] = true
			}
		}
		if len(redacted) != 2 {
			t.Fatalf("PostgreSQL control recorded %d of the 2 sensitive queries", len(redacted))
		}
		for _, order := range []string{"mean", "total"} {
			out := run(t, exitOK, "", diagnose, "slow-statements", "list", "--sort", order, "--limit", "100")
			var view slowStatementsView
			if err := json.Unmarshal([]byte(out), &view); err != nil {
				t.Fatalf("decode slow statements: %v: %s", err, out)
			}
			if view.Sort != order || !strings.Contains(out, "pg_sleep($1), length($2)") {
				t.Errorf("sort=%s: expected normalized workload in the response", order)
			}
			for _, secret := range []string{commentSecret, utilitySecret, literalSecret} {
				if strings.Contains(out, secret) {
					t.Errorf("sort=%s: slow-query output disclosed %q", order, secret)
				}
			}
			found := make(map[string]bool)
			for _, stmt := range view.Statements {
				if redacted[stmt.Query] && stmt.Calls == 1 {
					found[stmt.Query] = true
				}
			}
			if len(found) != len(redacted) {
				t.Errorf("sort=%s: expected both redacted queries and their counters, got %v", order, found)
			}
		}
		for _, query := range []string{"sort=unknown", "limit=101"} {
			const path = "/api/v1/admin/database/statements?"
			body := operatorAPIGet(t, s, diagnose, path+query, http.StatusUnprocessableEntity)
			if !bytes.Contains(body, []byte(`"code":"validation_failed"`)) || bytes.Contains(body, []byte("operator-private-")) {
				t.Errorf("%s: validation details missing or a diagnostic leaked", query)
			}
		}
		run(t, exitFailed, "unauthorized", "invalid-operator-key", "slow-statements", "list")
		run(t, exitFailed, "forbidden", wrongScope, "slow-statements", "list")
		run(t, exitFailed, "forbidden", diagnose, "slow-statements", "reset")
		run(t, exitFailed, "forbidden", wrongScope, "slow-statements", "reset")
		remaining := func(want bool) {
			t.Helper()
			var found bool
			if err := s.db.Raw(`SELECT EXISTS (SELECT 1 FROM pg_stat_statements
				WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
				AND query = ?)`, `SELECT pg_sleep($1), length($2)`).Scan(&found).Error; err != nil || found != want {
				t.Fatalf("workload present in PostgreSQL = %v, want %v (err %v)", found, want, err)
			}
		}
		remaining(true)
		run(t, exitOK, "", operator, "slow-statements", "reset")
		remaining(false)
	})

	t.Run("write_audit_records_actor_outcome_and_safe_targets", func(t *testing.T) {
		var entries []models.AuditEntry
		if err := s.db.Where("action = ? AND route IN ?", "admin.change", []string{retryBase, resetPath}).Order("seq").Find(&entries).Error; err != nil {
			t.Fatal(err)
		}
		if len(entries) != 6 {
			t.Fatalf("got %d audited writes, want 6", len(entries))
		}
		wantCodes := []int{403, 403, 200, 403, 403, 204}
		for i, entry := range entries {
			outcome := models.AuditOutcomeDenied
			if i == 2 || i == 5 {
				outcome = models.AuditOutcomeSuccess
				if entry.CredentialID != nil {
					t.Errorf("entry %d: operator session recorded as API key", i)
				}
			} else if entry.CredentialID == nil || (i == 3 && *entry.CredentialID != diagnosticID) {
				t.Errorf("entry %d: incorrect credential attribution", i)
			}
			if entry.Status != wantCodes[i] || entry.Outcome != outcome || entry.ActorID == nil || *entry.ActorID != s.holder.ID {
				t.Errorf("entry %d: unexpected status/outcome/actor: %+v", i, entry)
			}
			if i < 3 && (entry.Route != retryBase || len(entry.Target) != 1 || entry.Target["id"] != rowID) {
				t.Errorf("entry %d: unexpected queue target: %+v", i, entry.Target)
			}
			if i >= 3 && (entry.Route != resetPath || len(entry.Target) != 0) {
				t.Errorf("entry %d: unexpected database target: %+v", i, entry.Target)
			}
		}
		data, err := json.Marshal(entries)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{queueSecret, s.read, wrongScope, diagnose, operator, "operator-private-query-", "operator-private-utility-"} {
			if bytes.Contains(data, []byte(secret)) {
				t.Error("audit log disclosed an operator diagnostic or credential secret")
			}
		}
		if result, err := audit.VerifyDB(s.db); err != nil || !result.OK {
			t.Errorf("verify audit chain: %+v, %v", result, err)
		}
	})
}

func operatorAPIGet(t *testing.T, s *queueServer, token, path string, status int) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.url+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := s.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != status {
		t.Fatalf("%s: got HTTP %d, want %d", path, res.StatusCode, status)
	}
	return body
}

func operatorMFASession(t *testing.T, s *queueServer) string {
	t.Helper()
	const (
		password = "operator-private-password-for-test"
		secret   = "JBSWY3DPEHPK3PXP"
	)
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repositories.NewCredentialRepository(s.db).SetPassword(s.holder.ID, hash, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	encrypted, err := crypto.EncryptString(secret, operatorMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	repo := repositories.NewTwoFactorRepository(s.db)
	factor, err := repo.Enroll(s.holder.ID, encrypted, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Confirm(factor.ID); err != nil {
		t.Fatal(err)
	}
	code, err := auth.GenerateTOTPCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"email":%q,"password":%q,"totp_code":%q,"client":"native"}`, s.holder.Address, password, code)
	resp, err := s.client.Post(s.url+"/api/v1/auth/login", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var session struct {
		Data struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil || resp.StatusCode != http.StatusOK || session.Data.AccessToken == "" {
		t.Fatalf("operator MFA sign-in: HTTP %d, decode error %v", resp.StatusCode, err)
	}
	return session.Data.AccessToken
}
