package wire

import "testing"

func TestUnquote_QuotedString(t *testing.T) {
	if result := Unquote(`"hello"`); result != "hello" {
		t.Errorf("got %q, want %q", result, "hello")
	}
}

func TestUnquote_NoQuotes(t *testing.T) {
	if result := Unquote("hello"); result != "hello" {
		t.Errorf("got %q, want %q", result, "hello")
	}
}

func TestUnquote_EmptyQuotes(t *testing.T) {
	if result := Unquote(`""`); result != "" {
		t.Errorf("got %q, want empty", result)
	}
}

func TestUnquote_WhitespaceAround(t *testing.T) {
	if result := Unquote(`  "hello"  `); result != "hello" {
		t.Errorf("got %q, want %q", result, "hello")
	}
}

func TestUnquote_SingleChar(t *testing.T) {
	if result := Unquote("a"); result != "a" {
		t.Errorf("got %q, want %q", result, "a")
	}
}

func TestUnquote_EmptyString(t *testing.T) {
	if result := Unquote(""); result != "" {
		t.Errorf("got %q, want empty", result)
	}
}

func TestUnquote_OnlyOpenQuote(t *testing.T) {
	if result := Unquote(`"hello`); result != `"hello` {
		t.Errorf("got %q, want %q", result, `"hello`)
	}
}

// A quoted-string body decodes its two RFC 3501 §4.3 escapes: \" -> " and
// \\ -> \. Passwords and mailbox names carrying either character depend on this.
func TestUnquote_EscapedQuote(t *testing.T) {
	if result := Unquote(`"pa\"ss"`); result != `pa"ss` {
		t.Errorf("got %q, want %q", result, `pa"ss`)
	}
}

func TestUnquote_EscapedBackslash(t *testing.T) {
	if result := Unquote(`"a\\b"`); result != `a\b` {
		t.Errorf("got %q, want %q", result, `a\b`)
	}
}

func TestUnquote_MixedEscapes(t *testing.T) {
	// "\\\"" is the wire form of the two-character value \" (backslash, quote).
	if result := Unquote(`"\\\""`); result != `\"` {
		t.Errorf("got %q, want %q", result, `\"`)
	}
}

// A backslash before an ordinary character is not an escape (only \" and \\
// are defined), so it is preserved literally.
func TestUnquote_NonEscapeBackslashPreserved(t *testing.T) {
	if result := Unquote(`"a\b"`); result != `a\b` {
		t.Errorf("got %q, want %q", result, `a\b`)
	}
}

func TestQuoteString_Empty(t *testing.T) {
	if result := QuoteString(""); result != "NIL" {
		t.Errorf("got %q, want %q", result, "NIL")
	}
}

func TestQuoteString_Simple(t *testing.T) {
	if result := QuoteString("hello"); result != `"hello"` {
		t.Errorf("got %q, want %q", result, `"hello"`)
	}
}

func TestQuoteString_WithBackslash(t *testing.T) {
	if result := QuoteString(`back\slash`); result != `"back\\slash"` {
		t.Errorf("got %q, want %q", result, `"back\\slash"`)
	}
}

func TestQuoteString_WithDoubleQuote(t *testing.T) {
	if result := QuoteString(`say "hello"`); result != `"say \"hello\""` {
		t.Errorf("got %q, want %q", result, `"say \"hello\""`)
	}
}

func TestQuoteString_WithBothSpecials(t *testing.T) {
	result := QuoteString(`a\"b`)
	expected := `"a\\\"b"`
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}
