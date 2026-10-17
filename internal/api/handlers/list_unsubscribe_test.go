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

	dkim "github.com/rest-mail/go-dkim"
	"github.com/restmail/restmail/internal/api/middleware"
	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// TestSendMessage_RejectsUnsafeListUnsubscribe sends list_unsubscribe values
// that must never become a header (#144): line breaks, extra targets smuggled
// into one field, other schemes, URI or display-name forms, credentials. Each
// is refused with 400 before the handler touches the database (it has none
// here), and an empty object is refused rather than read as "no header".
func TestSendMessage_RejectsUnsafeListUnsubscribe(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"empty object", `{}`},
		{"empty strings", `{"mailto":"","https":""}`},
		{"CRLF in https", `{"https":"https://example.com/u\r\nBcc: victim@example.net"}`},
		{"LF in mailto", `{"mailto":"unsub@example.com\nX-Evil: 1"}`},
		{"second target in https", `{"https":"https://example.com/u>, <https://evil.example/u"}`},
		{"comma in https", `{"https":"https://example.com/u,https://evil.example/u"}`},
		{"space in https", `{"https":"https://example.com/u x"}`},
		{"http scheme", `{"https":"http://example.com/u"}`},
		{"mailto in https", `{"https":"mailto:unsub@example.com"}`},
		{"javascript scheme", `{"https":"javascript:alert(1)"}`},
		{"relative URL", `{"https":"/unsubscribe"}`},
		{"no host", `{"https":"https:///u"}`},
		{"credentials", `{"https":"https://user:pass@example.com/u"}`},
		{"non-ASCII URL", `{"https":"https://exämple.com/u"}`},
		{"over-long URL", `{"https":"https://example.com/` + strings.Repeat("a", maxUnsubscribeURLLen) + `"}`},
		{"mailto URI form", `{"mailto":"mailto:unsub@example.com"}`},
		{"mailto with query", `{"mailto":"unsub@example.com?subject=x"}`},
		{"mailto with query in local part", `{"mailto":"unsub?subject=x@example.com"}`},
		{"mailto with escape", `{"mailto":"unsub%0d%0a@example.com"}`},
		{"mailto domain literal", `{"mailto":"unsub@[127.0.0.1]"}`},
		{"mailto with display name", `{"mailto":"Unsub <unsub@example.com>"}`},
		{"mailto in angle brackets", `{"mailto":"<unsub@example.com>"}`},
		{"mailto two addresses", `{"mailto":"a@example.com, b@example.com"}`},
		{"mailto not an address", `{"mailto":"unsubscribe"}`},
		{"wrong type", `"https://example.com/u"`},
		{"unknown scheme key", `{"http":"http://example.com/u"}`},
		{"caller-set one-click field", `{"https":"https://example.com/u","post":"List-Unsubscribe=One-Click"}`},
		{"raw header", `{"mailto":"unsub@example.com","List-Unsubscribe":"<https://evil.example/u>"}`},
		{"list of targets", `[{"https":"https://example.com/u"},{"https":"https://example.com/v"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"from":"news@example.com","to":["bob@example.net"],"subject":"x","body_text":"hi",` +
				`"list_unsubscribe":` + tc.value + `}`
			rr := sendAs(&MessageHandler{}, &auth.Claims{AccountID: 1, CredentialKind: "app_password"}, body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rr.Code, rr.Body.String())
			}
		})
	}
}

// TestListUnsubscribe_Headers checks the rendering: mailto first, then the
// URL, and the one-click field only with an HTTPS target.
func TestListUnsubscribe_Headers(t *testing.T) {
	for _, tc := range []struct {
		name     string
		in       listUnsubscribe
		list     string
		oneClick bool
	}{
		{"mailto only", listUnsubscribe{Mailto: "unsub@example.com"}, "<mailto:unsub@example.com>", false},
		{"https only", listUnsubscribe{HTTPS: "https://example.com/u?t=abc"}, "<https://example.com/u?t=abc>", true},
		{"both", listUnsubscribe{Mailto: "unsub@example.com", HTTPS: "https://example.com/u"},
			"<mailto:unsub@example.com>, <https://example.com/u>", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.in.validate(); err != nil {
				t.Fatalf("validate: %v", err)
			}
			got := tc.in.headers()
			if got[headerListUnsubscribe] != tc.list {
				t.Errorf("List-Unsubscribe = %q, want %q", got[headerListUnsubscribe], tc.list)
			}
			post, ok := got[headerListUnsubscribePost]
			if ok != tc.oneClick || (ok && post != listUnsubscribeOneClick) {
				t.Errorf("List-Unsubscribe-Post = %q (present %v), want present %v", post, ok, tc.oneClick)
			}
		})
	}
}

// TestWithListUnsubscribe_ReplacesTransformFields checks a transform cannot
// leave its own List-Unsubscribe fields beside the request's (whatever their
// case), and that with no list_unsubscribe the transform headers pass as they
// were.
func TestWithListUnsubscribe_ReplacesTransformFields(t *testing.T) {
	extra := map[string]string{
		"list-unsubscribe":      "<https://evil.example/u>",
		"LIST-UNSUBSCRIBE-POST": listUnsubscribeOneClick,
		"X-Transformed":         "yes",
	}
	if got := withListUnsubscribe(extra, nil); len(got) != 3 {
		t.Fatalf("without list_unsubscribe the transform headers changed: %v", got)
	}
	got := withListUnsubscribe(extra, &listUnsubscribe{Mailto: "unsub@example.com"})
	want := map[string]string{"List-Unsubscribe": "<mailto:unsub@example.com>", "X-Transformed": "yes"}
	if len(got) != len(want) {
		t.Fatalf("headers = %v, want %v", got, want)
	}
	for name, value := range want {
		if got[name] != value {
			t.Fatalf("headers = %v, want %v", got, want)
		}
	}
}

// TestSendMessage_ListUnsubscribeIsSignedOnTheWire sends through the handler
// and reads the message it queued for relay (#144): the List-Unsubscribe
// fields are on the wire as asked, the one-click field only with an HTTPS
// target, both are in the DKIM signature's h=, the signature verifies, and
// changing either field breaks it. A send without list_unsubscribe carries
// neither field and signs as before. Needs the unit-test Postgres; skips when
// none is reachable.
func TestSendMessage_ListUnsubscribeIsSignedOnTheWire(t *testing.T) {
	h, domain, resolver := seedSigningDomain(t, "unsub")
	sender := seedSendingMailbox(t, h.db, domain, "news")

	for _, tc := range []struct {
		name     string
		value    string
		list     string
		oneClick bool
	}{
		{"https and mailto", `{"mailto":"unsub@` + domain + `","https":"https://` + domain + `/u?t=r1"}`,
			"<mailto:unsub@" + domain + ">, <https://" + domain + "/u?t=r1>", true},
		{"https only", `{"https":"https://` + domain + `/u?t=r2"}`, "<https://" + domain + "/u?t=r2>", true},
		{"mailto only", `{"mailto":"unsub@` + domain + `"}`, "<mailto:unsub@" + domain + ">", false},
		{"absent", ``, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := sendAndReadQueued(t, h, sender, tc.value)
			fields := headerFields(raw)

			if tc.list == "" {
				if len(fields["list-unsubscribe"]) != 0 || len(fields["list-unsubscribe-post"]) != 0 {
					t.Fatalf("a send without list_unsubscribe carries List-Unsubscribe fields:\n%s", raw)
				}
			} else if got := fields["list-unsubscribe"]; len(got) != 1 || got[0] != tc.list {
				t.Fatalf("List-Unsubscribe = %q, want [%q]:\n%s", got, tc.list, raw)
			}
			post := fields["list-unsubscribe-post"]
			if tc.oneClick && (len(post) != 1 || post[0] != listUnsubscribeOneClick) {
				t.Fatalf("List-Unsubscribe-Post = %q, want [%q]:\n%s", post, listUnsubscribeOneClick, raw)
			}
			if !tc.oneClick && len(post) != 0 {
				t.Fatalf("a send with no HTTPS target claims one-click: %q", post)
			}

			sigs := fields["dkim-signature"]
			if len(sigs) != 1 {
				t.Fatalf("the queued message carries %d DKIM-Signature fields, want 1:\n%s", len(sigs), raw)
			}
			named := map[string]int{}
			for _, name := range strings.Split(dkim.StripWSP(dkim.ParseTagList(sigs[0])["h"]), ":") {
				named[name]++
			}
			if tc.list != "" && named["list-unsubscribe"] < 1 {
				t.Errorf("h= does not sign List-Unsubscribe: %v", named)
			}
			if tc.oneClick && named["list-unsubscribe-post"] < 1 {
				t.Errorf("h= does not sign List-Unsubscribe-Post: %v", named)
			}
			if !tc.oneClick && named["list-unsubscribe-post"] != 0 {
				t.Errorf("h= names List-Unsubscribe-Post, which the message lacks: %v", named)
			}

			if got := verifyOne(t, raw, resolver); got.Result != dkim.ResultPass || got.Domain != domain {
				t.Fatalf("go-dkim verdict = %+v, want pass for d=%s", got, domain)
			}
			if tc.list != "" {
				// One target, which folding never splits.
				target := strings.SplitN(tc.list, ", ", 2)[0]
				tampered := strings.Replace(raw, target, "<https://evil.example/u>", 1)
				if got := verifyOne(t, tampered, resolver); got.Result != dkim.ResultFail {
					t.Errorf("a changed List-Unsubscribe still verifies: %+v", got)
				}
			}
			if tc.oneClick {
				tampered := strings.Replace(raw, "List-Unsubscribe-Post: "+listUnsubscribeOneClick+"\r\n", "", 1)
				if got := verifyOne(t, tampered, resolver); got.Result != dkim.ResultFail {
					t.Errorf("a message stripped of List-Unsubscribe-Post still verifies: %+v", got)
				}
			}
		})
	}
}

// TestSendMessage_OneClickNeedsDKIM sends from a hosted domain with no DKIM
// key (#144): an HTTPS target would put an unsigned one-click claim on the
// wire, which RFC 8058 §4 says receivers must not honour, so it is refused
// with 422 dkim_required and nothing is queued. A mailto-only target makes no
// claim, so it still sends. Needs the unit-test Postgres; skips when none is
// reachable.
func TestSendMessage_OneClickNeedsDKIM(t *testing.T) {
	h, _, _ := seedSigningDomain(t, "unsub-signing")
	domain := fmt.Sprintf("unsub-nokey-%d.test", time.Now().UnixNano())
	if err := h.db.Create(&models.Domain{Name: domain}).Error; err != nil {
		t.Fatalf("seed the unsigned domain: %v", err)
	}
	sender := seedSendingMailbox(t, h.db, domain, "news")
	claims := &auth.Claims{AccountID: sender.ID, CredentialKind: "app_password"}

	rr := sendAs(h, claims, sendBody(sender.Address, `{"https":"https://`+domain+`/u"}`))
	if rr.Code != http.StatusUnprocessableEntity || errorCodeOf(rr) != "dkim_required" {
		t.Fatalf("status = %d, body %s; want 422 dkim_required", rr.Code, rr.Body.String())
	}
	var queued int64
	h.db.Model(&models.OutboundQueue{}).Where("sender = ?", sender.Address).Count(&queued)
	if queued != 0 {
		t.Fatalf("a refused one-click send queued %d messages", queued)
	}

	raw := sendAndReadQueued(t, h, sender, `{"mailto":"unsub@`+domain+`"}`)
	fields := headerFields(raw)
	if got := fields["list-unsubscribe"]; len(got) != 1 || got[0] != "<mailto:unsub@"+domain+">" {
		t.Fatalf("List-Unsubscribe = %q:\n%s", got, raw)
	}
	if len(fields["list-unsubscribe-post"]) != 0 || len(fields["dkim-signature"]) != 0 {
		t.Fatalf("an unsigned mailto-only send claims one-click or carries a signature:\n%s", raw)
	}
}

// seedSendingMailbox adds a mailbox on domain, in the test's transaction, for
// an app password to send as.
func seedSendingMailbox(t *testing.T, db *gorm.DB, domain, local string) models.Mailbox {
	t.Helper()
	if err := db.AutoMigrate(&models.Mailbox{}, &models.Message{}, &models.OutboundQueue{}, &models.Contact{}); err != nil {
		t.Skipf("list-unsubscribe send test skipped: migrate failed (%v)", err)
	}
	var d models.Domain
	if err := db.Where("name = ?", domain).First(&d).Error; err != nil {
		t.Fatalf("find the domain: %v", err)
	}
	mb := models.Mailbox{DomainID: d.ID, LocalPart: local, Address: local + "@" + domain, DisplayName: "News", Active: true}
	if err := db.Create(&mb).Error; err != nil {
		t.Fatalf("seed the mailbox: %v", err)
	}
	return mb
}

// sendAndReadQueued sends one message to a remote recipient through the
// handler and returns the raw message it queued for relay.
func sendAndReadQueued(t *testing.T, h *MessageHandler, sender models.Mailbox, listUnsub string) string {
	t.Helper()
	claims := &auth.Claims{AccountID: sender.ID, CredentialKind: "app_password"}
	rr := sendAs(h, claims, sendBody(sender.Address, listUnsub))
	if rr.Code != http.StatusCreated {
		t.Fatalf("send: status %d, body %s", rr.Code, rr.Body.String())
	}
	var sent models.Message
	if err := json.Unmarshal(dataOf(t, rr), &sent); err != nil {
		t.Fatalf("decode the sent message: %v", err)
	}
	var q models.OutboundQueue
	if err := h.db.Where("message_id = ?", sent.ID).First(&q).Error; err != nil {
		t.Fatalf("find the queued message: %v", err)
	}
	return q.RawMessage
}

func sendBody(from, listUnsub string) string {
	body := `{"from":"` + from + `","to":["bob@example.net"],"subject":"Weekly news","body_text":"Hello Bob"`
	if listUnsub != "" {
		body += `,"list_unsubscribe":` + listUnsub
	}
	return body + `}`
}

func sendAs(h *MessageHandler, claims *auth.Claims, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/messages/send", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), middleware.ClaimsKey, claims))
	rr := httptest.NewRecorder()
	h.SendMessage(rr, req)
	return rr
}

func dataOf(t *testing.T, rr *httptest.ResponseRecorder) json.RawMessage {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil || env.Data == nil {
		t.Fatalf("decode the response %s: %v", rr.Body.String(), err)
	}
	return env.Data
}

// headerFields maps each lowercased field name to its unfolded values.
func headerFields(raw string) map[string][]string {
	headers, _ := dkim.SplitMessage([]byte(raw))
	out := map[string][]string{}
	for _, hdr := range headers {
		name := strings.ToLower(hdr.Name)
		value := strings.TrimSpace(strings.NewReplacer("\r\n", "", "\n", "").Replace(hdr.Value))
		out[name] = append(out[name], value)
	}
	return out
}

func verifyOne(t *testing.T, raw string, resolver dkim.TXTResolver) dkim.VerifyResult {
	t.Helper()
	results := dkim.Verify(context.Background(), []byte(raw), resolver)
	if len(results) != 1 {
		t.Fatalf("go-dkim found %d signatures, want 1: %+v", len(results), results)
	}
	return results[0]
}
