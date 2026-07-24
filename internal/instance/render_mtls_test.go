package instance

import (
	"strings"
	"testing"
)

func TestRenderEmitsInternalMTLSWhenEnabled(t *testing.T) {
	m := &Manifest{Project: "p", InternalMTLS: true}
	out, err := Render(m)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(string(out), "MAIL3_INTERNAL_MTLS=true") {
		t.Errorf("expected MAIL3_INTERNAL_MTLS=true in output, got:\n%s", out)
	}
}

func TestRenderOmitsInternalMTLSWhenDisabled(t *testing.T) {
	m := &Manifest{Project: "p"} // InternalMTLS defaults false
	out, err := Render(m)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(string(out), "MAIL3_INTERNAL_MTLS") {
		t.Errorf("did not expect MAIL3_INTERNAL_MTLS in output (avoids config.env drift), got:\n%s", out)
	}
}

func TestParseAcceptsInternalMTLS(t *testing.T) {
	m, err := Parse([]byte("domain: x.test\ninternal_mtls: true\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !m.InternalMTLS {
		t.Error("InternalMTLS = false, want true")
	}
}

// ── tls.internal block (PR6 / G7) ────────────────────────────────────────

// TestRenderTLSInternalModeEnablesMTLS proves the declarative block turns the
// switch on: mode require/verify — and an empty mode (default require) — all emit
// MAIL3_INTERNAL_MTLS=true, the same line the legacy `internal_mtls` bool emits.
func TestRenderTLSInternalModeEnablesMTLS(t *testing.T) {
	for _, mode := range []string{"require", "verify", ""} {
		m := &Manifest{Project: "p", TLS: TLS{Internal: &InternalTLS{Mode: mode}}}
		out, err := Render(m)
		if err != nil {
			t.Fatalf("render mode=%q: %v", mode, err)
		}
		if !strings.Contains(string(out), "MAIL3_INTERNAL_MTLS=true") {
			t.Errorf("mode=%q: expected MAIL3_INTERNAL_MTLS=true, got:\n%s", mode, out)
		}
	}
}

// TestRenderTLSInternalModeOffDisablesMTLS proves mode: off suppresses the line
// (routes stay tokenless on the public listener — pre-mTLS behavior).
func TestRenderTLSInternalModeOffDisablesMTLS(t *testing.T) {
	m := &Manifest{Project: "p", TLS: TLS{Internal: &InternalTLS{Mode: "off"}}}
	out, err := Render(m)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(string(out), "MAIL3_INTERNAL_MTLS") {
		t.Errorf("mode: off should emit no MAIL3_INTERNAL_MTLS line, got:\n%s", out)
	}
}

// TestRenderTLSInternalBlockBeatsLegacyBool proves the block is authoritative
// over the legacy bool when present: internal_mtls false + mode require → on.
func TestRenderTLSInternalBlockBeatsLegacyBool(t *testing.T) {
	m := &Manifest{Project: "p", InternalMTLS: false, TLS: TLS{Internal: &InternalTLS{Mode: "require"}}}
	out, err := Render(m)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(string(out), "MAIL3_INTERNAL_MTLS=true") {
		t.Errorf("block mode require should win over internal_mtls:false, got:\n%s", out)
	}
}

// TestRenderInternalCASourceNonDefault proves a non-default ca_source emits the
// provisioning switch consumed by `task instance:mtls:issue`.
func TestRenderInternalCASourceNonDefault(t *testing.T) {
	m := &Manifest{Project: "p", TLS: TLS{Internal: &InternalTLS{Mode: "require", CASource: "manual"}}}
	out, err := Render(m)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(string(out), "RESTMAIL_INTERNAL_CA_SOURCE=manual") {
		t.Errorf("expected RESTMAIL_INTERNAL_CA_SOURCE=manual, got:\n%s", out)
	}
}

// TestRenderInternalCASourceDefaultOmitsLine proves the default ca_source
// (testbed-certgen) and an empty ca_source render NO line, so the Taskfile
// `| default "testbed-certgen"` supplies it and the testbed is byte-identical.
func TestRenderInternalCASourceDefaultOmitsLine(t *testing.T) {
	for _, src := range []string{"", "testbed-certgen"} {
		m := &Manifest{Project: "p", TLS: TLS{Internal: &InternalTLS{Mode: "require", CASource: src}}}
		out, err := Render(m)
		if err != nil {
			t.Fatalf("render ca_source=%q: %v", src, err)
		}
		if strings.Contains(string(out), "RESTMAIL_INTERNAL_CA_SOURCE") {
			t.Errorf("ca_source=%q should emit no line, got:\n%s", src, out)
		}
	}
}

func TestParseAcceptsTLSInternalBlock(t *testing.T) {
	m, err := Parse([]byte("domain: x.test\ntls:\n  internal:\n    mode: require\n    ca_source: manual\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.TLS.Internal == nil || m.TLS.Internal.Mode != "require" || m.TLS.Internal.CASource != "manual" {
		t.Errorf("tls.internal parsed wrong: %+v", m.TLS.Internal)
	}
}

func TestParseRejectsUnknownTLSInternalKey(t *testing.T) {
	if _, err := Parse([]byte("domain: x.test\ntls:\n  internal:\n    bogus: 1\n")); err == nil {
		t.Fatal("expected strict parse to reject unknown key under tls.internal, got nil")
	}
}

func TestValidateRejectsBadTLSInternal(t *testing.T) {
	cases := map[string]string{
		"bad mode":      "domain: x.test\ntls:\n  internal:\n    mode: bogus\n",
		"bad ca_source": "domain: x.test\ntls:\n  internal:\n    ca_source: bogus\n",
		"conflict":      "domain: x.test\ninternal_mtls: true\ntls:\n  internal:\n    mode: off\n",
	}
	for label, src := range cases {
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("%s: expected Parse to reject, got nil", label)
		}
	}
}
