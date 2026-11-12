package smtp

import (
	"errors"
	"testing"
)

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
	// testBody's From: header is sender@remote.test — an authorized linked
	// address of the account, so the #181 From-header check accepts it.
	store.authorized["sender@remote.test"] = true

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

// TestSMTP_DSNProvenance_LocalOnlyPersists confirms a submission whose every
// recipient is local still saves its one Sent copy (#1809) and links the queue
// row to it, since local recipients go through the queue too (#1564).
func TestSMTP_DSNProvenance_LocalOnlyPersists(t *testing.T) {
	back := newMockBackend()
	back.local["bob@local.test"] = true
	store := newMockStore()
	// testBody's From: header (sender@remote.test) is an authorized linked
	// address, so the #181 From-header check accepts the submission.
	store.authorized["sender@remote.test"] = true

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

	// A local recipient goes through the queue too (#1564), so a later refusal
	// has a sent message to bounce against.
	if n := len(store.persistedSubmissions()); n != 1 {
		t.Errorf("persisted submissions = %d, want 1", n)
	}
	queued := store.queued()
	if len(queued) != 1 {
		t.Fatalf("enqueued = %d, want 1", len(queued))
	}
	if queued[0].MessageID == nil {
		t.Error("queue row names no submitted message, so its bounce has no provenance")
	}
}

// TestSMTP_SubmissionSavesOneSentCopyWhateverTheRecipients: every accepted
// authenticated submission saves its Sent copy exactly once, whether its
// recipients are local, remote or both, in either order (#1809), and every
// queue row of a submission names that one copy. Two submissions on one
// connection save one copy each.
func TestSMTP_SubmissionSavesOneSentCopyWhateverTheRecipients(t *testing.T) {
	for _, tc := range []struct {
		name       string
		recipients []string
	}{
		{"local only", []string{"bob@local.test", "dave@local.test"}},
		{"remote only", []string{"carol@remote.test", "erin@remote.test"}},
		{"local then remote", []string{"bob@local.test", "carol@remote.test"}},
		{"remote then local", []string{"carol@remote.test", "bob@local.test"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			back := newMockBackend()
			back.local["bob@local.test"] = true
			back.local["dave@local.test"] = true
			store := newMockStore()
			store.authorized["sender@remote.test"] = true

			h := newSMTPHarness(t, back, store, true)
			h.ehlo()
			if r := h.authPlain(back.user, back.pass); replyCode(r) != "235" {
				t.Fatalf("AUTH = %q", r)
			}
			for submission := 1; submission <= 2; submission++ {
				if r := h.cmd("MAIL FROM:<alice@example.com>"); replyCode(r) != "250" {
					t.Fatalf("submission %d: MAIL FROM = %q", submission, r)
				}
				for _, rcpt := range tc.recipients {
					if r := h.cmd("RCPT TO:<%s>", rcpt); replyCode(r) != "250" {
						t.Fatalf("submission %d: RCPT %s = %q", submission, rcpt, r)
					}
				}
				if final := h.dataBody(testBody); replyCode(final) != "250" {
					t.Fatalf("submission %d: DATA = %q", submission, final)
				}
				if n := len(store.persistedSubmissions()); n != submission {
					t.Fatalf("after submission %d: persisted %d Sent copies, want %d", submission, n, submission)
				}
				id := store.lastPersistedID()
				queued := store.queued()
				if len(queued) != submission*len(tc.recipients) {
					t.Fatalf("after submission %d: queued %d rows, want %d", submission, len(queued), submission*len(tc.recipients))
				}
				for i, row := range queued[(submission-1)*len(tc.recipients):] {
					if row.Recipient != tc.recipients[i] {
						t.Errorf("submission %d row %d recipient = %q, want %q", submission, i, row.Recipient, tc.recipients[i])
					}
					if row.MessageID == nil || *row.MessageID != id {
						t.Errorf("submission %d row for %s names message %v, want its Sent copy %d", submission, row.Recipient, row.MessageID, id)
					}
				}
			}
			if got := back.deliveredTo(); len(got) != 0 {
				t.Errorf("delivered directly to %v, want every recipient queued", got)
			}
		})
	}
}

// TestSMTP_SubmissionSentCopyFailureStillQueues: saving the Sent copy is best
// effort. When it fails the submission is still accepted and queued, with no
// linked message.
func TestSMTP_SubmissionSentCopyFailureStillQueues(t *testing.T) {
	back := newMockBackend()
	back.local["bob@local.test"] = true
	store := newMockStore()
	store.authorized["sender@remote.test"] = true
	store.persistErr = errors.New("database unavailable")

	h := newSMTPHarness(t, back, store, true)
	h.ehlo()
	if r := h.authPlain(back.user, back.pass); replyCode(r) != "235" {
		t.Fatalf("AUTH = %q", r)
	}
	h.cmd("MAIL FROM:<alice@example.com>")
	h.cmd("RCPT TO:<bob@local.test>")
	h.cmd("RCPT TO:<carol@remote.test>")
	if final := h.dataBody(testBody); replyCode(final) != "250" {
		t.Fatalf("DATA = %q", final)
	}
	queued := store.queued()
	if len(queued) != 2 {
		t.Fatalf("queued %d rows, want 2", len(queued))
	}
	for _, row := range queued {
		if row.MessageID != nil {
			t.Errorf("row for %s names message %d, want none after the Sent copy failed", row.Recipient, *row.MessageID)
		}
	}
}

// TestSMTP_SubmissionThatQueuesNothingIsRefused: when no recipient can be
// queued the submission is refused, so the client retries the whole message.
func TestSMTP_SubmissionThatQueuesNothingIsRefused(t *testing.T) {
	back := newMockBackend()
	back.local["bob@local.test"] = true
	store := newMockStore()
	store.authorized["sender@remote.test"] = true
	store.enqueueErr = errors.New("queue unavailable")

	h := newSMTPHarness(t, back, store, true)
	h.ehlo()
	if r := h.authPlain(back.user, back.pass); replyCode(r) != "235" {
		t.Fatalf("AUTH = %q", r)
	}
	h.cmd("MAIL FROM:<alice@example.com>")
	h.cmd("RCPT TO:<bob@local.test>")
	if final := h.dataBody(testBody); replyCode(final) != "451" {
		t.Fatalf("DATA = %q, want 451", final)
	}
	if got := back.deliveredTo(); len(got) != 0 {
		t.Errorf("delivered directly to %v, want nothing", got)
	}
}
