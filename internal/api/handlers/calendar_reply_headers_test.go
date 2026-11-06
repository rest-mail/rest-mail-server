package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	stdmime "mime"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmime "github.com/rest-mail/rest-mail-server/internal/mime"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// calendarReplyFixture is a hosted mailbox that can answer invitations, and a
// second hosted mailbox that can organize one.
type calendarReplyFixture struct {
	tx        *gorm.DB
	h         *MessageHandler
	attendee  models.Mailbox
	organizer models.Mailbox
	remote    string
}

func newCalendarReplyFixture(t *testing.T) *calendarReplyFixture {
	t.Helper()
	db := openPlusAddrTestDB(t)
	if err := db.AutoMigrate(&models.OutboundQueue{}, &models.Contact{}, &models.CalendarEventVersion{}); err != nil {
		t.Skipf("calendar reply test skipped: migrate failed (%v)", err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })

	name := fmt.Sprintf("calendar-reply-%d.test", time.Now().UnixNano())
	domain := models.Domain{Name: name, Active: true}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	seed := func(local string) models.Mailbox {
		t.Helper()
		box := models.Mailbox{DomainID: domain.ID, LocalPart: local, Address: local + "@" + name, DisplayName: "Ann Attendee", Active: true}
		if err := tx.Create(&box).Error; err != nil {
			t.Fatal(err)
		}
		seedReceiveFixture(t, tx, box)
		return box
	}
	return &calendarReplyFixture{
		tx:        tx,
		h:         NewMessageHandler(tx, nil, nil, "", nil),
		attendee:  seed("attendee"),
		organizer: seed("organizer"),
		remote:    "organizer@remote-" + name,
	}
}

// deliverInvitation takes an invitation in through local delivery, the way one
// arrives, so that its calendar events are the ones the parser made of it. Its
// iCalendar body travels as base64 because that is how a raw CR survives the
// transfer: the parser sees every byte the sender wrote.
func (f *calendarReplyFixture) deliverInvitation(t *testing.T, organizer, summaryLine string) models.Message {
	t.Helper()
	ics := strings.Join([]string{
		"BEGIN:VCALENDAR", "VERSION:2.0", "METHOD:REQUEST", "BEGIN:VEVENT",
		"UID:invitation-1@example.test", "DTSTAMP:20261103T100000Z", "DTSTART:20261110T120000Z",
		summaryLine, "ORGANIZER:mailto:" + organizer,
		"ATTENDEE:mailto:" + f.attendee.Address, "END:VEVENT", "END:VCALENDAR", "",
	}, "\r\n")
	raw := "From: <" + organizer + ">\r\n" +
		"To: <" + f.attendee.Address + ">\r\n" +
		"Subject: Invitation\r\n" +
		"Message-ID: <invitation@example.test>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/calendar; charset=utf-8; method=REQUEST\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" +
		base64.StdEncoding.EncodeToString([]byte(ics)) + "\r\n"
	to, _ := json.Marshal([]string{f.attendee.Address})
	msg, err := f.h.deliverToLocal(context.Background(), localDeliveryParams{
		Mailbox:      f.attendee,
		Sender:       organizer,
		RecipientsTo: to,
		RecipientsCc: json.RawMessage("[]"),
		Subject:      "Invitation",
		MessageID:    "<invitation@example.test>",
		RawMessage:   raw,
	})
	if err != nil {
		t.Fatalf("deliver the invitation: %v", err)
	}
	return *msg
}

// storeInvitation stores an invitation as it stands, for values that reach the
// database some other way than the iCalendar parser.
func (f *calendarReplyFixture) storeInvitation(t *testing.T, msg models.Message, event pipeline.CalendarEvent) models.Message {
	t.Helper()
	events, err := json.Marshal([]pipeline.CalendarEvent{event})
	if err != nil {
		t.Fatal(err)
	}
	msg.MailboxID = f.attendee.ID
	msg.Folder = "INBOX"
	msg.CalendarEventsRaw = events
	msg.ReceivedAt = time.Now()
	if err := f.tx.Create(&msg).Error; err != nil {
		t.Fatal(err)
	}
	return msg
}

// post answers the invitation through the real route.
func (f *calendarReplyFixture) post(invitation models.Message) *httptest.ResponseRecorder {
	id := fmt.Sprint(invitation.ID)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/messages/"+id+"/calendar-reply",
		strings.NewReader(`{"response":"ACCEPTED","from":"`+f.attendee.Address+`"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	req = req.WithContext(context.WithValue(ctx, middleware.ClaimsKey, &auth.Claims{AccountID: f.attendee.ID, CredentialKind: "app_password"}))
	rr := httptest.NewRecorder()
	composeRoutes(f.h).ServeHTTP(rr, req)
	return rr
}

// reply answers the invitation and expects the answer to be accepted.
func (f *calendarReplyFixture) reply(t *testing.T, invitation models.Message) {
	t.Helper()
	if rr := f.post(invitation); rr.Code != http.StatusOK {
		t.Fatalf("calendar reply: %d %s", rr.Code, rr.Body.String())
	}
}

// queued is the raw reply waiting to leave for the given recipient.
func (f *calendarReplyFixture) queued(t *testing.T, recipient string) string {
	t.Helper()
	var entry models.OutboundQueue
	if err := f.tx.Where("recipient = ?", recipient).First(&entry).Error; err != nil {
		t.Fatalf("no queued reply for %s: %v", recipient, err)
	}
	return entry.RawMessage
}

// sentCopy is the reply as the attendee's Sent folder holds it.
func (f *calendarReplyFixture) sentCopy(t *testing.T) models.Message {
	t.Helper()
	var sent models.Message
	if err := f.tx.Where("mailbox_id = ? AND folder = ?", f.attendee.ID, "Sent").First(&sent).Error; err != nil {
		t.Fatalf("no Sent copy: %v", err)
	}
	return sent
}

// replyHeaderNames are every header a calendar reply may carry. Anything else
// in the header block was not written by the server's own rendering.
var replyHeaderNames = map[string]bool{
	"From": true, "To": true, "Subject": true, "Date": true, "Message-Id": true,
	"In-Reply-To": true, "References": true, "Mime-Version": true, "Content-Type": true,
}

// checkReplyHeaders holds the raw reply to one Subject of the given value and to
// no header the server did not mean to write, read by a mail parser and, because
// a parser may treat a lone CR as ordinary text, byte by byte as well.
func checkReplyHeaders(t *testing.T, raw, wantSubject string) {
	t.Helper()
	block, _, found := strings.Cut(raw, "\r\n\r\n")
	if !found {
		t.Fatalf("no CRLF blank line ends the header block:\n%q", raw)
	}
	if strings.ContainsAny(strings.ReplaceAll(block, "\r\n", ""), "\r\n") {
		t.Errorf("the header block has a line break that is not CRLF:\n%q", block)
	}
	for _, line := range strings.Split(block, "\r\n") {
		if len(line) > 998 {
			t.Errorf("a header line runs to %d octets", len(line))
		}
		for _, r := range line {
			if r > 0x7e {
				t.Errorf("a header line carries raw non-ASCII text: %q", line)
				break
			}
		}
	}

	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("parse the reply: %v\n%q", err, raw)
	}
	for name := range msg.Header {
		if !replyHeaderNames[name] {
			t.Errorf("the reply carries a header it should not: %s: %q", name, msg.Header[name])
		}
	}
	if got := len(msg.Header["Subject"]); got != 1 {
		t.Errorf("the reply has %d Subject headers, want 1", got)
	}
	subject, err := new(stdmime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		t.Fatalf("decode the Subject: %v", err)
	}
	if subject != wantSubject {
		t.Errorf("Subject decodes to %q, want %q", subject, wantSubject)
	}
}

// checkReplyBody holds the reply to the shape an invitation's organizer reads:
// a one-line note and the iCalendar answer, with the summary able to add no line
// to either.
func checkReplyBody(t *testing.T, raw, wantNote string) {
	t.Helper()
	parsed, err := rmime.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse the reply: %v", err)
	}
	if !strings.HasPrefix(parsed.Body.ContentType, "multipart/alternative") {
		t.Fatalf("the reply's body is %q, want multipart/alternative", parsed.Body.ContentType)
	}
	if len(parsed.Body.Parts) != 2 {
		t.Fatalf("the reply has %d parts, want 2", len(parsed.Body.Parts))
	}
	if got := strings.TrimRight(parsed.Body.Parts[0].Content, "\r\n"); got != wantNote {
		t.Errorf("the note reads %q, want %q", got, wantNote)
	}
	for _, line := range strings.Split(parsed.Body.Parts[1].Content, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.Contains(line, "\r") {
			t.Errorf("the iCalendar line %q has a CR inside it", line)
		}
		if strings.HasPrefix(line, "Bcc:") {
			t.Errorf("the summary added the iCalendar line %q", line)
		}
	}
	if len(parsed.CalendarEvents) != 1 || parsed.CalendarEvents[0].Method != "REPLY" {
		t.Fatalf("the reply's calendar events = %+v, want one REPLY", parsed.CalendarEvents)
	}
}

// A line break in an invitation's summary stays out of the reply's headers,
// however the sender spelled it: the iCalendar escape, either case of it, a raw
// CR before the escape (which the parser turns into a CRLF), or a raw CR alone.
func TestCalendarReplySummaryCannotAddAHeader(t *testing.T) {
	const wantSubject = "Accepted: Lunch Bcc: attacker@evil.test"
	const wantNote = "Calendar invite accepted: Lunch Bcc: attacker@evil.test"
	summaries := map[string]string{
		"LF":      `SUMMARY:Lunch\nBcc: attacker@evil.test`,
		"LF as N": `SUMMARY:Lunch\NBcc: attacker@evil.test`,
		"CRLF":    "SUMMARY:Lunch\r" + `\nBcc: attacker@evil.test`,
		"bare CR": "SUMMARY:Lunch\rBcc: attacker@evil.test",
	}
	for name, summary := range summaries {
		t.Run(name+" to a remote organizer", func(t *testing.T) {
			f := newCalendarReplyFixture(t)
			f.reply(t, f.deliverInvitation(t, f.remote, summary))
			raw := f.queued(t, f.remote)
			checkReplyHeaders(t, raw, wantSubject)
			checkReplyBody(t, raw, wantNote)
			if sent := f.sentCopy(t); sent.Subject != wantSubject || sent.BodyText != wantNote {
				t.Errorf("the Sent copy has subject %q and text %q", sent.Subject, sent.BodyText)
			}
		})
		t.Run(name+" to a hosted organizer", func(t *testing.T) {
			f := newCalendarReplyFixture(t)
			f.reply(t, f.deliverInvitation(t, f.organizer.Address, summary))
			raw := f.queued(t, f.organizer.Address)
			checkReplyHeaders(t, raw, wantSubject)
			checkReplyBody(t, raw, wantNote)
			deliverQueued(t, f.h, f.tx, f.sentCopy(t))
			var delivered models.Message
			if err := f.tx.Where("mailbox_id = ? AND folder = ?", f.organizer.ID, "INBOX").First(&delivered).Error; err != nil {
				t.Fatalf("no delivered reply: %v", err)
			}
			if delivered.Subject != wantSubject || delivered.BodyText != wantNote {
				t.Errorf("the delivered reply has subject %q and text %q", delivered.Subject, delivered.BodyText)
			}
			if delivered.RawMessage != "" {
				checkReplyHeaders(t, delivered.RawMessage, wantSubject)
			}
		})
	}
}

// A summary is the invitation's own text: a stored one with a line break the
// parser could not have left there is held to the same rule.
func TestCalendarReplyStoredSummaryCannotAddAHeader(t *testing.T) {
	for name, summary := range map[string]string{
		"CRLF":    "Lunch\r\nBcc: attacker@evil.test",
		"LF":      "Lunch\nBcc: attacker@evil.test",
		"bare CR": "Lunch\rBcc: attacker@evil.test",
	} {
		t.Run(name, func(t *testing.T) {
			f := newCalendarReplyFixture(t)
			f.reply(t, f.storeInvitation(t, models.Message{Sender: f.remote, Subject: "Invitation"}, pipeline.CalendarEvent{
				Method: "REQUEST", UID: "stored-1", Summary: summary,
				Organizer: pipeline.CalendarAddress{Address: f.remote},
			}))
			raw := f.queued(t, f.remote)
			checkReplyHeaders(t, raw, "Accepted: Lunch Bcc: attacker@evil.test")
			checkReplyBody(t, raw, "Calendar invite accepted: Lunch Bcc: attacker@evil.test")
		})
	}
}

// The reply's other headers also take values the invitation's sender chose: its
// subject when the event has no summary, and its Message-ID. Neither adds a
// header, and neither does an event's UID or organizer name add an iCalendar
// line.
func TestCalendarReplyOtherSenderValuesCannotAddAHeader(t *testing.T) {
	t.Run("subject when the summary is empty", func(t *testing.T) {
		f := newCalendarReplyFixture(t)
		f.reply(t, f.storeInvitation(t, models.Message{Sender: f.remote, Subject: "Lunch\r\nBcc: attacker@evil.test"}, pipeline.CalendarEvent{
			Method: "REQUEST", UID: "stored-2",
			Organizer: pipeline.CalendarAddress{Address: f.remote},
		}))
		checkReplyHeaders(t, f.queued(t, f.remote), "Accepted: Lunch Bcc: attacker@evil.test")
	})
	t.Run("a summary of nothing but line breaks", func(t *testing.T) {
		f := newCalendarReplyFixture(t)
		f.reply(t, f.storeInvitation(t, models.Message{Sender: f.remote, Subject: "Invitation"}, pipeline.CalendarEvent{
			Method: "REQUEST", UID: "stored-3", Summary: "\r\n",
			Organizer: pipeline.CalendarAddress{Address: f.remote},
		}))
		checkReplyHeaders(t, f.queued(t, f.remote), "Accepted: Invitation")
	})
	t.Run("message id", func(t *testing.T) {
		f := newCalendarReplyFixture(t)
		f.reply(t, f.storeInvitation(t, models.Message{Sender: f.remote, Subject: "Invitation", MsgID: "<id@example.test>\r\nBcc: attacker@evil.test"}, pipeline.CalendarEvent{
			Method: "REQUEST", UID: "stored-4", Summary: "Lunch",
			Organizer: pipeline.CalendarAddress{Address: f.remote},
		}))
		raw := f.queued(t, f.remote)
		checkReplyHeaders(t, raw, "Accepted: Lunch")
		if sent := f.sentCopy(t); strings.ContainsAny(sent.InReplyTo, "\r\n") {
			t.Errorf("the Sent copy's In-Reply-To keeps a line break: %q", sent.InReplyTo)
		}
	})
	t.Run("uid and organizer name", func(t *testing.T) {
		f := newCalendarReplyFixture(t)
		f.reply(t, f.storeInvitation(t, models.Message{Sender: f.remote, Subject: "Invitation"}, pipeline.CalendarEvent{
			Method: "REQUEST", UID: "stored-5\r\nBcc: attacker@evil.test", Summary: "Lunch",
			Organizer: pipeline.CalendarAddress{Name: "Org\rBcc: attacker@evil.test", Address: f.remote},
		}))
		checkReplyBody(t, f.queued(t, f.remote), "Calendar invite accepted: Lunch")
	})
}

// A non-ASCII summary leaves as RFC 2047 encoded-words and reads back the same,
// including one long enough that it has to be split and folded.
func TestCalendarReplyNonASCIISummaryRoundTrips(t *testing.T) {
	for name, summary := range map[string]string{
		"short": "Mittagessen für Zoë – 日本語",
		"long":  strings.TrimSpace(strings.Repeat("Mittagessen für Zoë – 日本語 ", 12)),
	} {
		t.Run(name, func(t *testing.T) {
			f := newCalendarReplyFixture(t)
			f.reply(t, f.deliverInvitation(t, f.remote, "SUMMARY:"+summary))
			raw := f.queued(t, f.remote)
			checkReplyHeaders(t, raw, "Accepted: "+summary)
			checkReplyBody(t, raw, "Calendar invite accepted: "+summary)
			if sent := f.sentCopy(t); sent.Subject != "Accepted: "+summary {
				t.Errorf("the Sent copy has subject %q", sent.Subject)
			}
		})
	}
}

// The organizer is validated before it reaches a header or an envelope (#166),
// and a display name around the address leaves the envelope a plain address.
func TestCalendarReplyOrganizerAddress(t *testing.T) {
	t.Run("a line break is refused", func(t *testing.T) {
		f := newCalendarReplyFixture(t)
		invitation := f.storeInvitation(t, models.Message{Sender: f.remote, Subject: "Invitation"}, pipeline.CalendarEvent{
			Method: "REQUEST", UID: "stored-6", Summary: "Lunch",
			Organizer: pipeline.CalendarAddress{Address: f.remote + "\r\nBcc: attacker@evil.test"},
		})
		rr := f.post(invitation)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("calendar reply: %d %s, want 400", rr.Code, rr.Body.String())
		}
		var queued int64
		f.tx.Model(&models.OutboundQueue{}).Where("sender = ?", f.attendee.Address).Count(&queued)
		if queued != 0 {
			t.Errorf("%d replies queued for a refused organizer", queued)
		}
	})
	t.Run("a display name is left out of the envelope", func(t *testing.T) {
		f := newCalendarReplyFixture(t)
		f.reply(t, f.storeInvitation(t, models.Message{Sender: f.remote, Subject: "Invitation"}, pipeline.CalendarEvent{
			Method: "REQUEST", UID: "stored-7", Summary: "Lunch",
			Organizer: pipeline.CalendarAddress{Address: "Olive Organizer <" + f.remote + ">"},
		}))
		raw := f.queued(t, f.remote)
		checkReplyHeaders(t, raw, "Accepted: Lunch")
		msg, err := mail.ReadMessage(strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		to, err := msg.Header.AddressList("To")
		if err != nil || len(to) != 1 || to[0].Address != f.remote {
			t.Errorf("To = %q (%v), want the organizer's address", msg.Header.Get("To"), err)
		}
	})
}
