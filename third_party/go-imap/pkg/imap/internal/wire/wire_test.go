package wire

import (
	"encoding/base64"
	"testing"
)

func TestParseIMAPCommand_Basic(t *testing.T) {
	tag, cmd, args := ParseCommand("A001 LOGIN user pass")
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
	tag, cmd, args := ParseCommand("A002 NOOP")
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
	tag, cmd, args := ParseCommand("")
	if tag != "" || cmd != "" || args != "" {
		t.Errorf("expected all empty for empty input, got tag=%q cmd=%q args=%q", tag, cmd, args)
	}
}

func TestParseIMAPCommand_SingleWord(t *testing.T) {
	tag, cmd, args := ParseCommand("LOGOUT")
	if tag != "" || cmd != "" || args != "" {
		t.Errorf("expected all empty for single word, got tag=%q cmd=%q args=%q", tag, cmd, args)
	}
}

func TestParseIMAPCommand_ArgsWithSpaces(t *testing.T) {
	tag, cmd, args := ParseCommand(`A003 FETCH 1:* (FLAGS BODY[HEADER.FIELDS (Subject From)])`)
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

func TestParseIMAPArgs_SimpleTokens(t *testing.T) {
	result := ParseArgs("FLAGS BODY ENVELOPE")
	if len(result) != 3 {
		t.Fatalf("got %d tokens, want 3: %v", len(result), result)
	}
	if result[0] != "FLAGS" || result[1] != "BODY" || result[2] != "ENVELOPE" {
		t.Errorf("got %v, want [FLAGS BODY ENVELOPE]", result)
	}
}

func TestParseIMAPArgs_QuotedString(t *testing.T) {
	result := ParseArgs(`"hello world" foo`)
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
	result := ParseArgs(`"unterminated`)
	if len(result) != 1 {
		t.Fatalf("got %d tokens, want 1: %v", len(result), result)
	}
	if result[0] != `"unterminated` {
		t.Errorf("result[0] = %q, want %q", result[0], `"unterminated`)
	}
}

func TestParseIMAPArgs_Parenthesized(t *testing.T) {
	result := ParseArgs(`(FLAGS BODY) ENVELOPE`)
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
	result := ParseArgs(`(BODY[HEADER.FIELDS (From Subject)])`)
	if len(result) != 1 {
		t.Fatalf("got %d tokens, want 1: %v", len(result), result)
	}
	if result[0] != "(BODY[HEADER.FIELDS (From Subject)])" {
		t.Errorf("result[0] = %q, want %q", result[0], "(BODY[HEADER.FIELDS (From Subject)])")
	}
}

func TestParseIMAPArgs_Empty(t *testing.T) {
	result := ParseArgs("")
	if len(result) != 0 {
		t.Errorf("expected empty result for empty input, got %v", result)
	}
}

func TestParseIMAPArgs_OnlySpaces(t *testing.T) {
	result := ParseArgs("   ")
	if len(result) != 0 {
		t.Errorf("expected empty result for whitespace input, got %v", result)
	}
}

func TestParseIMAPArgs_MixedQuotesAndParens(t *testing.T) {
	result := ParseArgs(`"INBOX" (\\Seen) 1:*`)
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

// An escaped quote inside a quoted argument must not terminate the token early:
// the whole `"pa\"ss"` is one argument, kept with its quotes and escape intact.
func TestParseIMAPArgs_EscapedQuoteInString(t *testing.T) {
	result := ParseArgs(`"pa\"ss" foo`)
	if len(result) != 2 {
		t.Fatalf("got %d tokens, want 2: %v", len(result), result)
	}
	if result[0] != `"pa\"ss"` {
		t.Errorf("result[0] = %q, want %q", result[0], `"pa\"ss"`)
	}
	if result[1] != "foo" {
		t.Errorf("result[1] = %q, want %q", result[1], "foo")
	}
}

// A quoted-string may end with an escaped backslash; the following quote still
// closes it.
func TestParseIMAPArgs_TrailingEscapedBackslash(t *testing.T) {
	result := ParseArgs(`"foo\\" bar`)
	if len(result) != 2 {
		t.Fatalf("got %d tokens, want 2: %v", len(result), result)
	}
	if result[0] != `"foo\\"` {
		t.Errorf("result[0] = %q, want %q", result[0], `"foo\\"`)
	}
	if result[1] != "bar" {
		t.Errorf("result[1] = %q, want %q", result[1], "bar")
	}
}

// A string whose only quote is escaped is unterminated: it does not close, so
// the whole remainder is taken as one (unterminated) token.
func TestParseIMAPArgs_EscapedQuoteUnterminated(t *testing.T) {
	result := ParseArgs(`"foo\"`)
	if len(result) != 1 {
		t.Fatalf("got %d tokens, want 1: %v", len(result), result)
	}
	if result[0] != `"foo\"` {
		t.Errorf("result[0] = %q, want %q", result[0], `"foo\"`)
	}
}

func TestDecodeBase64_Valid(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("hello world"))
	decoded, err := DecodeBase64(encoded)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(decoded) != "hello world" {
		t.Errorf("got %q, want %q", string(decoded), "hello world")
	}
}

func TestDecodeBase64_Empty(t *testing.T) {
	decoded, err := DecodeBase64("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decoded) != 0 {
		t.Errorf("expected empty slice, got %v", decoded)
	}
}

func TestDecodeBase64_Invalid(t *testing.T) {
	if _, err := DecodeBase64("!!!not-valid-base64!!!"); err == nil {
		t.Error("expected error for invalid base64, got nil")
	}
}
