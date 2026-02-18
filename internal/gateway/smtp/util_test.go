package smtp

import (
	"testing"
)

// ---------- parseCommand ----------

func TestParseCommand_Basic(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		wantCmd string
		wantArg string
	}{
		{"EHLO with arg", "EHLO example.com", "EHLO", "example.com"},
		{"MAIL FROM", "MAIL FROM:<user@example.com>", "MAIL", "FROM:<user@example.com>"},
		{"RCPT TO", "RCPT TO:<bob@example.com>", "RCPT", "TO:<bob@example.com>"},
		{"QUIT no arg", "QUIT", "QUIT", ""},
		{"DATA no arg", "DATA", "DATA", ""},
		{"lowercase command", "ehlo example.com", "EHLO", "example.com"},
		{"mixed case", "Ehlo Example.COM", "EHLO", "Example.COM"},
		{"multiple spaces in arg", "MAIL FROM:<a@b.com> SIZE=1024", "MAIL", "FROM:<a@b.com> SIZE=1024"},
		{"empty string", "", "", ""},
		{"single space", " ", "", ""},
		{"command with trailing space", "NOOP ", "NOOP", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, arg := parseCommand(tt.line)
			if cmd != tt.wantCmd {
				t.Errorf("parseCommand(%q) cmd = %q, want %q", tt.line, cmd, tt.wantCmd)
			}
			if arg != tt.wantArg {
				t.Errorf("parseCommand(%q) arg = %q, want %q", tt.line, arg, tt.wantArg)
			}
		})
	}
}

// ---------- extractAddress ----------

func TestExtractAddress(t *testing.T) {
	tests := []struct {
		name   string
		arg    string
		prefix string
		want   string
	}{
		{"MAIL FROM with brackets", "FROM:<alice@example.com>", "FROM", "alice@example.com"},
		{"RCPT TO with brackets", "TO:<bob@example.com>", "TO", "bob@example.com"},
		{"case insensitive prefix", "from:<alice@example.com>", "FROM", "alice@example.com"},
		{"mixed case arg", "From:<Alice@Example.COM>", "FROM", "Alice@Example.COM"},
		{"with extra params", "FROM:<alice@example.com> SIZE=1024", "FROM", "alice@example.com"},
		{"no brackets", "FROM:alice@example.com", "FROM", "alice@example.com"},
		{"no brackets with space", "FROM: alice@example.com SIZE=1024", "FROM", "alice@example.com"},
		{"empty address", "FROM:<>", "FROM", ""},
		{"missing closing bracket", "FROM:<alice@example.com", "FROM", ""},
		{"wrong prefix", "FROM:<alice@example.com>", "TO", ""},
		{"empty arg", "", "FROM", ""},
		{"space before bracket", "FROM: <alice@example.com>", "FROM", "alice@example.com"},
		{"null sender", "FROM:<> SIZE=0", "FROM", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractAddress(tt.arg, tt.prefix)
			if got != tt.want {
				t.Errorf("extractAddress(%q, %q) = %q, want %q", tt.arg, tt.prefix, got, tt.want)
			}
		})
	}
}

// ---------- extractIP ----------

func TestExtractIP(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string
	}{
		{"IPv4 with port", "192.168.1.1:25", "192.168.1.1"},
		{"IPv4 no port", "192.168.1.1", "192.168.1.1"},
		{"IPv6 with port", "[::1]:25", "::1"},
		{"IPv6 no port", "[::1]", "::1"},
		{"IPv6 full with port", "[2001:db8::1]:587", "2001:db8::1"},
		{"empty string", "", ""},
		{"localhost with port", "127.0.0.1:2525", "127.0.0.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractIP(tt.addr)
			if got != tt.want {
				t.Errorf("extractIP(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}

// ---------- splitHostPort ----------

func TestSplitHostPort(t *testing.T) {
	tests := []struct {
		name     string
		addr     string
		wantHost string
		wantPort string
	}{
		{"IPv4 with port", "192.168.1.1:25", "192.168.1.1", "25"},
		{"IPv4 no port", "192.168.1.1", "192.168.1.1", ""},
		{"IPv6 with port", "[::1]:25", "::1", "25"},
		{"IPv6 no port brackets", "[::1]", "::1", ""},
		{"IPv6 full with port", "[2001:db8::1]:587", "2001:db8::1", "587"},
		{"empty string", "", "", ""},
		{"hostname with port", "mail.example.com:25", "mail.example.com", "25"},
		{"hostname no port", "mail.example.com", "mail.example.com", ""},
		{"IPv6 missing closing bracket", "[::1", "[::1", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port, _ := splitHostPort(tt.addr)
			if host != tt.wantHost {
				t.Errorf("splitHostPort(%q) host = %q, want %q", tt.addr, host, tt.wantHost)
			}
			if port != tt.wantPort {
				t.Errorf("splitHostPort(%q) port = %q, want %q", tt.addr, port, tt.wantPort)
			}
		})
	}
}

// ---------- decodeBase64 ----------

func TestDecodeBase64(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"valid", "SGVsbG8gV29ybGQ=", "Hello World", false},
		{"empty", "", "", false},
		{"padded", "YQ==", "a", false},
		{"invalid chars", "not!valid!base64", "", true},
		{"plain ascii", "dGVzdA==", "test", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeBase64(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("decodeBase64(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && string(got) != tt.want {
				t.Errorf("decodeBase64(%q) = %q, want %q", tt.input, string(got), tt.want)
			}
		})
	}
}

// ---------- unhex ----------

func TestUnhex(t *testing.T) {
	tests := []struct {
		name string
		c    byte
		want int
	}{
		{"digit 0", '0', 0},
		{"digit 5", '5', 5},
		{"digit 9", '9', 9},
		{"upper A", 'A', 10},
		{"upper F", 'F', 15},
		{"upper C", 'C', 12},
		{"lower a", 'a', 10},
		{"lower f", 'f', 15},
		{"lower c", 'c', 12},
		{"invalid G", 'G', -1},
		{"invalid space", ' ', -1},
		{"invalid at", '@', -1},
		{"invalid slash", '/', -1},
		{"invalid colon", ':', -1},
		{"invalid g", 'g', -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unhex(tt.c)
			if got != tt.want {
				t.Errorf("unhex(%q) = %d, want %d", tt.c, got, tt.want)
			}
		})
	}
}

// ---------- decodeQPLine ----------

func TestDecodeQPLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{"plain text", "Hello World", "Hello World"},
		{"encoded equals", "price =3D 100", "price = 100"},
		{"encoded space", "foo=20bar", "foo bar"},
		{"multiple encoded", "=48=65=6C=6C=6F", "Hello"},
		{"lowercase hex", "=48=65=6c=6c=6f", "Hello"},
		{"empty", "", ""},
		{"trailing equals no hex", "abc=", "abc="},
		{"incomplete hex at end", "abc=4", "abc=4"},
		{"encoded non-ascii", "=C3=A9", "\xc3\xa9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decodeQPLine(tt.line)
			if got != tt.want {
				t.Errorf("decodeQPLine(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}

// ---------- decodeQuotedPrintable ----------

func TestDecodeQuotedPrintable(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want string
	}{
		{
			"simple text",
			"Hello World",
			"Hello World",
		},
		{
			"soft line break",
			"Hello =\nWorld",
			"Hello World",
		},
		{
			"hard line break",
			"Hello\nWorld",
			"Hello\nWorld",
		},
		{
			"encoded chars",
			"price =3D 100",
			"price = 100",
		},
		{
			"soft break with encoded",
			"Hello =3D=\n World",
			"Hello = World",
		},
		{
			"multiple lines",
			"Line1\nLine2\nLine3",
			"Line1\nLine2\nLine3",
		},
		{
			"CRLF handling",
			"Hello\r\nWorld",
			"Hello\nWorld",
		},
		{
			"empty string",
			"",
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decodeQuotedPrintable(tt.s)
			if got != tt.want {
				t.Errorf("decodeQuotedPrintable(%q) = %q, want %q", tt.s, got, tt.want)
			}
		})
	}
}

// ---------- extractEmailFromHeader ----------

func TestExtractEmailFromHeader(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want string
	}{
		{"name and angle brackets", "Alice Smith <alice@example.com>", "alice@example.com"},
		{"angle brackets only", "<bob@example.com>", "bob@example.com"},
		{"bare email", "user@example.com", "user@example.com"},
		{"no email at all", "just a name", ""},
		{"empty string", "", ""},
		{"quoted name with brackets", "\"Smith, Alice\" <alice@example.com>", "alice@example.com"},
		{"missing closing bracket", "Alice <alice@example.com", "Alice <alice@example.com"},
		{"no at sign no brackets", "localhost", ""},
		{"spaces around bare email", "  user@example.com  ", "user@example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractEmailFromHeader(tt.s)
			if got != tt.want {
				t.Errorf("extractEmailFromHeader(%q) = %q, want %q", tt.s, got, tt.want)
			}
		})
	}
}

// ---------- parseRawMessage ----------

func TestParseRawMessage_SimpleText(t *testing.T) {
	raw := "Subject: Hello\r\nFrom: Alice <alice@example.com>\r\nTo: bob@example.com\r\nMessage-ID: <msg1@example.com>\r\n\r\nThis is the body."
	subject, bodyText, bodyHTML, messageID, senderName, _, _, toList, _ := parseRawMessage([]byte(raw))
	if subject != "Hello" {
		t.Errorf("subject = %q, want %q", subject, "Hello")
	}
	if bodyText != "This is the body." {
		t.Errorf("bodyText = %q, want %q", bodyText, "This is the body.")
	}
	if bodyHTML != "" {
		t.Errorf("bodyHTML = %q, want empty", bodyHTML)
	}
	if messageID != "msg1@example.com" {
		t.Errorf("messageID = %q, want %q", messageID, "msg1@example.com")
	}
	if senderName != "Alice" {
		t.Errorf("senderName = %q, want %q", senderName, "Alice")
	}
	if len(toList) != 1 || toList[0] != "bob@example.com" {
		t.Errorf("toList = %v, want [bob@example.com]", toList)
	}
}

func TestParseRawMessage_HTMLContentType(t *testing.T) {
	raw := "Subject: HTML Test\r\nContent-Type: text/html\r\n\r\n<h1>Hello</h1>"
	_, bodyText, bodyHTML, _, _, _, _, _, _ := parseRawMessage([]byte(raw))
	if bodyHTML != "<h1>Hello</h1>" {
		t.Errorf("bodyHTML = %q, want %q", bodyHTML, "<h1>Hello</h1>")
	}
	if bodyText != "" {
		t.Errorf("bodyText = %q, want empty", bodyText)
	}
}

func TestParseRawMessage_LFOnly(t *testing.T) {
	raw := "Subject: LF Test\nFrom: test@example.com\n\nBody here."
	subject, bodyText, _, _, _, _, _, _, _ := parseRawMessage([]byte(raw))
	if subject != "LF Test" {
		t.Errorf("subject = %q, want %q", subject, "LF Test")
	}
	if bodyText != "Body here." {
		t.Errorf("bodyText = %q, want %q", bodyText, "Body here.")
	}
}

func TestParseRawMessage_FoldedHeaders(t *testing.T) {
	raw := "Subject: This is a very\r\n long subject line\r\nTo: alice@example.com\r\n\r\nBody."
	subject, _, _, _, _, _, _, toList, _ := parseRawMessage([]byte(raw))
	expected := "This is a very long subject line"
	if subject != expected {
		t.Errorf("subject = %q, want %q", subject, expected)
	}
	if len(toList) != 1 || toList[0] != "alice@example.com" {
		t.Errorf("toList = %v, want [alice@example.com]", toList)
	}
}

func TestParseRawMessage_MultipleToCc(t *testing.T) {
	raw := "To: Alice <alice@a.com>, Bob <bob@b.com>\r\nCc: Carol <carol@c.com>, dave@d.com\r\n\r\nBody."
	_, _, _, _, _, _, _, toList, ccList := parseRawMessage([]byte(raw))
	if len(toList) != 2 {
		t.Fatalf("toList len = %d, want 2", len(toList))
	}
	if toList[0] != "alice@a.com" || toList[1] != "bob@b.com" {
		t.Errorf("toList = %v, want [alice@a.com, bob@b.com]", toList)
	}
	if len(ccList) != 2 {
		t.Fatalf("ccList len = %d, want 2", len(ccList))
	}
	if ccList[0] != "carol@c.com" || ccList[1] != "dave@d.com" {
		t.Errorf("ccList = %v, want [carol@c.com, dave@d.com]", ccList)
	}
}

func TestParseRawMessage_InReplyToAndReferences(t *testing.T) {
	raw := "In-Reply-To: <orig@example.com>\r\nReferences: <orig@example.com> <reply@example.com>\r\n\r\nBody."
	_, _, _, _, _, inReplyTo, references, _, _ := parseRawMessage([]byte(raw))
	if inReplyTo != "orig@example.com" {
		t.Errorf("inReplyTo = %q, want %q", inReplyTo, "orig@example.com")
	}
	if references != "<orig@example.com> <reply@example.com>" {
		t.Errorf("references = %q, want %q", references, "<orig@example.com> <reply@example.com>")
	}
}

func TestParseRawMessage_NoBody(t *testing.T) {
	raw := "Subject: Headers Only"
	subject, bodyText, bodyHTML, _, _, _, _, _, _ := parseRawMessage([]byte(raw))
	if subject != "Headers Only" {
		t.Errorf("subject = %q, want %q", subject, "Headers Only")
	}
	if bodyText != "" {
		t.Errorf("bodyText = %q, want empty", bodyText)
	}
	if bodyHTML != "" {
		t.Errorf("bodyHTML = %q, want empty", bodyHTML)
	}
}

func TestParseRawMessage_QuotedSenderName(t *testing.T) {
	raw := "From: \"Smith, Alice\" <alice@example.com>\r\n\r\nBody."
	_, _, _, _, senderName, _, _, _, _ := parseRawMessage([]byte(raw))
	if senderName != "Smith, Alice" {
		t.Errorf("senderName = %q, want %q", senderName, "Smith, Alice")
	}
}

func TestParseRawMessage_EmptyInput(t *testing.T) {
	subject, bodyText, bodyHTML, messageID, senderName, inReplyTo, references, toList, ccList := parseRawMessage([]byte(""))
	if subject != "" || bodyText != "" || bodyHTML != "" || messageID != "" || senderName != "" || inReplyTo != "" || references != "" {
		t.Errorf("expected all empty strings for empty input")
	}
	if len(toList) != 0 || len(ccList) != 0 {
		t.Errorf("expected empty lists for empty input")
	}
}

func TestParseRawMessage_TabFolding(t *testing.T) {
	raw := "Subject: Folded\r\n\twith tab\r\n\r\nBody."
	subject, _, _, _, _, _, _, _, _ := parseRawMessage([]byte(raw))
	expected := "Folded with tab"
	if subject != expected {
		t.Errorf("subject = %q, want %q", subject, expected)
	}
}

// ---------- parseMultipartBody ----------

func TestParseMultipartBody_AlternativeParts(t *testing.T) {
	boundary := "boundary123"
	contentType := "multipart/alternative; boundary=" + boundary
	body := "--" + boundary + "\r\n" +
		"Content-Type: text/plain\r\n\r\n" +
		"Plain text here\r\n" +
		"--" + boundary + "\r\n" +
		"Content-Type: text/html\r\n\r\n" +
		"<p>HTML here</p>\r\n" +
		"--" + boundary + "--\r\n"

	text, html := parseMultipartBody(contentType, body)
	if text != "Plain text here" {
		t.Errorf("text = %q, want %q", text, "Plain text here")
	}
	if html != "<p>HTML here</p>" {
		t.Errorf("html = %q, want %q", html, "<p>HTML here</p>")
	}
}

func TestParseMultipartBody_NestedMultipart(t *testing.T) {
	innerBoundary := "inner_boundary"
	outerBoundary := "outer_boundary"

	innerBody := "--" + innerBoundary + "\r\n" +
		"Content-Type: text/plain\r\n\r\n" +
		"Nested plain\r\n" +
		"--" + innerBoundary + "\r\n" +
		"Content-Type: text/html\r\n\r\n" +
		"<b>Nested HTML</b>\r\n" +
		"--" + innerBoundary + "--\r\n"

	outerContentType := "multipart/mixed; boundary=" + outerBoundary
	body := "--" + outerBoundary + "\r\n" +
		"Content-Type: multipart/alternative; boundary=" + innerBoundary + "\r\n\r\n" +
		innerBody +
		"--" + outerBoundary + "--\r\n"

	text, html := parseMultipartBody(outerContentType, body)
	if text != "Nested plain" {
		t.Errorf("text = %q, want %q", text, "Nested plain")
	}
	if html != "<b>Nested HTML</b>" {
		t.Errorf("html = %q, want %q", html, "<b>Nested HTML</b>")
	}
}

func TestParseMultipartBody_NoBoundary(t *testing.T) {
	text, html := parseMultipartBody("multipart/mixed", "some body")
	if text != "some body" {
		t.Errorf("text = %q, want %q", text, "some body")
	}
	if html != "" {
		t.Errorf("html = %q, want empty", html)
	}
}

func TestParseMultipartBody_InvalidContentType(t *testing.T) {
	text, html := parseMultipartBody(";;;invalid", "some body")
	if text != "some body" {
		t.Errorf("text = %q, want %q", text, "some body")
	}
	if html != "" {
		t.Errorf("html = %q, want empty", html)
	}
}

func TestParseMultipartBody_TextOnly(t *testing.T) {
	boundary := "textonly"
	contentType := "multipart/mixed; boundary=" + boundary
	body := "--" + boundary + "\r\n" +
		"Content-Type: text/plain\r\n\r\n" +
		"Only text\r\n" +
		"--" + boundary + "--\r\n"

	text, html := parseMultipartBody(contentType, body)
	if text != "Only text" {
		t.Errorf("text = %q, want %q", text, "Only text")
	}
	if html != "" {
		t.Errorf("html = %q, want empty", html)
	}
}

func TestParseRawMessage_MultipartMessage(t *testing.T) {
	boundary := "msgboundary"
	raw := "Subject: Multipart\r\n" +
		"Content-Type: multipart/alternative; boundary=" + boundary + "\r\n" +
		"\r\n" +
		"--" + boundary + "\r\n" +
		"Content-Type: text/plain\r\n\r\n" +
		"Plain body\r\n" +
		"--" + boundary + "\r\n" +
		"Content-Type: text/html\r\n\r\n" +
		"<p>HTML body</p>\r\n" +
		"--" + boundary + "--\r\n"

	subject, bodyText, bodyHTML, _, _, _, _, _, _ := parseRawMessage([]byte(raw))
	if subject != "Multipart" {
		t.Errorf("subject = %q, want %q", subject, "Multipart")
	}
	if bodyText != "Plain body" {
		t.Errorf("bodyText = %q, want %q", bodyText, "Plain body")
	}
	if bodyHTML != "<p>HTML body</p>" {
		t.Errorf("bodyHTML = %q, want %q", bodyHTML, "<p>HTML body</p>")
	}
}
