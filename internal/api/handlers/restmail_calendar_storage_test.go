package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"gorm.io/gorm"
)

func restmailCalendarRequest(t *testing.T, db *gorm.DB, ctx context.Context, raw string, recipients ...string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"from": "organizer@remote.example", "to": recipients, "raw_message": raw})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, restmailMessagesPath, strings.NewReader(string(body))).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.10:1234"
	handler := NewRestmailHandler(db, nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{TrustedCIDRs: []string{"192.0.2.10/32"}})
	response := httptest.NewRecorder()
	restmailRoutes(handler).ServeHTTP(response, req)
	return response
}

func restmailCalendarRaw(t *testing.T, event pipeline.CalendarEvent, recipients ...string) string {
	t.Helper()
	ics, err := rmime.GenerateICS(event)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := buildOutboundRaw(outboundMessage{
		From: "organizer@remote.example", To: recipients, Subject: "Planning", BodyText: "Join us.",
		MessageID: fmt.Sprintf("<planning-%d@remote.example>", event.Sequence), Date: time.Now(), ICS: ics, ICSMethod: event.Method,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func restmailCalendarFixture(t *testing.T) (*gorm.DB, models.Mailbox) {
	t.Helper()
	db, mailbox := restmailStorageFixture(t)
	if err := migrateModels(db, &models.CalendarEventVersion{}); err != nil {
		t.Fatal(err)
	}
	return db, mailbox
}

func TestRESTMAILCalendarInvitationCanBeListedAndAnswered(t *testing.T) {
	fixture := newCalendarReplyFixture(t)
	event := pipeline.CalendarEvent{
		UID: "planning@remote.example", Method: "REQUEST", Summary: "Planning", Sequence: 1,
		Organizer: pipeline.CalendarAddress{Address: "organizer@remote.example"},
		Attendees: []pipeline.CalendarAddress{{Address: fixture.attendee.Address}},
		DTStart:   time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC), DTEnd: time.Date(2030, 1, 1, 11, 0, 0, 0, time.UTC),
	}
	raw := restmailCalendarRaw(t, event, fixture.attendee.Address)
	if response := restmailCalendarRequest(t, fixture.tx, t.Context(), raw, fixture.attendee.Address); response.Code != http.StatusCreated {
		t.Fatalf("delivery: %d %s", response.Code, response.Body)
	}
	var invitation models.Message
	if err := fixture.tx.Where("mailbox_id = ? AND folder = ?", fixture.attendee.ID, "INBOX").First(&invitation).Error; err != nil {
		t.Fatal(err)
	}
	var stored []pipeline.CalendarEvent
	if err := json.Unmarshal(invitation.CalendarEventsRaw, &stored); err != nil || len(stored) != 1 || stored[0].UID != event.UID {
		t.Fatalf("received calendar metadata: %s, error %v", invitation.CalendarEventsRaw, err)
	}
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d/calendar-events", fixture.attendee.ID), nil)
	request = request.WithContext(context.WithValue(request.Context(), middleware.ClaimsKey,
		&auth.Claims{AccountID: fixture.attendee.ID, CredentialKind: "app_password"}))
	response := httptest.NewRecorder()
	composeRoutes(fixture.h).ServeHTTP(response, request)
	var listing struct{ Data []CalendarEventSummary }
	if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil || response.Code != http.StatusOK || len(listing.Data) != 1 {
		t.Fatalf("calendar listing: %d %s, error %v", response.Code, response.Body, err)
	}
	listed := listing.Data[0]
	if listed.UID != event.UID || listed.MessageID == nil || *listed.MessageID != invitation.ID || listed.Sequence != 1 || listed.Versions != 1 {
		t.Fatalf("listing does not identify the received invitation: %+v", listed)
	}
	foreign := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/messages/%d/calendar-reply", invitation.ID),
		strings.NewReader(`{"response":"ACCEPTED","from":"someone@other.test"}`)).WithContext(request.Context())
	refused := httptest.NewRecorder()
	composeRoutes(fixture.h).ServeHTTP(refused, foreign)
	if refused.Code != http.StatusForbidden {
		t.Fatalf("foreign sender: %d %s", refused.Code, refused.Body)
	}
	fixture.reply(t, invitation)
	if reply := fixture.queued(t, event.Organizer.Address); !strings.Contains(reply, "text/calendar") {
		t.Fatalf("calendar reply was not queued: %s", reply)
	}
}

func TestRESTMAILCalendarVersionsAreKeptForEveryRecipient(t *testing.T) {
	db, mailbox := restmailCalendarFixture(t)
	other := models.Mailbox{DomainID: mailbox.DomainID, LocalPart: "other", Address: "other@restmail-storage.test"}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(t, db, other)
	for sequence, method := range []string{"REQUEST", "REQUEST", "CANCEL"} {
		event := pipeline.CalendarEvent{UID: "history@remote.example", Method: method, Sequence: sequence, Summary: "Planning",
			Organizer: pipeline.CalendarAddress{Address: "organizer@remote.example"}}
		if sequence == 0 {
			event.DTStart = time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC)
			event.DTEnd = event.DTStart.Add(time.Hour)
		}
		raw := restmailCalendarRaw(t, event, mailbox.Address, other.Address)
		if response := restmailCalendarRequest(t, db, t.Context(), raw, mailbox.Address, other.Address); response.Code != http.StatusCreated {
			t.Fatalf("delivery %d: %d %s", sequence, response.Code, response.Body)
		}
	}
	for _, recipient := range []models.Mailbox{mailbox, other} {
		var versions []models.CalendarEventVersion
		if err := db.Where("mailbox_id = ?", recipient.ID).Order("sequence").Find(&versions).Error; err != nil {
			t.Fatal(err)
		}
		if len(versions) != 3 {
			t.Fatalf("recipient %s has %d calendar versions, want 3", recipient.Address, len(versions))
		}
		for index, version := range versions {
			if version.Sequence != index || version.UID != "history@remote.example" || version.Summary != "Planning" || version.Organizer != "organizer@remote.example" || version.MessageID == nil {
				t.Fatalf("incomplete calendar version: %+v", version)
			}
			if (version.DTStart != nil) != (index == 0) || (version.DTEnd != nil) != (index == 0) || (version.Method == "CANCEL") != (index == 2) {
				t.Errorf("calendar dates/method changed: %+v", version)
			}
			var message models.Message
			if err := db.First(&message, *version.MessageID).Error; err != nil || message.MailboxID != recipient.ID || !strings.Contains(string(message.CalendarEventsRaw), version.UID) {
				t.Fatalf("version does not link to its recipient's invitation: %+v, error %v", version, err)
			}
		}
	}
}

func TestRESTMAILCalendarWithoutUIDKeepsMetadataButNoVersion(t *testing.T) {
	db, mailbox := restmailCalendarFixture(t)
	raw := strings.Replace(inviteRaw, "UID:planning-1@remote.example\r\n", "", 1)
	if response := restmailCalendarRequest(t, db, t.Context(), raw, mailbox.Address); response.Code != http.StatusCreated {
		t.Fatalf("delivery: %d %s", response.Code, response.Body)
	}
	var message models.Message
	if err := db.Where("mailbox_id = ?", mailbox.ID).First(&message).Error; err != nil {
		t.Fatal(err)
	}
	var events []pipeline.CalendarEvent
	if err := json.Unmarshal(message.CalendarEventsRaw, &events); err != nil || len(events) != 1 || events[0].UID != "" {
		t.Fatalf("calendar metadata was lost: %s, error %v", message.CalendarEventsRaw, err)
	}
	var versions int64
	if err := db.Model(&models.CalendarEventVersion{}).Count(&versions).Error; err != nil || versions != 0 {
		t.Fatalf("UID-less version count = %d, error %v", versions, err)
	}
}

func TestRESTMAILCalendarStorageRollsBackAndRetries(t *testing.T) {
	for _, failure := range []string{"calendar version", "quota after calendar"} {
		t.Run(failure, func(t *testing.T) {
			db, mailbox := restmailCalendarFixture(t)
			const callback = "restmail_calendar_storage_failure"
			fail := func(tx *gorm.DB) { _ = tx.AddError(errors.New("private calendar storage detail")) }
			var remove func() error
			if failure == "calendar version" {
				if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
					if tx.Statement.Table == "calendar_event_versions" {
						fail(tx)
					}
				}); err != nil {
					t.Fatal(err)
				}
				remove = func() error { return db.Callback().Create().Remove(callback) }
			} else {
				if err := db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
					if tx.Statement.Table == "quota_usage" {
						fail(tx)
					}
				}); err != nil {
					t.Fatal(err)
				}
				remove = func() error { return db.Callback().Update().Remove(callback) }
			}
			t.Cleanup(func() { _ = remove() })
			response := restmailCalendarRequest(t, db, t.Context(), inviteRaw, mailbox.Address)
			if response.Code != http.StatusUnprocessableEntity || strings.Contains(response.Body.String(), "private") {
				t.Fatalf("failed storage was acknowledged or leaked its error: %d %s", response.Code, response.Body)
			}
			assertRESTMAILStorage(t, db, mailbox, false)
			var versions int64
			if err := db.Model(&models.CalendarEventVersion{}).Count(&versions).Error; err != nil || versions != 0 {
				t.Fatalf("rolled-back versions = %d, error %v", versions, err)
			}
			if err := remove(); err != nil {
				t.Fatal(err)
			}
			response = restmailCalendarRequest(t, db, t.Context(), inviteRaw, mailbox.Address)
			if response.Code != http.StatusCreated {
				t.Fatalf("retry: %d %s", response.Code, response.Body)
			}
			var messages int64
			if err := db.Model(&models.Message{}).Where("mailbox_id = ?", mailbox.ID).Count(&messages).Error; err != nil || messages != 1 {
				t.Fatalf("retry messages = %d, error %v", messages, err)
			}
			if err := db.Model(&models.CalendarEventVersion{}).Count(&versions).Error; err != nil || versions != 1 {
				t.Fatalf("retry versions = %d, error %v", versions, err)
			}
		})
	}
}
