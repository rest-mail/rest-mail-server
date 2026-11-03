package smtp

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/srs"
)

const (
	srsTestSecret   = "fake-srs-secret-current-for-tests"
	srsTestPrevious = "fake-srs-secret-previous-for-tests"
	srsTestDomain   = "forward.example"
	srsTestSender   = "alice@orig.example"
)

func srsAddress(t *testing.T, secret, sender string, at time.Time) string {
	t.Helper()
	address, err := srs.New(secret, "").Rewrite(sender, srsTestDomain, at)
	if err != nil {
		t.Fatal(err)
	}
	return address
}

func newSRSHarness(t *testing.T, store *mockStore, isSubmission bool) *smtpHarness {
	t.Helper()
	store.hosted = map[string]bool{srsTestDomain: true}
	h := newSMTPHarness(t, newMockBackend(), store, isSubmission, func(s *Server) {
		s.SetSRS(srs.New(srsTestSecret, srsTestPrevious), 7, time.Hour)
	})
	h.ehlo()
	return h
}

// A bounce to a valid SRS address is accepted, and passed on to the sender the
// address carries with a null reverse path, at the DSN limit (#775, #715).
func TestSRSBounceIsRelayedToTheOriginalSender(t *testing.T) {
	store := newMockStore()
	h := newSRSHarness(t, store, false)
	address := srsAddress(t, srsTestSecret, srsTestSender, time.Now())

	if r := h.cmd("MAIL FROM:<>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<%s>", address); replyCode(r) != "250" {
		t.Fatalf("RCPT = %q", r)
	}
	if r := h.dataBody(testBody); replyCode(r) != "250" {
		t.Fatalf("DATA = %q", r)
	}

	relayed := store.relayedBounces()
	if len(relayed) != 1 {
		t.Fatalf("relayed %d bounces, want 1", len(relayed))
	}
	got := relayed[0]
	if got.Recipient != srsTestSender || got.SendingDomain != srsTestDomain {
		t.Errorf("relayed to %q as %q, want %q as %q", got.Recipient, got.SendingDomain, srsTestSender, srsTestDomain)
	}
	if got.MaxPerRecipient != 7 || got.Window != time.Hour {
		t.Errorf("limit = %d per %v, want 7 per 1h", got.MaxPerRecipient, got.Window)
	}
	if !strings.Contains(got.RawMessage, "Received:") {
		t.Errorf("the relayed message lacks this hop's trace:\n%s", got.RawMessage)
	}
	if n := len(store.queued()); n != 0 {
		t.Errorf("%d ordinary queue rows, want none", n)
	}
	if delivered := h.back.deliveredTo(); len(delivered) != 0 {
		t.Errorf("delivered locally to %v", delivered)
	}
}

// A bounce to an address signed before a rotation is still relayed.
func TestSRSBounceAcceptsThePreviousSecret(t *testing.T) {
	h := newSRSHarness(t, newMockStore(), false)
	if r := h.cmd("MAIL FROM:<>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	address := srsAddress(t, srsTestPrevious, srsTestSender, time.Now())
	if r := h.cmd("RCPT TO:<%s>", address); replyCode(r) != "250" {
		t.Fatalf("RCPT = %q", r)
	}
}

// A next hop may change the case of the local part, and the bounce still arrives.
func TestSRSBounceIgnoresCase(t *testing.T) {
	store := newMockStore()
	h := newSRSHarness(t, store, false)
	address := srsAddress(t, srsTestSecret, srsTestSender, time.Now())
	local, domain, _ := strings.Cut(address, "@")
	if r := h.cmd("MAIL FROM:<>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<%s@%s>", strings.ToLower(local), domain); replyCode(r) != "250" {
		t.Fatalf("RCPT = %q", r)
	}
	if r := h.dataBody(testBody); replyCode(r) != "250" {
		t.Fatalf("DATA = %q", r)
	}
	if relayed := store.relayedBounces(); len(relayed) != 1 || relayed[0].Recipient != srsTestSender {
		t.Fatalf("relayed = %+v, want one to %s", relayed, srsTestSender)
	}
}

// A forged or expired address is refused at RCPT with 550 5.1.1, and counts as a
// rejected recipient for the tarpit like any unknown one.
func TestSRSForgedOrExpiredIsRefusedAtRcpt(t *testing.T) {
	now := time.Now()
	valid := srsAddress(t, srsTestSecret, srsTestSender, now)
	for name, address := range map[string]string{
		"forged":    srsAddress(t, "fake-srs-secret-another-for-tests", srsTestSender, now),
		"altered":   strings.Replace(valid, "alice", "mallory", 1),
		"malformed": "SRS0=nonsense@" + srsTestDomain,
		"expired":   srsAddress(t, srsTestSecret, srsTestSender, now.AddDate(0, 0, -srs.Lifetime-1)),
	} {
		t.Run(name, func(t *testing.T) {
			store := newMockStore()
			h := newSRSHarness(t, store, false)
			if r := h.cmd("MAIL FROM:<>"); replyCode(r) != "250" {
				t.Fatalf("MAIL FROM = %q", r)
			}
			r := h.cmd("RCPT TO:<%s>", address)
			if !strings.HasPrefix(r, "550 5.1.1") {
				t.Fatalf("RCPT = %q, want 550 5.1.1", r)
			}
			if len(store.relayedBounces()) != 0 {
				t.Fatal("a refused address was relayed")
			}
		})
	}
}

// A valid SRS address under a domain not hosted here is an unknown recipient, and
// inbound must never become an outbound relay.
func TestSRSAddressUnderAnotherDomainIsNotOurs(t *testing.T) {
	store := newMockStore()
	h := newSRSHarness(t, store, false)
	address := srsAddress(t, srsTestSecret, srsTestSender, time.Now())
	local, _, _ := strings.Cut(address, "@")
	if r := h.cmd("MAIL FROM:<>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<%s@elsewhere.example>", local); !strings.HasPrefix(r, "550 5.1.1") {
		t.Fatalf("RCPT = %q, want 550 5.1.1", r)
	}
}

// A database failure says nothing about the address, so the peer is told to retry
// rather than refused for good.
func TestSRSHostedLookupFailureIsTemporary(t *testing.T) {
	store := newMockStore()
	h := newSRSHarness(t, store, false)
	store.mu.Lock()
	store.hostedErr = errTestStore
	store.mu.Unlock()
	if r := h.cmd("MAIL FROM:<>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<%s>", srsAddress(t, srsTestSecret, srsTestSender, time.Now())); replyCode(r) != "451" {
		t.Fatalf("RCPT = %q, want 451", r)
	}
}

// A queue failure after RCPT fails the transaction with a retry, so the bounce is
// not lost.
func TestSRSRelayFailureIsTemporary(t *testing.T) {
	store := newMockStore()
	store.relayErr = errTestStore
	h := newSRSHarness(t, store, false)
	if r := h.cmd("MAIL FROM:<>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<%s>", srsAddress(t, srsTestSecret, srsTestSender, time.Now())); replyCode(r) != "250" {
		t.Fatalf("RCPT = %q", r)
	}
	if r := h.dataBody(testBody); replyCode(r) != "451" {
		t.Fatalf("DATA = %q, want 451", r)
	}
}

// A transaction reset forgets the addresses it accepted.
func TestSRSRelaysResetWithTheTransaction(t *testing.T) {
	store := newMockStore()
	back := newMockBackend()
	back.local["bob@"+srsTestDomain] = true
	store.hosted = map[string]bool{srsTestDomain: true}
	h := newSMTPHarness(t, back, store, false, func(s *Server) {
		s.SetSRS(srs.New(srsTestSecret, ""), 7, time.Hour)
	})
	h.ehlo()
	if r := h.cmd("MAIL FROM:<>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<%s>", srsAddress(t, srsTestSecret, srsTestSender, time.Now())); replyCode(r) != "250" {
		t.Fatalf("RCPT = %q", r)
	}
	if r := h.cmd("RSET"); replyCode(r) != "250" {
		t.Fatalf("RSET = %q", r)
	}
	if r := h.cmd("MAIL FROM:<>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<bob@%s>", srsTestDomain); replyCode(r) != "250" {
		t.Fatalf("RCPT = %q", r)
	}
	if r := h.dataBody(testBody); replyCode(r) != "250" {
		t.Fatalf("DATA = %q", r)
	}
	if n := len(store.relayedBounces()); n != 0 {
		t.Fatalf("relayed %d bounces after RSET, want none", n)
	}
	if delivered := back.deliveredTo(); len(delivered) != 1 {
		t.Fatalf("delivered = %v, want bob", delivered)
	}
}

// Without a secret there is no way to check an address, so one is refused like any
// unknown recipient, and nothing is relayed.
func TestSRSWithoutASecretRefuses(t *testing.T) {
	store := newMockStore()
	store.hosted = map[string]bool{srsTestDomain: true}
	h := newSMTPHarness(t, newMockBackend(), store, false)
	h.ehlo()
	if r := h.cmd("MAIL FROM:<>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<%s>", srsAddress(t, srsTestSecret, srsTestSender, time.Now())); !strings.HasPrefix(r, "550 5.1.1") {
		t.Fatalf("RCPT = %q, want 550 5.1.1", r)
	}
}

// An authenticated submission is a client sending mail out, not a bounce coming
// back: the check is for inbound, and the address goes out like any other, to be
// checked at the MX it reaches.
func TestSRSNotCheckedOnAuthenticatedSubmission(t *testing.T) {
	store := newMockStore()
	back := newMockBackend()
	store.authorized["sender@remote.test"] = true
	store.hosted = map[string]bool{srsTestDomain: true}
	h := newSMTPHarness(t, back, store, true, func(s *Server) {
		s.SetSRS(srs.New(srsTestSecret, ""), 7, time.Hour)
	})
	h.ehlo()
	if r := h.authPlain(back.user, back.pass); replyCode(r) != "235" {
		t.Fatalf("AUTH = %q", r)
	}
	if r := h.cmd("MAIL FROM:<alice@example.com>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	address := srsAddress(t, "fake-srs-secret-another-for-tests", srsTestSender, time.Now())
	if r := h.cmd("RCPT TO:<%s>", address); replyCode(r) != "250" {
		t.Fatalf("RCPT = %q", r)
	}
	if r := h.dataBody(testBody); replyCode(r) != "250" {
		t.Fatalf("DATA = %q", r)
	}
	if n := len(store.relayedBounces()); n != 0 {
		t.Fatalf("relayed %d bounces from a submission, want none", n)
	}
	if queued := store.queued(); len(queued) != 1 || queued[0].Recipient != address {
		t.Fatalf("queued = %+v, want one to %s", queued, address)
	}
}

var errTestStore = errors.New("test: store unavailable")
