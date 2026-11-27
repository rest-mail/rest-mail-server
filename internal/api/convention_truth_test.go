package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
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
	const draftSend = "/api/v1/messages/draft/{id}/send"
	draft := models.Message{MailboxID: fixture.mailboxID, Folder: "Drafts", IsDraft: true,
		Sender: "admin@restmail.test", RecipientsTo: models.JSONB(`["recipient@example.net"]`), RecipientsCc: models.JSONB(`[]`)}
	if err := fixture.db.Create(&draft).Error; err != nil {
		t.Fatal(err)
	}
	var hidden *httptest.ResponseRecorder
	for _, decision := range []struct {
		operation, name, path, code, field string
		status, wrong                      int
	}{
		{"receiveTlsReport", "unsupported_media", intake, "bad_request", "", 400, 422},
		{"sendMessage", "malformed_json", send, "bad_request", "", 400, 422},
		{"sendDraft", "malformed_json", draftSend, "bad_request", "", 400, 422},
		{"receiveTlsReport", "malformed_json", intake, "bad_request", "", 400, 422},
		{"sendMessage", "from_missing", send, "validation_failed", "from", 422, 400},
		{"sendMessage", "foreign_sender", send, "not_found", "", 404, 403},
		{"sendMessage", "read_only_send_as", send, "forbidden", "", 403, 404},
	} {
		t.Run(decision.operation+"/"+decision.name, func(t *testing.T) {
			examplepairs.Proven(t, decision.operation, decision.name)
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
				switch decision.operation {
				case "sendDraft":
					pair = withPathID(pair, draft.ID)
				case "receiveTlsReport":
					token = ""
				}
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
	var kept models.Message
	if err := fixture.db.First(&kept, draft.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !kept.IsDraft || kept.Folder != "Drafts" {
		t.Fatalf("malformed sendDraft moved the draft: draft=%v folder=%q", kept.IsDraft, kept.Folder)
	}
	var reports int64
	if err := fixture.db.Model(&models.TLSReport{}).Count(&reports).Error; err != nil {
		t.Fatal(err)
	}
	if reports != 0 {
		t.Fatalf("malformed TLS report stored %d reports", reports)
	}
}

// Every operation whose live description takes an application/json request body
// publishes a pair sending truncated JSON and answering 400 bad_request, so the
// malformed-400 half of the 400/422 convention is stated for each body, not only
// for the operations someone remembered (#2099 AC02).
func TestEveryJSONBodyPublishesItsMalformedRefusal(t *testing.T) {
	fixture := newSendLimitFixture(t)
	doc, _ := liveDescription(t, fixture.router)
	pairs := readPublishedDescriptionPairs(t)
	operations := jsonBodyOperations(t, doc)
	if len(operations) == 0 {
		t.Fatal("the live description has no application/json request bodies")
	}
	var missing []string
	for _, operation := range operations {
		if !publishesMalformedRefusal(pairs[operation]) {
			missing = append(missing, operation)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%d of %d JSON-body operations publish no malformed_json 400 pair: %v", len(missing), len(operations), missing)
	}
	t.Logf("%d JSON-body operations each publish a malformed-body 400", len(operations))
	t.Run("control", func(t *testing.T) {
		without := maps.Clone(pairs["sendDraft"])
		delete(without, "malformed_json")
		if publishesMalformedRefusal(without) {
			t.Fatal("sendDraft without its malformed_json pair still counted as covered")
		}
		wrong := maps.Clone(pairs["sendDraft"])
		pair := wrong["malformed_json"]
		pair.Response.Status = http.StatusUnprocessableEntity
		wrong["malformed_json"] = pair
		if publishesMalformedRefusal(wrong) {
			t.Fatal("a malformed body answered 422 still counted as covered")
		}
	})
}

func jsonBodyOperations(t *testing.T, doc map[string]any) []string {
	t.Helper()
	paths, _ := doc["paths"].(map[string]any)
	var operations []string
	for _, item := range paths {
		methods, _ := item.(map[string]any)
		for _, raw := range methods {
			operation, _ := raw.(map[string]any)
			body, _ := operation["requestBody"].(map[string]any)
			content, _ := body["content"].(map[string]any)
			if _, ok := content["application/json"]; !ok {
				continue
			}
			id, _ := operation["operationId"].(string)
			if id == "" {
				t.Fatalf("a JSON-body operation has no operationId: %v", operation["summary"])
			}
			operations = append(operations, id)
		}
	}
	sort.Strings(operations)
	return operations
}

func publishesMalformedRefusal(named map[string]oapifly.ExamplePair) bool {
	for _, pair := range named {
		if pair.Requires["body"] != "malformed" || pair.Response.Status != http.StatusBadRequest {
			continue
		}
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(pair.Response.Body, &envelope) == nil && envelope.Error.Code == "bad_request" {
			return true
		}
	}
	return false
}

func refusalStatus(response *httptest.ResponseRecorder, want int) error {
	if response.Code != want {
		return fmt.Errorf("status %d, want %d: %s", response.Code, want, response.Body)
	}
	return nil
}
