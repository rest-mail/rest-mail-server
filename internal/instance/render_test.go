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
