package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
)

func TestOwnMailboxSendingStillNeedsOnlyMailSend(t *testing.T) {
	d := &draftSendDB{}
	h := draftSendHandler(t, d)
	claims := &auth.Claims{AccountID: 7, Address: "owner@example.test", CredentialKind: auth.CredentialKindAPIKey, CredentialID: 9, APIKeyPermissions: []auth.ScopedPermission{{Capability: "mail:send", Scope: "mailbox:owner@example.test"}}}
	r := withClaims(httptest.NewRequest(http.MethodPost, "/api/v1/messages/send", strings.NewReader(`{"from":"owner@example.test","to":["far@remote.example"],"subject":"hello"}`)), claims)
	w := httptest.NewRecorder()
	composeRoute(h)(w, r)
	if w.Code != 201 || d.stored.sent != 1 || d.stored.queued != 1 {
		t.Fatalf("own send: %d %s, sent=%d queued=%d", w.Code, w.Body, d.stored.sent, d.stored.queued)
	}
}

func TestDelegatedSenderHeaderCannotBeOverridden(t *testing.T) {
	for _, sender := range []string{"", "actor@example.test"} {
		t.Run(sender, func(t *testing.T) {
			raw, err := buildOutboundRaw(outboundMessage{From: "shared@example.test", Sender: sender, To: []string{"recipient@example.test"}, Date: time.Now(), Extra: map[string]string{"sender": "impostor@example.test", "sEnDeR": "other@example.test"}})
			if err != nil {
				t.Fatal(err)
			}
			msg, err := mail.ReadMessage(strings.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			if len(msg.Header["Sender"]) > 1 || msg.Header.Get("Sender") != sender {
				t.Fatalf("Sender=%v, want %q", msg.Header["Sender"], sender)
			}
			from, err := mail.ParseAddress(msg.Header.Get("From"))
			if err != nil || from.Address != "shared@example.test" {
				t.Fatalf("From=%q: %v", msg.Header.Get("From"), err)
			}
		})
	}
}

func TestSendingRejectsClientChosenSender(t *testing.T) {
	for _, body := range []string{
		`{"from":"shared@example.test","to":["recipient@example.test"],"sender":"impostor@example.test"}`,
		`{"from":"shared@example.test","to":["recipient@example.test"],"on_behalf":true,"sender":"impostor@example.test"}`,
	} {
		req := sendingAs(httptest.NewRequest(http.MethodPost, "/api/v1/messages/send", strings.NewReader(body)), 1)
		w := httptest.NewRecorder()
		composeRoute(&MessageHandler{})(w, req)
		if w.Code != 422 || !strings.Contains(w.Body.String(), `"sender":"not allowed"`) {
			t.Fatalf("client Sender: %d %s", w.Code, w.Body)
		}
	}
}
