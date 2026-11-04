package api

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	goimap "github.com/rest-mail/go-imap/pkg/imap"
	imapserver "github.com/rest-mail/go-imap/pkg/imap/server"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/gateway/apiclient"
	imapgw "github.com/rest-mail/rest-mail-server/internal/gateway/imap"
)

func TestIMAPSnoozeKeepsMessagesUIDsAndCounts(t *testing.T) {
	fixture := newCredentialFixture(t)
	password := fixture.createAppPassword("IMAP snooze regression").Password
	const raw = "From: sender@example.test\r\nSubject: Snoozable\r\n\r\nOriginal body\r\n"
	var messages []models.Message
	for index := 0; index < 102; index++ {
		message := models.Message{
			MailboxID: fixture.mailboxID, Folder: "INBOX", ThreadID: "visible",
			Sender: "sender@example.test", Subject: "Snoozable", RawMessage: raw,
			ReceivedAt: time.Now().UTC().Add(time.Duration(index) * time.Second),
			IsRead:     index == 0,
		}
		if index == 0 || index >= 100 {
			message.ThreadID = "snoozable"
		}
		if index == 101 {
			message.Folder = "Later"
		}
		messages = append(messages, message)
	}
	if err := fixture.db.Create(&messages).Error; err != nil {
		t.Fatal(err)
	}
	storedMessages := func() []models.Message {
		t.Helper()
		var stored []models.Message
		if err := fixture.db.Where("mailbox_id = ?", fixture.mailboxID).Order("id").Find(&stored).Error; err != nil {
			t.Fatal(err)
		}
		return stored
	}
	before := storedMessages()
	server := httptest.NewServer(fixture.router)
	t.Cleanup(server.Close)
	client, peer := net.Pipe()
	session := imapserver.NewSession(peer, imapgw.NewBackend(apiclient.New(server.URL), nil), "imap.test", nil, goimap.NopLimiter{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		session.Handle()
	}()
	t.Cleanup(func() {
		_ = client.Close()
		<-done
	})
	reader := bufio.NewReader(client)
	if err := client.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	command := func(request string) string {
		t.Helper()
		if err := client.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(client, "test %s\r\n", request); err != nil {
			t.Fatal(err)
		}
		var response strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("%s: %v", request, err)
			}
			response.WriteString(line)
			if strings.HasPrefix(line, "test ") {
				if !strings.HasPrefix(line, "test OK ") {
					t.Fatalf("%s: %s", request, response.String())
				}
				return response.String()
			}
		}
	}
	command(fmt.Sprintf("LOGIN %q %q", fixture.address, password))
	snapshot := func() map[string]string {
		t.Helper()
		responses := make(map[string]string)
		for _, request := range []string{
			"NOOP",
			`LIST "" "*"`,
			`LIST "" "*" RETURN (STATUS (MESSAGES UNSEEN UIDNEXT UIDVALIDITY))`,
		} {
			responses[request] = command(request)
		}
		for _, folder := range []string{"INBOX", "Later"} {
			uid := messages[0].ID
			if folder == "Later" {
				uid = messages[101].ID
			}
			for _, request := range []string{
				"SELECT " + folder,
				"FETCH 1:* (UID FLAGS RFC822.SIZE)",
				"SEARCH ALL",
				"SEARCH UNSEEN",
				"UID SEARCH ALL",
				fmt.Sprintf("UID SEARCH UID %d", uid),
				fmt.Sprintf("UID FETCH %d (UID FLAGS BODY.PEEK[])", uid),
				"STATUS " + folder + " (MESSAGES UNSEEN UIDNEXT UIDVALIDITY)",
				"EXAMINE " + folder,
			} {
				responses[folder+"/"+request] = command(request)
			}
		}
		return responses
	}
	baseline := snapshot()
	for _, folder := range []struct {
		name          string
		count, unread int
		first, last   uint
	}{
		{"INBOX", 101, 100, messages[0].ID, messages[100].ID},
		{"Later", 1, 1, messages[101].ID, messages[101].ID},
	} {
		if !strings.Contains(baseline[folder.name+"/SELECT "+folder.name], fmt.Sprintf("* %d EXISTS\r\n", folder.count)) {
			t.Fatalf("%s: incorrect initial SELECT count", folder.name)
		}
		status := baseline[folder.name+"/STATUS "+folder.name+" (MESSAGES UNSEEN UIDNEXT UIDVALIDITY)"]
		if !strings.Contains(status, fmt.Sprintf("MESSAGES %d", folder.count)) || !strings.Contains(status, fmt.Sprintf("UNSEEN %d", folder.unread)) {
			t.Fatalf("%s: incorrect initial STATUS: %s", folder.name, status)
		}
		var uids []string
		for uid := folder.first; uid <= folder.last; uid++ {
			uids = append(uids, fmt.Sprint(uid))
		}
		if !strings.Contains(baseline[folder.name+"/UID SEARCH ALL"], "* SEARCH "+strings.Join(uids, " ")+"\r\n") {
			t.Fatalf("%s: UID SEARCH omitted messages", folder.name)
		}
		if !strings.Contains(baseline[folder.name+fmt.Sprintf("/UID FETCH %d (UID FLAGS BODY.PEEK[])", folder.first)], raw) {
			t.Fatalf("%s: UID FETCH did not return the original message", folder.name)
		}
	}
	for _, stage := range []struct {
		name, body string
		inboxTotal int
		laterTotal int
	}{
		{"snoozed", fmt.Sprintf(`{"snoozed_until":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339)), 99, 0},
		{"unsnoozed", `{"snoozed_until":null}`, 101, 1},
	} {
		t.Run(stage.name, func(t *testing.T) {
			path := fmt.Sprintf("/api/v1/accounts/%d/threads/snoozable/snooze", fixture.accountID)
			response := fixture.do(http.MethodPost, path, fixture.session, stage.body)
			if response.Code != http.StatusOK {
				t.Fatalf("snooze: %d %s", response.Code, response.Body)
			}
			for request, response := range snapshot() {
				if response != baseline[request] {
					t.Errorf("%s changed after %s: got %q, want %q", request, stage.name, response, baseline[request])
				}
			}
			if !reflect.DeepEqual(before, storedMessages()) {
				t.Error("snooze changed stored messages")
			}
			for folder, total := range map[string]int{"INBOX": stage.inboxTotal, "Later": stage.laterTotal} {
				path := fmt.Sprintf("/api/v1/accounts/%d/folders/%s/messages?limit=100", fixture.accountID, folder)
				response := fixture.do(http.MethodGet, path, fixture.session, "")
				if response.Code != http.StatusOK {
					t.Fatalf("default listing: %d %s", response.Code, response.Body)
				}
				var listing apiclient.MessageListResponse
				decodeInto(t, response, &listing)
				if listing.Pagination == nil || listing.Pagination.Total != int64(total) {
					t.Errorf("default %s total = %#v, want %d", folder, listing.Pagination, total)
				}
				if stage.name == "snoozed" {
					for _, message := range listing.Data {
						if message.ThreadID == "snoozable" {
							t.Errorf("default %s listing exposed a snoozed message", folder)
						}
					}
				}
			}
			response = fixture.do(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d/folders", fixture.accountID), fixture.session, "")
			if response.Code != http.StatusOK {
				t.Fatalf("default folders: %d %s", response.Code, response.Body)
			}
			var folders apiclient.FolderListResponse
			decodeInto(t, response, &folders)
			for _, folder := range folders.Data {
				if folder.Folder == "INBOX" && folder.Total != int64(stage.inboxTotal) || folder.Folder == "Later" && folder.Total != int64(stage.laterTotal) {
					t.Errorf("default folder count changed: %+v", folder)
				}
			}
		})
	}
}
