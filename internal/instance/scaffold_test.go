package instance

import (
	"bytes"
	"os"
	"path/filepath"
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

	res, err := Scaffold("mail4.test", dir)
	if err != nil {
		t.Fatalf("scaffold: %v", err)
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
	// Three distinct 64-hex-char secrets.
	if n := bytes.Count(res.Secrets, []byte("MAIL3_")); n != 3 {
		t.Errorf("expected 3 secret lines, got %d", n)
	}
}

func TestScaffoldAvoidsExistingIPs(t *testing.T) {
	dir := t.TempDir()

	// Write a first instance occupying the .50 block.
	first, err := Scaffold("first.test", dir)
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
	second, err := Scaffold("second.test", dir)
	if err != nil {
		t.Fatal(err)
	}
	if second.IPs["postgres"] != "10.99.0.58" {
		t.Errorf("second postgres IP = %s, want 10.99.0.58 (after first's 50-57 block)", second.IPs["postgres"])
	}
}
