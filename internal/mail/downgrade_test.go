package mail

import (
	"mime"
	"strings"
	"testing"
)

func TestDowngrade_ASCIIHeaderUnchanged(t *testing.T) {
	raw := "From: a@example.com\r\nSubject: hi\r\n\r\nbody with ü stays\r\n"
	if got := DowngradeHeaders(raw); got != raw {
		t.Fatalf("ASCII header changed:\n%q", got)
	}
}

func TestDowngrade_HeaderBecomesASCII(t *testing.T) {
	raw := "Received: from mx.example.com\r\n" +
		"From: Jörg <joerg@bücher.example>\r\n" +
		"To: 用户@例え.jp, bob@example.com\r\n" +
		"Subject: Grüße\r\n" +
		" aus München\r\n" +
		"Message-ID: <ü@example.com>\r\n" +
		"Content-Type: text/plain; charset=utf-8; name=\"Grüße.txt\"\r\n" +
		"\r\n" +
		"Hallo, Jörg.\r\n"
	got := DowngradeHeaders(raw)
	header, body, ok := strings.Cut(got, "\r\n\r\n")
	if !ok {
		t.Fatalf("no header/body separator:\n%s", got)
	}
	if containsNonASCII(header) {
		t.Fatalf("header still carries non-ASCII:\n%s", header)
	}
	if body != "Hallo, Jörg.\r\n" {
		t.Fatalf("body changed: %q", body)
	}

	dec := new(mime.WordDecoder)
	fields := map[string]string{}
	for _, f := range headerFields(header) {
		name, value, _ := strings.Cut(f, ":")
		fields[strings.ToLower(name)] = strings.TrimSpace(value)
	}
	if fields["received"] != "from mx.example.com" {
		t.Errorf("ASCII field changed: %q", fields["received"])
	}
	if want := "=?utf-8?q?J=C3=B6rg?= <joerg@xn--bcher-kva.example>"; fields["from"] != want {
		t.Errorf("From = %q, want %q", fields["from"], want)
	}
	to := fields["to"]
	if !strings.HasSuffix(to, " :;, <bob@example.com>") {
		t.Errorf("To = %q, want a UTF-8 local part as an empty group then bob", to)
	}
	if name, err := dec.DecodeHeader(strings.TrimSuffix(to, " :;, <bob@example.com>")); err != nil || name != "用户@例え.jp" {
		t.Errorf("group name decodes to %q (%v)", name, err)
	}
	if subject, err := dec.DecodeHeader(fields["subject"]); err != nil || subject != "Grüße aus München" {
		t.Errorf("Subject decodes to %q (%v)", subject, err)
	}
	if _, ok := fields["message-id"]; ok {
		t.Error("non-ASCII Message-ID kept its name")
	}
	if id, err := dec.DecodeHeader(fields["downgraded-message-id"]); err != nil || id != "<ü@example.com>" {
		t.Errorf("Downgraded-Message-ID decodes to %q (%v)", id, err)
	}
	if mediaType, params, err := mime.ParseMediaType(fields["content-type"]); err != nil ||
		mediaType != "text/plain" || params["name"] != "Grüße.txt" || params["charset"] != "utf-8" {
		t.Errorf("Content-Type = %q parses to %q %v (%v)", fields["content-type"], mediaType, params, err)
	}
}

// A POP3 client gets the same downgrade as IMAP, so it keeps the same parameters.
func TestDowngrade_KeepsParameterInOtherCharset(t *testing.T) {
	raw := "Content-Type: application/x-y (Jörg); name*=windows-1252''x%F6.bin; note=\"Grü\"\r\n\r\nbody\r\n"
	want := "Content-Type: application/x-y (=?UTF-8?Q?J=C3=B6rg?=);\r\n name*=windows-1252''x%F6.bin;\r\n note*=UTF-8''Gr%C3%BC\r\n\r\nbody\r\n"
	if got := DowngradeHeaders(raw); got != want {
		t.Fatalf("DowngradeHeaders = %q\n  want %q", got, want)
	}
}

// A line that is not a field can't stay 8-bit, and its octets still reach the client.
func TestDowngrade_KeepsLineThatIsNoField(t *testing.T) {
	got := DowngradeHeaders("Subject: s\r\nGrüße ohne Doppelpunkt\r\n\r\nbody\r\n")
	want := "Subject: s\r\n=?UTF-8?Q?Gr=C3=BC=C3=9Fe_ohne_Doppelpunkt?=\r\n\r\nbody\r\n"
	if got != want {
		t.Fatalf("DowngradeHeaders = %q\n  want %q", got, want)
	}
}

func TestDowngrade_UnparseableAddressFieldRenamed(t *testing.T) {
	got := DowngradeHeaders("To: ü@@broken\r\n\r\nx")
	if !strings.HasPrefix(got, "Downgraded-To: =?utf-8?") || containsNonASCII(got) {
		t.Fatalf("got %q", got)
	}
}

// headerFields is the fields of a header block, each still folded.
func headerFields(header string) []string {
	var fields []string
	eachHeaderField(header, func(span string, bare bool) {
		var b strings.Builder
		writeFolded(&b, span, bare)
		fields = append(fields, b.String())
	})
	return fields
}
