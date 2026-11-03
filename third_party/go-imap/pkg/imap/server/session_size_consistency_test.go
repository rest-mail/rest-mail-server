package server

import (
	"strconv"
	"strings"
	"testing"
)

// TestFetch_RFC822SizeSingleSourceOfTruth pins issue #35 item 6: RFC822.SIZE must
// come from one source — the stored Message.Size — regardless of whether the same
// FETCH also pulls the body. An earlier structure reported len(Fetch(...)) on the
// full-body path and Message.Size on the metadata path, so a client could see two
// different sizes for one message when the backend's Size differs from the fetched
// octet count. This test seeds a message whose stored Size (100) deliberately
// differs from its raw body length, then asserts RFC822.SIZE is 100 both on a
// metadata-only fetch and on a fetch that also returns BODY[].
func TestFetch_RFC822SizeSingleSourceOfTruth(t *testing.T) {
	b := newMockBackend()
	raw := "Subject: S\r\n\r\nbody that is not 100 octets long\r\n"
	if len(raw) == 100 {
		t.Fatalf("test setup: raw must differ from the stored Size of 100")
	}
	b.seed("INBOX", 1, 100, raw) // stored Size=100, body length != 100

	h := newIMAPHarness(t, b)
	h.login("a1")
	h.selectInbox("a2")

	// Metadata-only fetch.
	untagged, status := h.command("a3", "FETCH 1 RFC822.SIZE")
	if !strings.Contains(status, " OK") {
		t.Fatalf("FETCH RFC822.SIZE status = %q", status)
	}
	if !anyContains(untagged, "RFC822.SIZE 100") {
		t.Errorf("metadata FETCH = %v, want RFC822.SIZE 100 (the stored size)", untagged)
	}

	// Fetch that also pulls the body in the SAME response: the size must be
	// identical (100), never the fetched octet count.
	untagged, status = h.command("a4", "FETCH 1 (RFC822.SIZE BODY[])")
	if !strings.Contains(status, " OK") {
		t.Fatalf("FETCH (RFC822.SIZE BODY[]) status = %q", status)
	}
	if !anyContains(untagged, "RFC822.SIZE 100") {
		t.Errorf("body-inclusive FETCH = %v, want RFC822.SIZE 100 consistent with the metadata fetch", untagged)
	}
	if anyContains(untagged, "RFC822.SIZE "+strconv.Itoa(len(raw))) {
		t.Errorf("RFC822.SIZE leaked the fetched body length %d instead of the stored size 100", len(raw))
	}
}
