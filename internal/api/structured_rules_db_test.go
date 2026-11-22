package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

func ruleAnswer(t *testing.T, rr *httptest.ResponseRecorder, status int) map[string]any {
	t.Helper()
	if rr.Code != status {
		t.Fatalf("status %d, want %d: %s", rr.Code, status, rr.Body)
	}
	var answer struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	return answer.Data
}
func structuredRuleBody(name, folder string, enabled bool, position int) string {
	return fmt.Sprintf(`{"name":%q,"enabled":%t,"position":%d,"conditions":[{"field":"subject","operator":"contains","value":"invoice"}],"actions":[{"type":"fileinto","value":%q},{"type":"flag","value":"\\Flagged"}]}`, name, enabled, position, folder)
}
func structuredRulePath(f *credentialFixture) string {
	return fmt.Sprintf("/api/v1/accounts/%d/rules", f.accountID)
}

func TestAC01StructuredRuleStableIdentityLifecycle(t *testing.T) {
	useRealGrants(t)
	f := newCredentialFixture(t)
	path := structuredRulePath(f)
	created := ruleAnswer(t, f.do("POST", path, f.session, structuredRuleBody("Invoices", "Finance", true, 10)), 201)
	id := uint(created["id"].(float64))
	item := fmt.Sprintf("%s/%d", path, id)
	second := ruleAnswer(t, f.do("POST", path, f.session, structuredRuleBody("Second", "Other", true, 20)), 201)
	secondID := uint(second["id"].(float64))
	for _, step := range []struct {
		name, folder string
		enabled      bool
		position     int
	}{{"Renamed", "Invoices", true, 30}, {"Disabled", "Invoices", false, 30}, {"Enabled", "Invoices", true, 0}} {
		got := ruleAnswer(t, f.do("PUT", item, f.session, structuredRuleBody(step.name, step.folder, step.enabled, step.position)), 200)
		if uint(got["id"].(float64)) != id {
			t.Fatal("identity changed")
		}
		exported := ruleAnswer(t, f.do("GET", path+"/sieve", f.session, ""), 200)
		text := exported["script"].(string)
		if strings.Count(text, fmt.Sprintf("# restmail-rule %d ", id)) != 1 || strings.Count(text, fmt.Sprintf("# restmail-rule %d ", secondID)) != 1 {
			t.Fatalf("missing or duplicate identity: %s", text)
		}
		firstIndex := strings.Index(text, fmt.Sprintf("# restmail-rule %d ", id))
		secondIndex := strings.Index(text, fmt.Sprintf("# restmail-rule %d ", secondID))
		if (firstIndex < secondIndex) != (step.position < 20) {
			t.Fatalf("reorder lost: %s", text)
		}
		if strings.Contains(text, `fileinto "Invoices";`) != step.enabled {
			t.Fatalf("disabled action exported: %s", text)
		}
		legacy := ruleAnswer(t, f.do("GET", fmt.Sprintf("/api/v1/accounts/%d/sieve", f.accountID), f.session, ""), 200)
		if legacy["script"] != text {
			t.Fatal("Sieve views diverge")
		}
	}
	// A raw replacement cannot create an independent active script beside the rules.
	ruleAnswer(t, f.do("PUT", fmt.Sprintf("/api/v1/accounts/%d/sieve", f.accountID), f.session, `{"script":"keep;"}`), 422)
	// Cross-account rule IDs cannot be edited, read or deleted.
	other := f.seedMailbox(f.mustDomainID(), "other")
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		rr := f.do(method, fmt.Sprintf("/api/v1/accounts/%d/rules/%d", other, id), f.session, structuredRuleBody("Bad", "Other", true, 0))
		if rr.Code != 404 && rr.Code != 403 {
			t.Fatalf("cross-account %s: %d %s", method, rr.Code, rr.Body)
		}
	}
	if rr := f.do("DELETE", item, f.session, ""); rr.Code != 204 {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body)
	}
	exported := ruleAnswer(t, f.do("GET", path+"/sieve", f.session, ""), 200)["script"].(string)
	if strings.Contains(exported, fmt.Sprintf("# restmail-rule %d ", id)) {
		t.Fatal("deleted rule still exported")
	}
	if rr := f.do("DELETE", fmt.Sprintf("%s/%d", path, secondID), f.session, ""); rr.Code != 204 {
		t.Fatal(rr.Body)
	}
	var scripts int64
	if err := f.db.Model(&models.SieveScript{}).Where("mailbox_id = ?", f.mailboxID).Count(&scripts).Error; err != nil || scripts != 0 {
		t.Fatalf("orphan scripts %d: %v", scripts, err)
	}
	// Raw scripts are explicitly labelled, retained verbatim and never converted.
	ruleAnswer(t, f.do("PUT", fmt.Sprintf("/api/v1/accounts/%d/sieve", f.accountID), f.session, `{"script":"if true { keep; }"}`), 201)
	raw := ruleAnswer(t, f.do("GET", path+"/sieve", f.session, ""), 200)
	if raw["representation"] != "raw" || raw["script"] != "if true { keep; }" {
		t.Fatalf("raw silently converted: %v", raw)
	}
	ruleAnswer(t, f.do("POST", path, f.session, structuredRuleBody("Conflict", "Finance", true, 0)), 409)
}

func deliverStructuredMessage(t *testing.T, f *credentialFixture, subject string) uint {
	t.Helper()
	return uint(ruleAnswer(t, tryDeliverStructuredMessage(t, f, subject), 201)["id"].(float64))
}
func tryDeliverStructuredMessage(t *testing.T, f *credentialFixture, subject string) *httptest.ResponseRecorder {
	t.Helper()
	raw := "From: alice@remote.test\r\nTo: " + f.address + "\r\nSubject: " + subject + "\r\nMessage-ID: <" + strconv.FormatInt(time.Now().UnixNano(), 36) + "@remote.test>\r\n\r\nhello\r\n"
	body, _ := json.Marshal(map[string]any{"address": f.address, "sender": "alice@remote.test", "subject": subject, "body_text": "hello", "raw_message": []byte(raw)})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.10:50000"
	rr := httptest.NewRecorder()
	asGateway(deliveryCN, f.gatewayRouter()).ServeHTTP(rr, req)
	return rr
}
func TestAC02DeliveredRuleEffectsAttributionAndUndo(t *testing.T) {
	useRealGrants(t)
	f := newCredentialFixture(t)
	steps, _ := json.Marshal([]pipeline.FilterConfig{{Name: "sieve", Type: pipeline.FilterTypeTransform, Enabled: true, Config: json.RawMessage(`{}`)}})
	if err := f.db.Create(&models.Pipeline{DomainID: f.mustDomainID(), Direction: "inbound", Filters: steps, Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&models.QuotaUsage{MailboxID: f.mailboxID}).Error; err != nil {
		t.Fatal(err)
	}
	path := structuredRulePath(f)
	body := structuredRuleBody("Invoices", "Finance", true, 0)
	// A same-domain forward exercises the existing redirect policy and real queue persistence.
	target := f.seedMailbox(f.mustDomainID(), "copy")
	_ = target
	body = strings.Replace(body, `}]}`, `},{"type":"forward","value":"copy@example.test"}]}`, 1)
	created := ruleAnswer(t, f.do("POST", path, f.session, body), 201)
	ruleID := created["id"].(float64)
	messageID := deliverStructuredMessage(t, f, "invoice 42")
	var stored models.Message
	if err := f.db.First(&stored, messageID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Folder != "Finance" || !stored.IsFlagged {
		t.Fatalf("effects not applied: %+v", stored)
	}
	effectsPath := fmt.Sprintf("/api/v1/messages/%d/rule-effects", messageID)
	rr := f.do("GET", effectsPath, f.session, "")
	if rr.Code != 200 {
		t.Fatalf("effects: %d %s", rr.Code, rr.Body)
	}
	var answer struct {
		Data []struct {
			ID       string  `json:"id"`
			RuleID   float64 `json:"rule_id"`
			Type     string  `json:"type"`
			Undoable bool    `json:"undoable"`
		}
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if len(answer.Data) != 3 {
		t.Fatalf("want file,flag,forward: %s", rr.Body)
	}
	other := f.seedMailbox(f.mustDomainID(), "other")
	otherSession, err := f.jwt.GenerateSession(other, "other@example.test", 0, []string{"pwd", "otp"}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, probe := range []struct{ method, path string }{{"GET", effectsPath}, {"POST", effectsPath + "/" + answer.Data[0].ID + "/undo"}} {
		rr := f.do(probe.method, probe.path, "Bearer "+otherSession.AccessToken, "")
		if rr.Code != 403 && rr.Code != 404 {
			t.Fatalf("cross-account effect accessible: %d %s", rr.Code, rr.Body)
		}
	}
	var queued int64
	if err := f.db.Model(&models.OutboundQueue{}).Where("recipient = ?", "copy@example.test").Count(&queued).Error; err != nil || queued != 1 {
		t.Fatalf("forward count %d: %v", queued, err)
	}
	// Attribution survives rule deletion; a sent/queued forward is never called undoable.
	if rr := f.do("DELETE", fmt.Sprintf("%s/%d", path, uint(ruleID)), f.session, ""); rr.Code != 204 {
		t.Fatal(rr.Body)
	}
	for _, effect := range answer.Data {
		if effect.RuleID != ruleID {
			t.Fatalf("wrong attribution: %+v", effect)
		}
		undo := effectsPath + "/" + effect.ID + "/undo"
		if effect.Type == "forward" {
			if effect.Undoable {
				t.Fatal("forward pretends undoable")
			}
			if rr := f.do("POST", undo, f.session, ""); rr.Code != 409 {
				t.Fatalf("forward undo %d %s", rr.Code, rr.Body)
			}
		} else {
			if !effect.Undoable {
				t.Fatal("local effect not undoable")
			}
			ruleAnswer(t, f.do("POST", undo, f.session, ""), 200)
			ruleAnswer(t, f.do("POST", undo, f.session, ""), 200) // idempotent
		}
	}
	if err := f.db.First(&stored, messageID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Folder != "INBOX" || stored.IsFlagged {
		t.Fatalf("undo not persisted: %s flagged=%t", stored.Folder, stored.IsFlagged)
	}
	control := deliverStructuredMessage(t, f, "ordinary mail")
	rr = f.do("GET", fmt.Sprintf("/api/v1/messages/%d/rule-effects", control), f.session, "")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"data":[]`) {
		t.Fatalf("control attributed: %d %s", rr.Code, rr.Body)
	}
}
