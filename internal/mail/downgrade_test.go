package mail

import (
	"mime"
	netmail "net/mail"
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
	if want := "=?UTF-8?Q?J=C3=B6rg?= <joerg@xn--bcher-kva.example>"; fields["from"] != want {
		t.Errorf("From = %q, want %q", fields["from"], want)
	}
	to := fields["to"]
	if !strings.HasSuffix(to, " :;, bob@example.com") {
		t.Errorf("To = %q, want a UTF-8 local part as an empty group then bob", to)
	}
	if name, err := dec.DecodeHeader(strings.TrimSuffix(to, " :;, bob@example.com")); err != nil || name != "用户@例え.jp" {
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
	for _, value := range []string{"ü@@broken", "Jörg", "Jörg <j@example.com", "Grüppe: a@example.com", "(Jörg a@example.com", "a@example.com (Jörg", "\"Jörg <j@example.com", "Jörg <", "(ü", "ü,", "ü)", ")ü@example.com"} {
		got := DowngradeHeaders("To: " + value + "\r\n\r\nx")
		if !strings.HasPrefix(got, "Downgraded-To: =?") || containsNonASCII(got) {
			t.Fatalf("To: %s became %q", value, got)
		}
	}
}

// Converting an address field changes only what holds an 8-bit octet: a group name, a
// comment and every 7-bit address stay (RFC 6857 sections 3.1.3 and 3.2.1).
func TestDowngrade_AddressFieldKeepsWhatItCan(t *testing.T) {
	for _, tc := range []struct{ name, stored, want string }{
		{"group name", "To: Grüppe: a@example.com, b@example.com;",
			"To: =?UTF-8?Q?Gr=C3=BCppe?= : a@example.com, b@example.com;"},
		{"empty group", "To: Grüppe:;", "To: =?UTF-8?Q?Gr=C3=BCppe?= :;"},
		{"comment", "From: Jörg <j@example.com> (Büro)",
			"From: =?UTF-8?Q?J=C3=B6rg?= <j@example.com> (=?UTF-8?Q?B=C3=BCro?=)"},
		{"7-bit comment stays", "Cc: Ann <a@example.com> (kept), Jörg <j@example.com>",
			"Cc: Ann <a@example.com> (kept), =?UTF-8?Q?J=C3=B6rg?= <j@example.com>"},
		{"comment on an addr-spec", "To: a@example.com (Büro)", "To: a@example.com (=?UTF-8?Q?B=C3=BCro?=)"},
		{"comment before the name", "To: (Büro) Jörg <j@example.com>",
			"To: (=?UTF-8?Q?B=C3=BCro?=) =?UTF-8?Q?J=C3=B6rg?= <j@example.com>"},
		{"nested comment", "To: a@example.com (x (Bü) y)", "To: a@example.com (=?UTF-8?Q?x_=28B=C3=BC=29_y?=)"},
		{"7-bit spelling stays", "To: \"Ann, Lee\"  <A.Lee@EXAMPLE.com>,b@example.com, Jörg <j@example.com>",
			"To: \"Ann, Lee\"  <A.Lee@EXAMPLE.com>,b@example.com, =?UTF-8?Q?J=C3=B6rg?= <j@example.com>"},
		{"quoted name", "To: \"Jörg \\\"J\\\" M.\" <j@example.com>", "To: =?UTF-8?Q?J=C3=B6rg_=22J=22_M=2E?= <j@example.com>"},
		{"words become one run", "To: Hans Jörg Müller Lee <j@example.com>",
			"To: Hans =?UTF-8?Q?J=C3=B6rg_M=C3=BCller?= Lee <j@example.com>"},
		{"encoded word stays and keeps its space", "To: =?UTF-8?Q?Ann?= Jörg =?UTF-8?Q?Lee?= <j@example.com>",
			"To: =?UTF-8?Q?Ann?= =?UTF-8?Q?_J=C3=B6rg_?= =?UTF-8?Q?Lee?= <j@example.com>"},
		{"no space around a word", "To: a@example.com,Jörg<j@example.com>",
			"To: a@example.com, =?UTF-8?Q?J=C3=B6rg?= <j@example.com>"},
		{"not UTF-8", "From: J\xf6rg <j@example.com> (caf\xe9)",
			"From: =?UNKNOWN-8BIT?Q?J=F6rg?= <j@example.com> (=?UNKNOWN-8BIT?Q?caf=E9?=)"},
		{"8-bit domain", "To: J <j@bücher.example> (x), k@bücher.example (y), l@example.com",
			"To: J <j@xn--bcher-kva.example> (x), k@xn--bcher-kva.example (y), l@example.com"},
		{"8-bit name in a group", "To: Grüppe: Jörg <j@example.com>, a@example.com;",
			"To: =?UTF-8?Q?Gr=C3=BCppe?= : =?UTF-8?Q?J=C3=B6rg?= <j@example.com>, a@example.com;"},
		{"comment inside an angle-addr", "To: <a(Bü)@example.com>, b@example.com (x), <ü(Bü)@example.com>",
			"To: <a(=?UTF-8?Q?B=C3=BC?=)@example.com>, b@example.com (x), \r\n =?UTF-8?Q?=C3=BC=40example=2Ecom?= :; (=?UTF-8?Q?B=C3=BC?=)"},
		{"domain literal", "To: Jörg <j@[1:2::3]>", "To: =?UTF-8?Q?J=C3=B6rg?= <j@[1:2::3]>"},
		{"no-break space stays text", "To: \"Jörg\u00a0\" <j@example.com>", "To: =?UTF-8?Q?J=C3=B6rg=C2=A0?= <j@example.com>"},
		{"8-bit domain in a group", "To: Grüppe: j@bücher.example, Lee <l@example.com>;",
			"To: =?UTF-8?Q?Gr=C3=BCppe?= : j@xn--bcher-kva.example, Lee <l@example.com>;"},
		{"domain with no A-label", "To: j@b\xfccher.example", "To: =?UNKNOWN-8BIT?Q?j=40b=FCcher=2Eexample?= :;"},
		{"8-bit local part", "To: üser@example.com (Büro), b@example.com",
			"To: =?UTF-8?Q?=C3=BCser=40example=2Ecom?= :; (=?UTF-8?Q?B=C3=BCro?=), b@example.com"},
		{"8-bit local part keeps a 7-bit name", "To: Ann <üser@example.com> (kept)",
			"To: Ann =?UTF-8?Q?=C3=BCser=40example=2Ecom?= :; (kept)"},
		{"8-bit local part beside an 8-bit name", "To: Jörg <üser@example.com>",
			"To: =?UTF-8?Q?J=C3=B6rg?= =?UTF-8?Q?_=C3=BCser=40example=2Ecom?= :;"},
		{"8-bit local part in a group", "To: Team: üser@example.com, b@example.com; (kept), c@example.com",
			"To: Team =?UTF-8?Q?=C3=BCser=40example=2Ecom=2C_b=40example=2Ecom?= :; (kept), c@example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.want + "\r\n\r\nbody ü\r\n"
			if got := DowngradeHeaders(tc.stored + "\r\n\r\nbody ü\r\n"); got != want {
				t.Fatalf("DowngradeHeaders(%q)\n got %q\nwant %q", tc.stored, got, want)
			}
		})
	}
}

// A folded field keeps its line breaks, so one 8-bit name in a long list can't push the
// line past the limit of RFC 5322 section 2.1.1, and an encoded word starts a new line
// where it would not fit within 76 characters (RFC 2047 section 2).
func TestDowngrade_AddressFieldKeepsItsFolding(t *testing.T) {
	const stored = "To: Ann <a@example.com>,\r\n\tJörg <j@example.com>,\r\n Lee <l@example.com>\r\n\r\nx"
	const want = "To: Ann <a@example.com>,\r\n\t=?UTF-8?Q?J=C3=B6rg?= <j@example.com>,\r\n Lee <l@example.com>\r\n\r\nx"
	if got := DowngradeHeaders(stored); got != want {
		t.Fatalf("DowngradeHeaders\n got %q\nwant %q", got, want)
	}
	const long = "To: long-address-one@example.com, long-address-two@example.com, Jörg <j@example.com>\r\n\r\nx"
	got := DowngradeHeaders(long)
	if !strings.Contains(got, "long-address-two@example.com, \r\n =?UTF-8?Q?J=C3=B6rg?= <j@example.com>") {
		t.Fatalf("no fold before the word: %q", got)
	}
	for _, line := range strings.Split(got, "\r\n") {
		if strings.Contains(line, "=?") && len(line) > 76 {
			t.Fatalf("a line with an encoded word is %d long: %q", len(line), line)
		}
	}
}

// A converted field still reads as the same addresses, group by group.
func TestDowngrade_AddressFieldStillParses(t *testing.T) {
	got := DowngradeHeaders("To: Jörg <j@bücher.example> (Büro), Grüppe: a@example.com, Lee <l@example.com> (x);, üser@example.com\r\n\r\nx")
	header, _, _ := strings.Cut(got, "\r\n\r\n")
	list, err := netmail.ParseAddressList(unfold(strings.TrimPrefix(header, "To: ")))
	if err != nil {
		t.Fatalf("%q: %v", header, err)
	}
	var addresses []string
	for _, a := range list {
		addresses = append(addresses, a.Address)
	}
	if want := "j@xn--bcher-kva.example a@example.com l@example.com"; strings.Join(addresses, " ") != want {
		t.Fatalf("%q reads as %v, want %s", header, addresses, want)
	}
}

// downgraded_size counts the converted field, which is what POP3 sends.
func TestDowngradedSize_CountsTheConvertedAddressField(t *testing.T) {
	const raw = "To: Grüppe: a@example.com, b@example.com;\r\n\r\nx"
	if got, want := DowngradedSize(raw), len("To: =?UTF-8?Q?Gr=C3=BCppe?= : a@example.com, b@example.com;\r\n\r\nx"); got != want {
		t.Fatalf("DowngradedSize = %d, want %d", got, want)
	}
}

// DowngradeForPOP3 respells a top-level 8-bit boundary, and DowngradedSize measures that
// form. DowngradeHeaders, which the IMAP engine reads, leaves the body to the engine, so
// what IMAP sends and reports doesn't change.
func TestDowngradeForPOP3_RespellsAnEightBitBoundary(t *testing.T) {
	const raw = "Subject: Grüße\r\nContent-Type: multipart/mixed; boundary=\"Jö\"\r\n\r\n--Jö\r\n\r\nHö\r\n--Jö--\r\n"
	const want = "Subject: =?utf-8?q?Gr=C3=BC=C3=9Fe?=\r\nContent-Type: multipart/mixed;\r\n boundary=\"J=C3=B6\"\r\n\r\n" +
		"--J=C3=B6\r\n\r\nHö\r\n--J=C3=B6--\r\n"
	got := DowngradeForPOP3(raw)
	if got != want {
		t.Fatalf("DowngradeForPOP3\n got %q\nwant %q", got, want)
	}
	if size := DowngradedSize(raw); size != len(want) {
		t.Fatalf("DowngradedSize = %d, want %d", size, len(want))
	}
	const imap = "Subject: =?utf-8?q?Gr=C3=BC=C3=9Fe?=\r\nContent-Type: multipart/mixed;\r\n boundary*=UTF-8''J%C3%B6\r\n\r\n" +
		"--Jö\r\n\r\nHö\r\n--Jö--\r\n"
	if got := DowngradeHeaders(raw); got != imap {
		t.Fatalf("DowngradeHeaders\n got %q\nwant %q", got, imap)
	}
}

// A message whose header block is ASCII goes out byte for byte, whatever its body holds.
func TestDowngradeForPOP3_LeavesAnASCIIHeaderAlone(t *testing.T) {
	const raw = "Content-Type: multipart/mixed; boundary*=UTF-8''J%C3%B6\n\n--Jö\n\nHö\n--Jö--\n"
	if got := DowngradeForPOP3(raw); got != raw {
		t.Fatalf("DowngradeForPOP3 = %q, want it as stored", got)
	}
}

// A malformed Content-Type stays a Content-Type through the downgrade with its text,
// rather than being renamed. An 8-bit boundary still decodes to what the delimiter lines
// hold, which is how the IMAP engine finds them; POP3 goes on to DowngradeForPOP3.
func TestDowngrade_MalformedParameterKeepsItsField(t *testing.T) {
	for _, tc := range []struct{ stored, want string }{
		{`Content-Type: application/pdf; name=Jörg (1).pdf`, "Content-Type: application/pdf;\r\n name*=UTF-8''J%C3%B6rg%20%281%29.pdf"},
		{`Content-Type: application/pdf; name=Jö"rg.pdf`, "Content-Type: application/pdf;\r\n name*=UTF-8''J%C3%B6%22rg.pdf"},
		{`Content-Type: application/pdf; näme=x`, "Content-Type: application/pdf;\r\n n%C3%A4me=x"},
		{`Content-Disposition: attachment; filename=Jö(rg.pdf`, "Content-Disposition: attachment;\r\n filename*=UTF-8''J%C3%B6%28rg.pdf"},
		{`Content-Type: multipart/mixed; boundary="Jö"`, "Content-Type: multipart/mixed;\r\n boundary*=UTF-8''J%C3%B6"},
	} {
		got := DowngradeHeaders(tc.stored + "\r\n\r\n--J\xc3\xb6\r\n")
		if want := tc.want + "\r\n\r\n--J\xc3\xb6\r\n"; got != want {
			t.Fatalf("%q:\n got %q\nwant %q", tc.stored, got, want)
		}
	}
}
