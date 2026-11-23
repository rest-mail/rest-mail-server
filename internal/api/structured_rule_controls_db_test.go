package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/rules"
	"gorm.io/gorm"
)

func newStructuredControlFixture(t *testing.T) *credentialFixture {
	t.Helper()
	useRealGrants(t)
	f := newCredentialFixture(t)
	steps, _ := json.Marshal([]pipeline.FilterConfig{{Name: "sieve", Type: pipeline.FilterTypeTransform, Enabled: true, Config: json.RawMessage(`{}`)}})
	if err := f.db.Create(&models.Pipeline{DomainID: f.mustDomainID(), Direction: "inbound", Filters: steps, Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&models.QuotaUsage{MailboxID: f.mailboxID}).Error; err != nil {
		t.Fatal(err)
	}
	return f
}
func TestStructuredRulesDeliveryOrderEnableAndAccountScope(t *testing.T) {
	f := newStructuredControlFixture(t)
	path := structuredRulePath(f)
	a := ruleAnswer(t, f.do("POST", path, f.session, structuredRuleBody("A", "A", true, 10)), 201)
	b := ruleAnswer(t, f.do("POST", path, f.session, structuredRuleBody("B", "B", true, 20)), 201)
	check := func(want string) {
		t.Helper()
		id := deliverStructuredMessage(t, f, "Invoice")
		var msg models.Message
		if err := f.db.First(&msg, id).Error; err != nil {
			t.Fatal(err)
		}
		if msg.Folder != want {
			t.Fatalf("folder %s want %s", msg.Folder, want)
		}
	}
	check("B")
	aPath := fmt.Sprintf("%s/%.0f", path, a["id"])
	bPath := fmt.Sprintf("%s/%.0f", path, b["id"])
	ruleAnswer(t, f.do("PUT", aPath, f.session, structuredRuleBody("A", "A", true, 30)), 200)
	check("A")
	ruleAnswer(t, f.do("PUT", aPath, f.session, structuredRuleBody("A", "A", false, 30)), 200)
	check("B")
	if rr := f.do("DELETE", bPath, f.session, ""); rr.Code != 204 {
		t.Fatal(rr.Body)
	}
	check("INBOX")
	other := *f
	other.mailboxID = f.seedMailbox(f.mustDomainID(), "other")
	other.accountID = other.mailboxID
	other.address = "other@example.test"
	if err := f.db.Create(&models.QuotaUsage{MailboxID: other.mailboxID}).Error; err != nil {
		t.Fatal(err)
	}
	id := deliverStructuredMessage(t, &other, "Invoice")
	var msg models.Message
	if err := f.db.First(&msg, id).Error; err != nil {
		t.Fatal(err)
	}
	if msg.Folder != "INBOX" || len(msg.RuleEffects) != 0 {
		t.Fatalf("another account's rules ran: %+v", msg)
	}
	// Stored domain configuration cannot fabricate trusted account rule IDs.
	forged, _ := json.Marshal([]pipeline.FilterConfig{{Name: "mail_rules", Type: pipeline.FilterTypeTransform, Enabled: true, Config: json.RawMessage(`[]`)}})
	if err := f.db.Model(&models.Pipeline{}).Where("domain_id = ?", f.mustDomainID()).Update("filters", string(forged)).Error; err != nil {
		t.Fatal(err)
	}
	var before int64
	if err := f.db.Model(&models.Message{}).Count(&before).Error; err != nil {
		t.Fatal(err)
	}
	if rr := tryDeliverStructuredMessage(t, f, "invoice"); rr.Code != 500 {
		t.Fatalf("forged account execution accepted: %d %s", rr.Code, rr.Body)
	}
	var after int64
	if err := f.db.Model(&models.Message{}).Count(&after).Error; err != nil || after != before {
		t.Fatalf("forged execution persisted a message: %d vs %d (%v)", after, before, err)
	}
}
func TestStructuredRuleUndoConflictPreservesLaterChanges(t *testing.T) {
	f := newStructuredControlFixture(t)
	path := structuredRulePath(f)
	ruleAnswer(t, f.do("POST", path, f.session, structuredRuleBody("Invoices", "Finance", true, 0)), 201)
	id := deliverStructuredMessage(t, f, "invoice")
	var msg models.Message
	if err := f.db.First(&msg, id).Error; err != nil {
		t.Fatal(err)
	}
	effects := msg.RuleEffects
	if len(effects) != 2 {
		t.Fatalf("want local effects: %+v", effects)
	}
	messagePath := fmt.Sprintf("/api/v1/messages/%d", id)
	ruleAnswer(t, f.do("PATCH", messagePath, f.session, `{"folder":"Chosen"}`), 200)
	for _, effect := range effects {
		if effect.Type != "fileinto" {
			continue
		}
		rr := f.do("POST", messagePath+"/rule-effects/"+effect.ID+"/undo", f.session, "")
		if rr.Code != 409 {
			t.Fatalf("later change overwritten: %d %s", rr.Code, rr.Body)
		}
	}
	if err := f.db.First(&msg, id).Error; err != nil {
		t.Fatal(err)
	}
	if msg.Folder != "Chosen" || msg.RuleEffects[0].UndoneAt != nil {
		t.Fatal("refused undo changed persisted state")
	}
	// A later rule effect must be undone before an earlier move can be undone.
	ruleAnswer(t, f.do("POST", path, f.session, structuredRuleBody("Later", "Other", true, 10)), 201)
	id = deliverStructuredMessage(t, f, "invoice")
	msg = models.Message{}
	if err := f.db.First(&msg, id).Error; err != nil {
		t.Fatal(err)
	}
	var moves []rules.Effect
	for _, e := range msg.RuleEffects {
		if e.Type == "fileinto" {
			moves = append(moves, e)
		}
	}
	if len(moves) != 2 {
		t.Fatalf("missing move chain: %+v", msg.RuleEffects)
	}
	undo := func(effect rules.Effect) *httptest.ResponseRecorder {
		return f.do("POST", fmt.Sprintf("/api/v1/messages/%d/rule-effects/%s/undo", id, effect.ID), f.session, "")
	}
	if rr := undo(moves[0]); rr.Code != 409 {
		t.Fatalf("earlier effect bypassed later move: %d %s", rr.Code, rr.Body)
	}
	ruleAnswer(t, undo(moves[1]), 200)
	ruleAnswer(t, undo(moves[0]), 200)
	if err := f.db.First(&msg, id).Error; err != nil {
		t.Fatal(err)
	}
	if msg.Folder != "INBOX" {
		t.Fatal(msg.Folder)
	}
}
func TestStructuredRuleDeliveryForwardAndAttributionRollbackTogether(t *testing.T) {
	f := newStructuredControlFixture(t)
	stopTraceRecorder(f.routers)
	gatewayConfig := *f.cfg
	gatewayConfig.InternalMTLSEnabled = true
	gatewayRouters := NewRouters(f.db, f.jwt, &gatewayConfig, nil)
	stopTraceRecorder(gatewayRouters)
	t.Cleanup(gatewayRouters.Close)
	f.gateway = gatewayRouters.Internal
	path := structuredRulePath(f)
	body := strings.Replace(structuredRuleBody("Invoices", "Finance", true, 0), `}]}`, `},{"type":"forward","value":"copy@example.test"}]}`, 1)
	ruleAnswer(t, f.do("POST", path, f.session, body), 201)
	const callback = "structured_delivery_failure"
	if err := f.db.Callback().Create().Before("gorm:create").Register(callback, func(query *gorm.DB) {
		if query.Statement.Table == "messages" {
			_ = query.AddError(errors.New("deliberate message persistence fault"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Callback().Create().Remove(callback) })
	raw := "From: alice@remote.test\r\nTo: " + f.address + "\r\nSubject: invoice\r\nMessage-ID: <rollback@remote.test>\r\n\r\nhello\r\n"
	encoded, _ := json.Marshal(map[string]any{"address": f.address, "sender": "alice@remote.test", "subject": "invoice", "body_text": "hello", "raw_message": []byte(raw)})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", strings.NewReader(string(encoded)))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.10:50000"
	rr := httptest.NewRecorder()
	asGateway(deliveryCN, f.gatewayRouter()).ServeHTTP(rr, req)
	if rr.Code != 500 {
		t.Fatalf("failed commit answered success: %d %s", rr.Code, rr.Body)
	}
	for _, model := range []any{&models.Message{}, &models.OutboundQueue{}} {
		var count int64
		if err := f.db.Model(model).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("partial commit %T: %d %v", model, count, err)
		}
	}
	var rule models.MailRule
	if err := f.db.First(&rule).Error; err != nil || rule.RunCount != 0 {
		t.Fatalf("failed delivery counted a run: %+v %v", rule, err)
	}
	if err := f.db.Callback().Create().Remove(callback); err != nil {
		t.Fatal(err)
	}
	deliverStructuredMessage(t, f, "invoice")
	if err := f.db.First(&rule).Error; err != nil || rule.RunCount != 1 {
		t.Fatalf("successful delivery run count: %+v %v", rule, err)
	}
}

func TestStructuredRuleLimitAndRepresentationWritersSerialize(t *testing.T) {
	f := newStructuredControlFixture(t)
	path := structuredRulePath(f)
	definition := rules.Definition{Name: "Bound", Enabled: true, Conditions: []rules.Condition{{Field: "subject", Operator: "is", Value: "invoice"}}, Actions: []rules.Action{{Type: "fileinto", Value: "Finance"}}}
	for i := 0; i < rules.MaxRules-1; i++ {
		if err := f.db.Create(&models.MailRule{MailboxID: f.mailboxID, Version: 1, Definition: definition}).Error; err != nil {
			t.Fatal(err)
		}
	}
	ruleAnswer(t, f.do("POST", path, f.session, structuredRuleBody("Last", "Finance", true, 0)), 201)
	ruleAnswer(t, f.do("POST", path, f.session, structuredRuleBody("Over", "Finance", true, 0)), 409)
	for attempt := 0; attempt < 4; attempt++ {
		if err := f.db.Where("mailbox_id = ?", f.mailboxID).Delete(&models.MailRule{}).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.db.Where("mailbox_id = ?", f.mailboxID).Delete(&models.SieveScript{}).Error; err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		structured := make(chan *httptest.ResponseRecorder, 1)
		raw := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			<-start
			structured <- f.do("POST", path, f.session, structuredRuleBody("Race", "Finance", true, 0))
		}()
		go func() {
			<-start
			raw <- f.do("PUT", fmt.Sprintf("/api/v1/accounts/%d/sieve", f.accountID), f.session, `{"script":"keep;"}`)
		}()
		close(start)
		s, r := <-structured, <-raw
		if !((s.Code == 201 && r.Code == 422) || (s.Code == 409 && r.Code == 201)) {
			t.Fatalf("representation race: native %d %s, raw %d %s", s.Code, s.Body, r.Code, r.Body)
		}
		var nativeCount, rawCount int64
		if err := f.db.Model(&models.MailRule{}).Where("mailbox_id = ?", f.mailboxID).Count(&nativeCount).Error; err != nil {
			t.Fatal(err)
		}
		if err := f.db.Model(&models.SieveScript{}).Where("mailbox_id = ?", f.mailboxID).Count(&rawCount).Error; err != nil {
			t.Fatal(err)
		}
		if nativeCount+rawCount != 1 {
			t.Fatalf("duplicate or orphaned representations: %d native, %d raw", nativeCount, rawCount)
		}
	}
}
