package filters

import (
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/pipeline"
)

// folderOf returns the fileinto target recorded on a filter result, or "".
func folderOf(r *pipeline.FilterResult) string {
	if r == nil || r.Message == nil {
		return ""
	}
	return r.Message.Metadata["deliver_to_folder"]
}

// ── if / elsif / else ────────────────────────────────────────────────

func TestSieve_IfElsifElse(t *testing.T) {
	script := `require "fileinto";
if header :contains "Subject" "invoice" {
  fileinto "Invoices";
} elsif header :contains "Subject" "receipt" {
  fileinto "Receipts";
} else {
  fileinto "Other";
}`

	cases := []struct {
		subject string
		want    string
	}{
		{"Your invoice is ready", "Invoices"},
		{"Payment receipt", "Receipts"},
		{"Random newsletter", "Other"},
	}
	for _, tc := range cases {
		email := sieveEmail()
		email.Headers.Subject = tc.subject
		if got := folderOf(runSieve(t, script, email)); got != tc.want {
			t.Errorf("subject %q: expected folder %q, got %q", tc.subject, tc.want, got)
		}
	}
}

func TestSieve_ElsifOnlyFirstMatchRuns(t *testing.T) {
	// Both branches would match; only the first should fire.
	script := `if header :contains "Subject" "test" {
  fileinto "First";
} elsif header :contains "Subject" "message" {
  fileinto "Second";
}`
	if got := folderOf(runSieve(t, script, sieveEmail())); got != "First" {
		t.Errorf("expected only first branch to run, got %q", got)
	}
}

// ── allof / anyof / not ──────────────────────────────────────────────

func TestSieve_Allof(t *testing.T) {
	script := `if allof (header :contains "Subject" "test", address :is "From" "sender@example.com") {
  fileinto "Both";
}`
	if got := folderOf(runSieve(t, script, sieveEmail())); got != "Both" {
		t.Errorf("expected allof match, got %q", got)
	}
}

func TestSieve_Allof_OneFalse(t *testing.T) {
	script := `if allof (header :contains "Subject" "test", address :is "From" "nobody@example.com") {
  fileinto "Both";
}`
	if got := folderOf(runSieve(t, script, sieveEmail())); got == "Both" {
		t.Error("allof should not match when one test is false")
	}
}

func TestSieve_Anyof(t *testing.T) {
	script := `if anyof (header :contains "Subject" "nope", header :contains "Subject" "test") {
  fileinto "Any";
}`
	if got := folderOf(runSieve(t, script, sieveEmail())); got != "Any" {
		t.Errorf("expected anyof match, got %q", got)
	}
}

func TestSieve_Anyof_NoneTrue(t *testing.T) {
	script := `if anyof (header :contains "Subject" "nope", header :contains "Subject" "nada") {
  fileinto "Any";
}`
	if got := folderOf(runSieve(t, script, sieveEmail())); got == "Any" {
		t.Error("anyof should not match when all tests are false")
	}
}

func TestSieve_Not(t *testing.T) {
	script := `if not header :contains "Subject" "spam" {
  fileinto "Ham";
}`
	if got := folderOf(runSieve(t, script, sieveEmail())); got != "Ham" {
		t.Errorf("expected not-match, got %q", got)
	}
}

func TestSieve_NotAnyofNested(t *testing.T) {
	script := `if not anyof (header :contains "Subject" "spam", header :contains "Subject" "junk") {
  fileinto "Clean";
}`
	if got := folderOf(runSieve(t, script, sieveEmail())); got != "Clean" {
		t.Errorf("expected nested not/anyof match, got %q", got)
	}
}

// ── true / false ─────────────────────────────────────────────────────

func TestSieve_TrueFalse(t *testing.T) {
	if got := folderOf(runSieve(t, `if true { fileinto "Yes"; }`, sieveEmail())); got != "Yes" {
		t.Errorf("true test should always match, got %q", got)
	}
	if got := folderOf(runSieve(t, `if false { fileinto "No"; }`, sieveEmail())); got == "No" {
		t.Error("false test should never match")
	}
}

// ── exists ───────────────────────────────────────────────────────────

func TestSieve_Exists(t *testing.T) {
	script := `if exists "Subject" { fileinto "HasSubject"; }`
	if got := folderOf(runSieve(t, script, sieveEmail())); got != "HasSubject" {
		t.Errorf("expected exists match, got %q", got)
	}
}

func TestSieve_Exists_AllRequired(t *testing.T) {
	// exists is true only if every listed header is present.
	script := `if exists ["Subject", "X-Missing"] { fileinto "Both"; }`
	if got := folderOf(runSieve(t, script, sieveEmail())); got == "Both" {
		t.Error("exists must be false when one header is absent")
	}
}

func TestSieve_Exists_RawHeader(t *testing.T) {
	script := `if exists "X-Custom" { fileinto "Custom"; }`
	email := sieveEmail()
	email.Headers.Raw = map[string][]string{"X-Custom": {"hello"}}
	if got := folderOf(runSieve(t, script, email)); got != "Custom" {
		t.Errorf("expected raw header exists match, got %q", got)
	}
}

// ── size ─────────────────────────────────────────────────────────────

func TestSieve_Size(t *testing.T) {
	email := sieveEmail() // body is a few dozen bytes
	if got := folderOf(runSieve(t, `if size :over 5 { fileinto "Big"; }`, email)); got != "Big" {
		t.Errorf("expected size :over 5 to match, got %q", got)
	}
	if got := folderOf(runSieve(t, `if size :under 5 { fileinto "Small"; }`, sieveEmail())); got == "Small" {
		t.Error("size :under 5 should not match a larger body")
	}
}

func TestSieve_Size_Quantifier(t *testing.T) {
	email := sieveEmail()
	email.Body.Content = strings.Repeat("x", 2048) // 2 KiB
	if got := folderOf(runSieve(t, `if size :over 1K { fileinto "Over1K"; }`, email)); got != "Over1K" {
		t.Errorf("expected size :over 1K to match 2KiB body, got %q", got)
	}
	small := sieveEmail()
	if got := folderOf(runSieve(t, `if size :over 1K { fileinto "Over1K"; }`, small)); got == "Over1K" {
		t.Error("small body should not exceed 1K")
	}
}

func TestSieve_Size_IncludesAttachments(t *testing.T) {
	email := sieveEmail()
	email.Attachments = []pipeline.Attachment{{Filename: "a.bin", Size: 5000}}
	if got := folderOf(runSieve(t, `if size :over 4K { fileinto "Heavy"; }`, email)); got != "Heavy" {
		t.Errorf("expected attachment bytes to count toward size, got %q", got)
	}
}

// ── :matches wildcards ───────────────────────────────────────────────

func TestSieve_MatchesStar(t *testing.T) {
	script := `if header :matches "Subject" "Test *" { fileinto "M"; }`
	if got := folderOf(runSieve(t, script, sieveEmail())); got != "M" {
		t.Errorf("expected '*' wildcard match, got %q", got)
	}
}

func TestSieve_MatchesQuestionMark(t *testing.T) {
	script := `if header :matches "Subject" "Te?t message" { fileinto "Q"; }`
	if got := folderOf(runSieve(t, script, sieveEmail())); got != "Q" {
		t.Errorf("expected '?' wildcard match, got %q", got)
	}
}

func TestSieve_MatchesAnchored(t *testing.T) {
	// :matches is anchored: a partial pattern must not match.
	script := `if header :matches "Subject" "message" { fileinto "NoMatch"; }`
	if got := folderOf(runSieve(t, script, sieveEmail())); got == "NoMatch" {
		t.Error(":matches must be anchored (full-string) and not match a substring")
	}
}

func TestSieve_MatchesEscapedWildcard(t *testing.T) {
	// "\\*" in the source becomes "\*" after string parsing, a literal star.
	email := sieveEmail()
	email.Headers.Subject = "50% off*"
	script := `if header :matches "Subject" "50% off\\*" { fileinto "Literal"; }`
	if got := folderOf(runSieve(t, script, email)); got != "Literal" {
		t.Errorf("expected escaped literal-star match, got %q", got)
	}
}

// ── address parts ────────────────────────────────────────────────────

func TestSieve_AddressLocalpart(t *testing.T) {
	script := `if address :localpart :is "From" "sender" { fileinto "Local"; }`
	if got := folderOf(runSieve(t, script, sieveEmail())); got != "Local" {
		t.Errorf("expected :localpart match, got %q", got)
	}
}

func TestSieve_AddressDomain(t *testing.T) {
	script := `if address :domain :is "From" "example.com" { fileinto "Domain"; }`
	if got := folderOf(runSieve(t, script, sieveEmail())); got != "Domain" {
		t.Errorf("expected :domain match, got %q", got)
	}
}

func TestSieve_AddressAllDefault(t *testing.T) {
	script := `if address :is "From" "sender@example.com" { fileinto "All"; }`
	if got := folderOf(runSieve(t, script, sieveEmail())); got != "All" {
		t.Errorf("expected default :all address match, got %q", got)
	}
}

func TestSieve_AddressCc(t *testing.T) {
	script := `if address :domain :is "Cc" "cc.example.com" { fileinto "CcDomain"; }`
	email := sieveEmail()
	email.Headers.Cc = []pipeline.Address{{Address: "someone@cc.example.com"}}
	if got := folderOf(runSieve(t, script, email)); got != "CcDomain" {
		t.Errorf("expected Cc domain match, got %q", got)
	}
}

func TestSieve_EnvelopeDomainPart(t *testing.T) {
	script := `require "envelope";
if envelope :domain :is "from" "example.com" { fileinto "EnvDom"; }`
	if got := folderOf(runSieve(t, script, sieveEmail())); got != "EnvDom" {
		t.Errorf("expected envelope :domain match, got %q", got)
	}
}

// ── comparators ──────────────────────────────────────────────────────

func TestSieve_ComparatorOctetCaseSensitive(t *testing.T) {
	// Subject is "Test message"; octet comparison is case-sensitive.
	script := `if header :is :comparator "i;octet" "Subject" "test message" { fileinto "NoMatch"; }`
	if got := folderOf(runSieve(t, script, sieveEmail())); got == "NoMatch" {
		t.Error("i;octet :is must be case-sensitive")
	}
}

func TestSieve_ComparatorCasemapDefault(t *testing.T) {
	script := `if header :is "Subject" "test message" { fileinto "CI"; }`
	if got := folderOf(runSieve(t, script, sieveEmail())); got != "CI" {
		t.Errorf("expected default casemap to match case-insensitively, got %q", got)
	}
}

func TestSieve_ComparatorAsciiNumeric(t *testing.T) {
	email := sieveEmail()
	email.Headers.Raw = map[string][]string{"X-Priority": {"1"}}
	match := `if header :is :comparator "i;ascii-numeric" "X-Priority" "1" { fileinto "Prio"; }`
	if got := folderOf(runSieve(t, match, email)); got != "Prio" {
		t.Errorf("expected ascii-numeric equality, got %q", got)
	}
	noMatch := `if header :is :comparator "i;ascii-numeric" "X-Priority" "2" { fileinto "Prio"; }`
	if got := folderOf(runSieve(t, noMatch, email)); got == "Prio" {
		t.Error("ascii-numeric 1 should not equal 2")
	}
}

// ── string lists ─────────────────────────────────────────────────────

func TestSieve_KeyList(t *testing.T) {
	script := `if header :contains "Subject" ["foo", "test", "bar"] { fileinto "List"; }`
	if got := folderOf(runSieve(t, script, sieveEmail())); got != "List" {
		t.Errorf("expected match against one of a key list, got %q", got)
	}
}

func TestSieve_HeaderList(t *testing.T) {
	script := `if header :contains ["X-Label", "Subject"] "message" { fileinto "HL"; }`
	if got := folderOf(runSieve(t, script, sieveEmail())); got != "HL" {
		t.Errorf("expected match across a header list, got %q", got)
	}
}

// ── fileinto :create ─────────────────────────────────────────────────

func TestSieve_FileintoCreate(t *testing.T) {
	script := `require ["fileinto", "mailbox"];
if header :contains "Subject" "test" { fileinto :create "Archive/2026"; }`
	r := runSieve(t, script, sieveEmail())
	if folderOf(r) != "Archive/2026" {
		t.Errorf("expected folder Archive/2026, got %q", folderOf(r))
	}
	if r.Message.Metadata["deliver_to_folder_create"] != "true" {
		t.Errorf("expected deliver_to_folder_create=true, got %q", r.Message.Metadata["deliver_to_folder_create"])
	}
}

// ── imap4flags ───────────────────────────────────────────────────────

func TestSieve_SetAndAddFlag(t *testing.T) {
	script := `require "imap4flags";
if true {
  setflag "work";
  addflag "urgent";
  addflag "work";
}`
	r := runSieve(t, script, sieveEmail())
	if got := r.Message.Metadata["imap_flags"]; got != "work urgent" {
		t.Errorf("expected flags 'work urgent' (deduped), got %q", got)
	}
}

func TestSieve_RemoveFlag(t *testing.T) {
	script := `require "imap4flags";
if true {
  setflag ["a", "b", "c"];
  removeflag "b";
}`
	r := runSieve(t, script, sieveEmail())
	if got := r.Message.Metadata["imap_flags"]; got != "a c" {
		t.Errorf("expected flags 'a c' after removeflag, got %q", got)
	}
}

func TestSieve_SetFlagEscaped(t *testing.T) {
	// "\\Seen" in source is the IMAP system flag \Seen.
	script := `require "imap4flags";
if true { setflag "\\Seen"; }`
	r := runSieve(t, script, sieveEmail())
	if got := r.Message.Metadata["imap_flags"]; got != `\Seen` {
		t.Errorf(`expected flag '\Seen', got %q`, got)
	}
}

// ── redirect with tag ────────────────────────────────────────────────

func TestSieve_RedirectCopyTag(t *testing.T) {
	script := `require ["copy"];
if true { redirect :copy "copy@example.com"; }`
	r := runSieve(t, script, sieveEmail())
	if got := r.Message.Metadata["redirect_to"]; got != "copy@example.com" {
		t.Errorf("expected redirect_to copy@example.com, got %q", got)
	}
}

// ── stop across top-level commands ───────────────────────────────────

func TestSieve_TopLevelStop(t *testing.T) {
	script := `if true { fileinto "First"; }
stop;
if true { fileinto "Second"; }`
	if got := folderOf(runSieve(t, script, sieveEmail())); got != "First" {
		t.Errorf("expected top-level stop to halt, got %q", got)
	}
}

// ── multi-line string ────────────────────────────────────────────────

func TestSieve_MultiLineString(t *testing.T) {
	script := "require \"vacation\";\n" +
		"vacation :subject \"OOO\"\n" +
		"text:\n" +
		"Line one.\n" +
		"Line two.\n" +
		".\n" +
		";"
	r := runSieve(t, script, sieveEmail())
	body := r.Message.Metadata["vacation_reply_body"]
	if !strings.Contains(body, "Line one.") || !strings.Contains(body, "Line two.") {
		t.Errorf("expected multi-line vacation body, got %q", body)
	}
}

func TestSieve_MultiLineDotStuffing(t *testing.T) {
	script := "require \"vacation\";\n" +
		"vacation :subject \"OOO\"\n" +
		"text:\n" +
		"..dotted line\n" +
		".\n" +
		";"
	r := runSieve(t, script, sieveEmail())
	body := r.Message.Metadata["vacation_reply_body"]
	if !strings.Contains(body, ".dotted line") || strings.Contains(body, "..dotted") {
		t.Errorf("expected dot-unstuffed body, got %q", body)
	}
}

// ── comments and escapes ─────────────────────────────────────────────

func TestSieve_Comments(t *testing.T) {
	script := `# hash comment
/* bracket
   comment */
if header :contains "Subject" "test" { # trailing comment
  fileinto "Commented";
}`
	if got := folderOf(runSieve(t, script, sieveEmail())); got != "Commented" {
		t.Errorf("expected match despite comments, got %q", got)
	}
}

func TestSieve_EscapedQuoteInString(t *testing.T) {
	email := sieveEmail()
	email.Headers.Subject = `say "hi"`
	script := `if header :is "Subject" "say \"hi\"" { fileinto "Quoted"; }`
	if got := folderOf(runSieve(t, script, email)); got != "Quoted" {
		t.Errorf("expected escaped-quote string match, got %q", got)
	}
}

// ── extension leniency ───────────────────────────────────────────────

func TestSieve_UnknownCommandSkipped(t *testing.T) {
	// An unknown command must not break parsing of the rest of the script.
	script := `frobnicate "x" 42;
if header :contains "Subject" "test" { fileinto "StillWorks"; }`
	if got := folderOf(runSieve(t, script, sieveEmail())); got != "StillWorks" {
		t.Errorf("expected script to run past unknown command, got %q", got)
	}
}

func TestSieve_UnknownTestIsFalse(t *testing.T) {
	// An unknown test evaluates to false but must not abort the script.
	script := `if spamtest :value "gt" "5" {
  fileinto "Spam";
} else {
  fileinto "Ham";
}`
	if got := folderOf(runSieve(t, script, sieveEmail())); got != "Ham" {
		t.Errorf("expected unknown test to be false (else branch), got %q", got)
	}
}

// ── parser-level structure and errors ────────────────────────────────

func TestParseSieveScript_RequiresAndCommands(t *testing.T) {
	script := `require ["fileinto", "envelope"];
if true { keep; }
discard;`
	s, err := parseSieveScript(script)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if len(s.requires) != 2 {
		t.Errorf("expected 2 requires, got %d (%v)", len(s.requires), s.requires)
	}
	if len(s.commands) != 2 {
		t.Errorf("expected 2 top-level commands, got %d", len(s.commands))
	}
}

func TestValidateSieve_Errors(t *testing.T) {
	bad := []string{
		`if true { keep;`,                      // unterminated block
		`if header :is "Subject" "x { keep; }`, // unterminated string
		`keep keep;`,                           // missing semicolon
		`if size 100 { keep; }`,                // size without :over/:under
		`else { keep; }`,                       // else without if
		`if header :is "Subject" { keep; }`,    // header missing key list
	}
	for _, s := range bad {
		if err := ValidateSieve(s); err == nil {
			t.Errorf("expected ValidateSieve to reject:\n%s", s)
		}
	}
}

func TestValidateSieve_AcceptsNewConstructs(t *testing.T) {
	good := []string{
		`if allof (exists "Subject", size :over 1K) { discard; }`,
		`if anyof (true, false) { keep; }`,
		`require "imap4flags";
if header :matches "Subject" "*urgent*" { addflag "\\Flagged"; }`,
		`if header :is "Subject" "x" { fileinto "A"; } elsif true { fileinto "B"; } else { keep; }`,
		`if address :localpart :contains "From" "admin" { redirect "ops@example.com"; }`,
	}
	for _, s := range good {
		if err := ValidateSieve(s); err != nil {
			t.Errorf("ValidateSieve rejected a valid script: %v\n%s", err, s)
		}
	}
}
