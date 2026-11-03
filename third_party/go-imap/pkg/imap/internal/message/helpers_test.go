package message

// envelopeRaw is a message whose headers carry every field the ENVELOPE reports,
// with a Date: header that differs from any arrival time a test supplies, so the
// two dates can be told apart.
const envelopeRaw = "From: Alice <alice@example.com>\r\n" +
	"Sender: Secretary <sec@example.com>\r\n" +
	"Reply-To: Alice Replies <replies@example.com>\r\n" +
	"To: Bob <bob@example.org>, carol@example.net\r\n" +
	"Cc: Dave <dave@example.com>\r\n" +
	"Bcc: Eve <eve@example.com>\r\n" +
	"In-Reply-To: <parent-123@example.com>\r\n" +
	"Message-ID: <msg-456@example.com>\r\n" +
	"Date: Mon, 01 Jan 2024 09:00:00 +0000\r\n" +
	"Subject: Hello there\r\n" +
	"\r\n" +
	"body text\r\n"

// parensBalanced reports whether every '(' in s is matched by a ')' and no ')'
// ever precedes its opener — a cheap well-formedness check for the parenthesized
// BODYSTRUCTURE / ENVELOPE structures RFC 3501 §7.4.2 requires.
func parensBalanced(s string) bool {
	depth := 0
	for _, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}
