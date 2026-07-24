package mail

import (
	"strings"
	"testing"
	"time"
)

func TestBuildReceivedHeader_WellFormed(t *testing.T) {
	ts := time.Date(2026, time.July, 24, 12, 34, 56, 0, time.FixedZone("UTC", 0))
	got := BuildReceivedHeader(ReceivedInfo{
		From:      "mail.example.com",
		RemoteIP:  "192.0.2.1",
		By:        "mx.local.test",
		With:      "ESMTPS",
		ID:        "ABC123",
		For:       "alice@local.test",
		Timestamp: ts,
	})

	// It is a single Received header terminated by CRLF.
	if !strings.HasPrefix(got, "Received: ") {
		t.Fatalf("must start with %q; got %q", "Received: ", got)
	}
	if !strings.HasSuffix(got, "\r\n") {
		t.Errorf("must end with CRLF; got %q", got)
	}

	for _, want := range []string{
		"from mail.example.com ([192.0.2.1])",
		"by mx.local.test (RESTMAIL) with ESMTPS",
		"id ABC123",
		"for <alice@local.test>",
		"; Fri, 24 Jul 2026 12:34:56 +0000",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Received header missing %q; got:\n%s", want, got)
		}
	}

	// Continuation lines are folded with CRLF + whitespace (valid RFC 5322
	// unfolding), and the header contains no bare LF.
	if strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") {
		t.Errorf("header contains a bare LF: %q", got)
	}
}

func TestBuildReceivedHeader_OmitsForWhenEmpty(t *testing.T) {
	got := BuildReceivedHeader(ReceivedInfo{
		From: "c", RemoteIP: "192.0.2.9", By: "h", With: "ESMTP", ID: "X",
	})
	if strings.Contains(got, "for <") {
		t.Errorf("no recipient should yield no for-clause; got:\n%s", got)
	}
}

// A hostile HELO name or recipient carrying CRLF must never break out of the
// header line and become a forged header of its own. The CR/LF is stripped, so
// any residual injected text stays glued inline on the (folded) Received line
// rather than starting a new header line.
func TestBuildReceivedHeader_StripsHeaderInjection(t *testing.T) {
	got := BuildReceivedHeader(ReceivedInfo{
		From:     "evil\r\nX-Injected: yes",
		RemoteIP: "192.0.2.1",
		By:       "mx.local.test",
		With:     "ESMTP",
		ID:       "ID1",
		For:      "victim@local.test\r\nBcc: attacker@evil.test",
	})
	body := strings.TrimSuffix(got, "\r\n")
	for i, line := range strings.Split(body, "\r\n") {
		// Every physical line except the first is a continuation fold (starts
		// with WSP) — so nothing the attacker supplied begins a new header line.
		if i > 0 && (line == "" || (line[0] != ' ' && line[0] != '\t')) {
			t.Errorf("injected content started a new header line: %q", line)
		}
		// No raw control characters survive in the line content (the leading
		// fold WSP on continuation lines is legitimate and stripped first).
		content := line
		if i > 0 {
			content = strings.TrimLeft(line, " \t")
		}
		for _, r := range content {
			if r < 0x20 || r == 0x7f {
				t.Errorf("control character %q survived sanitization; got:\n%q", r, got)
			}
		}
	}
}

func TestGenerateQueueID_UniqueNonEmpty(t *testing.T) {
	a, b := GenerateQueueID(), GenerateQueueID()
	if a == "" || b == "" {
		t.Fatal("queue id must be non-empty")
	}
	if a == b {
		t.Errorf("queue ids should differ: %q == %q", a, b)
	}
}
