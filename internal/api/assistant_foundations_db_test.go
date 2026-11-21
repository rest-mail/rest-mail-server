package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/handlers"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

func assistantFoundationMessage(t *testing.T, fixture *credentialFixture, mailboxID uint, subject string) models.Message {
	t.Helper()
	message := models.Message{
		MailboxID: mailboxID, Folder: "INBOX", Sender: "person@remote.test", Subject: subject,
		MsgID: "<" + subject + "@remote.test>", BodyText: "Original body", ReceivedAt: time.Now(),
	}
	if err := fixture.db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	return assistantFoundationStored(t, fixture, message.ID)
}

func assistantFoundationStored(t *testing.T, fixture *credentialFixture, id uint) models.Message {
	t.Helper()
	var message models.Message
	if err := fixture.db.First(&message, id).Error; err != nil {
		t.Fatal(err)
	}
	return message
}

func assistantFoundationRead(t *testing.T, fixture *credentialFixture, id uint, credential string) handlers.Message {
	t.Helper()
	response := fixture.do(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d", id), credential, "")
	if response.Code != http.StatusOK {
		t.Fatalf("read message %d: %d %s", id, response.Code, response.Body)
	}
	var result struct{ Data handlers.Message }
	decodeInto(t, response, &result)
	return result.Data
}

func assistantFoundationCount(t *testing.T, fixture *credentialFixture, model any, want int64) {
	t.Helper()
	var count int64
	if err := fixture.db.Model(model).Count(&count).Error; err != nil || count != want {
		t.Fatalf("%T rows = %d, want %d (%v)", model, count, want, err)
	}
}

func TestAC01AssistantFoundationScopedGrantCeiling(t *testing.T) {
	fixture := newCredentialFixture(t)
	var mailbox models.Mailbox
	if err := fixture.db.First(&mailbox, fixture.mailboxID).Error; err != nil {
		t.Fatal(err)
	}
	foreignID := fixture.seedMailbox(mailbox.DomainID, "foreign")
	own := assistantFoundationMessage(t, fixture, fixture.mailboxID, "own-ceiling")
	foreign := assistantFoundationMessage(t, fixture, foreignID, "foreign-ceiling")
	var grantsBefore []models.Grant
	if err := fixture.db.Order("id").Find(&grantsBefore).Error; err != nil {
		t.Fatal(err)
	}
	for _, capability := range []string{models.CapMailRead, models.CapMailWrite, models.CapMailSend} {
		t.Run(capability, func(t *testing.T) {
			credential := personalMailboxKey(t, fixture.db, fixture.accountID, fixture.address, capability)
			before := assistantFoundationStored(t, fixture, own.ID)
			response := fixture.do(http.MethodPatch, fmt.Sprintf("/api/v1/messages/%d", own.ID), credential, `{"is_starred":true}`)
			want := http.StatusForbidden
			if capability == models.CapMailWrite {
				want = http.StatusOK
			}
			if response.Code != want {
				t.Fatalf("%s update = %d %s, want %d", capability, response.Code, response.Body, want)
			}
			after := assistantFoundationStored(t, fixture, own.ID)
			if want == http.StatusForbidden && !reflect.DeepEqual(before, after) {
				t.Fatalf("refused %s update changed persisted message", capability)
			}
			if want == http.StatusOK && (!after.IsStarred || after.ModSeq <= before.ModSeq) {
				t.Fatalf("authorized write not persisted: %+v", after)
			}
			response = fixture.do(http.MethodPatch, fmt.Sprintf("/api/v1/messages/%d", foreign.ID), credential, `{"is_starred":true}`)
			if response.Code != http.StatusNotFound {
				t.Fatalf("foreign update = %d %s, want 404", response.Code, response.Body)
			}
			if got := assistantFoundationStored(t, fixture, foreign.ID); !reflect.DeepEqual(foreign, got) {
				t.Fatal("refused foreign update changed persisted message")
			}
		})
	}
	credential := personalMailboxKey(t, fixture.db, fixture.accountID, "foreign@example.test", models.CapMailWrite)
	response := fixture.do(http.MethodPatch, fmt.Sprintf("/api/v1/messages/%d", foreign.ID), credential, `{"is_starred":true}`)
	if response.Code != http.StatusNotFound {
		t.Fatalf("self-issued foreign scope widened account grants: %d %s", response.Code, response.Body)
	}
	if got := assistantFoundationStored(t, fixture, foreign.ID); !reflect.DeepEqual(foreign, got) {
		t.Fatal("foreign-scoped credential changed another account's message")
	}
	response = fixture.do(http.MethodPatch, fmt.Sprintf("/api/v1/messages/%d", own.ID), fixture.session, `{"is_starred":false}`)
	if response.Code != http.StatusOK || assistantFoundationStored(t, fixture, own.ID).IsStarred {
		t.Fatalf("owner restore = %d %s", response.Code, response.Body)
	}
	var grantsAfter []models.Grant
	if err := fixture.db.Order("id").Find(&grantsAfter).Error; err != nil || !reflect.DeepEqual(grantsBefore, grantsAfter) {
		t.Fatalf("actions changed grant ceiling: before=%+v after=%+v (%v)", grantsBefore, grantsAfter, err)
	}
}

func TestAC02AssistantFoundationDraftSendRequiresWriteAndSend(t *testing.T) {
	fixture := newCredentialFixture(t)
	reader := personalMailboxKey(t, fixture.db, fixture.accountID, fixture.address, models.CapMailRead)
	writer := personalMailboxKey(t, fixture.db, fixture.accountID, fixture.address, models.CapMailWrite)
	sender := personalMailboxKey(t, fixture.db, fixture.accountID, fixture.address, models.CapMailSend)
	body := fmt.Sprintf(`{"from":%q,"to":["recipient@remote.test"],"subject":"Reviewed draft","body_text":"Original proposal"}`, fixture.address)
	response := fixture.do(http.MethodPost, "/api/v1/messages/draft", reader, body)
	if response.Code != http.StatusForbidden {
		t.Fatalf("read-only draft = %d %s", response.Code, response.Body)
	}
	assistantFoundationCount(t, fixture, &models.Message{}, 0)
	response = fixture.do(http.MethodPost, "/api/v1/messages/draft", writer, body)
	if response.Code != http.StatusCreated {
		t.Fatalf("save draft = %d %s", response.Code, response.Body)
	}
	var draft struct{ Data handlers.Message }
	decodeInto(t, response, &draft)
	stored := assistantFoundationStored(t, fixture, draft.Data.ID)
	if !stored.IsDraft || stored.Folder != "Drafts" || stored.Sender != fixture.address || stored.BodyText != "Original proposal" {
		t.Fatalf("draft not persisted: %+v", stored)
	}
	path := fmt.Sprintf("/api/v1/messages/draft/%d", stored.ID)
	for _, credential := range []string{reader, writer, sender} {
		response = fixture.do(http.MethodPost, path+"/send", credential, "")
		if response.Code != http.StatusForbidden {
			t.Fatalf("single-capability draft send = %d %s, want 403", response.Code, response.Body)
		}
		if got := assistantFoundationStored(t, fixture, stored.ID); !reflect.DeepEqual(stored, got) {
			t.Fatal("refused send changed the draft")
		}
		assistantFoundationCount(t, fixture, &models.Message{}, 1)
		assistantFoundationCount(t, fixture, &models.OutboundQueue{}, 0)
	}
	response = fixture.do(http.MethodPut, path, writer, `{"body_text":"Reviewed and edited"}`)
	if response.Code != http.StatusOK || assistantFoundationStored(t, fixture, stored.ID).BodyText != "Reviewed and edited" {
		t.Fatalf("edit draft = %d %s", response.Code, response.Body)
	}
	response = fixture.do(http.MethodPut, path, writer, `{"body_text":"Original proposal"}`)
	if response.Code != http.StatusOK || assistantFoundationStored(t, fixture, stored.ID).BodyText != stored.BodyText {
		t.Fatalf("restore draft = %d %s", response.Code, response.Body)
	}
	response = fixture.do(http.MethodPost, path+"/send", fixture.session, "")
	if response.Code != http.StatusCreated {
		t.Fatalf("owner send = %d %s", response.Code, response.Body)
	}
	var sent struct{ Data handlers.Message }
	decodeInto(t, response, &sent)
	got := assistantFoundationStored(t, fixture, sent.Data.ID)
	if got.ID == stored.ID || got.IsDraft || got.Folder != "Sent" || got.Subject != stored.Subject || got.BodyText != stored.BodyText || got.SubmittedAt != nil || got.RawMessage == "" {
		t.Fatalf("sent state does not match reviewed draft: %+v", got)
	}
	assistantFoundationCount(t, fixture, &models.Message{}, 1)
	assistantFoundationCount(t, fixture, &models.OutboundQueue{}, 1)
	var queued models.OutboundQueue
	if err := fixture.db.First(&queued).Error; err != nil || queued.MessageID == nil || *queued.MessageID != got.ID || queued.Recipient != "recipient@remote.test" || queued.Sender != fixture.address || queued.Status != "pending" || queued.RawMessage != got.RawMessage {
		t.Fatalf("queue does not link to Sent: %+v (%v)", queued, err)
	}
	if read := assistantFoundationRead(t, fixture, got.ID, reader); read.Folder != "Sent" || read.IsDraft || read.BodyText != stored.BodyText {
		t.Fatalf("Sent API differs from storage: %+v", read)
	}
	response = fixture.do(http.MethodPost, path+"/send", fixture.session, "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("consumed draft resend = %d %s", response.Code, response.Body)
	}
	assistantFoundationCount(t, fixture, &models.OutboundQueue{}, 1)
}

func TestAC03AssistantFoundationClassifiedMovesAreScopedAndReversible(t *testing.T) {
	fixture := newCredentialFixture(t)
	reader := personalMailboxKey(t, fixture.db, fixture.accountID, fixture.address, models.CapMailRead)
	writer := personalMailboxKey(t, fixture.db, fixture.accountID, fixture.address, models.CapMailWrite)
	newsletter := assistantFoundationMessage(t, fixture, fixture.mailboxID, "newsletter")
	if err := fixture.db.Model(&newsletter).Update("headers", models.JSONB(`{"List-Id":["<news.remote.test>"]}`)).Error; err != nil {
		t.Fatal(err)
	}
	person := assistantFoundationMessage(t, fixture, fixture.mailboxID, "person")
	held := assistantFoundationMessage(t, fixture, fixture.mailboxID, "unclassified")
	for _, sample := range []struct {
		message models.Message
		class   string
	}{{newsletter, "newsletter"}, {person, "person"}, {held, "person"}} {
		if got := assistantFoundationRead(t, fixture, sample.message.ID, reader); got.Class != sample.class {
			t.Fatalf("%s classified as %q, want %q", sample.message.Subject, got.Class, sample.class)
		}
	}
	path := fmt.Sprintf("/api/v1/messages/%d", newsletter.ID)
	before := assistantFoundationStored(t, fixture, newsletter.ID)
	response := fixture.do(http.MethodPatch, path, reader, `{"folder":"Archive"}`)
	if response.Code != http.StatusForbidden || !reflect.DeepEqual(before, assistantFoundationStored(t, fixture, newsletter.ID)) {
		t.Fatalf("read-only archive changed state: %d %s", response.Code, response.Body)
	}
	response = fixture.do(http.MethodPatch, path, writer, `{"folder":"Archive"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("archive newsletter = %d %s", response.Code, response.Body)
	}
	for _, folder := range []struct {
		name string
		want int
	}{{"Archive", 1}, {"INBOX", 2}} {
		response = fixture.do(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d/folders/%s/messages", fixture.accountID, folder.name), reader, "")
		if response.Code != http.StatusOK {
			t.Fatalf("list %s = %d %s", folder.name, response.Code, response.Body)
		}
		var listed struct{ Data []handlers.Message }
		decodeInto(t, response, &listed)
		var persisted int64
		if err := fixture.db.Model(&models.Message{}).Where("mailbox_id = ? AND folder = ?", fixture.mailboxID, folder.name).Count(&persisted).Error; err != nil || len(listed.Data) != folder.want || persisted != int64(folder.want) {
			t.Fatalf("%s API count=%d, stored=%d, want %d (%v)", folder.name, len(listed.Data), persisted, folder.want, err)
		}
		if folder.name == "Archive" && (listed.Data[0].ID != newsletter.ID || listed.Data[0].Class != "newsletter") {
			t.Fatalf("archive lost classification or message link: %+v", listed.Data)
		}
	}
	response = fixture.do(http.MethodPatch, path, writer, `{"folder":"INBOX"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("reverse move = %d %s", response.Code, response.Body)
	}
	after := assistantFoundationStored(t, fixture, newsletter.ID)
	if after.Folder != "INBOX" || after.MsgID != before.MsgID || after.BodyText != before.BodyText || !reflect.DeepEqual(after.Headers, before.Headers) || after.ModSeq <= before.ModSeq {
		t.Fatalf("reverse move changed mail facts or failed to advance sequence: %+v", after)
	}
	for _, untouched := range []models.Message{person, held} {
		if got := assistantFoundationStored(t, fixture, untouched.ID); !reflect.DeepEqual(untouched, got) {
			t.Fatalf("manual newsletter move changed untouched message %d", untouched.ID)
		}
	}
	assistantFoundationCount(t, fixture, &models.Message{}, 3)
	assistantFoundationCount(t, fixture, &models.OutboundQueue{}, 0)
}

func TestAC04AssistantFoundationCalendarContactsRetainSourceFacts(t *testing.T) {
	fixture := newCredentialFixture(t)
	reader := personalMailboxKey(t, fixture.db, fixture.accountID, fixture.address, models.CapMailRead)
	writer := personalMailboxKey(t, fixture.db, fixture.accountID, fixture.address, models.CapMailWrite)
	var mailbox models.Mailbox
	if err := fixture.db.First(&mailbox, fixture.mailboxID).Error; err != nil {
		t.Fatal(err)
	}
	foreignID := fixture.seedMailbox(mailbox.DomainID, "foreign")
	start := time.Date(2020, time.January, 2, 10, 0, 0, 0, time.UTC)
	wantEvents := map[uint]pipeline.CalendarEvent{}
	for index, offset := range []time.Duration{0, 30 * time.Minute, 3 * time.Hour} {
		event := pipeline.CalendarEvent{
			UID: fmt.Sprintf("meeting-%d", index), Method: "REQUEST", Summary: fmt.Sprintf("Meeting %d", index), Status: "CONFIRMED",
			DTStart: start.Add(offset), DTEnd: start.Add(offset + time.Hour),
			Organizer: pipeline.CalendarAddress{Address: "organizer@remote.test", Name: "Organizer"},
			Attendees: []pipeline.CalendarAddress{{Address: fixture.address, Name: "Chris"}, {Address: "colleague@remote.test", Name: "Colleague"}},
		}
		message := assistantFoundationMessage(t, fixture, fixture.mailboxID, event.UID)
		encoded, err := json.Marshal([]pipeline.CalendarEvent{event})
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Model(&message).Update("calendar_events", models.JSONB(encoded)).Error; err != nil {
			t.Fatal(err)
		}
		version := models.CalendarEventVersion{MailboxID: fixture.mailboxID, UID: event.UID, Method: event.Method, Status: event.Status, Summary: event.Summary, DTStart: &event.DTStart, DTEnd: &event.DTEnd, Organizer: event.Organizer.Address, MessageID: &message.ID}
		if err := fixture.db.Create(&version).Error; err != nil {
			t.Fatal(err)
		}
		wantEvents[message.ID] = event
	}
	contact := models.Contact{MailboxID: fixture.mailboxID, Email: "colleague@remote.test", Name: "Colleague", Source: "manual"}
	foreignContact := models.Contact{MailboxID: foreignID, Email: "private@remote.test", Name: "Private", Source: "manual"}
	if err := fixture.db.Create(&[]models.Contact{contact, foreignContact}).Error; err != nil {
		t.Fatal(err)
	}
	calendarPath := fmt.Sprintf("/api/v1/accounts/%d/calendar-events", fixture.accountID)
	contactsPath := fmt.Sprintf("/api/v1/accounts/%d/contacts", fixture.accountID)
	for _, path := range []string{calendarPath, contactsPath} {
		response := fixture.do(http.MethodGet, path, writer, "")
		if response.Code != http.StatusForbidden {
			t.Fatalf("write-only source read = %d %s, want 403", response.Code, response.Body)
		}
	}
	for _, resource := range []string{"calendar-events", "contacts"} {
		response := fixture.do(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d/%s", foreignID, resource), reader, "")
		if response.Code != http.StatusNotFound {
			t.Fatalf("foreign %s read = %d %s, want 404", resource, response.Code, response.Body)
		}
	}
	response := fixture.do(http.MethodGet, calendarPath, reader, "")
	if response.Code != http.StatusOK {
		t.Fatalf("calendar source = %d %s", response.Code, response.Body)
	}
	var calendar struct {
		Data []handlers.CalendarEventSummary
	}
	decodeInto(t, response, &calendar)
	if len(calendar.Data) != len(wantEvents) {
		t.Fatalf("calendar returned %d events, want %d", len(calendar.Data), len(wantEvents))
	}
	seen := map[uint]bool{}
	for _, summary := range calendar.Data {
		if summary.MessageID == nil {
			t.Fatalf("calendar entry lacks source link: %+v", summary)
		}
		want, exists := wantEvents[*summary.MessageID]
		if !exists || seen[*summary.MessageID] || summary.UID != want.UID || summary.Summary != want.Summary || summary.Method != want.Method || summary.Status != want.Status || summary.Versions != 1 || summary.IsCancelled {
			t.Fatalf("invented, duplicated or changed event: %+v", summary)
		}
		seen[*summary.MessageID] = true
		read := assistantFoundationRead(t, fixture, *summary.MessageID, reader)
		var actual []pipeline.CalendarEvent
		if err := json.Unmarshal(read.CalendarEventsRaw, &actual); err != nil || !reflect.DeepEqual(actual, []pipeline.CalendarEvent{want}) {
			t.Fatalf("source participant/time facts changed: %+v, want %+v (%v)", actual, want, err)
		}
	}
	response = fixture.do(http.MethodGet, contactsPath, reader, "")
	if response.Code != http.StatusOK {
		t.Fatalf("contact source = %d %s", response.Code, response.Body)
	}
	var contacts struct{ Data []handlers.Contact }
	decodeInto(t, response, &contacts)
	if len(contacts.Data) != 1 || contacts.Data[0].Email != contact.Email || contacts.Data[0].Name != contact.Name {
		t.Fatalf("contacts leaked or invented participants: %+v", contacts.Data)
	}
	assistantFoundationCount(t, fixture, &models.CalendarEventVersion{}, 3)
	assistantFoundationCount(t, fixture, &models.Message{}, 3)
	assistantFoundationCount(t, fixture, &models.Contact{}, 2)
	assistantFoundationCount(t, fixture, &models.OutboundQueue{}, 0)
}
