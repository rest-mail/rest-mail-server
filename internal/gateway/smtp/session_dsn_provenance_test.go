package smtp

import "testing"

// TestSMTP_DSNProvenance_RemoteSubmissionLinksMessage proves that an
// authenticated submission bound for a remote recipient persists a message
// reference owned by the sender and threads that reference id onto the outbound
// queue row (OutboundMessage.MessageID). Without this link, a later bounce/DSN
// for the message has no provenance and falls to the "no linked message" branch
// of the DSN sender-auth check.
func TestSMTP_DSNProvenance_RemoteSubmissionLinksMessage(t *testing.T) {
	back := newMockBackend()
	// carol@remote.test is not local, so it is enqueued for outbound delivery.
	store := newMockStore()

	h := newSMTPHarness(t, back, store, true) // submission
	h.ehlo()
	if r := h.authPlain(back.user, back.pass); replyCode(r) != "235" {
		t.Fatalf("AUTH = %q", r)
	}
	if r := h.cmd("MAIL FROM:<alice@example.com>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<carol@remote.test>"); replyCode(r) != "250" {
		t.Fatalf("RCPT = %q", r)
	}
	if final := h.dataBody(testBody); replyCode(final) != "250" {
		t.Fatalf("DATA = %q", final)
	}

	persisted := store.persistedSubmissions()
	if len(persisted) != 1 {
		t.Fatalf("persisted submissions = %d, want 1", len(persisted))
	}
	if persisted[0].Sender != "alice@example.com" {
		t.Errorf("persisted submission sender = %q, want alice@example.com", persisted[0].Sender)
	}

	queued := store.queued()
	if len(queued) != 1 {
		t.Fatalf("enqueued = %d, want 1", len(queued))
	}
	if queued[0].MessageID == nil {
		t.Fatalf("enqueued MessageID is nil; want the persisted reference id for DSN provenance")
	}
	if *queued[0].MessageID != store.lastPersistedID() {
		t.Errorf("enqueued MessageID = %d, want persisted id %d", *queued[0].MessageID, store.lastPersistedID())
	}
}

// TestSMTP_DSNProvenance_LocalOnlyDoesNotPersist confirms the reference is
// persisted only when there is outbound mail to bounce: a submission whose every
// recipient is local delivers via the API and enqueues nothing, so no reference
// is created.
func TestSMTP_DSNProvenance_LocalOnlyDoesNotPersist(t *testing.T) {
	back := newMockBackend()
	back.local["bob@local.test"] = true
	store := newMockStore()

	h := newSMTPHarness(t, back, store, true) // submission
	h.ehlo()
	if r := h.authPlain(back.user, back.pass); replyCode(r) != "235" {
		t.Fatalf("AUTH = %q", r)
	}
	if r := h.cmd("MAIL FROM:<alice@example.com>"); replyCode(r) != "250" {
		t.Fatalf("MAIL FROM = %q", r)
	}
	if r := h.cmd("RCPT TO:<bob@local.test>"); replyCode(r) != "250" {
		t.Fatalf("RCPT = %q", r)
	}
	if final := h.dataBody(testBody); replyCode(final) != "250" {
		t.Fatalf("DATA = %q", final)
	}

	if n := len(store.persistedSubmissions()); n != 0 {
		t.Errorf("persisted submissions = %d, want 0 (local-only submission has no outbound to bounce)", n)
	}
	if n := len(store.queued()); n != 0 {
		t.Errorf("enqueued = %d, want 0", n)
	}
}
