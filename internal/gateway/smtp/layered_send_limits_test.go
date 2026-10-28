package smtp

import (
	"testing"
)

type budgetStore struct {
	*mockStore
	attempts []SubmissionAttempt
}

func (s *budgetStore) PersistentSendLimits() bool { return true }
func (s *budgetStore) ReserveSubmission(a SubmissionAttempt) (bool, error) {
	s.attempts = append(s.attempts, a)
	return len(s.attempts) > 1, nil
}

func TestLayeredSubmissionTempfailsBeforeAnyLocalOrRemoteDelivery(t *testing.T) {
	back := newMockBackend()
	back.local["local@example.com"] = true
	store := &budgetStore{mockStore: newMockStore()}
	h := newSMTPHarness(t, back, store.mockStore, true, func(s *Server) { s.store = store })
	h.ehlo()
	if r := h.authPlain(back.user, back.pass); replyCode(r) != "235" {
		t.Fatal(r)
	}
	for i := range 2 {
		if r := h.cmd("MAIL FROM:<alice@example.com>"); replyCode(r) != "250" {
			t.Fatal(r)
		}
		for _, to := range []string{"remote@far.test", "local@example.com"} {
			if r := h.cmd("RCPT TO:<%s>", to); replyCode(r) != "250" {
				t.Fatal(r)
			}
		}
		reply := h.dataBody("From: alice@example.com\r\nSubject: budget\r\n\r\nbody\r\n")
		want := "250"
		if i == 1 {
			want = "451"
		}
		if replyCode(reply) != want {
			t.Fatalf("submission %d: %s", i, reply)
		}
	}
	if len(store.queued()) != 1 || len(back.delivered) != 1 {
		t.Fatalf("limited mail delivered: remote=%d local=%d", len(store.queued()), len(back.delivered))
	}
	if len(store.attempts) != 2 {
		t.Fatalf("budget attempts=%d", len(store.attempts))
	}
	a := store.attempts[1]
	if a.AccountID != back.accountID || a.CredentialID != 1 || a.Recipients != 2 || a.Sender != "alice@example.com" {
		t.Fatalf("lost actor/credential/recipients: %+v", a)
	}
}
