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
		{"ascii-numeric comparator", "comparator-i;ascii-numeric", `require "comparator-i;ascii-numeric";`},
		{"ascii-numeric used undeclared", "comparator-i;ascii-numeric", `if header :is :comparator "i;ascii-numeric" "X" "1" { keep; }`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { assertSieveCapabilityRefused(t, tc.script, tc.capability) })
	}
}

// go-sieve does not implement these, so the parser refuses them. They answer in the
// same shape as an extension this server turned off. `variables`, `relational` and
// `subaddress` are on the owner's list, but stay refused here until go-sieve runs them
// and they are added to enabledSieveCapabilities.
func TestSieveCapabilitiesTheParserLacksAreRefused(t *testing.T) {
	for _, capability := range []string{"variables", "relational", "subaddress", "enotify", "x-unknown", `quo"te`} {
		t.Run(capability, func(t *testing.T) {
			quoted, err := json.Marshal(capability)
			if err != nil {
				t.Fatal(err)
			}
			assertSieveCapabilityRefused(t, `require `+string(quoted)+`;`, capability)
		})
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
