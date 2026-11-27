package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestForwardedAddressFieldsAgreeThroughDelivery is #1891 end to end: a message carrying the
// same address fields at the top and in an encapsulated message is delivered through the
// production router into PostgreSQL and read back over the real IMAP gateway, in a session
// that has not enabled UTF-8. The gateway downgrades the top-level header (RFC 6857) and the
// engine converts the encapsulated one; both keep group names, comments and quoted names, and
// spell them the same. RFC822.SIZE, which the API measured when it stored the message, is the
// octet count BODY[] sends.
func TestForwardedAddressFieldsAgreeThroughDelivery(t *testing.T) {
	f := newCredentialFixture(t)
	srv := httptest.NewServer(f.router)
	t.Cleanup(srv.Close)
	legacy := openIMAPSession(t, f, f.gatewayClient(srv.URL, imapCN))

	for _, row := range []struct{ name, fields, want string }{
		{"group name and members",
			"To: Grüppe: a@example.com,\r\n \"Jörg \\\"J\\\"\" <j@example.com>;, chris@example.test\r\n",
			"To: =?UTF-8?Q?Gr=C3=BCppe?= : a@example.com,\r\n =?UTF-8?Q?J=C3=B6rg_=22J=22?= <j@example.com>;, chris@example.test\r\n"},
		{"comments, nested and before a name",
			"From: (Büro) Jörg <j@example.com> (x (Bü) y)\r\n",
			"From: (=?UTF-8?Q?B=C3=BCro?=) =?UTF-8?Q?J=C3=B6rg?= <j@example.com> \r\n (=?UTF-8?Q?x_=28B=C3=BC=29_y?=)\r\n"},
		{"a name that isn't UTF-8 beside a 7-bit comment",
			"Cc: Ann <a@example.com> (kept), J\xf6rg <k@example.com>\r\n",
			"Cc: Ann <a@example.com> (kept), =?UNKNOWN-8BIT?Q?J=F6rg?= <k@example.com>\r\n"},
		{"an address with no 7-bit form and an 8-bit domain",
			"Reply-To: üser@example.com (Büro), J <j@bücher.example>\r\n",
			"Reply-To: =?UTF-8?Q?=C3=BCser=40example=2Ecom?= :; (=?UTF-8?Q?B=C3=BCro?=), J <j@xn--bcher-kva.example>\r\n"},
	} {
		t.Run(row.name, func(t *testing.T) {
			if err := f.db.Exec("DELETE FROM messages WHERE mailbox_id = ?", f.mailboxID).Error; err != nil {
				t.Fatal(err)
			}
			const date = "Date: Thu, 02 Jan 2020 03:04:05 +0000\r\n"
			message := row.fields + "Subject: fwd\r\n" + date + "MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n" +
				"--b\r\nContent-Type: text/plain\r\n\r\nsee below\r\n" +
				"--b\r\nContent-Type: message/rfc822\r\n\r\n" + row.fields + "Subject: inner\r\n" + date + "\r\ninner body\r\n--b--\r\n"
			payload, err := json.Marshal(map[string]any{"address": f.address, "sender": "envelope@remote.test", "recipients_to": []string{f.address},
				"raw_message": base64.StdEncoding.EncodeToString([]byte(message))})
			if err != nil {
				t.Fatal(err)
			}
			if answer := f.do(http.MethodPost, "/api/v1/messages/deliver", "", string(payload)); answer.Code != http.StatusCreated {
				t.Fatalf("refused: %d %s", answer.Code, answer.Body)
			}
			lastStored(t, f, message)

			legacy.do("SELECT INBOX")
			top := onlyFetchedLiteral(t, legacy.do("FETCH 1 BODY.PEEK[HEADER]"))
			inner := onlyFetchedLiteral(t, legacy.do("FETCH 1 BODY.PEEK[2.HEADER]"))
			if !strings.Contains(top, "\r\n"+row.want+"Subject: fwd\r\n") {
				t.Errorf("top-level HEADER\n got %q\nwant it to hold %q", top, row.want)
			}
			if !strings.HasPrefix(inner, row.want+"Subject: inner\r\n") {
				t.Errorf("encapsulated 2.HEADER\n got %q\nwant it to start %q", inner, row.want)
			}
			size := legacy.do("FETCH 1 RFC822.SIZE")
			whole := onlyFetchedLiteral(t, legacy.do("FETCH 1 BODY.PEEK[]"))
			if want := fmt.Sprintf("RFC822.SIZE %d)", len(whole)); !strings.Contains(size, want) {
				t.Errorf("%q lacks %q", size, want)
			}
		})
	}
}

// onlyFetchedLiteral is the one literal a FETCH response holds.
func onlyFetchedLiteral(t *testing.T, response string) string {
	t.Helper()
	open := strings.Index(response, "}\r\n")
	if open < 0 || strings.Count(response, "}\r\n") != 1 {
		t.Fatalf("not one literal in %q", response)
	}
	var size int
	if _, err := fmt.Sscanf(response[strings.LastIndex(response[:open], "{")+1:open], "%d", &size); err != nil {
		t.Fatalf("literal size in %q: %v", response, err)
	}
	return response[open+3 : open+3+size]
}
