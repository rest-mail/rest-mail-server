package wire

import (
	"strings"
	"testing"
)

// ── ParseCommand ─────────────────────────────────────────────────────

func TestParseCommand_UserCommand(t *testing.T) {
	cmd, arg := ParseCommand("USER foo")
	if cmd != "USER" {
		t.Errorf("expected cmd USER, got %q", cmd)
	}
	if arg != "foo" {
		t.Errorf("expected arg foo, got %q", arg)
	}
}

func TestParseCommand_PassCommand(t *testing.T) {
	cmd, arg := ParseCommand("PASS bar")
	if cmd != "PASS" {
		t.Errorf("expected cmd PASS, got %q", cmd)
	}
	if arg != "bar" {
		t.Errorf("expected arg bar, got %q", arg)
	}
}

func TestParseCommand_QuitNoArg(t *testing.T) {
	cmd, arg := ParseCommand("QUIT")
	if cmd != "QUIT" {
		t.Errorf("expected cmd QUIT, got %q", cmd)
	}
	if arg != "" {
		t.Errorf("expected empty arg, got %q", arg)
	}
}

func TestParseCommand_EmptyString(t *testing.T) {
	cmd, arg := ParseCommand("")
	if cmd != "" {
		t.Errorf("expected empty cmd, got %q", cmd)
	}
	if arg != "" {
		t.Errorf("expected empty arg, got %q", arg)
	}
}

func TestParseCommand_StatNoArg(t *testing.T) {
	cmd, arg := ParseCommand("STAT")
	if cmd != "STAT" {
		t.Errorf("expected cmd STAT, got %q", cmd)
	}
	if arg != "" {
		t.Errorf("expected empty arg, got %q", arg)
	}
}

func TestParseCommand_RetrWithNumber(t *testing.T) {
	cmd, arg := ParseCommand("RETR 1")
	if cmd != "RETR" {
		t.Errorf("expected cmd RETR, got %q", cmd)
	}
	if arg != "1" {
		t.Errorf("expected arg 1, got %q", arg)
	}
}

func TestParseCommand_CaseInsensitivity(t *testing.T) {
	cmd, arg := ParseCommand("user alice")
	if cmd != "USER" {
		t.Errorf("expected cmd USER (uppercased), got %q", cmd)
	}
	if arg != "alice" {
		t.Errorf("expected arg alice, got %q", arg)
	}
}

func TestParseCommand_MixedCase(t *testing.T) {
	cmd, arg := ParseCommand("rEtR 5")
	if cmd != "RETR" {
		t.Errorf("expected cmd RETR (uppercased), got %q", cmd)
	}
	if arg != "5" {
		t.Errorf("expected arg 5, got %q", arg)
	}
}

func TestParseCommand_ArgWithSpaces(t *testing.T) {
	cmd, arg := ParseCommand("PASS my secret password")
	if cmd != "PASS" {
		t.Errorf("expected cmd PASS, got %q", cmd)
	}
	if arg != "my secret password" {
		t.Errorf("expected arg 'my secret password', got %q", arg)
	}
}

// ── CanonicalCRLF ────────────────────────────────────────────────────

func TestCanonicalCRLF(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"crlf idempotent", "a\r\nb\r\n", "a\r\nb\r\n"},
		{"bare lf to crlf", "a\nb\n", "a\r\nb\r\n"},
		{"mixed endings", "a\r\nb\nc", "a\r\nb\r\nc"},
		{"lone dot line via bare lf", "x\n.\ny\n", "x\r\n.\r\ny\r\n"},
		{"bare cr not before lf kept", "a\rb", "a\rb"},
		{"crlf not doubled", "a\r\n", "a\r\n"},
		{"no trailing terminator", "abc", "abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanonicalCRLF(tc.in); got != tc.want {
				t.Errorf("canonicalCRLF(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// RFC 1939 §3 keywords are ASCII, so only ASCII case is folded. Unicode case
// mapping would turn U+017F (ſ) into S and U+0131 (ı) into I, dispatching a
// non-ASCII keyword as a real command, and would rewrite invalid UTF-8.
func TestParseCommand_KeywordCaseIsASCIIOnly(t *testing.T) {
	cases := []struct {
		line, cmd, arg string
	}{
		{"pass Secret", "PASS", "Secret"},
		{"aPoP mrose c4c9334bac560ecc979e58001b3e22fb", "APOP", "mrose c4c9334bac560ecc979e58001b3e22fb"},
		{"auth PLAIN dGVzdAB0ZXN0AHRlc3Q=", "AUTH", "PLAIN dGVzdAB0ZXN0AHRlc3Q="},
		{"paſs secret", "PAſS", "secret"},
		{"lıst 1", "LıST", "1"},
		{"quıt", "QUıT", ""},
		{"\xffpass x", "\xffPASS", "x"},
		{"PASS\x80 x", "PASS\x80", "x"},
		{strings.Repeat("0", 57) + "ı", strings.Repeat("0", 57) + "ı", ""}, // fuzz corpus cdbf828f0b681216
	}
	for _, tc := range cases {
		cmd, arg := ParseCommand(tc.line)
		if cmd != tc.cmd || arg != tc.arg {
			t.Errorf("parseCommand(%q) = (%q, %q), want (%q, %q)", tc.line, cmd, arg, tc.cmd, tc.arg)
		}
	}
}
