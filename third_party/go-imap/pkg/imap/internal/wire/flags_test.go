package wire

import "testing"

func TestParseFlags_Standard(t *testing.T) {
	result := ParseFlags(`(\Seen \Flagged)`)
	if len(result) != 2 {
		t.Fatalf("got %v, want 2 flags", result)
	}
	if result[0] != `\Seen` || result[1] != `\Flagged` {
		t.Errorf("got %v, want [\\Seen \\Flagged]", result)
	}
}

func TestParseFlags_SingleFlag(t *testing.T) {
	result := ParseFlags(`(\Draft)`)
	if len(result) != 1 || result[0] != `\Draft` {
		t.Errorf("got %v, want [\\Draft]", result)
	}
}

func TestParseFlags_EmptyParens(t *testing.T) {
	if result := ParseFlags("()"); len(result) != 0 {
		t.Errorf("got %v, want empty", result)
	}
}

func TestParseFlags_NoParens(t *testing.T) {
	result := ParseFlags(`\Seen \Flagged`)
	if len(result) != 2 {
		t.Fatalf("got %v, want 2 flags", result)
	}
	if result[0] != `\Seen` || result[1] != `\Flagged` {
		t.Errorf("got %v, want [\\Seen \\Flagged]", result)
	}
}

func TestParseFlags_WithWhitespace(t *testing.T) {
	result := ParseFlags("  (  \\Seen  )  ")
	if len(result) != 1 || result[0] != `\Seen` {
		t.Errorf("got %v, want [\\Seen]", result)
	}
}

func TestParseFlags_EmptyString(t *testing.T) {
	if result := ParseFlags(""); len(result) != 0 {
		t.Errorf("got %v, want empty for empty input", result)
	}
}
