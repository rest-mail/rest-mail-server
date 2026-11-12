package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"gorm.io/gorm"
)

// A local recipient that refused a send reads as failed with the reason, not as unknown
// (#1899): the send answers 201 for its other recipients, and this is the only place the
// sender can learn the refusal. What the refusing filter said stays with the server.
func TestLocalRefusalReadsAsFailed(t *testing.T) {
	const filterMessage = "rejected by internal rule 7 on mx7.secret.idor.test"
	cases := []struct {
		name     string
		refuse   func(t *testing.T, tx *gorm.DB, domain uint, recipient models.Mailbox)
		engine   func() *pipeline.Engine
		category models.FailureCategory
	}{
		{
			name: "mailbox full",
			refuse: func(t *testing.T, tx *gorm.DB, _ uint, recipient models.Mailbox) {
				if err := tx.Model(&recipient).Updates(map[string]any{"quota_bytes": 1, "quota_used_bytes": 1}).Error; err != nil {
					t.Fatal(err)
				}
			},
			engine:   func() *pipeline.Engine { return nil },
			category: models.FailureMailboxFull,
		},
		{
			name: "pipeline rejection",
			refuse: func(t *testing.T, tx *gorm.DB, domain uint, _ models.Mailbox) {
				stored, _ := json.Marshal([]pipeline.FilterConfig{{Name: "refuses", Type: pipeline.FilterTypeAction, Enabled: true}})
				if err := tx.Create(&models.Pipeline{DomainID: domain, Direction: "outbound", Active: true, Filters: []byte(`[]`)}).Error; err != nil {
					t.Fatal(err)
				}
				if err := tx.Create(&models.Pipeline{DomainID: domain, Direction: "inbound", Active: true, Filters: stored}).Error; err != nil {
					t.Fatal(err)
				}
			},
			engine: func() *pipeline.Engine {
				registry := withQuietVacation(pipeline.NewRegistry())
				registry.Register("refuses", func([]byte) (pipeline.Filter, error) {
					return refusingFilter{message: filterMessage}, nil
				})
				return pipeline.NewEngine(registry, nil)
			},
			category: models.FailurePolicy,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			database, err := connectTestDB()
			if err != nil {
				t.Fatal(err)
			}
			if err := rmdb.AutoMigrate(database); err != nil {
				t.Fatal(err)
			}
			tx := database.Begin()
			t.Cleanup(func() { tx.Rollback() })
			fixture := seedAuthzFixture(t, tx)
			seedReceiveFixture(t, tx, fixture.ownerMailbox, fixture.attackerMailbox)
			owner, refused := fixture.ownerMailbox, fixture.attackerMailbox
			test.refuse(t, tx, owner.DomainID, refused)

			handler := NewMessageHandler(tx, nil, test.engine(), "", nil)
			body := fmt.Sprintf(`{"from":%q,"to":[%q],"subject":"refused","body_text":"hello"}`, owner.Address, refused.Address)
			sent := sendAs(handler, &auth.Claims{AccountID: owner.ID, CredentialKind: "app_password"}, body)
			if sent.Code != http.StatusCreated {
				t.Fatalf("send: %d %s", sent.Code, sent.Body)
			}
			var message struct {
				ID uint `json:"id"`
			}
			if err := json.Unmarshal(dataOf(t, sent), &message); err != nil {
				t.Fatal(err)
			}
			if copies := countMessages(t, tx, refused.ID); copies != 0 {
				t.Fatalf("the refused recipient holds %d copies", copies)
			}

			response := httptest.NewRecorder()
			messageRoutes(handler).ServeHTTP(response, authzRequest(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/recipients", message.ID), owner.ID, nil, ""))
			if response.Code != http.StatusOK {
				t.Fatalf("recipients: %d %s", response.Code, response.Body)
			}
			if strings.Contains(response.Body.String(), "mx7.secret") {
				t.Fatalf("the filter's own message reached the sender: %s", response.Body)
			}
			var recipients MessageRecipients
			if err := json.Unmarshal(dataOf(t, response), &recipients); err != nil {
				t.Fatal(err)
			}
			if len(recipients.Recipients) != 1 {
				t.Fatalf("recipients: %+v", recipients.Recipients)
			}
			got := recipients.Recipients[0]
			if got.Address != refused.Address || got.Delivery != "local" || got.State != "failed" ||
				got.Category != string(test.category) || got.Reason != test.category.Description() {
				t.Fatalf("refused recipient: %+v", got)
			}
		})
	}
}

// refusingFilter rejects every message, with a message meant for the server's logs.
type refusingFilter struct{ message string }

func (f refusingFilter) Name() string              { return "refuses" }
func (f refusingFilter) Type() pipeline.FilterType { return pipeline.FilterTypeAction }
func (f refusingFilter) Execute(context.Context, *pipeline.EmailJSON) (*pipeline.FilterResult, error) {
	return &pipeline.FilterResult{Type: pipeline.FilterTypeAction, Action: pipeline.ActionReject, RejectMsg: f.message}, nil
}

// refusalFixture is a sender and a recipient on this server whose mailbox is full.
func refusalFixture(t *testing.T) (*gorm.DB, *MessageHandler, models.Mailbox, models.Mailbox) {
	t.Helper()
	database, err := connectTestDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := rmdb.AutoMigrate(database); err != nil {
		t.Fatal(err)
	}
	tx := database.Begin()
	t.Cleanup(func() { tx.Rollback() })
	fixture := seedAuthzFixture(t, tx)
	seedReceiveFixture(t, tx, fixture.ownerMailbox, fixture.attackerMailbox)
	if err := tx.Model(&fixture.attackerMailbox).Updates(map[string]any{"quota_bytes": 1, "quota_used_bytes": 1}).Error; err != nil {
		t.Fatal(err)
	}
	return tx, NewMessageHandler(tx, nil, nil, "", nil), fixture.ownerMailbox, fixture.attackerMailbox
}

func recipientsOf(t *testing.T, handler *MessageHandler, owner models.Mailbox, id uint) []RecipientStatus {
	t.Helper()
	response := httptest.NewRecorder()
	messageRoutes(handler).ServeHTTP(response, authzRequest(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/recipients", id), owner.ID, nil, ""))
	if response.Code != http.StatusOK {
		t.Fatalf("recipients: %d %s", response.Code, response.Body)
	}
	var body MessageRecipients
	if err := json.Unmarshal(dataOf(t, response), &body); err != nil {
		t.Fatal(err)
	}
	return body.Recipients
}

// A refused local Bcc recipient is listed, as a queued remote one is, and a delivered
// recipient of the same send still reads delivered.
func TestLocalRefusalOfABccRecipient(t *testing.T) {
	_, handler, owner, refused := refusalFixture(t)
	body := fmt.Sprintf(`{"from":%q,"to":[%q],"bcc":[%q],"subject":"bcc","body_text":"hello"}`, owner.Address, owner.Address, refused.Address)
	sent := sendAs(handler, &auth.Claims{AccountID: owner.ID, CredentialKind: "app_password"}, body)
	if sent.Code != http.StatusCreated {
		t.Fatalf("send: %d %s", sent.Code, sent.Body)
	}
	var message struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(dataOf(t, sent), &message); err != nil {
		t.Fatal(err)
	}
	got := recipientsOf(t, handler, owner, message.ID)
	if len(got) != 2 || got[0].Address != owner.Address || got[0].State != "delivered" ||
		got[1].Address != refused.Address || got[1].State != "failed" || got[1].Category != "mailbox_full" {
		t.Fatalf("recipients: %+v", got)
	}
}

// A copy in the mailbox outranks a refusal: an address listed twice can have both.
func TestDeliveredCopyOutranksARefusal(t *testing.T) {
	tx, handler, owner, recipient := refusalFixture(t)
	to, _ := json.Marshal([]string{recipient.Address})
	refusals, _ := json.Marshal([]models.LocalRefusal{{Address: recipient.Address, Category: models.FailureMailboxFull}})
	sent := models.Message{MailboxID: owner.ID, Folder: "Sent", MsgID: "<1899@idor.test>", Sender: owner.Address,
		RecipientsTo: models.JSONB(to), LocalRefusals: models.JSONB(refusals), ReceivedAt: time.Now()}
	delivered := models.Message{MailboxID: recipient.ID, Folder: "INBOX", MsgID: sent.MsgID, Sender: owner.Address, ReceivedAt: time.Now()}
	for _, row := range []*models.Message{&sent, &delivered} {
		if err := tx.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if got := recipientsOf(t, handler, owner, sent.ID); len(got) != 1 || got[0].State != "delivered" {
		t.Fatalf("recipients: %+v", got)
	}
}

// A calendar reply to a local organizer that refuses it answers success, like a send, so
// the refusal is recorded for it too.
func TestLocalRefusalOfACalendarReply(t *testing.T) {
	tx, handler, owner, organizer := refusalFixture(t)
	events, err := json.Marshal([]pipeline.CalendarEvent{{Method: "REQUEST", UID: "1899", Summary: "Meeting", Organizer: pipeline.CalendarAddress{Address: organizer.Address}}})
	if err != nil {
		t.Fatal(err)
	}
	invite := models.Message{MailboxID: owner.ID, Folder: "INBOX", Sender: organizer.Address, Subject: "Meeting", CalendarEventsRaw: events, ReceivedAt: time.Now()}
	if err := tx.Create(&invite).Error; err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprint(invite.ID)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/messages/"+id+"/calendar-reply", strings.NewReader(fmt.Sprintf(`{"response":"ACCEPTED","from":%q}`, owner.Address)))
	route := chi.NewRouteContext()
	route.URLParams.Add("id", id)
	ctx := context.WithValue(request.Context(), chi.RouteCtxKey, route)
	request = request.WithContext(context.WithValue(ctx, middleware.ClaimsKey, &auth.Claims{AccountID: owner.ID, CredentialKind: "app_password"}))
	response := httptest.NewRecorder()
	composeRoutes(handler).ServeHTTP(response, request)
	if response.Code != http.StatusOK && response.Code != http.StatusCreated {
		t.Fatalf("calendar reply: %d %s", response.Code, response.Body)
	}
	var reply models.Message
	if err := tx.Where("mailbox_id = ? AND folder = ?", owner.ID, "Sent").First(&reply).Error; err != nil {
		t.Fatal(err)
	}
	got := recipientsOf(t, handler, owner, reply.ID)
	if len(got) != 1 || got[0].Address != organizer.Address || got[0].State != "failed" || got[0].Category != "mailbox_full" {
		t.Fatalf("recipients: %+v", got)
	}
}
