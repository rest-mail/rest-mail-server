package imap

import (
	"testing"
	"time"

	imapsrv "github.com/rest-mail/go-imap"
)

// These tests are the red-green regression for issue #190: IMAP COPY lost the
// source message's flags and INTERNALDATE, COPY/APPEND delivered to INBOX and then
// moved in a second, error-swallowing call (leaving transient/permanent INBOX
// state), and a quarantined/discarded delivery was reported to the client as a
// bogus success carrying UID 0.

func bptr(b bool) *bool { return &b }

// seedFlagged seeds a source message with explicit flags and internal date so a
// COPY of it can be checked for flag / INTERNALDATE preservation.
func (f *fakeAPI) seedFlagged(id uint, folder, raw, subject string, read, flagged, starred, draft bool, received time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs[id] = &fakeMsg{
		id: id, folder: folder, raw: raw, subject: subject,
		sender: "sender@example.com", senderNm: "Sender",
		isRead: read, isFlagged: flagged, isStarred: starred, isDraft: draft,
		receivedAt: received,
	}
}

// TestCopyUID_PreservesFlagsAndInternalDate proves a COPY carries the source
// message's flags and INTERNALDATE into the destination (RFC 3501 §6.4.7). On the
// pre-fix code the delivery request set neither, so the copy defaulted to unread /
// unflagged with a now() internal date.
func TestCopyUID_PreservesFlagsAndInternalDate(t *testing.T) {
	srcReceived := time.Date(2021, 6, 7, 8, 9, 10, 0, time.UTC)
	api := newFakeAPI()
	api.seedFlagged(1, "INBOX", "Subject: Src\r\n\r\nsrc\r\n", "Src",
		true /*read*/, true /*flagged*/, true /*starred*/, false /*draft*/, srcReceived)
	api.nextID = 900
	m := newUnitMailbox(t, api)

	uid, err := m.CopyUID(1, "Archive")
	if err != nil {
		t.Fatalf("CopyUID: %v", err)
	}
	if uid != 900 {
		t.Fatalf("CopyUID uid = %d, want 900", uid)
	}

	req := api.lastDeliver(t)
	if req.Folder != "Archive" {
		t.Errorf("copy delivered to folder %q, want Archive (must be created directly in the destination)", req.Folder)
	}
	if req.IsRead == nil || !*req.IsRead {
		t.Errorf("copy dropped the source \\Seen flag: IsRead = %v, want true", req.IsRead)
	}
	if req.IsFlagged == nil || !*req.IsFlagged {
		t.Errorf("copy dropped the source \\Flagged flag: IsFlagged = %v, want true", req.IsFlagged)
	}
	// The source's is_starred column is preserved verbatim (COPY duplicates the
	// stored flags, it does not re-derive them).
	if req.IsStarred == nil || !*req.IsStarred {
		t.Errorf("copy dropped the source star: IsStarred = %v, want true", req.IsStarred)
	}
	if req.ReceivedAt == nil || !req.ReceivedAt.Equal(srcReceived) {
		t.Errorf("copy dropped the source INTERNALDATE: ReceivedAt = %v, want %v", req.ReceivedAt, srcReceived)
	}
}

// TestCopyUID_LandsDirectlyInDestination proves the copy is created directly in
// the destination with NO separate folder-PATCH move. The pre-fix code delivered
// to INBOX and then PATCHed the folder in a second call — transient INBOX state
// visible to IDLE/SSE, and a stranded message if that move failed.
func TestCopyUID_LandsDirectlyInDestination(t *testing.T) {
	api := newFakeAPI()
	api.seed(1, "INBOX", "Subject: Src\r\n\r\nsrc\r\n", "Src")
	api.nextID = 901
	m := newUnitMailbox(t, api)

	uid, err := m.CopyUID(1, "Archive")
	if err != nil {
		t.Fatalf("CopyUID: %v", err)
	}
	if got := folderOf(api, uint(uid)); got != "Archive" {
		t.Errorf("copy created in %q, want Archive", got)
	}
	if patched := folderPatched(api, uint(uid)); patched != "" {
		t.Errorf("copy performed a separate folder move (PATCH folder=%q); it must be created directly in the destination", patched)
	}
}

// TestCopyUID_QuarantinedDeliveryReturnsError proves a delivery that stores no
// message (pipeline quarantine/discard: 200 with no data.id) is surfaced as an
// error, never reported as a success carrying UID 0. The pre-fix code returned
// (0, nil), so the client saw `OK [COPYUID … 0]` for a message that exists
// nowhere.
func TestCopyUID_QuarantinedDeliveryReturnsError(t *testing.T) {
	api := newFakeAPI()
	api.seed(1, "INBOX", "Subject: Src\r\n\r\nsrc\r\n", "Src")
	api.deliverStatus = 200
	api.deliverBody = map[string]interface{}{"data": map[string]interface{}{"status": "processed"}}
	m := newUnitMailbox(t, api)

	uid, err := m.CopyUID(1, "Archive")
	if err == nil {
		t.Fatalf("CopyUID returned nil error for a quarantined/discarded delivery; want an error")
	}
	if uid != 0 {
		t.Fatalf("CopyUID uid = %d on failure, want 0", uid)
	}
}

// TestAppendUID_AppliesFlagsAtCreation proves APPEND applies the client-supplied
// flags at creation time in the destination folder, with no separate move/flag
// PATCH. The pre-fix code delivered to INBOX then issued extra PATCH calls whose
// errors were swallowed.
func TestAppendUID_AppliesFlagsAtCreation(t *testing.T) {
	api := newFakeAPI()
	api.nextID = 910
	m := newUnitMailbox(t, api)

	uid, err := m.AppendUID("Archive", imapsrv.FlagUpdate{Seen: bptr(true), Flagged: bptr(true)},
		[]byte("Subject: Hi\r\n\r\nbody\r\n"))
	if err != nil {
		t.Fatalf("AppendUID: %v", err)
	}
	if uid != 910 {
		t.Fatalf("AppendUID uid = %d, want 910", uid)
	}

	req := api.lastDeliver(t)
	if req.Folder != "Archive" {
		t.Errorf("append delivered to folder %q, want Archive", req.Folder)
	}
	if req.IsRead == nil || !*req.IsRead {
		t.Errorf("append dropped the \\Seen flag: IsRead = %v, want true", req.IsRead)
	}
	if req.IsFlagged == nil || !*req.IsFlagged {
		t.Errorf("append dropped the \\Flagged flag: IsFlagged = %v, want true", req.IsFlagged)
	}
	if req.IsStarred == nil || !*req.IsStarred {
		t.Errorf("append dropped the mirrored star: IsStarred = %v, want true", req.IsStarred)
	}
	if patched := folderPatched(api, uint(uid)); patched != "" {
		t.Errorf("append performed a separate folder move (PATCH folder=%q); it must be created directly in the destination", patched)
	}
}

// TestAppendUID_QuarantinedDeliveryReturnsError is the APPEND counterpart of the
// UID-0 regression: a stored-nowhere delivery must be an error, not `OK
// [APPENDUID 1 0]`.
func TestAppendUID_QuarantinedDeliveryReturnsError(t *testing.T) {
	api := newFakeAPI()
	api.deliverStatus = 200
	api.deliverBody = map[string]interface{}{"data": map[string]interface{}{"status": "processed"}}
	m := newUnitMailbox(t, api)

	uid, err := m.AppendUID("INBOX", imapsrv.FlagUpdate{}, []byte("Subject: Hi\r\n\r\nbody\r\n"))
	if err == nil {
		t.Fatalf("AppendUID returned nil error for a quarantined/discarded delivery; want an error")
	}
	if uid != 0 {
		t.Fatalf("AppendUID uid = %d on failure, want 0", uid)
	}
}
