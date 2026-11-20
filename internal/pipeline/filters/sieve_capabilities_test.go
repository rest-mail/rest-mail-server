package filters

// The owner's decision on #1468 (carried by #2108): REST Mail enables only the
// standard Sieve extensions on its list, and refuses everything else at upload
// with the capability named. go-sieve stays a general library, so it still parses
// `regex` and `notify`; the refusal belongs to this host.

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
)

// Each script here uses the capability it requires, so the row proves the feature
// works and not only that its name is accepted.
func TestSieveEnabledCapabilitiesInstall(t *testing.T) {
	cases := map[string]string{
		"fileinto":                   `require "fileinto"; fileinto "Filed";`,
		"mailbox":                    `require ["fileinto", "mailbox"]; fileinto :create "Filed";`,
		"copy":                       `require ["fileinto", "copy"]; fileinto :copy "Filed";`,
		"envelope":                   `require "envelope"; if envelope :is "from" "a@example.com" { keep; }`,
		"body":                       `require "body"; if body :contains "invoice" { keep; }`,
		"imap4flags":                 `require "imap4flags"; setflag "\\Seen";`,
		"vacation":                   `require "vacation"; if false { vacation "Away."; }`,
		"reject":                     `require "reject"; if false { reject "No."; }`,
		"ereject":                    `require "ereject"; if false { ereject "No."; }`,
		"encoded-character":          `require "encoded-character"; if header :is "Subject" "${hex:41}" { keep; }`,
		"comparator-i;octet":         `require "comparator-i;octet"; if header :is :comparator "i;octet" "Subject" "A" { keep; }`,
		"comparator-i;ascii-casemap": `require "comparator-i;ascii-casemap"; if header :is :comparator "i;ascii-casemap" "Subject" "a" { keep; }`,
		"variables":                  `require ["fileinto", "variables"]; if header :matches "Subject" "* *" { set "first" "${1}"; fileinto "Filed-${first}"; }`,
		"relational":                 `require ["relational", "comparator-i;ascii-numeric"]; if header :count "ge" :comparator "i;ascii-numeric" "Received" "1" { keep; }`,
		"subaddress":                 `require ["envelope", "subaddress"]; if envelope :detail "to" "work" { keep; }`,
		"comparator-i;ascii-numeric": `require "comparator-i;ascii-numeric"; if header :is :comparator "i;ascii-numeric" "X-Count" "1" { keep; }`,
	}
	if len(cases) != len(enabledSieveCapabilities) {
		t.Fatalf("%d cases for %d enabled capabilities: every enabled capability needs a working script", len(cases), len(enabledSieveCapabilities))
	}
	for capability, script := range cases {
		t.Run(capability, func(t *testing.T) {
			if !enabledSieveCapabilities[capability] {
				t.Fatalf("%s is not enabled", capability)
			}
			if err := ValidateSieveForInstall(script); err != nil {
				t.Fatalf("enabled capability refused: %v", err)
			}
		})
	}
}

// The go-sieve capabilities the owner's list leaves out are refused however a script
// reaches them. The dry run cannot catch these: nothing here runs the feature.
func TestSieveCapabilitiesLeftOffTheListAreRefused(t *testing.T) {
	cases := []struct{ name, capability, script string }{
		{"regex declared alone", "regex", `require "regex";`},
		{"regex in an untaken branch", "regex", `require "regex"; if false { if header :regex "Subject" "a.*" { keep; } }`},
		{"regex in a list", "regex", `require ["fileinto", "regex"]; fileinto "Filed";`},
		{"regex in a second require", "regex", `require "fileinto"; require "regex"; fileinto "Filed";`},
		{"regex spelled with an escape", "regex", `require "re\gex";`},
		{"regex used undeclared", "regex", `if header :regex "Subject" "a.*" { keep; }`},
		{"notify declared alone", "notify", `require "notify";`},
		{"notify in an untaken branch", "notify", `require "notify"; if false { notify :method "mailto:a@example.com"; }`},
		{"notify used undeclared", "notify", `notify :method "mailto:a@example.com";`},
		{"vacation-seconds", "vacation-seconds", `require "vacation-seconds"; if false { vacation :seconds 60 "Away."; }`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { assertSieveCapabilityRefused(t, tc.script, tc.capability) })
	}
}

// go-sieve does not implement these, so the parser refuses them. They answer in the
// same shape as an extension this server turned off.
func TestSieveCapabilitiesTheParserLacksAreRefused(t *testing.T) {
	for _, capability := range []string{"enotify", "x-unknown", `quo"te`} {
		t.Run(capability, func(t *testing.T) {
			quoted, err := json.Marshal(capability)
			if err != nil {
				t.Fatal(err)
			}
			assertSieveCapabilityRefused(t, `require `+string(quoted)+`;`, capability)
		})
	}
}

// Until go-sieve v0.9.0 the parser lacked `variables`, `relational` and `subaddress`,
// and this server refused `comparator-i;ascii-numeric` as off its list; the rows here
// pinned those refusals. go-sieve v0.9.0 runs all four and the server enables them
// (#2108), so a require of each, alone or in a list, installs, and the comparator used
// without its require gets the parse advice that fixes the script instead of a refusal.
func TestSieveCapabilitiesGoSieve090RunsAreAccepted(t *testing.T) {
	for _, capability := range []string{"variables", "relational", "subaddress", "comparator-i;ascii-numeric"} {
		t.Run(capability, func(t *testing.T) {
			quoted, err := json.Marshal(capability)
			if err != nil {
				t.Fatal(err)
			}
			for _, script := range []string{`require ` + string(quoted) + `;`, `require ["fileinto", ` + string(quoted) + `]; fileinto "Filed";`} {
				if err := ValidateSieveForInstall(script); err != nil {
					t.Fatalf("%s: refused: %v", script, err)
				}
			}
		})
	}
	err := ValidateSieveForInstall(`if header :is :comparator "i;ascii-numeric" "X" "1" { keep; }`)
	var refused *SieveInstallError
	if !errors.As(err, &refused) {
		t.Fatalf("got %T (%v), want *SieveInstallError", err, err)
	}
	if refused.Stage != "parse" || refused.MissingRequire != "comparator-i;ascii-numeric" || refused.Capability != "" {
		t.Fatalf("stage %q missing %q capability %q: %v", refused.Stage, refused.MissingRequire, refused.Capability, err)
	}
	if !strings.Contains(err.Error(), `add: require "comparator-i;ascii-numeric"`) {
		t.Fatalf("no advice to add the require: %v", err)
	}
}

// RFC 5228 §2 ends every script line in CRLF and go-sieve v0.9.0 refuses a bare LF.
// The server turns each bare LF into CRLF before it parses, at upload and at delivery,
// leaves CRLF as it is, and still refuses a bare CR.
func TestSieveSourceTurnsBareLFIntoCRLF(t *testing.T) {
	for in, want := range map[string]string{
		"":                  "",
		"keep;":             "keep;",
		"keep;\n":           "keep;\r\n",
		"\nkeep;":           "\r\nkeep;",
		"keep;\r\n":         "keep;\r\n",
		"a\nb\r\nc\n\n":     "a\r\nb\r\nc\r\n\r\n",
		"text:\r\nx\n.\r\n": "text:\r\nx\r\n.\r\n",
		"bare\rCR\n":        "bare\rCR\r\n",
	} {
		if got := sieveSource(in); got != want {
			t.Errorf("sieveSource(%q) = %q, want %q", in, got, want)
		}
	}
	for _, script := range []string{"require \"fileinto\";\nfileinto \"Filed\";\n", "require \"fileinto\";\r\nfileinto \"Filed\";\r\n"} {
		if err := ValidateSieveForInstall(script); err != nil {
			t.Errorf("ValidateSieveForInstall(%q): %v", script, err)
		}
		if err := ValidateSieve(script); err != nil {
			t.Errorf("ValidateSieve(%q): %v", script, err)
		}
		raw, _ := json.Marshal(sieveConfig{Script: script, ScriptHash: HashSieveScript(script)})
		if _, err := NewSieve(raw); err != nil {
			t.Errorf("NewSieve(%q): %v", script, err)
		}
	}
	if err := ValidateSieveForInstall("keep;\rkeep;"); err == nil || !strings.Contains(err.Error(), "CR") {
		t.Errorf("a bare CR installed or was refused for another reason: %v", err)
	}
}

func assertSieveCapabilityRefused(t *testing.T, script, capability string) {
	t.Helper()
	err := ValidateSieveForInstall(script)
	if err == nil {
		t.Fatalf("script installed, want %q refused", capability)
	}
	var refused *SieveInstallError
	if !errors.As(err, &refused) {
		t.Fatalf("got %T (%v), want *SieveInstallError", err, err)
	}
	if refused.Stage != "capability" || refused.Capability != capability {
		t.Fatalf("stage %q capability %q, want capability %q: %v", refused.Stage, refused.Capability, capability, err)
	}
	if refused.MissingRequire != "" {
		t.Fatalf("told the author to require %q, which would still be refused", refused.MissingRequire)
	}
	if !strings.Contains(err.Error(), strconv.Quote(capability)) {
		t.Fatalf("message does not name %q: %v", capability, err)
	}
	if strings.Contains(err.Error(), "add: require") {
		t.Fatalf("message advises adding a require that would be refused: %v", err)
	}
}

// A require the server does enable keeps the advice that fixes the script.
func TestSieveMissingRequireOfAnEnabledCapabilityStillAdvises(t *testing.T) {
	err := ValidateSieveForInstall(`fileinto "Filed";`)
	var refused *SieveInstallError
	if !errors.As(err, &refused) {
		t.Fatalf("got %T (%v), want *SieveInstallError", err, err)
	}
	if refused.Stage != "parse" || refused.MissingRequire != "fileinto" || refused.Capability != "" {
		t.Fatalf("stage %q missing %q capability %q", refused.Stage, refused.MissingRequire, refused.Capability)
	}
}

// Upload is not the only way a script reaches the filter: a row can be written around
// the API, and the test-filter route builds the filter from its request. Neither runs
// an extension the server does not enable, even with a matching validated hash.
func TestSieveFilterDoesNotBuildACapabilityOffTheList(t *testing.T) {
	script := `require "regex"; if header :regex "Subject" "a.*" { discard; }`
	for name, cfg := range map[string]sieveConfig{
		"domain config":     {Script: script},
		"validated mailbox": {Script: script, ScriptHash: HashSieveScript(script), MailboxID: 7},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			filter, err := NewSieve(raw)
			if err == nil {
				t.Fatalf("built %T, want the script refused", filter)
			}
			if !strings.Contains(err.Error(), `"regex"`) {
				t.Fatalf("error does not name the capability: %v", err)
			}
		})
	}
}

// Two go-sieve v0.9.0 tests ask the host for something this server does not give it:
// `mailboxexists` (under the enabled `mailbox`) needs a mailbox lookup, and `body :raw`
// needs the undecoded body. Both pass the upload gate, which does not evaluate a
// runtime error, and at delivery each is a runtime error that keeps the message in
// INBOX (RFC 5228 §2.10.6) instead of filing it.
func TestSieveV090HostlessTestsKeepTheMessage(t *testing.T) {
	for name, script := range map[string]string{
		"mailboxexists": `require ["fileinto", "mailbox"]; if mailboxexists "INBOX" { fileinto "Filed"; }`,
		"body :raw":     `require ["fileinto", "body"]; if body :raw :contains "Hello" { fileinto "Filed"; }`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateSieveForInstall(script); err != nil {
				t.Fatalf("refused at upload: %v", err)
			}
			result := runSieve(t, script, sieveEmail())
			if result.Message == nil {
				t.Fatalf("no message: %+v", result)
			}
			if folder := result.Message.Metadata["deliver_to_folder"]; folder != "" {
				t.Fatalf("filed into %q, want the message kept", folder)
			}
		})
	}
}
