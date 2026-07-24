package instance

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlugFor(t *testing.T) {
	cases := map[string]string{
		"mail4.test":  "mail4",
		"acme.example": "acme",
		"MAIL5.Test":  "mail5",
		"a-b.co":      "ab",
	}
	for in, want := range cases {
		if got := slugFor(in); got != want {
			t.Errorf("slugFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAllocateBlock(t *testing.T) {
	// Empty -> starts at allocStart.
	if got, _ := allocateBlock(map[int]bool{}, 8); got != allocStart {
		t.Errorf("empty: got %d, want %d", got, allocStart)
	}
	// 50..57 taken -> next block is 58.
	used := map[int]bool{}
	for o := 50; o <= 57; o++ {
		used[o] = true
	}
	if got, _ := allocateBlock(used, 8); got != 58 {
		t.Errorf("after 50-57: got %d, want 58", got)
	}
	// A hole big enough is used before a later gap: free 60-67 (mark 50-59 used).
	used2 := map[int]bool{}
	for o := 50; o <= 59; o++ {
		used2[o] = true
	}
	if got, _ := allocateBlock(used2, 8); got != 60 {
		t.Errorf("hole: got %d, want 60", got)
	}
}

func TestScaffoldEndToEnd(t *testing.T) {
	dir := t.TempDir()

	res, err := Scaffold("mail4.test", dir, "testbed")
	if err != nil {
		t.Fatalf("scaffold: %v", err)
	}
	if res.Profile != "testbed" {
		t.Errorf("profile = %q, want testbed", res.Profile)
	}
	if res.Slug != "mail4" {
		t.Errorf("slug = %q, want mail4", res.Slug)
	}
	// First instance in an empty dir gets the .50 block, in order.
	want := map[string]string{
		"postgres": "10.99.0.50", "api": "10.99.0.51", "smtp-gateway": "10.99.0.52",
		"imap-gateway": "10.99.0.53", "pop3-gateway": "10.99.0.54", "js-filter": "10.99.0.55",
		"webmail": "10.99.0.56", "admin": "10.99.0.57",
	}
	for name, ip := range want {
		if res.IPs[name] != ip {
			t.Errorf("IP[%s] = %s, want %s", name, res.IPs[name], ip)
		}
	}
	// Generated manifest must parse and render to exactly the returned config.
	m, err := Parse(res.Manifest)
	if err != nil {
		t.Fatalf("generated manifest invalid: %v", err)
	}
	rendered, err := Render(m)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.Equal(rendered, res.Config) {
		t.Error("scaffold Config does not match Render(manifest)")
	}
	// Project must be per-instance to avoid container-name collisions.
	if !bytes.Contains(res.Manifest, []byte("project:        rest-mail-mail4")) {
		t.Error("manifest missing per-instance project rest-mail-mail4")
	}
	// Secondary instances are mailnet-only so they can run beside the primary.
	if !bytes.Contains(res.Config, []byte("MAIL3_MAILNET_ONLY=true\n")) {
		t.Error("scaffolded config.env should set MAIL3_MAILNET_ONLY=true")
	}
	// Internal mTLS is secure-by-default for NEW instances: the scaffolded
	// manifest sets internal_mtls: true and its rendered config.env carries the
	// switch, so `task instance:new` (which auto-provisions the certs) brings the
	// instance up with the gateway→API handshake enforced. This is the "on by
	// default at the instance layer" posture — the compiled-in config default
	// stays off so a bare binary is unaffected.
	if !bytes.Contains(res.Manifest, []byte("internal_mtls: true")) {
		t.Error("scaffolded manifest should set internal_mtls: true (secure-by-default for new instances)")
	}
	if !bytes.Contains(res.Config, []byte("MAIL3_INTERNAL_MTLS=true\n")) {
		t.Error("scaffolded config.env should set MAIL3_INTERNAL_MTLS=true")
	}
	// Three distinct 64-hex-char secrets.
	if n := bytes.Count(res.Secrets, []byte("MAIL3_")); n != 3 {
		t.Errorf("expected 3 secret lines, got %d", n)
	}
}

func TestScaffoldAvoidsExistingIPs(t *testing.T) {
	dir := t.TempDir()

	// Write a first instance occupying the .50 block.
	first, err := Scaffold("first.test", dir, "testbed")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "first.test"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "first.test", "manifest.yml"), first.Manifest, 0o644); err != nil {
		t.Fatal(err)
	}

	// Second instance must not reuse the .50 block.
	second, err := Scaffold("second.test", dir, "testbed")
	if err != nil {
		t.Fatal(err)
	}
	if second.IPs["postgres"] != "10.99.0.58" {
		t.Errorf("second postgres IP = %s, want 10.99.0.58 (after first's 50-57 block)", second.IPs["postgres"])
	}
}

// TestScaffoldTestbedGolden pins the testbed profile byte-for-byte. This is the
// contract that keeps the testbed + e2e working: the default scaffold output
// (empty dir → .50 block, mail4.test) must exactly equal the committed golden.
// The golden files were captured from the pre-PR7 scaffold, so any drift in the
// testbed template — including an accidental change from adding the --profile
// seam — fails here. Both "" and "testbed" select this profile.
func TestScaffoldTestbedGolden(t *testing.T) {
	for _, profile := range []string{"testbed", ""} {
		t.Run("profile="+profile, func(t *testing.T) {
			res, err := Scaffold("mail4.test", t.TempDir(), profile)
			if err != nil {
				t.Fatalf("scaffold: %v", err)
			}
			assertGolden(t, "scaffold_testbed.manifest.yml", res.Manifest)
			assertGolden(t, "scaffold_testbed.config.env", res.Config)
		})
	}
}

// TestScaffoldHostGolden pins the host profile and proves it is a real-host
// substrate with NO testbed leakage: it renders to the committed golden, the
// generated manifest passes strict Parse (which also runs Validate), re-renders
// identically (so `instance render -check` is green from birth), and carries
// none of the testbed substrate (10.99.0.x IPs, testbed_* volumes/dnsmasq,
// ghcr.io/rest-mail registry).
func TestScaffoldHostGolden(t *testing.T) {
	res, err := Scaffold("mail4.test", t.TempDir(), "host")
	if err != nil {
		t.Fatalf("scaffold host: %v", err)
	}
	if res.Profile != "host" {
		t.Errorf("profile = %q, want host", res.Profile)
	}
	assertGolden(t, "scaffold_host.manifest.yml", res.Manifest)
	assertGolden(t, "scaffold_host.config.env", res.Config)

	// The host manifest must strict-parse (Parse runs Validate) and render to
	// exactly the config.env scaffold emitted — i.e. `instance render -check`
	// passes with no edits, satisfying the CI drift guard.
	m, err := Parse(res.Manifest)
	if err != nil {
		t.Fatalf("host manifest invalid: %v", err)
	}
	rendered, err := Render(m)
	if err != nil {
		t.Fatalf("render host manifest: %v", err)
	}
	if !bytes.Equal(rendered, res.Config) {
		t.Error("host scaffold Config does not match Render(manifest) — check would be stale")
	}

	// Real-host posture: mailnet_only off, cert_provider manual, production.
	for _, want := range []string{
		"MAIL3_MAILNET_ONLY=false\n",
		"RESTMAIL_CERT_PROVIDER=manual\n",
		"MAIL3_ENVIRONMENT=production\n",
	} {
		if !bytes.Contains(res.Config, []byte(want)) {
			t.Errorf("host config.env missing %q", want)
		}
	}
	// Component IPs are unset — the deployer assigns addresses on their network.
	for _, name := range []string{"postgres", "api", "smtp-gateway", "imap-gateway", "pop3-gateway", "js-filter", "webmail", "admin"} {
		if res.IPs[name] != "" {
			t.Errorf("host IPs[%s] = %q, want unset", name, res.IPs[name])
		}
	}
	if !bytes.Contains(res.Config, []byte("MAIL3_POSTGRES_IP=\n")) {
		t.Error("host config.env should leave MAIL3_POSTGRES_IP unset")
	}
	// No testbed substrate leakage in the rendered config.env (values only; the
	// manifest keeps one 'NOT the testbed' clarifying comment by design).
	for _, leak := range []string{"10.99.0.", "testbed", "ghcr.io/rest-mail"} {
		if bytes.Contains(res.Config, []byte(leak)) {
			t.Errorf("host config.env leaks testbed substrate %q", leak)
		}
	}
}

// TestScaffoldUnknownProfile checks that an unrecognized --profile value fails
// loudly rather than silently falling back to a default.
func TestScaffoldUnknownProfile(t *testing.T) {
	_, err := Scaffold("mail4.test", t.TempDir(), "bogus")
	if err == nil {
		t.Fatal("expected error for unknown profile, got nil")
	}
	if !strings.Contains(err.Error(), "unknown profile") {
		t.Errorf("error = %v, want it to mention 'unknown profile'", err)
	}
}

// assertGolden compares got against testdata/<name>, failing with a readable
// diff-ish message on mismatch.
func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s does not match golden.\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}
