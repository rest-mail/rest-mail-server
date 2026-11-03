package server

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

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

// TestMatchIMAPPattern_InboxCaseInsensitive pins RFC 3501 §5.1: INBOX is the one
// mailbox name matched case-insensitively, but every OTHER name is case-sensitive.
// The old matcher lower-cased both sides, so "sent" wrongly matched "Sent" and a
// sub-mailbox of INBOX folded too; the negative cases here were RED before the fix.
func TestMatchIMAPPattern_InboxCaseInsensitive(t *testing.T) {
	// INBOX, in any spelling, is case-insensitive.
	if !matchIMAPPattern("inbox", "INBOX") {
		t.Error("inbox should match INBOX (INBOX is case-insensitive)")
	}
	if !matchIMAPPattern("INBOX", "inbox") {
		t.Error("INBOX should match inbox (INBOX is case-insensitive)")
	}
	// Non-INBOX names are case-sensitive.
	if matchIMAPPattern("sent", "Sent") {
		t.Error("sent should NOT match Sent — only INBOX is case-insensitive (RFC 3501 §5.1)")
	}
	if matchIMAPPattern("SENT", "Sent") {
		t.Error("SENT should NOT match Sent — only INBOX is case-insensitive (RFC 3501 §5.1)")
	}
	// A sub-mailbox of INBOX is not the special INBOX and stays case-sensitive.
	if matchIMAPPattern("inbox/sent", "INBOX/Sent") {
		t.Error("inbox/sent should NOT match INBOX/Sent — only the top-level INBOX is special")
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

// matchPatternReference is a straightforward recursive backtracker used only as
// a correctness oracle in tests. Its RESULTS define the intended matched set
// ('*' spans any characters including '/'; '%' spans any run of non-'/'); the
// production matcher must agree with it on every input. It must not be used on
// pathological inputs (it is exponential in wildcard count — the very bug #31
// fixes) so the differential test below keeps inputs small.
func matchPatternReference(pattern, name string) bool {
	for len(pattern) > 0 {
		switch pattern[0] {
		case '*':
			pattern = pattern[1:]
			if len(pattern) == 0 {
				return true
			}
			for i := 0; i <= len(name); i++ {
				if matchPatternReference(pattern, name[i:]) {
					return true
				}
			}
			return false
		case '%':
			pattern = pattern[1:]
			if len(pattern) == 0 {
				return !strings.Contains(name, "/")
			}
			for i := 0; i <= len(name); i++ {
				if i > 0 && name[i-1] == '/' {
					break
				}
				if matchPatternReference(pattern, name[i:]) {
					return true
				}
			}
			return false
		default:
			if len(name) == 0 || pattern[0] != name[0] {
				return false
			}
			pattern = pattern[1:]
			name = name[1:]
		}
	}
	return len(name) == 0
}

// referenceMatchIMAPPattern mirrors matchIMAPPattern's wrapper (INBOX-only case
// folding + trivial special cases) around the reference backtracker, so the
// differential test compares full, equivalent behaviour. Like the production
// wrapper it folds ONLY the special INBOX name — all other names are
// case-sensitive (RFC 3501 §5.1).
func referenceMatchIMAPPattern(pattern, name string) bool {
	name = canonicalizeInbox(name)
	if strings.EqualFold(pattern, "INBOX") {
		pattern = "INBOX"
	}
	if pattern == "*" {
		return true
	}
	if pattern == "%" {
		return !strings.Contains(name, "/")
	}
	return matchPatternReference(pattern, name)
}

// ---------------------------------------------------------------------------
// matchIMAPPattern — wildcard semantics and DoS resistance (issue #31)
// ---------------------------------------------------------------------------

// TestMatchIMAPPattern_Semantics is a correctness table proving that '*', '%'
// and separator handling are unchanged by the linear-time rewrite. It includes
// cases that require falling back from a stuck '%' to an earlier '*' (a naive
// single-anchor greedy would get these wrong).
func TestMatchIMAPPattern_Semantics(t *testing.T) {
	cases := []struct {
		pattern string
		name    string
		want    bool
	}{
		// '*' spans the hierarchy separator, '%' does not.
		{"*", "a/b/c", true},
		{"%", "a/b/c", false},
		{"%", "abc", true},
		{"a/*", "a/b/c", true},
		{"a/%", "a/b", true},
		{"a/%", "a/b/c", false},
		{"a/%/c", "a/b/c", true},
		// Both wildcards match zero characters.
		{"a*", "a", true},
		{"a%", "a", true},
		{"*a", "a", true},
		{"%a", "a", true},
		// '%' matches a run of non-separator chars but not across '/'.
		{"a%c", "abc", true},
		{"a%c", "ab/c", false},
		{"a*c", "ab/c", true},
		// Trailing wildcards vs. a remaining separator.
		{"a%", "a/b", false},
		{"a*", "a/b", true},
		// Multiple stars (the shape that made the old matcher explode) still
		// produce the correct answer.
		{"*a*b*c*", "xaybzc", true},
		{"*a*b*c*d", "xaybzc", false},
		// A stuck '%' must fall back to an earlier '*' that CAN cross '/'.
		{"*a%b", "a/axb", true},
		{"*x%y", "a/x_/y", false},
		{"*/%", "a/b/c", true},
		{"%/*", "a/b/c", true},
		// Exact; only INBOX is case-insensitive (RFC 3501 §5.1).
		{"INBOX", "inbox", true},
		{"Sent", "sent", false},
		{"inbox/sent", "INBOX/Sent", false},
		{"", "", true},
		{"", "x", false},
	}
	for _, c := range cases {
		if got := matchIMAPPattern(c.pattern, c.name); got != c.want {
			t.Errorf("matchIMAPPattern(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
		// The oracle must agree with the expected value too (guards the table).
		if ref := referenceMatchIMAPPattern(c.pattern, c.name); ref != c.want {
			t.Errorf("reference(%q, %q) = %v, want %v (bad table entry)", c.pattern, c.name, ref, c.want)
		}
	}
}

// TestMatchIMAPPattern_DifferentialFuzz asserts the linear matcher agrees with
// the reference backtracker across many randomized small inputs, proving the
// matched set is unchanged. Inputs are kept small and low-wildcard so the
// exponential reference stays fast.
func TestMatchIMAPPattern_DifferentialFuzz(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	const alphabet = "ab/%*" // small alphabet rich in separators and wildcards
	randStr := func(maxLen int) string {
		var b strings.Builder
		for l := rng.Intn(maxLen + 1); l > 0; l-- {
			b.WriteByte(alphabet[rng.Intn(len(alphabet))])
		}
		return b.String()
	}
	for i := 0; i < 20000; i++ {
		pattern := randStr(8) // bounded wildcard count keeps the oracle cheap
		name := strings.NewReplacer("*", "a", "%", "b").Replace(randStr(10))
		got := matchIMAPPattern(pattern, name)
		want := referenceMatchIMAPPattern(pattern, name)
		if got != want {
			t.Fatalf("mismatch: matchIMAPPattern(%q, %q) = %v, reference = %v", pattern, name, got, want)
		}
	}
}

// TestMatchIMAPPattern_NoExponentialBlowup is the pathological case from issue
// #31: a pattern with many '*' against a long non-matching name drove the old
// recursive matcher into exponential (effectively unbounded) work — an
// authenticated CPU DoS. The linear matcher completes in microseconds. This
// test is RED (never completes within the budget) on the old code and GREEN
// afterwards.
func TestMatchIMAPPattern_NoExponentialBlowup(t *testing.T) {
	pattern := strings.Repeat("*", 20) + "b"
	name := strings.Repeat("a", 30) // no trailing 'b' — forces worst-case work

	const budget = 2 * time.Second
	done := make(chan bool, 1)
	start := time.Now()
	go func() { done <- matchIMAPPattern(pattern, name) }()

	select {
	case got := <-done:
		if got {
			t.Fatalf("pattern %q should NOT match %q", pattern, name)
		}
		if elapsed := time.Since(start); elapsed > budget {
			t.Fatalf("match took %v, exceeding budget %v (exponential blowup)", elapsed, budget)
		}
	case <-time.After(budget):
		t.Fatalf("matchIMAPPattern did not complete within %v — exponential blowup (issue #31)", budget)
	}
}
