package instance

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestRenderTLSBlockMatchesGolden is the with-block counterpart to the drift
// guard: a manifest that sets a non-default cert_provider plus the optional
// `tls:` block (extra_hostnames + acme) must render byte-for-byte to the
// committed config_tls.env, pinning the RESTMAIL_CERT_PROVIDER + RESTMAIL_TLS_*
// lines. Together with TestRenderMatchesCommittedConfig (no block → committed
// config.env, unchanged) this proves the seam is purely additive.
func TestRenderTLSBlockMatchesGolden(t *testing.T) {
	dir := "testdata"

	raw, err := os.ReadFile(filepath.Join(dir, "manifest_tls.yml"))
	if err != nil {
		t.Fatalf("read manifest_tls: %v", err)
	}
	m, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse manifest_tls: %v", err)
	}
	got, err := Render(m)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want, err := os.ReadFile(filepath.Join(dir, "config_tls.env"))
	if err != nil {
		t.Fatalf("read config_tls.env: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("rendered config_tls.env is stale — re-run render.\n--- got ---\n%s\n--- committed ---\n%s", got, want)
	}
	for _, want := range []string{
		"RESTMAIL_CERT_PROVIDER=manual\n",
		"RESTMAIL_TLS_EXTRA_HOSTNAMES=autoconfig.mail3.test,mta-sts.mail3.test\n",
		"RESTMAIL_TLS_CERT_SANS=mail3.test,autoconfig.mail3.test,mta-sts.mail3.test\n",
		"RESTMAIL_TLS_ACME_ENABLED=false\n",
		"RESTMAIL_TLS_ACME_EMAIL=hostmaster@mail3.test\n",
		"RESTMAIL_TLS_ACME_STAGING=true\n",
		"RESTMAIL_TLS_ACME_DIRECTORY=https://acme-staging-v02.api.letsencrypt.org/directory\n",
	} {
		if !bytes.Contains(got, []byte(want)) {
			t.Errorf("tls output missing line %q", want)
		}
	}
}

// TestRenderOmittedTLSBlockEmitsNoLines proves omission is additive-safe: a
// manifest with neither a `tls:` block nor a non-default cert_provider must not
// emit any RESTMAIL_CERT_PROVIDER / RESTMAIL_TLS_* line, so existing manifests
// render exactly as before. testbed-certgen (the default) must render no
// provider line.
func TestRenderOmittedTLSBlockEmitsNoLines(t *testing.T) {
	m := &Manifest{Domain: "x.test", Project: "p", CertProvider: "testbed-certgen"}
	out, err := Render(m)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, absent := range []string{
		"RESTMAIL_CERT_PROVIDER",
		"RESTMAIL_TLS_EXTRA_HOSTNAMES",
		"RESTMAIL_TLS_CERT_SANS",
		"RESTMAIL_TLS_ACME_ENABLED",
		"RESTMAIL_TLS_ACME_EMAIL",
		"RESTMAIL_TLS_ACME_STAGING",
		"RESTMAIL_TLS_ACME_DIRECTORY",
	} {
		if bytes.Contains(out, []byte(absent)) {
			t.Errorf("no-tls manifest still emitted %q\ngot:\n%s", absent, out)
		}
	}
}

// TestRenderEmitsCertProviderOnlyWhenNonDefault pins the emit-when-non-default
// discipline for the cert-provider switch line: the default and empty values
// render no line (byte-identical to before), a non-default value renders it.
func TestRenderEmitsCertProviderOnlyWhenNonDefault(t *testing.T) {
	cases := map[string]bool{ // cert_provider -> line expected?
		"":                false, // unset
		"testbed-certgen": false, // the default
		"manual":          true,
		"letsencrypt":     true,
	}
	for provider, wantLine := range cases {
		m := &Manifest{Domain: "x.test", Project: "p", CertProvider: provider}
		out, err := Render(m)
		if err != nil {
			t.Fatalf("render %q: %v", provider, err)
		}
		has := bytes.Contains(out, []byte("RESTMAIL_CERT_PROVIDER="))
		if has != wantLine {
			t.Errorf("cert_provider %q: RESTMAIL_CERT_PROVIDER present=%v, want %v", provider, has, wantLine)
		}
		if wantLine && !bytes.Contains(out, []byte("RESTMAIL_CERT_PROVIDER="+provider+"\n")) {
			t.Errorf("cert_provider %q: expected value line not found\ngot:\n%s", provider, out)
		}
	}
}

// TestCertSANHostnames proves the SAN set = served hostnames ∪ extra_hostnames,
// primary first, de-duplicated in first-seen order. Covers the multi-domain
// case (every served domain's hostname appears) and the overlap case (an
// extra_hostname that duplicates a served hostname is not repeated).
func TestCertSANHostnames(t *testing.T) {
	// Bare single-domain, no extras → exactly [hostname]. This is the default
	// issuance input and must stay a single name.
	single := &Manifest{Domain: "mail3.test", Hostname: "mail3.test"}
	if got := single.CertSANHostnames(); len(got) != 1 || got[0] != "mail3.test" {
		t.Fatalf("single-domain SANs = %v, want [mail3.test]", got)
	}

	// Multi-domain + extra_hostnames: every served hostname, primary first, then
	// the (non-duplicate) extras.
	m := &Manifest{
		Domain:   "mail3.test",
		Hostname: "mail3.test",
		Domains: []DomainEntry{
			{Name: "shop.test"}, // hostname defaults to name
			{Name: "legacy.test", Hostname: "mx.legacy.test"}, // explicit hostname
		},
		TLS: TLS{ExtraHostnames: []string{
			"mta-sts.mail3.test",
			"mail3.test", // duplicate of the primary served hostname → dropped
		}},
	}
	got := m.CertSANHostnames()
	want := []string{"mail3.test", "shop.test", "mx.legacy.test", "mta-sts.mail3.test"}
	if len(got) != len(want) {
		t.Fatalf("SANs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SANs[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// TestParseRejectsUnknownTLSField guards strict parsing of the new block:
// unknown keys under `tls:` and under `tls.acme:` still error, so typos fail
// loudly rather than being silently dropped.
func TestParseRejectsUnknownTLSField(t *testing.T) {
	cases := map[string]string{
		"unknown tls key":  "domain: x.test\ntls:\n  bogus_field: 1\n",
		"unknown acme key": "domain: x.test\ntls:\n  acme:\n    bogus_field: 1\n",
	}
	for label, src := range cases {
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("%s: expected strict parse to reject, got nil", label)
		}
	}
}

// TestParseAcceptsTLSBlock proves a well-formed tls block parses and the
// pointer/list fields survive the round trip.
func TestParseAcceptsTLSBlock(t *testing.T) {
	src := "domain: x.test\n" +
		"tls:\n" +
		"  extra_hostnames: [autoconfig.x.test, mta-sts.x.test]\n" +
		"  acme:\n" +
		"    enabled: true\n" +
		"    email: admin@x.test\n" +
		"    staging: false\n" +
		"    directory: https://acme.example/dir\n"
	m, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(m.TLS.ExtraHostnames) != 2 || m.TLS.ExtraHostnames[0] != "autoconfig.x.test" {
		t.Errorf("extra_hostnames wrong: %v", m.TLS.ExtraHostnames)
	}
	if m.TLS.ACME.Enabled == nil || !*m.TLS.ACME.Enabled {
		t.Errorf("acme.enabled = %v, want true", m.TLS.ACME.Enabled)
	}
	if m.TLS.ACME.Staging == nil || *m.TLS.ACME.Staging {
		t.Errorf("acme.staging = %v, want false", m.TLS.ACME.Staging)
	}
	if m.TLS.ACME.Email != "admin@x.test" || m.TLS.ACME.Directory != "https://acme.example/dir" {
		t.Errorf("acme email/directory wrong: %q / %q", m.TLS.ACME.Email, m.TLS.ACME.Directory)
	}
}
