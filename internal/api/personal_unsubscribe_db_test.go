package api

import (
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/api/handlers"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func TestAC01OneClickRedirectsRequireAnActualPost(t *testing.T) {
	fixture := newCredentialFixture(t)
	reader := personalMailboxKey(t, fixture.db, fixture.accountID, fixture.address, models.CapMailRead)
	writer := personalMailboxKey(t, fixture.db, fixture.accountID, fixture.address, models.CapMailWrite)
	for _, redirect := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(fmt.Sprint(redirect), func(t *testing.T) {
			type exchange struct {
				method, path, body, contentType, cookie, authorization string
			}
			requests := make(chan exchange, 4)
			server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Errorf("read unsubscribe body: %v", err)
				}
				requests <- exchange{request.Method, request.URL.Path, string(body), request.Header.Get("Content-Type"), request.Header.Get("Cookie"), request.Header.Get("Authorization")}
				if request.URL.Path == "/start" {
					http.Redirect(response, request, "/accepted", redirect)
					return
				}
				response.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			fixture.routers.Messages.SetOneClickTarget(server.Listener.Addr().(*net.TCPAddr).IP, roots)
			target := server.URL + "/start"
			message := models.Message{MailboxID: fixture.mailboxID, Folder: "INBOX", Subject: "List unsubscribe redirect",
				RawMessage: "Authentication-Results: restmail; dkim=pass header.d=news.example.com\r\n" +
					"DKIM-Signature: v=1; a=rsa-sha256; d=news.example.com; s=news; h=From:List-Unsubscribe:List-Unsubscribe-Post; bh=YQ==; b=YQ==\r\n" +
					"From: news@news.example.com\r\nList-Unsubscribe: <" + target + ">\r\n" +
					"List-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n\r\nweekly news\r\n"}
			if err := fixture.db.Create(&message).Error; err != nil {
				t.Fatal(err)
			}
			var before models.Message
			if err := fixture.db.First(&before, message.ID).Error; err != nil {
				t.Fatal(err)
			}
			path := fmt.Sprintf("/api/v1/messages/%d/unsubscribe", message.ID)
			options := fixture.do(http.MethodGet, path, reader, "")
			var offered struct{ Data handlers.UnsubscribeOptions }
			decodeInto(t, options, &offered)
			if options.Code != http.StatusOK || offered.Data.OneClick != target || len(requests) != 0 {
				t.Fatalf("options must offer the stored target without contacting it: %d %s, %d requests", options.Code, options.Body, len(requests))
			}
			refused := fixture.do(http.MethodPost, path+"/one-click", reader, "")
			if refused.Code != http.StatusForbidden || len(requests) != 0 {
				t.Fatalf("read-only key must not post: %d %s, %d requests", refused.Code, refused.Body, len(requests))
			}
			response := fixture.do(http.MethodPost, path+"/one-click", writer, "")
			preservesPost := redirect == http.StatusTemporaryRedirect || redirect == http.StatusPermanentRedirect
			wantStatus, wantRequests := http.StatusBadGateway, 1
			if preservesPost {
				wantStatus, wantRequests = http.StatusOK, 2
			}
			if response.Code != wantStatus {
				t.Errorf("redirect %d: API returned %d %s, want %d; a redirected GET cannot unsubscribe", redirect, response.Code, response.Body, wantStatus)
			}
			if len(requests) != wantRequests {
				t.Errorf("redirect %d reached %d endpoints, want %d", redirect, len(requests), wantRequests)
			}
			for len(requests) > 0 {
				request := <-requests
				if request.method != http.MethodPost || request.body != "List-Unsubscribe=One-Click" || request.contentType != "application/x-www-form-urlencoded" || request.cookie != "" || request.authorization != "" {
					t.Errorf("unsubscribe must only send a credential-free RFC 8058 POST, got %+v", request)
				}
			}
			if preservesPost {
				var result struct{ Data handlers.UnsubscribedOneClick }
				decodeInto(t, response, &result)
				if result.Data.Method != "one_click" || result.Data.Target != target || result.Data.ResponseStatus != http.StatusNoContent {
					t.Errorf("response did not report the observed POST result: %+v", result.Data)
				}
			} else if response.Code == http.StatusBadGateway {
				var failure struct{ Error struct{ Code string } }
				decodeInto(t, response, &failure)
				if failure.Error.Code != "unsubscribe_failed" {
					t.Errorf("redirect refusal must be an upstream failure: %s", response.Body)
				}
			}
			var after models.Message
			if err := fixture.db.First(&after, message.ID).Error; err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("unsubscribe changed the received message: before=%+v after=%+v (%v)", before, after, err)
			}
			var queued int64
			if err := fixture.db.Model(&models.OutboundQueue{}).Count(&queued).Error; err != nil || queued != 0 {
				t.Fatalf("HTTPS unsubscribe created %d outbound messages (%v)", queued, err)
			}
		})
	}
}
