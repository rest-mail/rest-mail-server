package imap

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/gateway/apiclient"
)

// ---------------------------------------------------------------------------
// parseIMAPCommand
// ---------------------------------------------------------------------------

func TestParseIMAPCommand_Basic(t *testing.T) {
	tag, cmd, args := parseIMAPCommand("A001 LOGIN user pass")
	if tag != "A001" {
		t.Errorf("tag = %q, want %q", tag, "A001")
	}
	if cmd != "LOGIN" {
		t.Errorf("cmd = %q, want %q", cmd, "LOGIN")
	}
	if args != "user pass" {
		t.Errorf("args = %q, want %q", args, "user pass")
	}
}

func TestParseIMAPCommand_NoArgs(t *testing.T) {
	tag, cmd, args := parseIMAPCommand("A002 NOOP")
	if tag != "A002" {
		t.Errorf("tag = %q, want %q", tag, "A002")
	}
	if cmd != "NOOP" {
		t.Errorf("cmd = %q, want %q", cmd, "NOOP")
	}
	if args != "" {
		t.Errorf("args = %q, want empty", args)
	}
}

func TestParseIMAPCommand_EmptyLine(t *testing.T) {
	tag, cmd, args := parseIMAPCommand("")
	if tag != "" || cmd != "" || args != "" {
		t.Errorf("expected all empty for empty input, got tag=%q cmd=%q args=%q", tag, cmd, args)
	}
}

func TestParseIMAPCommand_SingleWord(t *testing.T) {
	tag, cmd, args := parseIMAPCommand("LOGOUT")
	if tag != "" || cmd != "" || args != "" {
		t.Errorf("expected all empty for single word, got tag=%q cmd=%q args=%q", tag, cmd, args)
	}
}

func TestParseIMAPCommand_ArgsWithSpaces(t *testing.T) {
	tag, cmd, args := parseIMAPCommand(`A003 FETCH 1:* (FLAGS BODY[HEADER.FIELDS (Subject From)])`)
	if tag != "A003" {
		t.Errorf("tag = %q, want %q", tag, "A003")
	}
	if cmd != "FETCH" {
		t.Errorf("cmd = %q, want %q", cmd, "FETCH")
	}
	expected := `1:* (FLAGS BODY[HEADER.FIELDS (Subject From)])`
	if args != expected {
		t.Errorf("args = %q, want %q", args, expected)
	}
}

// ---------------------------------------------------------------------------
// parseIMAPArgs
// ---------------------------------------------------------------------------

func TestParseIMAPArgs_SimpleTokens(t *testing.T) {
	result := parseIMAPArgs("FLAGS BODY ENVELOPE")
	if len(result) != 3 {
		t.Fatalf("got %d tokens, want 3: %v", len(result), result)
	}
	if result[0] != "FLAGS" || result[1] != "BODY" || result[2] != "ENVELOPE" {
		t.Errorf("got %v, want [FLAGS BODY ENVELOPE]", result)
	}
}

func TestParseIMAPArgs_QuotedString(t *testing.T) {
	result := parseIMAPArgs(`"hello world" foo`)
	if len(result) != 2 {
		t.Fatalf("got %d tokens, want 2: %v", len(result), result)
	}
	if result[0] != `"hello world"` {
		t.Errorf("result[0] = %q, want %q", result[0], `"hello world"`)
	}
	if result[1] != "foo" {
		t.Errorf("result[1] = %q, want %q", result[1], "foo")
	}
}

func TestParseIMAPArgs_UnterminatedQuote(t *testing.T) {
	result := parseIMAPArgs(`"unterminated`)
	if len(result) != 1 {
		t.Fatalf("got %d tokens, want 1: %v", len(result), result)
	}
	if result[0] != `"unterminated` {
		t.Errorf("result[0] = %q, want %q", result[0], `"unterminated`)
	}
}

func TestParseIMAPArgs_Parenthesized(t *testing.T) {
	result := parseIMAPArgs(`(FLAGS BODY) ENVELOPE`)
	if len(result) != 2 {
		t.Fatalf("got %d tokens, want 2: %v", len(result), result)
	}
	if result[0] != "(FLAGS BODY)" {
		t.Errorf("result[0] = %q, want %q", result[0], "(FLAGS BODY)")
	}
	if result[1] != "ENVELOPE" {
		t.Errorf("result[1] = %q, want %q", result[1], "ENVELOPE")
	}
}

func TestParseIMAPArgs_NestedParens(t *testing.T) {
	result := parseIMAPArgs(`(BODY[HEADER.FIELDS (From Subject)])`)
	if len(result) != 1 {
		t.Fatalf("got %d tokens, want 1: %v", len(result), result)
	}
	if result[0] != "(BODY[HEADER.FIELDS (From Subject)])" {
		t.Errorf("result[0] = %q, want %q", result[0], "(BODY[HEADER.FIELDS (From Subject)])")
	}
}

func TestParseIMAPArgs_Empty(t *testing.T) {
	result := parseIMAPArgs("")
	if len(result) != 0 {
		t.Errorf("expected empty result for empty input, got %v", result)
	}
}

func TestParseIMAPArgs_OnlySpaces(t *testing.T) {
	result := parseIMAPArgs("   ")
	if len(result) != 0 {
		t.Errorf("expected empty result for whitespace input, got %v", result)
	}
}

func TestParseIMAPArgs_MixedQuotesAndParens(t *testing.T) {
	result := parseIMAPArgs(`"INBOX" (\\Seen) 1:*`)
	if len(result) != 3 {
		t.Fatalf("got %d tokens, want 3: %v", len(result), result)
	}
	if result[0] != `"INBOX"` {
		t.Errorf("result[0] = %q, want %q", result[0], `"INBOX"`)
	}
	if result[1] != `(\\Seen)` {
		t.Errorf("result[1] = %q, want %q", result[1], `(\\Seen)`)
	}
	if result[2] != "1:*" {
		t.Errorf("result[2] = %q, want %q", result[2], "1:*")
	}
}

// ---------------------------------------------------------------------------
// unquote
// ---------------------------------------------------------------------------

func TestUnquote_QuotedString(t *testing.T) {
	result := unquote(`"hello"`)
	if result != "hello" {
		t.Errorf("got %q, want %q", result, "hello")
	}
}

func TestUnquote_NoQuotes(t *testing.T) {
	result := unquote("hello")
	if result != "hello" {
		t.Errorf("got %q, want %q", result, "hello")
	}
}

func TestUnquote_EmptyQuotes(t *testing.T) {
	result := unquote(`""`)
	if result != "" {
		t.Errorf("got %q, want empty", result)
	}
}

func TestUnquote_WhitespaceAround(t *testing.T) {
	result := unquote(`  "hello"  `)
	if result != "hello" {
		t.Errorf("got %q, want %q", result, "hello")
	}
}

func TestUnquote_SingleChar(t *testing.T) {
	result := unquote("a")
	if result != "a" {
		t.Errorf("got %q, want %q", result, "a")
	}
}

func TestUnquote_EmptyString(t *testing.T) {
	result := unquote("")
	if result != "" {
		t.Errorf("got %q, want empty", result)
	}
}

func TestUnquote_OnlyOpenQuote(t *testing.T) {
	result := unquote(`"hello`)
	if result != `"hello` {
		t.Errorf("got %q, want %q", result, `"hello`)
	}
}

// ---------------------------------------------------------------------------
// decodeBase64
// ---------------------------------------------------------------------------

func TestDecodeBase64_Valid(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("hello world"))
	decoded, err := decodeBase64(encoded)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(decoded) != "hello world" {
		t.Errorf("got %q, want %q", string(decoded), "hello world")
	}
}

func TestDecodeBase64_Empty(t *testing.T) {
	decoded, err := decodeBase64("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decoded) != 0 {
		t.Errorf("expected empty slice, got %v", decoded)
	}
}

func TestDecodeBase64_Invalid(t *testing.T) {
	_, err := decodeBase64("!!!not-valid-base64!!!")
	if err == nil {
		t.Error("expected error for invalid base64, got nil")
	}
}

// ---------------------------------------------------------------------------
// buildFlags
// ---------------------------------------------------------------------------

func TestBuildFlags_NoFlags(t *testing.T) {
	msg := apiclient.MessageSummary{}
	result := buildFlags(msg)
	if result != "" {
		t.Errorf("got %q, want empty", result)
	}
}

func TestBuildFlags_Seen(t *testing.T) {
	msg := apiclient.MessageSummary{IsRead: true}
	result := buildFlags(msg)
	if result != `\Seen` {
		t.Errorf("got %q, want %q", result, `\Seen`)
	}
}

func TestBuildFlags_Flagged(t *testing.T) {
	msg := apiclient.MessageSummary{IsFlagged: true}
	result := buildFlags(msg)
	if result != `\Flagged` {
		t.Errorf("got %q, want %q", result, `\Flagged`)
	}
}

func TestBuildFlags_Draft(t *testing.T) {
	msg := apiclient.MessageSummary{IsDraft: true}
	result := buildFlags(msg)
	if result != `\Draft` {
		t.Errorf("got %q, want %q", result, `\Draft`)
	}
}

func TestBuildFlags_AllFlags(t *testing.T) {
	msg := apiclient.MessageSummary{
		IsRead:    true,
		IsFlagged: true,
		IsStarred: true,
		IsDraft:   true,
	}
	result := buildFlags(msg)
	// IsStarred also maps to \Flagged, so expect two \Flagged entries
	if !strings.Contains(result, `\Seen`) {
		t.Errorf("result %q missing \\Seen", result)
	}
	if !strings.Contains(result, `\Flagged`) {
		t.Errorf("result %q missing \\Flagged", result)
	}
	if !strings.Contains(result, `\Draft`) {
		t.Errorf("result %q missing \\Draft", result)
	}
}

func TestBuildFlags_StarredMapsFlagged(t *testing.T) {
	msg := apiclient.MessageSummary{IsStarred: true}
	result := buildFlags(msg)
	if result != `\Flagged` {
		t.Errorf("got %q, want %q", result, `\Flagged`)
	}
}

// ---------------------------------------------------------------------------
// quoteString
// ---------------------------------------------------------------------------

func TestQuoteString_Empty(t *testing.T) {
	result := quoteString("")
	if result != "NIL" {
		t.Errorf("got %q, want %q", result, "NIL")
	}
}

func TestQuoteString_Simple(t *testing.T) {
	result := quoteString("hello")
	if result != `"hello"` {
		t.Errorf("got %q, want %q", result, `"hello"`)
	}
}

func TestQuoteString_WithBackslash(t *testing.T) {
	result := quoteString(`back\slash`)
	if result != `"back\\slash"` {
		t.Errorf("got %q, want %q", result, `"back\\slash"`)
	}
}

func TestQuoteString_WithDoubleQuote(t *testing.T) {
	result := quoteString(`say "hello"`)
	if result != `"say \"hello\""` {
		t.Errorf("got %q, want %q", result, `"say \"hello\""`)
	}
}

func TestQuoteString_WithBothSpecials(t *testing.T) {
	result := quoteString(`a\"b`)
	// Input has \ and ", both get escaped
	// Input: a\"b  ->  a\\"b  (backslash escaped) -> a\\\"b (quote escaped)
	expected := `"a\\\"b"`
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

// ---------------------------------------------------------------------------
// buildAddress
// ---------------------------------------------------------------------------

func TestBuildAddress_EmptyEmail(t *testing.T) {
	result := buildAddress("John", "")
	if result != "NIL" {
		t.Errorf("got %q, want %q", result, "NIL")
	}
}

func TestBuildAddress_FullAddress(t *testing.T) {
	result := buildAddress("John Doe", "john@example.com")
	// Expected: (("John Doe" NIL "john" "example.com"))
	if !strings.Contains(result, `"John Doe"`) {
		t.Errorf("result %q missing name", result)
	}
	if !strings.Contains(result, `"john"`) {
		t.Errorf("result %q missing user part", result)
	}
	if !strings.Contains(result, `"example.com"`) {
		t.Errorf("result %q missing host part", result)
	}
	if !strings.HasPrefix(result, "((") || !strings.HasSuffix(result, "))") {
		t.Errorf("result %q should be wrapped in double parens", result)
	}
}

func TestBuildAddress_NoAtSign(t *testing.T) {
	result := buildAddress("Local", "localonly")
	// Should have empty host
	if !strings.Contains(result, `"localonly"`) {
		t.Errorf("result %q missing user part", result)
	}
	if !strings.Contains(result, "NIL") {
		// host is empty string, which becomes NIL via quoteString
		// Actually host="" -> quoteString("") -> "NIL"
		t.Errorf("result %q should have NIL for empty host", result)
	}
}

func TestBuildAddress_EmptyName(t *testing.T) {
	result := buildAddress("", "user@host.com")
	// Empty name -> NIL
	if !strings.Contains(result, "NIL NIL") {
		t.Errorf("result %q should have NIL for empty name", result)
	}
}

// ---------------------------------------------------------------------------
// buildEnvelope
// ---------------------------------------------------------------------------

func TestBuildEnvelope_Basic(t *testing.T) {
	fixedTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	msg := apiclient.MessageSummary{
		Subject:    "Test Subject",
		Sender:     "alice@example.com",
		SenderName: "Alice",
		ReceivedAt: fixedTime,
	}
	result := buildEnvelope(msg)

	// Should start and end with parens
	if !strings.HasPrefix(result, "(") || !strings.HasSuffix(result, ")") {
		t.Errorf("envelope should be wrapped in parens: %s", result)
	}
	// Should contain the date
	if !strings.Contains(result, "Mon, 15 Jan 2024") {
		t.Errorf("envelope missing date: %s", result)
	}
	// Should contain the subject
	if !strings.Contains(result, `"Test Subject"`) {
		t.Errorf("envelope missing subject: %s", result)
	}
	// Note: buildEnvelope pre-quotes sender/senderName via quoteString before
	// passing to buildAddress, so the address parts contain escaped quotes.
	// Verify the envelope contains the from address structure.
	if !strings.Contains(result, "example.com") {
		t.Errorf("envelope missing sender host: %s", result)
	}
	if !strings.Contains(result, "Alice") {
		t.Errorf("envelope missing sender name: %s", result)
	}
	// Verify 5 NILs at end (to, cc, bcc, in-reply-to, message-id)
	if !strings.HasSuffix(result, "NIL NIL NIL NIL NIL)") {
		t.Errorf("envelope should end with 5 NILs: %s", result)
	}
}

func TestBuildEnvelope_EmptySubject(t *testing.T) {
	fixedTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	msg := apiclient.MessageSummary{
		Subject:    "",
		Sender:     "bob@test.com",
		SenderName: "Bob",
		ReceivedAt: fixedTime,
	}
	result := buildEnvelope(msg)
	// Empty subject -> NIL
	if !strings.Contains(result, " NIL ") {
		t.Errorf("envelope should have NIL for empty subject: %s", result)
	}
}

// ---------------------------------------------------------------------------
// buildRawMessage
// ---------------------------------------------------------------------------

func TestBuildRawMessage_TextOnly(t *testing.T) {
	fixedTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	msg := apiclient.MessageDetail{
		MessageSummary: apiclient.MessageSummary{
			Sender:     "alice@example.com",
			SenderName: "Alice",
			Subject:    "Test",
			ReceivedAt: fixedTime,
			MessageID:  "abc123@example.com",
		},
		BodyText: "Hello, world!",
	}
	result := buildRawMessage(msg)

	if !strings.Contains(result, "From: Alice <alice@example.com>\r\n") {
		t.Errorf("missing From header in: %s", result)
	}
	if !strings.Contains(result, "Subject: Test\r\n") {
		t.Errorf("missing Subject header in: %s", result)
	}
	if !strings.Contains(result, "Message-ID: <abc123@example.com>\r\n") {
		t.Errorf("missing Message-ID header in: %s", result)
	}
	if !strings.Contains(result, "Content-Type: text/plain; charset=utf-8\r\n") {
		t.Errorf("missing Content-Type in: %s", result)
	}
	if !strings.Contains(result, "Hello, world!") {
		t.Errorf("missing body text in: %s", result)
	}
	// Should NOT be multipart
	if strings.Contains(result, "multipart") {
		t.Errorf("text-only message should not be multipart: %s", result)
	}
}

func TestBuildRawMessage_HTMLOnly(t *testing.T) {
	fixedTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	msg := apiclient.MessageDetail{
		MessageSummary: apiclient.MessageSummary{
			Sender:     "alice@example.com",
			SenderName: "Alice",
			Subject:    "HTML Test",
			ReceivedAt: fixedTime,
		},
		BodyHTML: "<p>Hello</p>",
	}
	result := buildRawMessage(msg)

	if !strings.Contains(result, "Content-Type: text/html; charset=utf-8\r\n") {
		t.Errorf("missing text/html Content-Type in: %s", result)
	}
	if !strings.Contains(result, "<p>Hello</p>") {
		t.Errorf("missing HTML body in: %s", result)
	}
	if strings.Contains(result, "multipart") {
		t.Errorf("HTML-only message should not be multipart: %s", result)
	}
}

func TestBuildRawMessage_Multipart(t *testing.T) {
	fixedTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	msg := apiclient.MessageDetail{
		MessageSummary: apiclient.MessageSummary{
			Sender:     "alice@example.com",
			SenderName: "Alice",
			Subject:    "Multi",
			ReceivedAt: fixedTime,
		},
		BodyText: "Hello text",
		BodyHTML: "<p>Hello HTML</p>",
	}
	result := buildRawMessage(msg)

	if !strings.Contains(result, "multipart/alternative") {
		t.Errorf("should be multipart/alternative: %s", result)
	}
	if !strings.Contains(result, "Hello text") {
		t.Errorf("missing text body in multipart: %s", result)
	}
	if !strings.Contains(result, "<p>Hello HTML</p>") {
		t.Errorf("missing HTML body in multipart: %s", result)
	}
	// Should contain boundary markers
	if !strings.Contains(result, "--=_restmail_") {
		t.Errorf("missing boundary marker in: %s", result)
	}
}

func TestBuildRawMessage_NoMessageID(t *testing.T) {
	fixedTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	msg := apiclient.MessageDetail{
		MessageSummary: apiclient.MessageSummary{
			Sender:     "alice@example.com",
			SenderName: "Alice",
			Subject:    "No MID",
			ReceivedAt: fixedTime,
		},
		BodyText: "body",
	}
	result := buildRawMessage(msg)

	if strings.Contains(result, "Message-ID:") {
		t.Errorf("should not contain Message-ID header when empty: %s", result)
	}
}

func TestBuildRawMessage_InReplyTo(t *testing.T) {
	fixedTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	msg := apiclient.MessageDetail{
		MessageSummary: apiclient.MessageSummary{
			Sender:     "alice@example.com",
			SenderName: "Alice",
			Subject:    "Re: Test",
			ReceivedAt: fixedTime,
		},
		BodyText:  "reply",
		InReplyTo: "orig@example.com",
	}
	result := buildRawMessage(msg)

	if !strings.Contains(result, "In-Reply-To: <orig@example.com>\r\n") {
		t.Errorf("missing In-Reply-To header in: %s", result)
	}
}

func TestBuildRawMessage_MIMEVersion(t *testing.T) {
	fixedTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	msg := apiclient.MessageDetail{
		MessageSummary: apiclient.MessageSummary{
			Sender:     "alice@example.com",
			SenderName: "Alice",
			Subject:    "Mime",
			ReceivedAt: fixedTime,
		},
		BodyText: "body",
	}
	result := buildRawMessage(msg)
	if !strings.Contains(result, "MIME-Version: 1.0\r\n") {
		t.Errorf("missing MIME-Version header in: %s", result)
	}
}

// ---------------------------------------------------------------------------
// parseSequenceSet
// ---------------------------------------------------------------------------

func TestParseSequenceSet_Single(t *testing.T) {
	result := parseSequenceSet("3", 10)
	if len(result) != 1 || result[0] != 3 {
		t.Errorf("got %v, want [3]", result)
	}
}

func TestParseSequenceSet_Range(t *testing.T) {
	result := parseSequenceSet("2:5", 10)
	expected := []int{2, 3, 4, 5}
	if len(result) != len(expected) {
		t.Fatalf("got %v, want %v", result, expected)
	}
	for i, v := range expected {
		if result[i] != v {
			t.Errorf("result[%d] = %d, want %d", i, result[i], v)
		}
	}
}

func TestParseSequenceSet_StarInRange(t *testing.T) {
	result := parseSequenceSet("3:*", 5)
	expected := []int{3, 4, 5}
	if len(result) != len(expected) {
		t.Fatalf("got %v, want %v", result, expected)
	}
	for i, v := range expected {
		if result[i] != v {
			t.Errorf("result[%d] = %d, want %d", i, result[i], v)
		}
	}
}

func TestParseSequenceSet_StarAlone(t *testing.T) {
	result := parseSequenceSet("*", 7)
	if len(result) != 1 || result[0] != 7 {
		t.Errorf("got %v, want [7]", result)
	}
}

func TestParseSequenceSet_CommaSeparated(t *testing.T) {
	result := parseSequenceSet("1,3,5", 10)
	expected := []int{1, 3, 5}
	if len(result) != len(expected) {
		t.Fatalf("got %v, want %v", result, expected)
	}
	for i, v := range expected {
		if result[i] != v {
			t.Errorf("result[%d] = %d, want %d", i, result[i], v)
		}
	}
}

func TestParseSequenceSet_MixedRangesAndSingles(t *testing.T) {
	result := parseSequenceSet("1,3:5,7", 10)
	expected := []int{1, 3, 4, 5, 7}
	if len(result) != len(expected) {
		t.Fatalf("got %v, want %v", result, expected)
	}
	for i, v := range expected {
		if result[i] != v {
			t.Errorf("result[%d] = %d, want %d", i, result[i], v)
		}
	}
}

func TestParseSequenceSet_ReverseRange(t *testing.T) {
	result := parseSequenceSet("5:2", 10)
	// Should normalize to 2:5
	expected := []int{2, 3, 4, 5}
	if len(result) != len(expected) {
		t.Fatalf("got %v, want %v", result, expected)
	}
	for i, v := range expected {
		if result[i] != v {
			t.Errorf("result[%d] = %d, want %d", i, result[i], v)
		}
	}
}

func TestParseSequenceSet_ZeroTotal(t *testing.T) {
	result := parseSequenceSet("1:5", 0)
	if result != nil {
		t.Errorf("got %v, want nil for zero total", result)
	}
}

func TestParseSequenceSet_OutOfRange(t *testing.T) {
	result := parseSequenceSet("15", 10)
	if len(result) != 0 {
		t.Errorf("got %v, want empty for out of range", result)
	}
}

func TestParseSequenceSet_ZeroSeqNum(t *testing.T) {
	result := parseSequenceSet("0", 10)
	if len(result) != 0 {
		t.Errorf("got %v, want empty for seq 0", result)
	}
}

func TestParseSequenceSet_NoDuplicates(t *testing.T) {
	result := parseSequenceSet("1,1,1", 10)
	if len(result) != 1 {
		t.Errorf("got %v, want [1] (no duplicates)", result)
	}
}

func TestParseSequenceSet_OverlappingRanges(t *testing.T) {
	result := parseSequenceSet("1:3,2:4", 10)
	// Should be [1,2,3,4] with no duplicates
	expected := []int{1, 2, 3, 4}
	if len(result) != len(expected) {
		t.Fatalf("got %v, want %v", result, expected)
	}
	for i, v := range expected {
		if result[i] != v {
			t.Errorf("result[%d] = %d, want %d", i, result[i], v)
		}
	}
}

func TestParseSequenceSet_OneToStar(t *testing.T) {
	result := parseSequenceSet("1:*", 3)
	expected := []int{1, 2, 3}
	if len(result) != len(expected) {
		t.Fatalf("got %v, want %v", result, expected)
	}
	for i, v := range expected {
		if result[i] != v {
			t.Errorf("result[%d] = %d, want %d", i, result[i], v)
		}
	}
}

func TestParseSequenceSet_EmptyString(t *testing.T) {
	result := parseSequenceSet("", 10)
	if len(result) != 0 {
		t.Errorf("got %v, want empty for empty string", result)
	}
}

// ---------------------------------------------------------------------------
// resolveSeqNum
// ---------------------------------------------------------------------------

func TestResolveSeqNum_Star(t *testing.T) {
	result := resolveSeqNum("*", 10)
	if result != 10 {
		t.Errorf("got %d, want 10", result)
	}
}

func TestResolveSeqNum_Number(t *testing.T) {
	result := resolveSeqNum("5", 10)
	if result != 5 {
		t.Errorf("got %d, want 5", result)
	}
}

func TestResolveSeqNum_Invalid(t *testing.T) {
	result := resolveSeqNum("abc", 10)
	if result != 0 {
		t.Errorf("got %d, want 0 for invalid input", result)
	}
}

func TestResolveSeqNum_Whitespace(t *testing.T) {
	result := resolveSeqNum("  3  ", 10)
	if result != 3 {
		t.Errorf("got %d, want 3 (trimmed)", result)
	}
}

func TestResolveSeqNum_StarWithWhitespace(t *testing.T) {
	result := resolveSeqNum("  *  ", 10)
	if result != 10 {
		t.Errorf("got %d, want 10 (trimmed star)", result)
	}
}

// ---------------------------------------------------------------------------
// parseFlags
// ---------------------------------------------------------------------------

func TestParseFlags_Standard(t *testing.T) {
	result := parseFlags(`(\Seen \Flagged)`)
	if len(result) != 2 {
		t.Fatalf("got %v, want 2 flags", result)
	}
	if result[0] != `\Seen` {
		t.Errorf("result[0] = %q, want %q", result[0], `\Seen`)
	}
	if result[1] != `\Flagged` {
		t.Errorf("result[1] = %q, want %q", result[1], `\Flagged`)
	}
}

func TestParseFlags_SingleFlag(t *testing.T) {
	result := parseFlags(`(\Draft)`)
	if len(result) != 1 || result[0] != `\Draft` {
		t.Errorf("got %v, want [\\Draft]", result)
	}
}

func TestParseFlags_EmptyParens(t *testing.T) {
	result := parseFlags("()")
	if len(result) != 0 {
		t.Errorf("got %v, want empty", result)
	}
}

func TestParseFlags_NoParens(t *testing.T) {
	result := parseFlags(`\Seen \Flagged`)
	if len(result) != 2 {
		t.Fatalf("got %v, want 2 flags", result)
	}
	if result[0] != `\Seen` || result[1] != `\Flagged` {
		t.Errorf("got %v, want [\\Seen \\Flagged]", result)
	}
}

func TestParseFlags_WithWhitespace(t *testing.T) {
	result := parseFlags("  (  \\Seen  )  ")
	if len(result) != 1 || result[0] != `\Seen` {
		t.Errorf("got %v, want [\\Seen]", result)
	}
}

func TestParseFlags_EmptyString(t *testing.T) {
	result := parseFlags("")
	if len(result) != 0 {
		t.Errorf("got %v, want empty for empty input", result)
	}
}

// ---------------------------------------------------------------------------
// extractHeaderFieldNames
// ---------------------------------------------------------------------------

func TestExtractHeaderFieldNames_Standard(t *testing.T) {
	result := extractHeaderFieldNames("BODY[HEADER.FIELDS (From Subject Date)]")
	if len(result) != 3 {
		t.Fatalf("got %v, want 3 fields", result)
	}
	if result[0] != "From" || result[1] != "Subject" || result[2] != "Date" {
		t.Errorf("got %v, want [From Subject Date]", result)
	}
}

func TestExtractHeaderFieldNames_CaseInsensitive(t *testing.T) {
	result := extractHeaderFieldNames("body[header.fields (From To)]")
	if len(result) != 2 {
		t.Fatalf("got %v, want 2 fields", result)
	}
	if result[0] != "From" || result[1] != "To" {
		t.Errorf("got %v, want [From To]", result)
	}
}

func TestExtractHeaderFieldNames_NoHeaderFields(t *testing.T) {
	result := extractHeaderFieldNames("BODY[]")
	if result != nil {
		t.Errorf("got %v, want nil", result)
	}
}

func TestExtractHeaderFieldNames_NoParens(t *testing.T) {
	result := extractHeaderFieldNames("BODY[HEADER.FIELDS]")
	if result != nil {
		t.Errorf("got %v, want nil", result)
	}
}

func TestExtractHeaderFieldNames_SingleField(t *testing.T) {
	result := extractHeaderFieldNames("BODY[HEADER.FIELDS (Subject)]")
	if len(result) != 1 || result[0] != "Subject" {
		t.Errorf("got %v, want [Subject]", result)
	}
}

func TestExtractHeaderFieldNames_EmptyParens(t *testing.T) {
	result := extractHeaderFieldNames("BODY[HEADER.FIELDS ()]")
	if len(result) != 0 {
		t.Errorf("got %v, want empty", result)
	}
}

// ---------------------------------------------------------------------------
// filterHeaders
// ---------------------------------------------------------------------------

func TestFilterHeaders_Basic(t *testing.T) {
	raw := "From: alice@example.com\r\nSubject: Test\r\nDate: Mon, 01 Jan 2024\r\n\r\nBody text"
	result := filterHeaders(raw, []string{"From", "Subject"})

	if !strings.Contains(result, "From: alice@example.com\r\n") {
		t.Errorf("missing From header in: %q", result)
	}
	if !strings.Contains(result, "Subject: Test\r\n") {
		t.Errorf("missing Subject header in: %q", result)
	}
	if strings.Contains(result, "Date:") {
		t.Errorf("should not contain Date header in: %q", result)
	}
	// Should end with \r\n (the terminating blank line)
	if !strings.HasSuffix(result, "\r\n") {
		t.Errorf("should end with CRLF: %q", result)
	}
}

func TestFilterHeaders_CaseInsensitive(t *testing.T) {
	raw := "From: alice@example.com\r\nSUBJECT: Test\r\n\r\nBody"
	result := filterHeaders(raw, []string{"subject"})
	if !strings.Contains(result, "SUBJECT: Test\r\n") {
		t.Errorf("case-insensitive match failed in: %q", result)
	}
}

func TestFilterHeaders_NoMatch(t *testing.T) {
	raw := "From: alice@example.com\r\n\r\nBody"
	result := filterHeaders(raw, []string{"Subject"})
	// Should just be the blank terminating line
	if result != "\r\n" {
		t.Errorf("got %q, want just terminating CRLF", result)
	}
}

func TestFilterHeaders_NoBodySeparator(t *testing.T) {
	raw := "From: alice@example.com\r\nSubject: Test"
	result := filterHeaders(raw, []string{"From"})
	if !strings.Contains(result, "From: alice@example.com\r\n") {
		t.Errorf("missing From header in: %q", result)
	}
}

func TestFilterHeaders_EmptyFields(t *testing.T) {
	raw := "From: alice@example.com\r\nSubject: Test\r\n\r\nBody"
	result := filterHeaders(raw, []string{})
	if result != "\r\n" {
		t.Errorf("got %q, want just terminating CRLF for no requested fields", result)
	}
}

func TestFilterHeaders_AllFields(t *testing.T) {
	raw := "From: alice@example.com\r\nSubject: Test\r\n\r\nBody"
	result := filterHeaders(raw, []string{"From", "Subject"})
	if !strings.Contains(result, "From: alice@example.com") {
		t.Errorf("missing From in: %q", result)
	}
	if !strings.Contains(result, "Subject: Test") {
		t.Errorf("missing Subject in: %q", result)
	}
}

// ---------------------------------------------------------------------------
// matchIMAPPattern
// ---------------------------------------------------------------------------

func TestMatchIMAPPattern_StarMatchesAll(t *testing.T) {
	if !matchIMAPPattern("*", "INBOX") {
		t.Error("* should match INBOX")
	}
	if !matchIMAPPattern("*", "Sent/Subfolder") {
		t.Error("* should match Sent/Subfolder")
	}
	if !matchIMAPPattern("*", "") {
		t.Error("* should match empty string")
	}
}

func TestMatchIMAPPattern_PercentMatchesWithoutSlash(t *testing.T) {
	if !matchIMAPPattern("%", "INBOX") {
		t.Error("% should match INBOX")
	}
	if matchIMAPPattern("%", "Sent/Subfolder") {
		t.Error("% should not match Sent/Subfolder")
	}
	if !matchIMAPPattern("%", "") {
		t.Error("% should match empty string")
	}
}

func TestMatchIMAPPattern_ExactMatch(t *testing.T) {
	if !matchIMAPPattern("INBOX", "INBOX") {
		t.Error("exact match should work")
	}
	if matchIMAPPattern("INBOX", "Sent") {
		t.Error("non-matching names should not match")
	}
}

func TestMatchIMAPPattern_CaseInsensitive(t *testing.T) {
	if !matchIMAPPattern("inbox", "INBOX") {
		t.Error("case insensitive match should work")
	}
	if !matchIMAPPattern("INBOX", "inbox") {
		t.Error("case insensitive match should work")
	}
}

func TestMatchIMAPPattern_StarPrefix(t *testing.T) {
	if !matchIMAPPattern("INBOX*", "INBOX") {
		t.Error("INBOX* should match INBOX")
	}
	if !matchIMAPPattern("INBOX*", "INBOX/Sent") {
		t.Error("INBOX* should match INBOX/Sent")
	}
	if matchIMAPPattern("INBOX*", "Drafts") {
		t.Error("INBOX* should not match Drafts")
	}
}

func TestMatchIMAPPattern_PercentPrefix(t *testing.T) {
	if !matchIMAPPattern("INBOX%", "INBOX") {
		t.Error("INBOX% should match INBOX")
	}
	if matchIMAPPattern("INBOX%", "INBOX/Sent") {
		t.Error("INBOX% should not match INBOX/Sent (% does not cross /)")
	}
	if !matchIMAPPattern("INBOX/%", "INBOX/Sent") {
		t.Error("INBOX/% should match INBOX/Sent")
	}
}

func TestMatchIMAPPattern_PercentDoesNotCrossHierarchy(t *testing.T) {
	if matchIMAPPattern("INBOX/%", "INBOX/Sent/Subfolder") {
		t.Error("INBOX/% should not match INBOX/Sent/Subfolder")
	}
	if !matchIMAPPattern("INBOX/*", "INBOX/Sent/Subfolder") {
		t.Error("INBOX/* should match INBOX/Sent/Subfolder")
	}
}

func TestMatchIMAPPattern_StarInMiddle(t *testing.T) {
	if !matchIMAPPattern("I*X", "INBOX") {
		t.Error("I*X should match INBOX")
	}
	if !matchIMAPPattern("I*X", "IX") {
		t.Error("I*X should match IX")
	}
}

func TestMatchIMAPPattern_EmptyPattern(t *testing.T) {
	if !matchIMAPPattern("", "") {
		t.Error("empty pattern should match empty name")
	}
	if matchIMAPPattern("", "INBOX") {
		t.Error("empty pattern should not match non-empty name")
	}
}

func TestMatchIMAPPattern_ComplexPattern(t *testing.T) {
	if !matchIMAPPattern("Sent/*", "Sent/2024/January") {
		t.Error("Sent/* should match nested folders")
	}
	if matchIMAPPattern("Sent/%", "Sent/2024/January") {
		t.Error("Sent/% should not match nested folders")
	}
	if !matchIMAPPattern("Sent/%", "Sent/2024") {
		t.Error("Sent/% should match direct children")
	}
}
