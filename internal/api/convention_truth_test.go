package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func TestWireConventionsRefuseByParseabilityAndVisibility(t *testing.T) {
	useRealGrants(t)
	fixture := newSendLimitFixture(t)
	other, _ := seedDomain(t, fixture.db, "other.test")
	shared, _ := seedDomain(t, fixture.db, "restmail.test")
	for _, mailbox := range []models.Mailbox{
		{DomainID: other.ID, LocalPart: "someone", Address: "someone@other.test", Active: true},
		{DomainID: shared.ID, LocalPart: "shared", Address: "shared@restmail.test", Active: true},
	} {
		if err := fixture.db.Create(&mailbox).Error; err != nil {
			t.Fatal(err)
		}
	}
	role := models.Role{Name: "status-contract-mail-reader"}
	if err := fixture.db.Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{models.CapMailRead, models.CapMailboxSession} {
		var cap models.Capability
		if err := fixture.db.Where("name = ?", name).First(&cap).Error; err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Create(&models.RoleCapability{RoleID: role.ID, CapabilityID: cap.ID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.db.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: fixture.accountID,
		RoleID: role.ID, Scope: "mailbox:shared@restmail.test", Mode: models.GrantActive,
		GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	pairs := readPublishedDescriptionPairs(t)
	doc, _ := liveDescription(t, fixture.router)
	compiler := descriptionSchemaCompiler(t, doc)
	const send = "/api/v1/messages/send"
	const intake = "/.well-known/smtp-tlsrpt"
	var hidden *httptest.ResponseRecorder
	for _, decision := range []struct {
		operation, name, path, code, field string
		status, wrong                      int
	}{
		{"receiveTlsReport", "unsupported_media", intake, "bad_request", "", 400, 422},
		{"sendMessage", "malformed_json", send, "bad_request", "", 400, 422},
		{"sendMessage", "from_missing", send, "validation_failed", "from", 422, 400},
		{"sendMessage", "foreign_sender", send, "not_found", "", 404, 403},
		{"sendMessage", "read_only_send_as", send, "forbidden", "", 403, 404},
	} {
		t.Run(decision.operation+"/"+decision.name, func(t *testing.T) {
			pair := publishedDescriptionPair(t, pairs, decision.operation, decision.name)
			if pair.Response.Status != decision.status {
				t.Fatalf("published status %d, decision requires %d", pair.Response.Status, decision.status)
			}
			token := fixture.session[len("Bearer "):]
			switch decision.name {
			case "unsupported_media":
				token = ""
				pair.Request.Body = publishedDescriptionPair(t, pairs, "receiveTlsReport", "stored").Request.Body
			case "malformed_json":
				var malformed string
				if err := json.Unmarshal(pair.Request.Body, &malformed); err != nil || json.Valid([]byte(malformed)) {
					t.Fatalf("expected malformed wire text, got %q (%v)", pair.Request.Body, err)
				}
				pair.Request.Body = []byte(malformed)
			}
			response := sendDescriptionPair(fixture.router, requirementRoute{http.MethodPost, decision.path}, token, pair)
			if err := refusalStatus(response, decision.status); err != nil {
				t.Fatal(err)
			}
			if err := refusalStatus(response, decision.wrong); err == nil {
				t.Fatalf("wrong status %d passed", decision.wrong)
			} else {
				t.Logf("wrong-status control rejected: %v", err)
			}
			assertRequirementResponse(t, pair, response)
			assertDescribedJSON(t, compiler, http.MethodPost, decision.path, response)
			var envelope struct {
				Error struct {
					Code    string `json:"code"`
					Details struct {
						Fields map[string]string `json:"fields"`
					} `json:"details"`
				} `json:"error"`
			}
			decodeInto(t, response, &envelope)
			if envelope.Error.Code != decision.code || decision.field != "" && envelope.Error.Details.Fields[decision.field] == "" {
				t.Fatalf("unexpected refusal: %s", response.Body)
			}
			if decision.name == "foreign_sender" {
				hidden = response
			}
		})
	}
	t.Run("missing_is_indistinguishable_from_hidden", func(t *testing.T) {
		if hidden == nil {
			t.Fatal("no hidden-sender response to compare")
		}
		missing := fixture.do(http.MethodPost, send, fixture.session, `{"from":"absent@other.test","to":["recipient@example.net"]}`)
		if err := refusalStatus(missing, http.StatusNotFound); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(missing.Body.Bytes(), hidden.Body.Bytes()) {
			t.Fatalf("missing sender = %s, hidden sender = %s", missing.Body, hidden.Body)
		}
		if err := refusalStatus(missing, http.StatusForbidden); err == nil {
			t.Fatal("403 passed for a missing sender")
		} else {
			t.Logf("wrong-status control rejected: %v", err)
		}
	})
	if got := outbound(fixture); got != 0 {
		t.Fatalf("refusals enqueued %d messages", got)
	}
}

func refusalStatus(response *httptest.ResponseRecorder, want int) error {
	if response.Code != want {
		return fmt.Errorf("status %d, want %d: %s", response.Code, want, response.Body)
	}
	return nil
}
