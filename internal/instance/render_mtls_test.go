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
