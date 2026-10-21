package smtp

import (
	"errors"
	"strings"
	"testing"
)

func TestSMTP_EAISessionIdentity(t *testing.T) {
	for _, kind := range []string{"password", "app_password"} {
		for _, tc := range []struct {
			name, login, sender string
		}{
			{"IDN", "ana@xn--bcher-kva.test", "ana@bücher.test"},
			{"NFC and case", "josé@example.test", "JOSE\u0301@example.test"},
			{"legacy login", "jose\u0301@xn--bcher-kva.test", "josé@bücher.test"},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				back, store := newMockBackend(), newMockStore()
				back.user = tc.login
				password := back.pass
				if kind == "app_password" {
					back.appPassword, back.credentialID = testSMTPAppPassword, 7
					password = testSMTPAppPassword
				}
				store.identities = map[string]uint{tc.sender: back.accountID}
				h := newSMTPHarness(t, back, store, true)
				h.ehlo()
				if r := h.authPlain(back.user, password); replyCode(r) != "235" {
					t.Fatal(r)
				}
				if r := h.cmd("MAIL FROM:<%s> SMTPUTF8", tc.sender); replyCode(r) != "250" {
					t.Fatalf("equivalent sender = %q", r)
				}
				if r := h.cmd("RCPT TO:<carol@remote.test>"); replyCode(r) != "250" {
					t.Fatal(r)
				}
				body := "From: <" + tc.sender + ">\r\nSubject: Grüße\r\n\r\nHello.\r\n"
				if r := h.dataBody(body); replyCode(r) != "250" {
					t.Fatalf("equivalent From header = %q", r)
				}
				queued, persisted := store.queued(), store.persistedSubmissions()
				if len(queued) != 1 || len(persisted) != 1 {
					t.Fatalf("queued/persisted = %d/%d, want 1/1", len(queued), len(persisted))
				}
				if queued[0].Sender != tc.sender || persisted[0].Sender != tc.sender || !strings.Contains(queued[0].RawMessage, body) {
					t.Fatal("submitted sender spelling or message bytes changed")
				}
			})
		}
	}
}

// Canonically equal strings may belong to distinct legacy accounts. Even a
// mail:send grant must not extend an app password to that other mailbox.
func TestSMTP_EAIAppPasswordIdentityDenied(t *testing.T) {
	for _, lookupError := range []bool{false, true} {
		name := "distinct legacy account"
		if lookupError {
			name = "lookup failure"
		}
		t.Run(name, func(t *testing.T) {
			back, store := newMockBackend(), newMockStore()
			back.user = "josé@example.test"
			back.appPassword, back.credentialID = testSMTPAppPassword, 7
			const sender = "jose\u0301@example.test"
			store.identities = map[string]uint{sender: back.accountID + 1}
			store.authorized[sender] = true
			if lookupError {
				store.identityErr = errors.New("database down")
			}
			h := newSMTPHarness(t, back, store, true)
			h.ehlo()
			if r := h.authPlain(back.user, testSMTPAppPassword); replyCode(r) != "235" {
				t.Fatal(r)
			}
			if r := h.cmd("MAIL FROM:<%s> SMTPUTF8", sender); replyCode(r) != "553" {
				t.Fatalf("other identity MAIL = %q, want 553", r)
			}
			if r := h.cmd("MAIL FROM:<%s> SMTPUTF8", back.user); replyCode(r) != "250" {
				t.Fatal(r)
			}
			if r := h.cmd("RCPT TO:<carol@remote.test>"); replyCode(r) != "250" {
				t.Fatal(r)
			}
			if r := h.dataBody("From: <" + sender + ">\r\nSubject: Hi\r\n\r\nbody\r\n"); replyCode(r) != "550" {
				t.Fatalf("other identity header = %q, want 550", r)
			}
			if len(store.queued()) != 0 || len(store.persistedSubmissions()) != 0 || len(back.deliveredTo()) != 0 {
				t.Fatal("rejected submission committed mail")
			}
		})
	}
}
