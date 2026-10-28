package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/api/endpoint"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/bootstrap"
	"github.com/restmail/restmail/internal/db/models"
	smtpgw "github.com/restmail/restmail/internal/gateway/smtp"
	"github.com/restmail/restmail/internal/grants"
	"github.com/restmail/restmail/internal/pipeline"
	"github.com/restmail/restmail/internal/sendlimits"
	"gorm.io/gorm"
)

func layeredSendFixture(t *testing.T) (*gorm.DB, *MessageHandler, models.Mailbox, models.Mailbox) {
	db := draftSendPostgres(t)
	if err := db.AutoMigrate(&models.SendLimitPolicy{}, &models.SendLimitCounter{}, &models.SendLimitNotice{}, &models.ScheduledSend{}, &models.AuditEntry{}, &models.Role{}, &models.Capability{}, &models.RoleCapability{}, &models.Grant{}, &models.GrantActivation{}, &models.Credential{}); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.EnsureRBAC(db); err != nil {
		t.Fatal(err)
	}
	domain := models.Domain{Name: "layered.example.test", Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	owner := models.Mailbox{DomainID: domain.ID, Address: "owner@" + domain.Name, LocalPart: "owner", Active: true}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	child := models.Mailbox{DomainID: domain.ID, Address: "child@" + domain.Name, LocalPart: "child", OwnerID: &owner.ID, Active: true}
	if err := db.Create(&child).Error; err != nil {
		t.Fatal(err)
	}
	var role models.Role
	if err := db.First(&role, "name = ?", models.RoleOwner).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: owner.ID, RoleID: role.ID, Scope: grants.Mailbox(child.Address), Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.QuotaUsage{MailboxID: child.ID}).Error; err != nil {
		t.Fatal(err)
	}
	h := NewMessageHandler(db, nil, nil, "", nil)
	h.forwardRoot = t.TempDir()
	h.SetLayeredSendLimits(&sendlimits.Service{Defaults: models.SendLimitValues{MessagesPerHour: 1, MessagesPerDay: 10, RecipientsPerHour: 10, RecipientsPerDay: 20}})
	return db, h, owner, child
}
func limitRequest(claims *auth.Claims) *http.Request {
	return withClaims(httptest.NewRequest("POST", "/api/v1/messages/send", nil), claims)
}
func heldCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.ScheduledSend{}).Where("status = ?", "held").Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestLayeredSendHoldPreservesMailAndHumanRelease(t *testing.T) {
	db, h, owner, child := layeredSendFixture(t)
	machine := &auth.Claims{AccountID: child.ID, Address: child.Address, CredentialID: 77, CredentialKind: models.CredentialKindAppPassword}
	req := sendMessageRequest{From: child.Address, To: []string{"far@remote.test"}, Cc: []string{"cc@remote.test"}, Bcc: []string{"hidden@remote.test"}, Subject: "retained", BodyText: "body"}
	if out, err := h.sendMessage(limitRequest(machine), req); err != nil || out.Code != 201 {
		t.Fatalf("first send=%+v %v", out, err)
	}
	parts := []pipeline.Attachment{{Filename: "empty.txt", ContentType: "text/plain", Content: ""}}
	out, err := h.sendMessageWithParts(limitRequest(machine), req, parts)
	if err != nil || out.Code != 202 {
		t.Fatalf("over limit=%+v %v; want 202 held", out, err)
	}
	held, ok := out.Value.(ScheduledMessage)
	if !ok || held.Status != "held" {
		t.Fatalf("not held: %+v", out.Value)
	}
	var stored models.ScheduledSend
	if err := db.First(&stored, held.ID).Error; err != nil {
		t.Fatal(err)
	}
	var payload sendMessageRequest
	if err := json.Unmarshal(stored.Request, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Bcc) != 1 || payload.BodyText != "body" || stored.CredentialID != 77 || len(stored.ForwardParts) == 0 {
		t.Fatalf("hold lost content or attribution: %+v", stored)
	}
	var queued int64
	db.Model(&models.OutboundQueue{}).Count(&queued)
	if queued != 3 {
		t.Fatalf("held send enqueued mail: %d", queued)
	}
	if _, err := h.releaseHeldSend(limitRequest(machine), scheduledPath{ID: uint32(held.ID)}); err == nil {
		t.Fatal("machine released hold")
	}
	human := &auth.Claims{AccountID: owner.ID, Address: owner.Address, TwoFactor: true, SessionID: "person-session"}
	// Release retains the original zero-byte attachment and all envelope recipients.
	if out, err := h.releaseHeldSend(limitRequest(human), scheduledPath{ID: uint32(held.ID)}); err != nil || out.Code != 200 {
		t.Fatalf("human release=%+v %v", out, err)
	}
	db.First(&stored, held.ID)
	if stored.Status != "sent" {
		t.Fatalf("held state=%s", stored.Status)
	}
	if out, err := h.releaseHeldSend(limitRequest(human), scheduledPath{ID: uint32(held.ID)}); err == nil {
		t.Fatalf("duplicate release allowed: %+v", out)
	}
	var entry models.AuditEntry
	db.Last(&entry, "action = ?", "send_limits.release")
	if entry.ActorID == nil || *entry.ActorID != owner.ID || entry.SessionID != "person-session" {
		t.Fatalf("release actor: %+v", entry)
	}
}

func TestLayeredDraftAndScheduledSendBecomeDurableHolds(t *testing.T) {
	db, h, _, child := layeredSendFixture(t)
	claims := &auth.Claims{AccountID: child.ID, Address: child.Address, CredentialKind: models.CredentialKindAppPassword}
	req := sendMessageRequest{From: child.Address, To: []string{"far@remote.test"}, BodyText: "body"}
	if _, err := h.sendMessage(limitRequest(claims), req); err != nil {
		t.Fatal(err)
	}
	draft := models.Message{MailboxID: child.ID, Folder: "Drafts", Sender: child.Address, Subject: "draft", BodyText: "draft body", IsDraft: true, RecipientsTo: models.JSONB(`["far@remote.test"]`), RecipientsCc: models.JSONB(`[]`)}
	if err := db.Create(&draft).Error; err != nil {
		t.Fatal(err)
	}
	if out, err := h.sendDraft(limitRequest(claims), sendDraftRequest{ID: uint32(draft.ID)}); err != nil || out.Code != 202 {
		t.Fatalf("draft hold: %+v %v", out, err)
	}
	if heldCount(t, db) != 1 {
		t.Fatal("draft hold missing")
	}
	body, _ := json.Marshal(req)
	row := models.ScheduledSend{AccountID: child.ID, From: child.Address, To: models.JSONB(`["far@remote.test"]`), SendAt: time.Now().Add(-time.Minute), Status: "pending", Request: body}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	h.runScheduledSends(context.Background())
	db.First(&row, row.ID)
	if row.Status != "held" || heldCount(t, db) != 2 {
		t.Fatalf("scheduled mail lost: %+v", row)
	}
}

func TestLayeredLimitChangesRequireHumanAndRespectParent(t *testing.T) {
	db, h, owner, child := layeredSendFixture(t)
	limits := models.SendLimitValues{MessagesPerHour: 2, MessagesPerDay: 10, RecipientsPerHour: 10, RecipientsPerDay: 20}
	machine := &auth.Claims{AccountID: owner.ID, TwoFactor: true, CredentialKind: models.CredentialKindAPIKey, CredentialID: 5}
	in := setSendLimitsRequest{ID: uint32(owner.ID), Body: limits}
	if _, err := h.setAccountSendLimits(limitRequest(machine), in); err == nil {
		t.Fatal("machine changed limits")
	}
	human := &auth.Claims{AccountID: owner.ID, Address: owner.Address, TwoFactor: true, SessionID: "human"}
	if _, err := h.setAccountSendLimits(limitRequest(human), in); err != nil {
		t.Fatal(err)
	}
	in.ID = uint32(child.ID)
	in.Body.MessagesPerHour = 3
	if _, err := h.setAccountSendLimits(limitRequest(human), in); err == nil {
		t.Fatal("child limit above owner")
	}
	in.Body.MessagesPerHour = 1
	if _, err := h.setAccountSendLimits(limitRequest(human), in); err != nil {
		t.Fatal(err)
	}
	effective, err := h.layeredLimits.EffectiveAccount(db, child.ID)
	if err != nil || effective.MessagesPerHour != 1 {
		t.Fatalf("child limits: %+v %v", effective, err)
	}
}

func TestLayeredRESTAndSMTPShareTheSameOwnerBudget(t *testing.T) {
	db, h, owner, child := layeredSendFixture(t)
	machine := &auth.Claims{AccountID: child.ID, Address: child.Address, CredentialID: 77, CredentialKind: models.CredentialKindAppPassword}
	req := sendMessageRequest{From: child.Address, To: []string{"far@remote.test"}, BodyText: "REST consumes budget"}
	if out, err := h.sendMessage(limitRequest(machine), req); err != nil || out.Code != 201 {
		t.Fatalf("REST admission: %+v %v", out, err)
	}
	// A separate gateway service instance sees REST's persistent reservation.
	service := *h.layeredLimits
	gateway := smtpgw.NewLimitedStore(db, "", &service).(smtpgw.SubmissionBudget)
	limited, err := gateway.ReserveSubmission(smtpgw.SubmissionAttempt{AccountID: owner.ID, CredentialID: 88, Sender: owner.Address, Recipients: 1, Remote: "192.0.2.5"})
	if err != nil || !limited {
		t.Fatalf("SMTP bypassed REST owner budget: limited=%v err=%v", limited, err)
	}
	var e models.AuditEntry
	db.Last(&e, "action = ?", "send_limits.hit")
	if e.Method != "SMTP" || e.Status != 451 || e.ActorID == nil || *e.ActorID != owner.ID || e.CredentialID == nil || *e.CredentialID != 88 {
		t.Fatalf("SMTP limit attribution: %+v", e)
	}
	var queued int64
	db.Model(&models.OutboundQueue{}).Count(&queued)
	if queued != 1 {
		t.Fatalf("SMTP limit queued mail: %d", queued)
	}
	observer := &sendLimitNoticeObserver{}
	h.broker, h.newMail = observer, observer
	h.dispatchSendLimitNotices(context.Background())
	h.dispatchSendLimitNotices(context.Background())
	if observer.events != 1 || observer.pushes != 1 || observer.owner != owner.ID {
		t.Fatalf("SMTP notice did not reach existing SSE/push exactly once: %+v", observer)
	}
}

type sendLimitNoticeObserver struct {
	events, pushes int
	owner          uint
}

func (o *sendLimitNoticeObserver) Publish(owner uint, _ SSEEvent) { o.owner = owner; o.events++ }
func (o *sendLimitNoticeObserver) NewMail(owner uint)             { o.owner = owner; o.pushes++ }

func TestLayeredHumanReleaseKeepsDomainCeilingAndSameHold(t *testing.T) {
	db, h, owner, child := layeredSendFixture(t)
	h.layeredLimits.Domain = models.SendLimitValues{MessagesPerHour: 1}
	machine := &auth.Claims{AccountID: child.ID, Address: child.Address, CredentialID: 77, CredentialKind: models.CredentialKindAppPassword}
	req := sendMessageRequest{From: child.Address, To: []string{"far@remote.test"}, BodyText: "retained"}
	if _, err := h.sendMessage(limitRequest(machine), req); err != nil {
		t.Fatal(err)
	}
	held, err := h.sendMessage(limitRequest(machine), req)
	if err != nil {
		t.Fatal(err)
	}
	id := held.Value.(ScheduledMessage).ID
	human := &auth.Claims{AccountID: owner.ID, Address: owner.Address, TwoFactor: true, SessionID: "person"}
	out, err := h.releaseHeldSend(limitRequest(human), scheduledPath{ID: uint32(id)})
	if err != nil || out.Code != 202 || out.Value.(ScheduledMessage).ID != id || out.Value.(ScheduledMessage).Status != "held" || heldCount(t, db) != 1 {
		t.Fatalf("domain ceiling lost original hold: %+v %v", out, err)
	}
	var queued int64
	db.Model(&models.OutboundQueue{}).Count(&queued)
	if queued != 1 {
		t.Fatalf("release bypassed domain budget: queued %d", queued)
	}
	var e models.AuditEntry
	db.Last(&e, "action = ?", "send_limits.hit")
	if e.ActorID == nil || *e.ActorID != owner.ID || e.Target["scope"] != "domain:1" || e.SessionID != "person" {
		t.Fatalf("human hit attribution: %+v", e)
	}
}

func TestLayeredConcurrentHumanReleaseSendsOnce(t *testing.T) {
	db, h, owner, child := layeredSendFixture(t)
	machine := &auth.Claims{AccountID: child.ID, Address: child.Address, CredentialKind: models.CredentialKindAppPassword}
	req := sendMessageRequest{From: child.Address, To: []string{"far@remote.test"}, BodyText: "retained"}
	if _, err := h.sendMessage(limitRequest(machine), req); err != nil {
		t.Fatal(err)
	}
	held, err := h.sendMessage(limitRequest(machine), req)
	if err != nil {
		t.Fatal(err)
	}
	id := held.Value.(ScheduledMessage).ID
	human := &auth.Claims{AccountID: owner.ID, Address: owner.Address, TwoFactor: true, SessionID: "person"}
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := h.releaseHeldSend(limitRequest(human), scheduledPath{ID: uint32(id)})
			if err != nil {
				var refusal *endpoint.Error
				if errors.As(err, &refusal) {
					codes <- refusal.Status
				} else {
					t.Error(err)
					codes <- 500
				}
				return
			}
			codes <- out.Code
		}()
	}
	wg.Wait()
	close(codes)
	seen := map[int]int{}
	for code := range codes {
		seen[code]++
	}
	if seen[200] != 1 || seen[409] != 1 {
		t.Fatalf("concurrent release responses: %v", seen)
	}
	var queued int64
	db.Model(&models.OutboundQueue{}).Count(&queued)
	if queued != 2 {
		t.Fatalf("duplicate released mail: queued %d", queued)
	}
}
