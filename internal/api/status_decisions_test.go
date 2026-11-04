package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

func TestSendingRefusalsHideUnknownSendersButExplainVisibleOnes(t *testing.T) {
	useRealGrants(t)
	fixture := newSendLimitFixture(t)
	var domain models.Domain
	if err := fixture.db.First(&domain).Error; err != nil {
		t.Fatal(err)
	}
	fixture.seedMailbox(domain.ID, "stranger")
	for _, operation := range []string{"sendMessage", "forwardMessage", "sendDraft", "releaseHeldMessage"} {
		t.Run(operation, func(t *testing.T) {
			message := models.Message{MailboxID: fixture.mailboxID, Folder: "Drafts", IsDraft: true,
				Sender: "stranger@example.test", RecipientsTo: models.JSONB(`["recipient@remote.test"]`), RecipientsCc: models.JSONB(`[]`)}
			if err := fixture.db.Create(&message).Error; err != nil {
				t.Fatal(err)
			}
			path := "/api/v1/messages/send"
			authorization := fixture.session
			var held models.ScheduledSend
			if operation == "forwardMessage" {
				path = fmt.Sprintf("/api/v1/messages/%d/forward", message.ID)
			} else if operation == "sendDraft" {
				path = fmt.Sprintf("/api/v1/messages/draft/%d/send", message.ID)
			} else if operation == "releaseHeldMessage" {
				held = seedHeld(fixture)
				path = fmt.Sprintf("/api/v1/messages/scheduled/%d/release", held.ID)
				authorization = twoFactorSession(fixture)
			}
			var missingBody []byte
			for _, sender := range []string{"missing@example.test", "stranger@example.test"} {
				body := fmt.Sprintf(`{"from":%q,"to":["recipient@remote.test"]}`, sender)
				if operation == "sendDraft" {
					if err := fixture.db.Model(&message).Update("sender", sender).Error; err != nil {
						t.Fatal(err)
					}
					body = `{}`
				} else if operation == "releaseHeldMessage" {
					if err := fixture.db.Model(&held).Update("request", models.JSONB(body)).Error; err != nil {
						t.Fatal(err)
					}
					body = `{}`
				}
				response := fixture.do(http.MethodPost, path, authorization, body)
				if response.Code != http.StatusNotFound {
					t.Fatalf("%s: %d %s, want 404", sender, response.Code, response.Body)
				}
				if missingBody == nil {
					missingBody = bytes.Clone(response.Body.Bytes())
				} else if !bytes.Equal(missingBody, response.Body.Bytes()) {
					t.Fatalf("hidden sender differs from missing sender: %s != %s", response.Body, missingBody)
				}
			}
			var persisted models.Message
			if err := fixture.db.First(&persisted, message.ID).Error; err != nil || !persisted.IsDraft {
				t.Fatalf("refused operation removed its source: %v", err)
			}
			if operation == "releaseHeldMessage" {
				if err := fixture.db.First(&held, held.ID).Error; err != nil || held.Status != "held" {
					t.Fatalf("refused release changed its hold: %v, status %q", err, held.Status)
				}
			}
		})
	}
	role := models.Role{Name: "status-decision-reader"}
	if err := fixture.db.Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{models.CapMailRead, models.CapMailboxSession} {
		var capability models.Capability
		if err := fixture.db.Where("name = ?", name).First(&capability).Error; err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Create(&models.RoleCapability{RoleID: role.ID, CapabilityID: capability.ID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.db.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: fixture.accountID,
		RoleID: role.ID, Scope: "mailbox:stranger@example.test", Mode: models.GrantActive,
		GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	routers := NewRouters(fixture.db, fixture.jwt, &config.Config{Environment: "test"}, nil)
	t.Cleanup(routers.Close)
	fixture.router = routers.Public
	response := fixture.do(http.MethodPost, "/api/v1/messages/send", fixture.session,
		`{"from":"stranger@example.test","to":["recipient@remote.test"]}`)
	if response.Code != http.StatusForbidden {
		t.Fatalf("visible sender: %d %s, want 403", response.Code, response.Body)
	}
	for _, expected := range []struct {
		model any
		count int64
	}{{&models.OutboundQueue{}, 0}, {&models.ScheduledSend{}, 1}} {
		var count int64
		if err := fixture.db.Model(expected.model).Count(&count).Error; err != nil || count != expected.count {
			t.Fatalf("refusals changed outbound work: count=%d, want %d, error=%v", count, expected.count, err)
		}
	}
}

func TestHeldSendPolicyRefusalsNameTheField(t *testing.T) {
	useRealGrants(t)
	fixture := newSendLimitFixture(t)
	held := seedHeld(fixture)
	if err := fixture.db.Model(&models.Domain{}).Where("name = ?", "example.test").
		Update("sending_policy", models.SendingPolicyBulk).Error; err != nil {
		t.Fatal(err)
	}
	response := fixture.do(http.MethodPost, fmt.Sprintf("/api/v1/messages/scheduled/%d/release", held.ID), twoFactorSession(fixture), `{}`)
	var answer struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				Fields map[string]string `json:"fields"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusUnprocessableEntity || answer.Error.Code != "validation_failed" || answer.Error.Details.Fields["list_unsubscribe"] == "" {
		t.Fatalf("held send policy refusal must name list_unsubscribe: %d %s", response.Code, response.Body)
	}
	if err := fixture.db.First(&held, held.ID).Error; err != nil || held.Status != "held" || outbound(fixture) != 0 {
		t.Fatalf("refused release changed held mail: %v, status %q", err, held.Status)
	}
}

func TestSendSchedulingRefusalsNameTheField(t *testing.T) {
	useRealGrants(t)
	fixture := newCredentialFixture(t)
	for _, scheduled := range []time.Time{time.Now().Add(-time.Hour), time.Now().Add(366 * 24 * time.Hour)} {
		body := fmt.Sprintf(`{"from":%q,"to":["recipient@remote.test"],"scheduled_at":%q}`,
			fixture.address, scheduled.UTC().Format(time.RFC3339))
		response := fixture.do(http.MethodPost, "/api/v1/messages/send", fixture.session, body)
		var answer struct {
			Error struct {
				Code    string `json:"code"`
				Details struct {
					Fields map[string]string `json:"fields"`
				} `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusUnprocessableEntity || answer.Error.Code != "validation_failed" || answer.Error.Details.Fields["scheduled_at"] == "" {
			t.Errorf("scheduling refusal must name scheduled_at: %d %s", response.Code, response.Body)
		}
	}
}

func TestStatusDecisionsPublishedPairsWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" || os.Getenv("DB_HOST") == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY and DB_HOST for local status-decision exchanges")
	}
	useRealGrants(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct{ operation, name, path string }{
		{"sendMessage", "foreign_sender", "/api/v1/messages/send"},
		{"sendMessage", "scheduled_at_past", "/api/v1/messages/send"},
		{"sendMessage", "scheduled_at_too_far", "/api/v1/messages/send"},
		{"sendMessage", "unsubscribe_not_allowed", "/api/v1/messages/send"},
		{"forwardMessage", "missing", "/api/v1/messages/{id}/forward"},
		{"sendDraft", "missing", "/api/v1/messages/draft/{id}/send"},
		{"releaseHeldMessage", "missing", "/api/v1/messages/scheduled/{id}/release"},
		{"receiveTlsReport", "no_matching_domains", "/.well-known/smtp-tlsrpt"},
	} {
		t.Run(scenario.operation+"/"+scenario.name, func(t *testing.T) {
			fixture := newSendLimitFixture(t)
			if err := fixture.db.Model(&models.Mailbox{}).Where("id = ?", fixture.mailboxID).
				Updates(map[string]any{"address": "admin@restmail.test", "local_part": "admin"}).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.Model(&models.Domain{}).Where("name = ?", "example.test").
				Updates(map[string]any{"name": "restmail.test", "sending_policy": models.SendingPolicyTransactional}).Error; err != nil {
				t.Fatal(err)
			}
			authorization := fixture.session
			if scenario.operation == "releaseHeldMessage" {
				authorization = twoFactorSession(fixture)
			}
			handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				request.Header.Set("Authorization", authorization)
				fixture.router.ServeHTTP(writer, request)
			})
			pair := pairs[scenario.operation][scenario.name]
			examplepairs.Proven(t, scenario.operation, scenario.name)
			checkQueueMutationPairWithVertrag(t, binary,
				bulkQueueExampleScenario{accountReadScenario: accountReadScenario{handler: handler}},
				http.MethodPost, scenario.path, scenario.name, pair, 513)
		})
	}
}
