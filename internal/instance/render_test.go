package instance

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestRenderMatchesCommittedConfig is the drift guard: the real mail3.test
// manifest must render byte-for-byte to the committed config.env. If someone
// edits the manifest without re-running `task instance:render` (or edits the
// generated config.env by hand), this fails — which is the whole point.
func TestRenderMatchesCommittedConfig(t *testing.T) {
	// Hermetic fixture (a committed copy), so this doesn't depend on any live
	// instance config — those are testbed-owned now, not shipped in this repo.
	dir := "testdata"

	raw, err := os.ReadFile(filepath.Join(dir, "manifest.yml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	m, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	got, err := Render(m)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	want, err := os.ReadFile(filepath.Join(dir, "config.env"))
	if err != nil {
		t.Fatalf("read config.env: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("rendered config.env is stale — re-run `task instance:render`.\n--- got ---\n%s\n--- committed ---\n%s", got, want)
	}
}

// TestRenderPolicyBlocksMatchesGolden is the with-blocks counterpart to the
// drift guard above: a manifest that sets the optional `smtp:` and `dkim:`
// blocks must render byte-for-byte to the committed config_policy.env, so the
// MAIL3_* policy lines are pinned. Together with TestRenderMatchesCommittedConfig
// (no blocks → committed config.env, unchanged) this proves the blocks are
// purely additive: present → new lines, absent → identical output.
func TestRenderPolicyBlocksMatchesGolden(t *testing.T) {
	dir := "testdata"

	raw, err := os.ReadFile(filepath.Join(dir, "manifest_policy.yml"))
	if err != nil {
		t.Fatalf("read manifest_policy: %v", err)
	}
	m, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse manifest_policy: %v", err)
	}
	got, err := Render(m)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	want, err := os.ReadFile(filepath.Join(dir, "config_policy.env"))
	if err != nil {
		t.Fatalf("read config_policy.env: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("rendered config_policy.env is stale — re-run render.\n--- got ---\n%s\n--- committed ---\n%s", got, want)
	}

	// Belt-and-braces: assert every policy knob emits its MAIL3_* line. The
	// meaningful-zero cases (MIN_TRANSFER_RATE=0, MTASTS_ENFORCE=false) prove the
	// pointer fields distinguish "unset" from a zero value.
	for _, want := range []string{
		"MAIL3_SMTP_MAX_MESSAGE_SIZE=52428800\n",
		"MAIL3_SMTP_MIN_TRANSFER_RATE=0\n",
		"MAIL3_SMTP_TRANSFER_GRACE_PERIOD=90\n",
		"MAIL3_SMTP_TRANSFER_STALL_TIMEOUT=600\n",
		"MAIL3_SMTP_QUEUE_WORKERS=8\n",
		"MAIL3_SMTP_QUEUE_POLL_INTERVAL=10s\n",
		"MAIL3_SMTP_MTASTS_ENFORCE=false\n",
		"MAIL3_DKIM_SELECTOR=s2026\n",
		"MAIL3_DKIM_BITS=4096\n",
	} {
		if !bytes.Contains(got, []byte(want)) {
			t.Errorf("rendered output missing policy line %q", want)
		}
	}
}

// TestRenderOmittedPolicyBlocksEmitNoLines proves omission is additive-safe: a
// manifest with neither block must not emit any MAIL3_SMTP_*/MAIL3_DKIM_* line.
func TestRenderOmittedPolicyBlocksEmitNoLines(t *testing.T) {
	m := &Manifest{Project: "p"}
	out, err := Render(m)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, absent := range []string{
		"MAIL3_SMTP_MAX_MESSAGE_SIZE",
		"MAIL3_SMTP_MIN_TRANSFER_RATE",
		"MAIL3_SMTP_QUEUE_WORKERS",
		"MAIL3_SMTP_QUEUE_POLL_INTERVAL",
		"MAIL3_SMTP_MTASTS_ENFORCE",
		"MAIL3_DKIM_SELECTOR",
		"MAIL3_DKIM_BITS",
	} {
		if bytes.Contains(out, []byte(absent)) {
			t.Errorf("omitted block still emitted %q\ngot:\n%s", absent, out)
		}
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	_, err := Parse([]byte("domain: x.test\nbogus_field: 1\n"))
	if err == nil {
		t.Fatal("expected strict parse to reject unknown field, got nil")
	}
}

// TestRenderMultiDomainMatchesGolden is the multi-domain counterpart to the
// drift guard: a manifest declaring additional served domains must render
// byte-for-byte to config_multidomain.env — pinning the MAIL3_SERVED_HOSTNAMES
// and MAIL3_SEED_SERVED_DOMAINS lines and their per-domain server_type mapping.
func TestRenderMultiDomainMatchesGolden(t *testing.T) {
	dir := "testdata"

	raw, err := os.ReadFile(filepath.Join(dir, "manifest_multidomain.yml"))
	if err != nil {
		t.Fatalf("read manifest_multidomain: %v", err)
	}
	m, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse manifest_multidomain: %v", err)
	}
	got, err := Render(m)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want, err := os.ReadFile(filepath.Join(dir, "config_multidomain.env"))
	if err != nil {
		t.Fatalf("read config_multidomain.env: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("rendered config_multidomain.env is stale — re-run render.\n--- got ---\n%s\n--- committed ---\n%s", got, want)
	}
	for _, want := range []string{
		"MAIL3_SERVED_HOSTNAMES=mail3.test,shop.test,legacy.test\n",
		"MAIL3_SEED_SERVED_DOMAINS=shop.test:restmail,legacy.test:traditional\n",
	} {
		if !bytes.Contains(got, []byte(want)) {
			t.Errorf("multi-domain output missing %q", want)
		}
	}
}

// TestRenderOmittedDomainsEmitNoServedLines proves the multi-domain block is
// purely additive: a manifest with no `domains:` list must not emit any
// MAIL3_SERVED_* line, so single-domain manifests render exactly as before.
func TestRenderOmittedDomainsEmitNoServedLines(t *testing.T) {
	m := &Manifest{Domain: "x.test", Project: "p"}
	out, err := Render(m)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, absent := range []string{"MAIL3_SERVED_HOSTNAMES", "MAIL3_SEED_SERVED_DOMAINS"} {
		if bytes.Contains(out, []byte(absent)) {
			t.Errorf("no-domains manifest still emitted %q\ngot:\n%s", absent, out)
		}
	}
}

func TestParseRejectsUnknownDomainField(t *testing.T) {
	src := "domain: p.test\ndomains:\n  - name: a.test\n    bogus_field: 1\n"
	if _, err := Parse([]byte(src)); err == nil {
		t.Fatal("expected strict parse to reject unknown field inside a domains entry, got nil")
	}
}

func TestValidateRejectsBadDomains(t *testing.T) {
	cases := map[string]string{
		"empty name":         "domain: p.test\ndomains:\n  - name: \"\"\n",
		"invalid servertype": "domain: p.test\ndomains:\n  - { name: a.test, server_type: bogus }\n",
		"duplicate name":     "domain: p.test\ndomains:\n  - { name: a.test }\n  - { name: a.test }\n",
		"primary duplicated": "domain: p.test\ndomains:\n  - { name: p.test }\n",
	}
	for label, src := range cases {
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("%s: expected Parse to reject, got nil", label)
		}
	}
}

func TestValidateAcceptsGoodDomains(t *testing.T) {
	src := "domain: p.test\ndomains:\n  - { name: a.test, server_type: restmail }\n  - { name: b.test, server_type: traditional }\n  - { name: c.test }\n"
	if _, err := Parse([]byte(src)); err != nil {
		t.Fatalf("expected valid multi-domain manifest to parse, got %v", err)
	}
}

func TestServedDomainsResolvesDefaults(t *testing.T) {
	bits := 2048
	m := &Manifest{
		Domain:   "primary.test",
		Hostname: "mail.primary.test",
		Domains: []DomainEntry{
			{Name: "extra.test"}, // no server_type/hostname/dkim → defaults
			{Name: "shop.test", ServerType: "traditional", Hostname: "mx.shop.test"},
		},
	}
	m.Domains[0].DKIM.Selector = "sel0"
	m.Domains[1].DKIM.Bits = &bits

	got := m.ServedDomains()
	if len(got) != 3 {
		t.Fatalf("ServedDomains len = %d, want 3", len(got))
	}
	// primary
	if !got[0].Primary || got[0].Name != "primary.test" || got[0].Hostname != "mail.primary.test" || got[0].ServerType != "restmail" {
		t.Errorf("primary resolved wrong: %+v", got[0])
	}
	// extra.test: hostname defaults to name, server_type defaults to restmail
	if got[1].Hostname != "extra.test" || got[1].ServerType != "restmail" || got[1].Selector != "sel0" {
		t.Errorf("extra.test resolved wrong: %+v", got[1])
	}
	// shop.test: explicit hostname + traditional + bits
	if got[2].Hostname != "mx.shop.test" || got[2].ServerType != "traditional" || got[2].Bits == nil || *got[2].Bits != 2048 {
		t.Errorf("shop.test resolved wrong: %+v", got[2])
	}
	if add := m.AdditionalServedDomains(); len(add) != 2 || add[0].Name != "extra.test" {
		t.Errorf("AdditionalServedDomains wrong: %+v", add)
	}
}

func TestParseSeedServedDomainsRoundTrip(t *testing.T) {
	got, err := ParseSeedServedDomains("shop.test:restmail,legacy.test:traditional")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 2 || got[0] != (SeedServedDomain{"shop.test", "restmail"}) || got[1] != (SeedServedDomain{"legacy.test", "traditional"}) {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	// Empty → no entries (single-domain instances seed unchanged).
	if got, err := ParseSeedServedDomains(""); err != nil || got != nil {
		t.Fatalf("empty spec: got %+v err %v, want nil,nil", got, err)
	}
	// Missing server_type defaults to restmail.
	if got, _ := ParseSeedServedDomains("a.test"); len(got) != 1 || got[0].ServerType != "restmail" {
		t.Fatalf("default server_type: got %+v", got)
	}
	// Invalid server_type is rejected.
	if _, err := ParseSeedServedDomains("a.test:bogus"); err == nil {
		t.Fatal("expected invalid server_type to error")
	}
}

func TestRenderRejectsUnknownComponent(t *testing.T) {
	m := &Manifest{Components: []Component{{Name: "gopher", IP: "10.0.0.1"}}}
	if _, err := Render(m); err == nil {
		t.Fatal("expected error for unknown component, got nil")
	}
}

func TestRenderMapsPortsAndIPs(t *testing.T) {
	m := &Manifest{
		Project: "p",
		Components: []Component{
			{Name: "smtp-gateway", IP: "10.0.0.9", Ports: map[string]int{"inbound": 25, "submission": 587, "submission_tls": 465}},
		},
	}
	out, err := Render(m)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"MAIL3_SMTP_IP=10.0.0.9\n",
		"MAIL3_SMTP_PORT_INBOUND=25\n",
		"MAIL3_SMTP_PORT_SUBMISSION=587\n",
		"MAIL3_SMTP_PORT_SUBMISSION_TLS=465\n",
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("rendered output missing %q\ngot:\n%s", want, out)
		}
	}
}
