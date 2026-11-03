package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"gorm.io/gorm"
)

const awaitingOther = "someone@elsewhere.test"

// awaitingFixture is a database transaction with two accounts and the routes under test.
type awaitingFixture struct {
	t       *testing.T
	tx      *gorm.DB
	handler *MessageHandler
	owner   models.Mailbox
	other   models.Mailbox
	start   time.Time
	next    int
}

func newAwaitingFixture(t *testing.T) *awaitingFixture {
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
	seeded := seedAuthzFixture(t, tx)
	seedReceiveFixture(t, tx, seeded.ownerMailbox, seeded.attackerMailbox)
	return &awaitingFixture{
		t: t, tx: tx, handler: NewMessageHandler(tx, nil, nil, "", nil),
		owner: seeded.ownerMailbox, other: seeded.attackerMailbox,
		start: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
	}
}

// add stores a message the given hours after the fixture's start.
func (f *awaitingFixture) add(thread, sender, folder string, hours int, change ...func(*models.Message)) models.Message {
	f.t.Helper()
	f.next++
	row := models.Message{
		MailboxID: f.owner.ID, Folder: folder, ThreadID: thread, Sender: sender, Subject: "subject",
		MsgID: fmt.Sprintf("<fixture-%d@awaiting.test>", f.next), ReceivedAt: f.start.Add(time.Duration(hours) * time.Hour),
	}
	for _, apply := range change {
		apply(&row)
	}
	if err := f.tx.Create(&row).Error; err != nil {
		f.t.Fatal(err)
	}
	return row
}

func (f *awaitingFixture) get(actor, account uint, query string) *httptest.ResponseRecorder {
	f.t.Helper()
	response := httptest.NewRecorder()
	messageRoutes(f.handler).ServeHTTP(response, authzRequest(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d/threads?%s", account, query), actor, nil, ""))
	return response
}

type awaitingPage struct {
	Data []struct {
		ThreadID      string    `json:"thread_id"`
		AwaitingSince time.Time `json:"awaiting_since"`
		LastMessage   Message   `json:"last_message"`
	} `json:"data"`
	Pagination struct {
		Cursor  string `json:"cursor"`
		HasMore bool   `json:"has_more"`
		Total   *int64 `json:"total"`
	} `json:"pagination"`
}

func (f *awaitingFixture) page(account uint, query string) awaitingPage {
	f.t.Helper()
	response := f.get(account, account, query)
	if response.Code != http.StatusOK {
		f.t.Fatalf("list: %d %s", response.Code, response.Body)
	}
	var page awaitingPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		f.t.Fatal(err)
	}
	return page
}

// waiting answers the thread's awaiting_since, nil when the account's list leaves it out.
func (f *awaitingFixture) waiting(thread string) *time.Time {
	f.t.Helper()
	var found *time.Time
	for _, row := range f.page(f.owner.ID, "state=awaiting&limit=100").Data {
		if row.ThreadID == thread {
			if found != nil {
				f.t.Fatalf("thread %s listed twice", thread)
			}
			since := row.AwaitingSince
			found = &since
		}
	}
	return found
}

func (f *awaitingFixture) wantWaiting(thread string, hours int) {
	f.t.Helper()
	want := f.start.Add(time.Duration(hours) * time.Hour)
	got := f.waiting(thread)
	if got == nil {
		f.t.Fatalf("thread %s is not waiting, want waiting since %s", thread, want)
	}
	if !got.Equal(want) {
		f.t.Fatalf("thread %s waits since %s, want %s", thread, got, want)
	}
}

func (f *awaitingFixture) wantNotWaiting(thread string) {
	f.t.Helper()
	if got := f.waiting(thread); got != nil {
		f.t.Fatalf("thread %s waits since %s, want not waiting", thread, got)
	}
}

func awaitingDraft(row *models.Message)   { row.IsDraft = true }
func awaitingDeleted(row *models.Message) { row.IsDeleted = true }

func TestAwaitingDerivation(t *testing.T) {
	f := newAwaitingFixture(t)
	me := f.owner.Address

	t.Run("last message from this account waits", func(t *testing.T) {
		f.t = t
		f.add("last-mine", awaitingOther, "INBOX", 1)
		f.add("last-mine", me, "Sent", 2)
		f.wantWaiting("last-mine", 2)
	})
	t.Run("a later reply ends the wait", func(t *testing.T) {
		f.t = t
		f.add("replied", me, "Sent", 1)
		f.add("replied", awaitingOther, "INBOX", 2)
		f.wantNotWaiting("replied")
	})
	t.Run("order is by received time, the id breaking ties", func(t *testing.T) {
		f.t = t
		f.add("tied-reply", me, "Sent", 3)
		f.add("tied-reply", awaitingOther, "INBOX", 3)
		f.wantNotWaiting("tied-reply")
		f.add("tied-mine", awaitingOther, "INBOX", 3)
		f.add("tied-mine", me, "Sent", 3)
		f.wantWaiting("tied-mine", 3)
		f.add("earlier-import", awaitingOther, "INBOX", 5)
		f.add("earlier-import", me, "Sent", 4)
		f.wantNotWaiting("earlier-import")
	})
	t.Run("a message to yourself waits, once, from its later copy", func(t *testing.T) {
		f.t = t
		id := "<note-to-self@awaiting.test>"
		f.add(id, me, "Sent", 1, func(row *models.Message) { row.MsgID = id })
		f.add(id, me, "INBOX", 2, func(row *models.Message) { row.MsgID = id })
		f.wantWaiting(id, 2)
	})
	t.Run("drafts are not sent", func(t *testing.T) {
		f.t = t
		f.add("only-draft", me, "Drafts", 1, awaitingDraft)
		f.wantNotWaiting("only-draft")
		f.add("draft-reply", awaitingOther, "INBOX", 1)
		f.add("draft-reply", me, "Drafts", 2, awaitingDraft)
		f.wantNotWaiting("draft-reply")
		f.add("draft-after-sent", me, "Sent", 1)
		f.add("draft-after-sent", me, "Drafts", 2, awaitingDraft)
		f.wantWaiting("draft-after-sent", 1)
		f.add("filed-draft", me, "Archive", 1, awaitingDraft)
		f.wantNotWaiting("filed-draft")
		f.add("unflagged-draft", me, "Drafts", 1)
		f.wantNotWaiting("unflagged-draft")
	})
	t.Run("deleted and trashed messages do not count", func(t *testing.T) {
		f.t = t
		f.add("deleted-mine", awaitingOther, "INBOX", 1)
		f.add("deleted-mine", me, "Sent", 2, awaitingDeleted)
		f.wantNotWaiting("deleted-mine")
		f.add("trashed-mine", awaitingOther, "INBOX", 1)
		f.add("trashed-mine", me, "Trash", 2)
		f.wantNotWaiting("trashed-mine")
		f.add("trashed-reply", me, "Sent", 1)
		f.add("trashed-reply", awaitingOther, "Trash", 2)
		f.wantWaiting("trashed-reply", 1)
		f.add("deleted-reply", me, "Sent", 1)
		f.add("deleted-reply", awaitingOther, "INBOX", 2, awaitingDeleted)
		f.wantWaiting("deleted-reply", 1)
	})
	t.Run("a thread spans folders", func(t *testing.T) {
		f.t = t
		f.add("span-waiting", awaitingOther, "Archive", 1)
		f.add("span-waiting", me, "Project", 2)
		f.wantWaiting("span-waiting", 2)
		f.add("span-replied", me, "Sent", 1)
		f.add("span-replied", awaitingOther, "Project", 2)
		f.wantNotWaiting("span-replied")
	})
	t.Run("the account's address in any spelling that resolves to it", func(t *testing.T) {
		f.t = t
		f.add("upper-case", strings.ToUpper(me), "Sent", 1)
		f.wantWaiting("upper-case", 1)
		f.add("display-name", "Owner <"+me+">", "Sent", 1)
		f.wantWaiting("display-name", 1)
	})
	t.Run("an alias, a tag and another account are not this account", func(t *testing.T) {
		f.t = t
		alias := models.Alias{DomainID: f.owner.DomainID, SourceAddress: "sales@idor.test", DestinationAddress: me, Active: true}
		if err := f.tx.Create(&alias).Error; err != nil {
			t.Fatal(err)
		}
		f.add("from-alias", alias.SourceAddress, "Sent", 1)
		f.wantNotWaiting("from-alias")
		f.add("from-tag", "owner+tag@idor.test", "Sent", 1)
		f.wantNotWaiting("from-tag")
		f.add("from-other-account", f.other.Address, "INBOX", 1)
		f.wantNotWaiting("from-other-account")
		f.add("after-alias", me, "Sent", 1)
		f.add("after-alias", alias.SourceAddress, "INBOX", 2)
		f.wantNotWaiting("after-alias")
	})
	t.Run("mail with no thread is not a thread", func(t *testing.T) {
		f.t = t
		f.add("", me, "Sent", 1)
		for _, row := range f.page(f.owner.ID, "state=awaiting&limit=100").Data {
			if row.ThreadID == "" {
				t.Fatal("a message without a thread was listed")
			}
		}
	})
}

func TestAwaitingIMAPDeletedMessages(t *testing.T) {
	f := newAwaitingFixture(t)
	deleted := func(row *models.Message) { row.IMAPDeleted = true }

	t.Run("deleted sent message does not start a wait", func(t *testing.T) {
		f.t = t
		f.add("imap-deleted-sent", awaitingOther, "INBOX", 1)
		f.add("imap-deleted-sent", f.owner.Address, "Sent", 2, deleted)
		f.wantNotWaiting("imap-deleted-sent")
	})
	t.Run("deleted later reply does not end a wait", func(t *testing.T) {
		f.t = t
		sent := f.add("imap-deleted-reply", f.owner.Address, "Sent", 1)
		reply := f.add("imap-deleted-reply", awaitingOther, "INBOX", 2, deleted)
		f.wantWaiting("imap-deleted-reply", 1)
		rows := []models.Message{sent}
		if err := attachAwaitingSince(f.tx, f.owner.ID, rows); err != nil {
			t.Fatal(err)
		}
		if rows[0].AwaitingSince == nil || !rows[0].AwaitingSince.Equal(sent.ReceivedAt) {
			t.Fatalf("message awaiting_since = %v, want %s", rows[0].AwaitingSince, sent.ReceivedAt)
		}
		request := authzRequest(http.MethodGet, "/", f.owner.ID, nil, "")
		thread, err := f.handler.getThread(request, threadRequest{ID: uint32(f.owner.ID), ThreadID: sent.ThreadID})
		if err != nil {
			t.Fatal(err)
		}
		if len(thread.Data) != 1 || thread.Data[0].ID != sent.ID {
			t.Fatalf("thread = %+v, want only message %d", thread.Data, sent.ID)
		}
		if err := f.tx.Model(&reply).Update("imap_deleted", false).Error; err != nil {
			t.Fatal(err)
		}
		f.wantNotWaiting("imap-deleted-reply")
	})
	for _, header := range []string{"in-reply-to", "references"} {
		t.Run("deleted split-thread reply via "+header+" does not end a wait", func(t *testing.T) {
			f.t = t
			thread := "imap-deleted-" + header
			sent := f.add(thread, f.owner.Address, "Sent", 1)
			f.add(thread+"-reply", awaitingOther, "INBOX", 2, deleted, func(row *models.Message) {
				if header == "in-reply-to" {
					row.InReplyTo = sent.MsgID
				} else {
					row.References = "<root@awaiting.test> " + sent.MsgID
				}
			})
			f.wantWaiting(thread, 1)
		})
	}
}

// TestAwaitingUnicodeDomain resolves the account's address the way sending as it does.
func TestAwaitingUnicodeDomain(t *testing.T) {
	spellings := ownSenderSpellings("Owner@xn--bcher-kva.example")
	if want := []string{"owner@xn--bcher-kva.example", "owner@bücher.example"}; !reflect.DeepEqual(spellings, want) {
		t.Fatalf("spellings = %q, want %q", spellings, want)
	}
	f := newAwaitingFixture(t)
	domain := models.Domain{Name: "xn--bcher-kva.example"}
	if err := f.tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	mailbox := models.Mailbox{DomainID: domain.ID, LocalPart: "owner", Address: "owner@xn--bcher-kva.example", Active: true}
	if err := f.tx.Create(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	f.owner = mailbox
	seedReceiveFixture(t, f.tx, mailbox)
	f.add("unicode-domain", "Owner@BÜCHER.example", "Sent", 1)
	f.wantWaiting("unicode-domain", 1)
	f.add("ascii-domain", "owner@xn--bcher-kva.example", "Sent", 1)
	f.wantWaiting("ascii-domain", 1)
}

// A conversation sent through the real route, between two accounts on this server. Every
// turn stays under one thread_id per mailbox, so the wait follows that one thread.
func TestAwaitingConversationThroughTheSendPath(t *testing.T) {
	f := newAwaitingFixture(t)
	send := func(from models.Mailbox, to models.Mailbox, inReplyTo string) {
		t.Helper()
		body, _ := json.Marshal(map[string]interface{}{"from": from.Address, "to": []string{to.Address}, "subject": "turn", "body_text": "body", "in_reply_to": inReplyTo})
		response := httptest.NewRecorder()
		composeRoute(f.handler)(response, sendingAs(httptest.NewRequest(http.MethodPost, "/api/v1/messages/send", strings.NewReader(string(body))), from.ID))
		if response.Code >= 300 {
			t.Fatalf("send: %d %s", response.Code, response.Body)
		}
		// A delivered row takes the database's now(), which this test's one transaction
		// freezes at its start. Outside a test it is the time of the delivery.
		if err := f.tx.Exec("UPDATE messages SET received_at = clock_timestamp() WHERE received_at = now()").Error; err != nil {
			t.Fatal(err)
		}
	}
	latest := func(account models.Mailbox) models.Message {
		t.Helper()
		var row models.Message
		if err := f.tx.Where("mailbox_id = ?", account.ID).Order("id DESC").First(&row).Error; err != nil {
			t.Fatal(err)
		}
		return row
	}
	waitingThreads := func() []string {
		t.Helper()
		threads := []string{}
		for _, row := range f.page(f.owner.ID, "state=awaiting").Data {
			threads = append(threads, row.ThreadID)
		}
		return threads
	}

	send(f.owner, f.other, "")
	first := latest(f.owner)
	if got := waitingThreads(); !reflect.DeepEqual(got, []string{first.ThreadID}) {
		t.Fatalf("after my first message: waiting %q", got)
	}
	send(f.other, f.owner, latest(f.other).MsgID)
	if got := waitingThreads(); len(got) != 0 {
		t.Fatalf("after their reply: waiting %q", got)
	}
	send(f.owner, f.other, latest(f.owner).MsgID)
	second := latest(f.owner)
	if got := waitingThreads(); !reflect.DeepEqual(got, []string{second.ThreadID}) {
		t.Fatalf("after my second message: waiting %q", got)
	}
	send(f.other, f.owner, latest(f.other).MsgID)
	if answer := latest(f.owner); answer.ThreadID != first.ThreadID || second.ThreadID != first.ThreadID {
		t.Fatalf("the conversation left its thread: first %s, second %s, answer %s", first.ThreadID, second.ThreadID, answer.ThreadID)
	}
	if got := waitingThreads(); len(got) != 0 {
		t.Fatalf("their answer did not end the wait: waiting %q", got)
	}
}

func TestAwaitingReplyUnderAnotherThreadID(t *testing.T) {
	f := newAwaitingFixture(t)
	me := f.owner.Address
	parent := func(id string) func(*models.Message) {
		return func(row *models.Message) { row.InReplyTo = id }
	}
	id := func(value string) func(*models.Message) {
		return func(row *models.Message) { row.MsgID = value }
	}

	f.add("in-reply-to", me, "Sent", 1, id("<a@awaiting.test>"))
	f.add("root", awaitingOther, "INBOX", 2, parent("<a@awaiting.test>"))
	f.wantNotWaiting("in-reply-to")

	f.add("unbracketed", me, "Sent", 1, id("<b@awaiting.test>"))
	f.add("root", awaitingOther, "INBOX", 2, parent("b@awaiting.test"))
	f.wantNotWaiting("unbracketed")

	f.add("doubled", me, "Sent", 1, id("<c@awaiting.test>"))
	f.add("root", awaitingOther, "INBOX", 2, parent("<<c@awaiting.test>>"))
	f.wantNotWaiting("doubled")

	f.add("references-only", me, "Sent", 1, id("<d@awaiting.test>"))
	f.add("root", awaitingOther, "INBOX", 2, func(row *models.Message) { row.References = "<root@awaiting.test> <d@awaiting.test>\r\n" })
	f.wantNotWaiting("references-only")

	f.add("older-reference", me, "Sent", 1, id("<e@awaiting.test>"))
	f.add("root", awaitingOther, "INBOX", 2, func(row *models.Message) { row.References = "<e@awaiting.test> <other@awaiting.test>" })
	f.wantWaiting("older-reference", 1)

	f.add("suffix", me, "Sent", 1, id("<f@awaiting.test>"))
	f.add("root", awaitingOther, "INBOX", 2, func(row *models.Message) { row.References = "<xf@awaiting.test>" })
	f.wantWaiting("suffix", 1)

	f.add("trashed-answer", me, "Sent", 1, id("<g@awaiting.test>"))
	f.add("root", awaitingOther, "Trash", 2, parent("<g@awaiting.test>"))
	f.wantWaiting("trashed-answer", 1)

	f.add("drafted-answer", me, "Sent", 1, id("<h@awaiting.test>"))
	f.add("root", me, "Drafts", 2, parent("<h@awaiting.test>"), awaitingDraft)
	f.wantWaiting("drafted-answer", 1)

	f.add("my-follow-up", me, "Sent", 1, id("<i@awaiting.test>"))
	f.add("my-follow-up-2", me, "Sent", 2, parent("<i@awaiting.test>"))
	f.wantNotWaiting("my-follow-up")
	f.wantWaiting("my-follow-up-2", 2)

	f.add("no-message-id", me, "Sent", 1, id(""))
	f.add("unrelated", awaitingOther, "INBOX", 2)
	f.wantWaiting("no-message-id", 1)
}

func TestAwaitingListIsScopedBoundedAndPaged(t *testing.T) {
	f := newAwaitingFixture(t)
	me := f.owner.Address
	for hour := 1; hour <= 5; hour++ {
		f.add(fmt.Sprintf("thread-%d", hour), me, "Sent", hour)
	}
	// Two threads share a time, so the id must break the tie across a page boundary.
	f.add("thread-tie", me, "Sent", 3)
	f.add("thread-replied", me, "Sent", 6)
	f.add("thread-replied", awaitingOther, "INBOX", 7)
	foreign := models.Message{MailboxID: f.other.ID, Folder: "Sent", ThreadID: "thread-foreign", Sender: f.other.Address, Subject: "x", ReceivedAt: f.start}
	if err := f.tx.Create(&foreign).Error; err != nil {
		t.Fatal(err)
	}
	// The same thread_id elsewhere changes nothing here.
	sameThread := models.Message{MailboxID: f.other.ID, Folder: "INBOX", ThreadID: "thread-5", Sender: awaitingOther, Subject: "x", ReceivedAt: f.start.Add(100 * time.Hour)}
	if err := f.tx.Create(&sameThread).Error; err != nil {
		t.Fatal(err)
	}

	var walked []string
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 6 {
			t.Fatal("paging did not end")
		}
		query := url.Values{"state": {"awaiting"}, "limit": {"2"}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		page := f.page(f.owner.ID, query.Encode())
		if len(page.Data) > 2 {
			t.Fatalf("page of %d rows at limit 2", len(page.Data))
		}
		if page.Pagination.Total != nil {
			t.Fatalf("total = %d: counting walks the whole mailbox", *page.Pagination.Total)
		}
		for _, row := range page.Data {
			walked = append(walked, row.ThreadID)
			if row.LastMessage.AwaitingSince == nil || !row.LastMessage.AwaitingSince.Equal(row.AwaitingSince) || row.LastMessage.ThreadID != row.ThreadID {
				t.Fatalf("last message disagrees with its thread: %+v", row)
			}
		}
		if !page.Pagination.HasMore {
			if page.Pagination.Cursor != "" {
				t.Fatal("last page has a cursor")
			}
			break
		}
		cursor = page.Pagination.Cursor
	}
	if want := []string{"thread-5", "thread-4", "thread-tie", "thread-3", "thread-2", "thread-1"}; !reflect.DeepEqual(walked, want) {
		t.Fatalf("walked %q, want %q", walked, want)
	}

	for _, query := range []string{"state=awaiting&limit=0", "state=awaiting&limit=101", "state=awaiting&limit=-1", "state=awaiting&cursor=not-a-cursor"} {
		if got := len(f.page(f.owner.ID, query).Data); got != 6 {
			t.Fatalf("%s answered %d rows, want the default page", query, got)
		}
	}
	for i := 0; i < 120; i++ {
		f.add(fmt.Sprintf("bulk-%d", i), me, "Sent", 10+i)
	}
	if got := len(f.page(f.owner.ID, "state=awaiting").Data); got != 50 {
		t.Fatalf("default page = %d rows", got)
	}
	if got := len(f.page(f.owner.ID, "state=awaiting&limit=100").Data); got != 100 {
		t.Fatalf("largest page = %d rows", got)
	}
}

func TestAwaitingListRefusals(t *testing.T) {
	f := newAwaitingFixture(t)
	f.add("waiting", f.owner.Address, "Sent", 1)

	for _, query := range []string{"state=open", "state=AWAITING", "state=", ""} {
		response := f.get(f.owner.ID, f.owner.ID, query)
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"state"`) {
			t.Fatalf("?%s: %d %s, want 422 on state", query, response.Code, response.Body)
		}
	}
	// Someone else's account reads as getThread answers it, with nothing about the account.
	neighbour := httptest.NewRecorder()
	messageRoutes(f.handler).ServeHTTP(neighbour, authzRequest(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d/threads/waiting", f.owner.ID), f.other.ID, nil, ""))
	refused := f.get(f.other.ID, f.owner.ID, "state=awaiting")
	if refused.Code != neighbour.Code || refused.Body.String() != neighbour.Body.String() {
		t.Fatalf("foreign account: %d %s, neighbour answers %d %s", refused.Code, refused.Body, neighbour.Code, neighbour.Body)
	}
	if refused.Code != http.StatusNotFound {
		t.Fatalf("foreign account: %d", refused.Code)
	}
	unsigned := httptest.NewRecorder()
	messageRoutes(f.handler).ServeHTTP(unsigned, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d/threads?state=awaiting", f.owner.ID), nil))
	if unsigned.Code != http.StatusUnauthorized {
		t.Fatalf("no session: %d %s", unsigned.Code, unsigned.Body)
	}

	withKey := func(capability string) *httptest.ResponseRecorder {
		request := authzRequest(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d/threads?state=awaiting", f.owner.ID), f.owner.ID, nil, "")
		claims := freshTwoFactorClaims(f.owner.ID, f.owner.Address)
		claims.CredentialKind = auth.CredentialKindAPIKey
		claims.APIKeyPermissions = []auth.ScopedPermission{{Capability: capability, Scope: "mailbox:" + f.owner.Address}}
		request = request.WithContext(context.WithValue(request.Context(), middleware.ClaimsKey, claims))
		response := httptest.NewRecorder()
		messageRoutes(f.handler).ServeHTTP(response, request)
		return response
	}
	if response := withKey(models.CapMailRead); response.Code != http.StatusOK {
		t.Fatalf("mail:read key: %d %s", response.Code, response.Body)
	}
	if response := withKey(models.CapMailWrite); response.Code != http.StatusForbidden {
		t.Fatalf("mail:write key: %d %s", response.Code, response.Body)
	}
}

// Someone holding mail:read on another account sees that account's waiting threads: "this
// account" is the one in the path, not the caller.
func TestAwaitingIsThePathAccountsNotTheCallers(t *testing.T) {
	f := newAwaitingFixture(t)
	var role models.Role
	if err := f.tx.Where("name = ?", models.RoleOwner).First(&role).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.tx.Create(&models.Grant{HolderKind: models.GrantHolderAccount, HolderID: f.other.ID, RoleID: role.ID, Scope: grants.Mailbox(f.owner.Address), Mode: models.GrantActive, GrantedByKind: models.AuditActorSystem, GrantedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	f.add("owner-wrote", f.owner.Address, "Sent", 1)
	f.add("delegate-wrote", f.other.Address, "INBOX", 1)
	response := f.get(f.other.ID, f.owner.ID, "state=awaiting")
	if response.Code != http.StatusOK {
		t.Fatalf("delegate: %d %s", response.Code, response.Body)
	}
	var page awaitingPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 1 || page.Data[0].ThreadID != "owner-wrote" {
		t.Fatalf("delegate sees %+v", page.Data)
	}
}

// Snoozing hides a thread from folder listings and nothing else (#1485), and the waiting list
// is not a folder listing. So a snoozed thread stays listed, with its deadline, as in search.
func TestAwaitingSnoozedThreadStaysListed(t *testing.T) {
	f := newAwaitingFixture(t)
	me := f.owner.Address
	f.add("snoozed", awaitingOther, "INBOX", 1, func(row *models.Message) { row.Subject = "needle" })
	f.add("snoozed", me, "Sent", 2, func(row *models.Message) { row.Subject = "needle" })
	f.add("open", me, "Sent", 3)

	at := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	snoozeCall(t, f.handler, f.owner.ID, "snoozed", fmt.Sprintf(`{"snoozed_until":%q}`, at.Format(time.RFC3339)), http.StatusOK)

	requestFor := func() *http.Request { return authzRequest(http.MethodGet, "/", f.owner.ID, nil, "") }
	snoozedRow := func() *Message {
		t.Helper()
		for _, row := range f.page(f.owner.ID, "state=awaiting&limit=100").Data {
			if row.ThreadID == "snoozed" {
				message := row.LastMessage
				return &message
			}
		}
		return nil
	}

	f.wantWaiting("snoozed", 2)
	f.wantWaiting("open", 3)
	listed := snoozedRow()
	if listed.SnoozedUntil == nil || !listed.SnoozedUntil.Equal(at) {
		t.Fatalf("waiting list deadline: %v, want %s", listed.SnoozedUntil, at)
	}

	for _, folder := range []string{"INBOX", "Sent"} {
		rows, err := f.handler.listMessages(requestFor(), listMessagesRequest{ID: uint32(f.owner.ID), Folder: folder})
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range rows.Data {
			if message.ThreadID == "snoozed" {
				t.Fatalf("folder %s shows a snoozed thread: %#v", folder, message)
			}
		}
	}

	want := f.start.Add(2 * time.Hour)
	found, err := NewSearchHandler(f.tx).search(requestFor(), searchRequest{ID: uint32(f.owner.ID), Q: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Data) != 2 {
		t.Fatalf("search: %d results, want 2", len(found.Data))
	}
	for _, message := range found.Data {
		if message.SnoozedUntil == nil || !message.SnoozedUntil.Equal(at) || message.AwaitingSince == nil || !message.AwaitingSince.Equal(want) {
			t.Fatalf("search must carry both fields: snoozed %v, awaiting %v", message.SnoozedUntil, message.AwaitingSince)
		}
	}

	// A reply still ends the wait while the thread sleeps, and the snooze does not change that.
	reply := f.add("snoozed", awaitingOther, "INBOX", 4)
	f.wantNotWaiting("snoozed")
	if err := f.tx.Delete(&models.Message{}, reply.ID).Error; err != nil {
		t.Fatal(err)
	}
	f.wantWaiting("snoozed", 2)

	// Cancelling leaves the thread waiting, now with no deadline.
	snoozeCall(t, f.handler, f.owner.ID, "snoozed", `{"snoozed_until":null}`, http.StatusOK)
	f.wantWaiting("snoozed", 2)
	if listed := snoozedRow(); listed.SnoozedUntil != nil {
		t.Fatalf("cancelled snooze still has a deadline: %v", listed.SnoozedUntil)
	}
}

func TestAwaitingSinceOnMessages(t *testing.T) {
	f := newAwaitingFixture(t)
	me := f.owner.Address
	asked := f.add("waiting", awaitingOther, "INBOX", 1, func(row *models.Message) { row.Subject = "needle" })
	f.add("waiting", me, "Sent", 2)
	replied := f.add("replied", me, "Sent", 1, func(row *models.Message) { row.Subject = "needle" })
	f.add("replied", awaitingOther, "INBOX", 2)
	want := f.start.Add(2 * time.Hour)

	requestFor := func() *http.Request { return authzRequest(http.MethodGet, "/", f.owner.ID, nil, "") }
	check := func(where string, message Message, waits bool) {
		t.Helper()
		if !waits {
			if message.AwaitingSince != nil {
				t.Fatalf("%s: awaiting since %s, want none", where, message.AwaitingSince)
			}
			return
		}
		if message.AwaitingSince == nil || !message.AwaitingSince.Equal(want) {
			t.Fatalf("%s: awaiting since %v, want %s", where, message.AwaitingSince, want)
		}
	}
	detail, err := f.handler.getMessage(requestFor(), messagePath{ID: uint32(asked.ID)})
	if err != nil {
		t.Fatal(err)
	}
	check("detail", detail, true)
	detail, err = f.handler.getMessage(requestFor(), messagePath{ID: uint32(replied.ID)})
	if err != nil {
		t.Fatal(err)
	}
	check("detail of a replied thread", detail, false)

	inbox, err := f.handler.listMessages(requestFor(), listMessagesRequest{ID: uint32(f.owner.ID), Folder: "INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox.Data) != 2 {
		t.Fatalf("inbox = %d rows", len(inbox.Data))
	}
	for _, message := range inbox.Data {
		check("folder list "+message.ThreadID, message, message.ThreadID == "waiting")
	}

	found, err := NewSearchHandler(f.tx).search(requestFor(), searchRequest{ID: uint32(f.owner.ID), Q: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Data) != 2 {
		t.Fatalf("search = %d rows", len(found.Data))
	}
	for _, message := range found.Data {
		check("search "+message.ThreadID, message, message.ThreadID == "waiting")
	}

	// The thread route stays exactly as it was.
	thread, err := f.handler.getThread(requestFor(), threadRequest{ID: uint32(f.owner.ID), ThreadID: "waiting"})
	if err != nil {
		t.Fatal(err)
	}
	if len(thread.Data) != 2 {
		t.Fatalf("thread = %d rows", len(thread.Data))
	}
	for _, message := range thread.Data {
		encoded, _ := json.Marshal(message)
		if strings.Contains(string(encoded), "awaiting_since") {
			t.Fatalf("the thread route changed: %s", encoded)
		}
	}

	// Reading derives; it stores nothing.
	var before, after int64
	f.tx.Model(&models.Message{}).Where("mailbox_id = ?", f.owner.ID).Count(&before)
	f.page(f.owner.ID, "state=awaiting")
	f.tx.Model(&models.Message{}).Where("mailbox_id = ?", f.owner.ID).Count(&after)
	if before != after {
		t.Fatalf("reading changed the messages: %d -> %d", before, after)
	}
}

// A scheduled send is not sent until its time comes: it stores no message.
func TestAwaitingScheduledSendDoesNotCount(t *testing.T) {
	f := newAwaitingFixture(t)
	f.add("<asked@awaiting.test>", awaitingOther, "INBOX", 1, func(row *models.Message) { row.MsgID = "<asked@awaiting.test>" })
	at := time.Now().Add(time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
	body, _ := json.Marshal(map[string]interface{}{"from": f.owner.Address, "to": []string{"rcpt@remote.test"}, "subject": "later", "body_text": "body", "in_reply_to": "<asked@awaiting.test>", "scheduled_at": at})
	response := httptest.NewRecorder()
	composeRoute(f.handler)(response, sendingAs(httptest.NewRequest(http.MethodPost, "/api/v1/messages/send", strings.NewReader(string(body))), f.owner.ID))
	if response.Code != http.StatusAccepted {
		t.Fatalf("schedule: %d %s", response.Code, response.Body)
	}
	f.wantNotWaiting("<asked@awaiting.test>")
	if page := f.page(f.owner.ID, "state=awaiting"); len(page.Data) != 0 {
		t.Fatalf("a scheduled send waits: %+v", page.Data)
	}
}
