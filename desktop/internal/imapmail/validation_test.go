package imapmail

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/smtp"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/rest-mail/rest-mail-server/desktop/internal/apiclient"
	apitypes "github.com/rest-mail/rest-mail-server/pkg/apitypes"
	keyring "github.com/zalando/go-keyring"
)

func validInput() AccountInput {
	return AccountInput{Host: "imap.test", Username: "reader", Password: testPassword, SMTPHost: "smtp.test", Address: "reader@example.test"}
}

func TestAccountValidationRequiresVerifiedTLSAndSafeIdentity(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*AccountInput)
	}{
		{"missing_host", func(input *AccountInput) { input.Host = "" }},
		{"host_control", func(input *AccountInput) { input.Host = "host\nLOGIN" }},
		{"host_port", func(input *AccountInput) { input.Host = "host:993" }},
		{"invalid_port", func(input *AccountInput) { input.Port = -1 }},
		{"invalid_SMTP_port", func(input *AccountInput) { input.SMTPPort = 65536 }},
		{"plaintext", func(input *AccountInput) { input.IMAPSecurity = "none" }},
		{"plaintext_SMTP", func(input *AccountInput) { input.SMTPSecurity = "none" }},
		{"missing_password", func(input *AccountInput) { input.Password = "" }},
		{"missing_username", func(input *AccountInput) { input.Username = "" }},
		{"invalid_address", func(input *AccountInput) { input.Address = "not-an-address" }},
		{"display_address", func(input *AccountInput) { input.Address = "Reader <reader@example.test>" }},
		{"username_control", func(input *AccountInput) { input.Username = "reader\r\n" }},
		{"password_control", func(input *AccountInput) { input.Password = "secret\n" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := validInput()
			test.change(&input)
			if _, err := validate(input); err == nil {
				t.Fatal("unsafe account accepted")
			}
		})
	}
	input := validInput()
	input.Host = "::1"
	input.Port = 143
	input.SMTPPort = 465
	account, err := validate(input)
	if err != nil || account.IMAPSecurity != "starttls" || account.SMTPSecurity != "tls" {
		t.Fatalf("port/security inference failed: %+v %v", account, err)
	}
	store := &Store{tlsConfig: &tls.Config{InsecureSkipVerify: true}}
	if _, err := store.tls("mail.test"); err == nil {
		t.Fatal("certificate verification disabled")
	}
	store.tlsConfig = &tls.Config{MinVersion: tls.VersionTLS10}
	config, err := store.tls("mail.test")
	if err != nil || config.MinVersion != tls.VersionTLS12 || config.ServerName != "mail.test" {
		t.Fatal("unsafe TLS defaults")
	}
}

func TestFolderRolesAndSMTPMessageValidation(t *testing.T) {
	for _, test := range []struct {
		name string
		attr imap.MailboxAttr
		role string
	}{
		{"INBOX", "", "inbox"}, {"other", imap.MailboxAttrSent, "sent"}, {"other", imap.MailboxAttrDrafts, "drafts"}, {"other", imap.MailboxAttrTrash, "trash"}, {"other", imap.MailboxAttrJunk, "junk"}, {"other", imap.MailboxAttrArchive, "archive"}, {"Sent", "", "sent"}, {"Drafts", "", "drafts"}, {"Trash", "", "trash"}, {"Spam", "", "junk"}, {"Archive", "", "archive"}, {"custom", "", ""},
	} {
		role := folderRole(&imap.ListData{Mailbox: test.name, Attrs: []imap.MailboxAttr{test.attr}})
		if test.role == "" {
			if role != nil {
				t.Fatal("custom folder received special-use role")
			}
		} else if role == nil || *role != test.role {
			t.Fatalf("wrong role for %s", test.name)
		}
	}
	account := Account{Address: "reader@example.test"}
	for _, request := range []apitypes.SendMessageRequest{{From: "other@example.test"}, {RequireTLS: true}, {Subject: "injected\r\nBcc:evil@example.test"}, {To: []string{"bad address"}}, {}} {
		if _, _, err := buildMessage(account, request); err == nil {
			t.Fatal("invalid SMTP message accepted")
		}
	}
	request := apitypes.SendMessageRequest{To: []string{"recipient@example.test"}, Cc: []string{"copy@example.test"}, Subject: "Résumé", BodyText: "text", InReplyTo: "<prior@example.test>"}
	raw, recipients, err := buildMessage(account, request)
	if err != nil || len(recipients) != 2 || !strings.Contains(string(raw), "In-Reply-To:") {
		t.Fatalf("valid SMTP message failed: %v", err)
	}
	auth := &loginAuth{username: "reader", password: testPassword}
	if _, _, err := auth.Start(&smtp.ServerInfo{}); err == nil {
		t.Fatal("LOGIN accepted plaintext")
	}
	if _, _, err := auth.Start(&smtp.ServerInfo{TLS: true}); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		if _, err := auth.Next(nil, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := auth.Next(nil, true); err == nil {
		t.Fatal("LOGIN accepted extra challenge")
	}
	if _, err := auth.Next(nil, false); err != nil {
		t.Fatal(err)
	}
}

func TestKeychainFailuresFailClosedAndIDsAreBounded(t *testing.T) {
	keyring.MockInit()
	store, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.AddIMAPAccount(t.Context(), AccountInput{Password: testPassword}); err == nil {
		t.Fatal("invalid account accepted")
	}
	if _, err := keyringPassword(IDBase + 1); err == nil {
		t.Fatal("missing password accepted")
	}
	if _, _, ok := store.resolve(1); ok {
		t.Fatal("server ID resolved locally")
	}
	if _, _, _, err := store.GetMessage(t.Context(), IDBase+1); err == nil {
		t.Fatal("unknown message fetched")
	}
	if store.RemoveIMAPAccount(IDBase+1) == nil {
		t.Fatal("unknown account removed")
	}
	if store.UpdateMessage(t.Context(), 1, apitypes.UpdateMessageBody{}) == nil || store.DeleteMessage(t.Context(), 1, true) == nil {
		t.Fatal("server message changed through local adapter")
	}
	keywords := []string{"keyword"}
	if store.UpdateMessage(t.Context(), IDBase+1, apitypes.UpdateMessageBody{Keywords: &keywords}) == nil {
		t.Fatal("unsupported keyword silently ignored")
	}
	if _, err := store.messageID(IDBase+1, "INBOX", 0, 1); err == nil {
		t.Fatal("missing UIDVALIDITY accepted")
	}
	first, err := store.messageID(IDBase+1, "INBOX", 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.messageID(IDBase+1, "INBOX", 11, 1)
	if err != nil || first == second {
		t.Fatal("UIDVALIDITY reuse collided")
	}
	store.data.NextFolder = maxFolderSlot
	if _, err := store.messageID(IDBase+1, "other", 1, 1); err == nil {
		t.Fatal("folder slot overflow")
	}
	keyring.MockInitWithError(errors.New("keychain unavailable"))
	if _, err := New(); err == nil {
		t.Fatal("keychain failure accepted")
	}
	if saveMetadata(metadata{}) == nil {
		t.Fatal("metadata write failure ignored")
	}
	keyring.MockInit()
	_ = keyring.Set(keychainService, metadataKey, "not JSON")
	if _, err := New(); err == nil {
		t.Fatal("corrupt metadata accepted")
	}
	keyring.MockInit()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if waitPoll(ctx) {
		t.Fatal("polling survived cancellation")
	}
	if err := store.useConnection(ctx, Account{}, &connection{}, nil); err == nil {
		t.Fatal("canceled command ran")
	}
	if err := store.useConnection(t.Context(), Account{}, &connection{closed: true}, nil); err == nil {
		t.Fatal("removed connection reused")
	}
}

func TestAdapterRejectsMalformedAndUnsupportedRequests(t *testing.T) {
	store, account, fixture, _ := setupStore(t, false, false)
	appendMail(t, fixture, "first", false)
	page, err := store.ListMessages(t.Context(), account.ID, "INBOX", apiclient.Page{Limit: 101}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method, path string
		body         any
		status       int
	}{
		{"GET", "/accounts/4503599627370999/folders", nil, 404},
		{"POST", "/accounts/4503599627370497/events", nil, 405},
		{"POST", "/accounts/4503599627370497/search", nil, 405},
		{"GET", "/accounts/4503599627370497/unknown", nil, 501},
		{"GET", "/accounts/4503599627370497/contacts/suggest", nil, 200},
		{"GET", "/messages/4503599627370497", nil, 404},
		{"POST", "/attachments/4503599627370497", nil, 405},
		{"GET", "/attachments/4503599627370497", nil, 404},
		{"POST", "/messages/send", nil, 400},
		{"POST", "/messages/draft", map[string]string{"from": account.Address}, 501},
	} {
		response := requestAdapter(t, store, test.method, test.path, test.body)
		response.Body.Close()
		if response.StatusCode != test.status {
			t.Fatalf("%s %s: got %d want %d", test.method, test.path, response.StatusCode, test.status)
		}
	}
	if accounts := store.AccountsResponse(); len(accounts) != 1 || !accounts[0].IsPrimary {
		t.Fatal("invalid local account envelope")
	}
	for _, body := range []string{"{", `{"unknown":true}`, `{} {}`} {
		request, _ := http.NewRequest("POST", "/", strings.NewReader(body))
		if decodeBody(request, &struct{}{}) == nil {
			t.Fatal("invalid body accepted")
		}
	}
	request, _ := http.NewRequest("POST", "/", nil)
	if decodeBody(request, &struct{}{}) == nil {
		t.Fatal("missing body accepted")
	}
	response := store.jsonResponse(200, make(chan int))
	response.Body.Close()
	if response.StatusCode != 500 {
		t.Fatal("encoding failure did not fail closed")
	}
	store.remember("special<&>password")
	response = store.jsonResponse(200, map[string]any{"body": []string{"special<&>password"}})
	var decoded any
	if json.NewDecoder(response.Body).Decode(&decoded) != nil {
		t.Fatal("invalid safe JSON")
	}
	response.Body.Close()
	encoded, _ := json.Marshal(decoded)
	if strings.Contains(string(encoded), "special") {
		t.Fatal("JSON escaping bypassed password redaction")
	}
	rawResponse := requestAdapter(t, store, "GET", "/messages/"+jsonNumber(page.Data[0].ID)+"/raw", nil)
	raw, _ := io.ReadAll(rawResponse.Body)
	rawResponse.Body.Close()
	if !strings.Contains(string(raw), "Subject: first") {
		t.Fatal("raw route lost original headers")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := store.Search(ctx, account.ID, "first", "INBOX", apiclient.Page{}); err != nil {
		t.Fatal(err)
	}
}

func jsonNumber(value uint) string { encoded, _ := json.Marshal(value); return string(encoded) }
