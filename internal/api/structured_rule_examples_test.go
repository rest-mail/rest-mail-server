package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/rules"
	"gorm.io/gorm"
)

func TestStructuredRuleNamedExamples(t *testing.T) {
	useRealGrants(t)
	f := newCredentialFixture(t)
	steps, _ := json.Marshal([]pipeline.FilterConfig{{Name: "sieve", Type: pipeline.FilterTypeTransform, Enabled: true, Config: json.RawMessage(`{}`)}})
	if err := f.db.Create(&models.Pipeline{DomainID: f.mustDomainID(), Direction: "inbound", Filters: steps, Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&models.QuotaUsage{MailboxID: f.mailboxID}).Error; err != nil {
		t.Fatal(err)
	}
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	definition := rules.Definition{Name: "Invoices", Enabled: true, Position: 10, Conditions: []rules.Condition{{Field: "subject", Operator: "contains", Value: "invoice"}}, Actions: []rules.Action{{Type: "fileinto", Value: "Finance"}}}
	routes := map[string]struct{ method, path string }{
		"createMailRule":         {"POST", "/api/v1/accounts/{id}/rules"},
		"listMailRules":          {"GET", "/api/v1/accounts/{id}/rules"},
		"getMailRule":            {"GET", "/api/v1/accounts/{id}/rules/{ruleID}"},
		"updateMailRule":         {"PUT", "/api/v1/accounts/{id}/rules/{ruleID}"},
		"deleteMailRule":         {"DELETE", "/api/v1/accounts/{id}/rules/{ruleID}"},
		"getMailRuleSieve":       {"GET", "/api/v1/accounts/{id}/rules/sieve"},
		"listMessageRuleEffects": {"GET", "/api/v1/messages/{id}/rule-effects"},
		"undoMessageRuleEffect":  {"POST", "/api/v1/messages/{id}/rule-effects/{effectID}/undo"},
	}
	for op, route := range routes {
		for name, pair := range pairs[op] {
			t.Run(op+"/"+name, func(t *testing.T) {
				examplepairs.Proven(t, op, name)
				if err := f.db.Where("mailbox_id = ?", f.mailboxID).Delete(&models.MailRule{}).Error; err != nil {
					t.Fatal(err)
				}
				if err := f.db.Where("mailbox_id = ?", f.mailboxID).Delete(&models.SieveScript{}).Error; err != nil {
					t.Fatal(err)
				}
				caseDefinition := definition
				if name == "forward_not_undoable" {
					caseDefinition.Actions = append(append([]rules.Action(nil), definition.Actions...), rules.Action{Type: "forward", Value: "copy@example.test"})
				}
				if op != "createMailRule" {
					if err := f.db.Create(&models.MailRule{ID: 1, MailboxID: f.mailboxID, Version: 1, Definition: caseDefinition}).Error; err != nil {
						t.Fatal(err)
					}
				}
				messageID := uint(1)
				effectID := "00000000-0000-4000-8000-000000000001"
				if op == "listMessageRuleEffects" || op == "undoMessageRuleEffect" {
					messageID = deliverStructuredMessage(t, f, "invoice")
					var msg models.Message
					if err := f.db.First(&msg, messageID).Error; err != nil {
						t.Fatal(err)
					}
					wantEffects := 1
					if name == "forward_not_undoable" {
						wantEffects = 2
					}
					if len(msg.RuleEffects) != wantEffects || msg.RuleEffects[0].RuleID != 1 {
						t.Fatalf("setup did not deliver rule effect: %+v", msg.RuleEffects)
					}
					effectID = msg.RuleEffects[0].ID
					if name == "forward_not_undoable" {
						effectID = msg.RuleEffects[1].ID
						var count int64
						if err := f.db.Model(&models.OutboundQueue{}).Where("recipient = ?", "copy@example.test").Count(&count).Error; err != nil || count != 1 {
							t.Fatalf("forward not queued: %d %v", count, err)
						}
					}
					if name == "later_change_preserved" {
						if err := f.db.Model(&msg).Update("folder", "Elsewhere").Error; err != nil {
							t.Fatal(err)
						}
					}
				}
				if name == "raw_sieve_conflict" {
					if err := f.db.Create(&models.SieveScript{MailboxID: f.mailboxID, Script: "keep;", Active: true}).Error; err != nil {
						t.Fatal(err)
					}
				}
				cfg := &config.Config{Environment: "test", APIMaxBodyBytes: 1024}
				if name == "rate_limited" {
					cfg.AuthRateLimitEnabled = true
					cfg.AuthRateLimitRPS = 1.0 / 3600
					cfg.AuthRateLimitBurst = 1
				}
				routers := NewRouters(f.db, f.jwt, cfg, nil)
				t.Cleanup(routers.Close)
				uri := route.path
				id := f.accountID
				if strings.Contains(uri, "/messages/") {
					id = messageID
				}
				if name == "invalid_id" {
					id = 0
				}
				if name == "missing" {
					id = 2147483647
				}
				uri = strings.ReplaceAll(uri, "{id}", fmt.Sprint(id))
				uri = strings.ReplaceAll(uri, "{ruleID}", "1")
				uri = strings.ReplaceAll(uri, "{effectID}", effectID)
				token := f.session
				if name == "unauthenticated" {
					token = ""
				}
				if name == "without_capability" {
					capability := models.CapMailWrite
					if route.method != "GET" {
						capability = models.CapMailRead
					}
					key, public, hash, err := auth.NewAPIKey()
					if err != nil {
						t.Fatal(err)
					}
					credential := models.Credential{AccountID: f.accountID, Kind: models.CredentialKindAPIKey, Name: op, PublicID: public, SecretHash: hash, Scope: models.CredentialScope{Permissions: []auth.ScopedPermission{{Capability: capability, Scope: grants.Mailbox(f.address)}}}}
					if err := f.db.Create(&credential).Error; err != nil {
						t.Fatal(err)
					}
					token = "Bearer " + key
				}
				if name == "rate_limited" {
					token = "Bearer rmk_invalid"
				}
				body := string(pair.Request.Body)
				if name == "malformed_json" {
					body = "{"
				}
				if name == "request_too_large" {
					body = strings.Repeat("x", 1025)
				}
				if name == "internal_error" || name == "session_unavailable" {
					const callback = "structured_rule_example_fault"
					table := "mail_rules"
					if strings.Contains(uri, "/messages/") {
						table = "messages"
					}
					if name == "session_unavailable" {
						table = "mailboxes"
					}
					fault := func(query *gorm.DB) {
						if query.Statement.Table == table {
							_ = query.AddError(errors.New("private rule storage fault"))
						}
					}
					if op == "deleteMailRule" && name == "internal_error" {
						if err := f.db.Callback().Delete().Before("gorm:delete").Register(callback, fault); err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = f.db.Callback().Delete().Remove(callback) })
					} else {
						if err := f.db.Callback().Query().Before("gorm:query").Register(callback, fault); err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = f.db.Callback().Query().Remove(callback) })
					}
				}
				send := func() *httptest.ResponseRecorder {
					req := httptest.NewRequest(route.method, uri, strings.NewReader(body))
					req.Header.Set("Content-Type", "application/json")
					req.RemoteAddr = "192.0.2.1:12345"
					if token != "" {
						req.Header.Set("Authorization", token)
					}
					rr := httptest.NewRecorder()
					routers.Public.ServeHTTP(rr, req)
					return rr
				}
				if name == "rate_limited" {
					send()
				}
				rr := send()
				if rr.Code != pair.Response.Status {
					t.Fatalf("status %d want %d: %s", rr.Code, pair.Response.Status, rr.Body)
				}
				// The transport allocates an effect UUID; normalize only that identity.
				got := strings.ReplaceAll(rr.Body.String(), effectID, "00000000-0000-4000-8000-000000000001")
				var undoneAt *time.Time
				if op == "undoMessageRuleEffect" && rr.Code == 200 {
					var response struct{ Data rules.Effect }
					if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil || response.Data.UndoneAt == nil {
						t.Fatalf("undo has no timestamp: %s (%v)", rr.Body, err)
					}
					undoneAt = response.Data.UndoneAt
					got = strings.ReplaceAll(got, undoneAt.Format(time.RFC3339Nano), "2026-11-22T12:00:00Z")
				}
				if rr.Code == 204 {
					if rr.Body.Len() != 0 {
						t.Fatal("204 has a body")
					}
				} else {
					assertPublicExampleBody(t, pair.Response.Body, []byte(got))
				}
				if rr.Code < 300 {
					switch op {
					case "createMailRule", "updateMailRule":
						var stored models.MailRule
						if err := f.db.Where("mailbox_id = ?", f.mailboxID).First(&stored).Error; err != nil {
							t.Fatal(err)
						}
						if stored.Name != definition.Name || stored.Actions[0].Value != "Finance" {
							t.Fatalf("rule not persisted: %+v", stored)
						}
					case "deleteMailRule":
						var count int64
						if err := f.db.Model(&models.MailRule{}).Where("mailbox_id = ?", f.mailboxID).Count(&count).Error; err != nil || count != 0 {
							t.Fatalf("rule not deleted: %d %v", count, err)
						}
					case "undoMessageRuleEffect":
						var msg models.Message
						if err := f.db.First(&msg, messageID).Error; err != nil || msg.Folder != "INBOX" || msg.RuleEffects[0].UndoneAt == nil || !msg.RuleEffects[0].UndoneAt.Equal(*undoneAt) {
							t.Fatalf("undo not persisted: %+v %v", msg, err)
						}
					}
				}
			})
		}
	}
}
