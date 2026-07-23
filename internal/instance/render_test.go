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
