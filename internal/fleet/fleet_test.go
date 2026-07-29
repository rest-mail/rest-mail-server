package fleet

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// fakeDocker answers from a map, so status logic is tested without a daemon.
type fakeDocker struct {
	states map[string]State
	list   []Container
}

func (f fakeDocker) Inspect(_ context.Context, name string) (Container, error) {
	s, ok := f.states[name]
	if !ok {
		return Container{Name: name, State: StateAbsent}, nil
	}
	return Container{Name: name, State: s}, nil
}
func (f fakeDocker) List(_ context.Context) ([]Container, error) { return f.list, nil }
func (f fakeDocker) NetworkExists(_ context.Context, _ string) (bool, error) {
	return true, nil
}

const testManifest = `domain:     mail4.test
hostname:   mail4.test
proxy_host: mail4.localhost
project:    rest-mail-mail4
network:    testbed_mailnet
registry:   ghcr.io/rest-mail
image_tag:  dev
db:
  name: restmail
  user: restmail
components:
  - { name: postgres, ip: 10.99.0.60 }
  - name: api
    ip: 10.99.0.61
    port: 8080
  - name: imap-gateway
    ip: 10.99.0.62
    ports: { plain: 143, tls: 993 }
  - { name: webmail, ip: 10.99.0.63 }
`

func writeConfig(t *testing.T, root, name, manifest string) {
	t.Helper()
	dir := filepath.Join(root, ConfigRoot, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.yml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A config named as a positional argument must be honoured — the whole reason
// this CLI exists, since `task <t> CONFIG=x` silently used the default instead.
func TestResolveHonoursPositionalArgument(t *testing.T) {
	root := t.TempDir()
	got, err := Resolve("mail4.test", root)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "mail4.test" {
		t.Errorf("name = %q, want mail4.test", got.Name)
	}
	want := filepath.Join(root, "config", "mail4.test", "manifest.yml")
	if got.ManifestPath != want {
		t.Errorf("manifest = %q, want %q", got.ManifestPath, want)
	}
}

func TestResolveDefaultAndEnv(t *testing.T) {
	root := t.TempDir()
	sel, err := Resolve("", root)
	if err != nil {
		t.Fatal(err)
	}
	if sel.Name != DefaultConfig {
		t.Errorf("default = %q, want %q", sel.Name, DefaultConfig)
	}

	t.Setenv("RESTMAIL_CONFIG", "from-env.test")
	sel, err = Resolve("", root)
	if err != nil {
		t.Fatal(err)
	}
	if sel.Name != "from-env.test" {
		t.Errorf("env = %q, want from-env.test", sel.Name)
	}

	// An explicit argument beats the environment.
	if sel, err = Resolve("explicit.test", root); err != nil || sel.Name != "explicit.test" {
		t.Errorf("explicit arg lost: %q (%v)", sel.Name, err)
	}
}

func TestResolvePathToManifest(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "mail4.test", testManifest)
	p := filepath.Join(root, "config", "mail4.test", "manifest.yml")

	sel, err := Resolve(p, root)
	if err != nil {
		t.Fatal(err)
	}
	if sel.ManifestPath != p {
		t.Errorf("manifest = %q, want %q", sel.ManifestPath, p)
	}
	if sel.Name != "mail4.test" {
		t.Errorf("name = %q, want mail4.test (from the containing dir)", sel.Name)
	}
	if _, err := sel.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
}

func TestBuildStatusReportsPerComponentState(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "mail4.test", testManifest)
	sels, err := Configs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(sels) != 1 {
		t.Fatalf("configs = %d, want 1", len(sels))
	}

	d := fakeDocker{states: map[string]State{
		"rest-mail-mail4-postgres": StateUp,
		"rest-mail-mail4-api":      StateDown,
		// imap-gateway absent entirely, webmail up
		"rest-mail-mail4-webmail": StateUp,
	}}
	st, err := BuildStatus(context.Background(), d, sels)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Configs) != 1 || len(st.Configs[0].Rows) != 4 {
		t.Fatalf("got %d configs / %d rows, want 1 / 4", len(st.Configs), len(st.Configs[0].Rows))
	}
	want := map[string]State{
		"postgres": StateUp, "api": StateDown, "imap-gateway": StateAbsent, "webmail": StateUp,
	}
	for _, r := range st.Configs[0].Rows {
		if want[r.Service] != r.State {
			t.Errorf("%s state = %q, want %q", r.Service, r.State, want[r.Service])
		}
	}
	// Declared ports surface as host-published; fixed container ports as internal.
	for _, r := range st.Configs[0].Rows {
		switch r.Service {
		case "api":
			if r.Notes != ":8080" {
				t.Errorf("api notes = %q, want :8080", r.Notes)
			}
			if r.ProxyURL != "http://mail4.localhost/api" {
				t.Errorf("api proxy = %q", r.ProxyURL)
			}
		case "imap-gateway":
			if r.Notes != ":143 :993" {
				t.Errorf("imap notes = %q, want :143 :993", r.Notes)
			}
		case "postgres":
			if r.Notes != "internal :5432" {
				t.Errorf("postgres notes = %q", r.Notes)
			}
			if r.ProxyURL != "" {
				t.Errorf("postgres should have no proxy URL, got %q", r.ProxyURL)
			}
		}
	}
}

func TestBuildStatusClassifiesOrphans(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "mail4.test", testManifest)
	sels, _ := Configs(root)

	d := fakeDocker{
		states: map[string]State{"rest-mail-mail4-api": StateUp},
		list: []Container{
			{Name: "rest-mail-mail4-api", State: StateUp},   // claimed by the config
			{Name: "rest-mail-api", State: StateUp},          // no config claims this prefix
			{Name: "mailref-mail1-postgres", State: StateUp}, // peer stack, never an orphan
			{Name: "testbed-dnsmasq", State: StateUp},        // substrate
			{Name: "rest-mail-old-api", State: StateDown},    // not running
			{Name: "some-random-thing", State: StateUp},      // not a rest-mail service name
		},
	}
	st, err := BuildStatus(context.Background(), d, sels)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Orphans) != 1 || st.Orphans[0].Container != "rest-mail-api" {
		t.Fatalf("orphans = %+v, want just rest-mail-api", st.Orphans)
	}
	if len(st.Prefixes) != 1 || st.Prefixes[0] != "rest-mail-mail4" {
		t.Errorf("prefixes = %v, want [rest-mail-mail4]", st.Prefixes)
	}
}

// The shell version mis-padded these because printf counts bytes; here widths
// are counted in runes, so every column must start at the same offset whichever
// glyph a row carries.
func TestRenderColumnsAlignAcrossGlyphs(t *testing.T) {
	st := Status{Configs: []ConfigView{{
		Name: "mail4.test", Project: "rest-mail-mail4",
		Rows: []ServiceRow{
			{Service: "api", State: StateUp, ProxyURL: "http://x/api", Notes: ":8080"},
			{Service: "postgres", State: StateAbsent, Notes: "internal :5432"},
			{Service: "webmail", State: StateDown, ProxyURL: "http://x/webmail", Notes: "internal :3000"},
		},
	}}}
	var sb strings.Builder
	RenderStatus(&sb, st)
	assertColumnsAlign(t, sb.String())
}

// assertColumnsAlign checks the PROXY URL column of every row against the header
// of the table that row belongs to. Widths are per-table by design — each block
// sizes to its own content — so the invariant is within a table, not across the
// whole page.
func assertColumnsAlign(t *testing.T, out string) {
	t.Helper()
	header, checked := -1, 0
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "SERVICE") && strings.Contains(line, "PROXY URL") {
			header = runeIndex(line, "PROXY URL")
			continue
		}
		// Table rows carry the 6-space body indent; the legend also has glyphs
		// but is not a row.
		if !strings.HasPrefix(line, "      ") {
			continue
		}
		if !strings.Contains(line, "●") && !strings.Contains(line, "○") && !strings.Contains(line, "·") {
			continue
		}
		at := runeIndex(line, "http://")
		if at < 0 {
			at = runeIndex(line, emDash)
		}
		if at < 0 {
			continue
		}
		if header < 0 {
			t.Fatalf("row before any header: %q", line)
		}
		checked++
		if at != header {
			t.Errorf("column at rune %d, table header at %d\n  row: %q", at, header, line)
		}
	}
	if checked == 0 {
		t.Fatal("no rows checked")
	}
}

// A component name wider than the column must widen the column, not push the
// remaining columns out of line — "smtp-gateway" is 12 chars and did exactly
// that when the widths were hardcoded.
func TestRenderWidensForLongServiceNames(t *testing.T) {
	st := Status{Configs: []ConfigView{{
		Name: "restmail.test", Project: "rest-mail",
		Rows: []ServiceRow{
			{Service: "api", State: StateUp, ProxyURL: "http://x/api", Notes: ":8080"},
			{Service: "smtp-gateway", State: StateAbsent, Notes: ":25 :587 :465"},
			{Service: "pop3-gateway", State: StateUp, Notes: ":110 :995"},
		},
	}}}
	var sb strings.Builder
	RenderStatus(&sb, st)
	assertColumnsAlign(t, sb.String())
}

// Reference stacks and the substrate must appear alongside the configs — the
// first cut of the CLI dropped both, which the Taskfile version showed.
func TestBuildStatusIncludesReferenceStacksAndTestbed(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "mail4.test", testManifest)
	sels, _ := Configs(root)

	d := fakeDocker{
		states: map[string]State{
			"mailref-mail1-postfix":  StateUp,
			"mailref-mail1-dovecot":  StateUp,
			"mailref-mail1-postgres": StateDown,
			// rspamd/fail2ban absent — must still be listed
			"mailref-mail2-postfix": StateUp,
			"testbed-dnsmasq":       StateUp,
		},
		list: []Container{
			{Name: "mailref-mail1-postfix", State: StateUp},
			{Name: "mailref-mail1-dovecot", State: StateUp},
			{Name: "mailref-mail2-postfix", State: StateUp},
			{Name: "testbed-dnsmasq", State: StateUp},
		},
	}
	st, err := BuildStatus(context.Background(), d, sels)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.References) != 2 {
		t.Fatalf("reference stacks = %d, want 2 (mail1, mail2)", len(st.References))
	}
	if st.References[0].Name != "mail1" || st.References[1].Name != "mail2" {
		t.Errorf("names = %q/%q, want mail1/mail2", st.References[0].Name, st.References[1].Name)
	}
	if got := len(st.References[0].Rows); got != len(referenceDaemons) {
		t.Errorf("mail1 rows = %d, want %d (absent daemons still listed)", got, len(referenceDaemons))
	}
	byName := map[string]State{}
	for _, r := range st.References[0].Rows {
		byName[r.Service] = r.State
	}
	for svc, want := range map[string]State{
		"postfix": StateUp, "dovecot": StateUp, "postgres": StateDown,
		"rspamd": StateAbsent, "fail2ban": StateAbsent,
	} {
		if byName[svc] != want {
			t.Errorf("mail1 %s = %q, want %q", svc, byName[svc], want)
		}
	}
	// A reference container is never an orphan, whatever its name suffix.
	if len(st.Orphans) != 0 {
		t.Errorf("orphans = %+v, want none", st.Orphans)
	}
	// Substrate: dnsmasq plus the network the manifest declares.
	if len(st.Testbed.Rows) != 1 || st.Testbed.Rows[0].State != StateUp {
		t.Errorf("testbed rows = %+v", st.Testbed.Rows)
	}
	if st.Testbed.Network != "testbed_mailnet" || !st.Testbed.NetworkPresent {
		t.Errorf("network = %q present=%v", st.Testbed.Network, st.Testbed.NetworkPresent)
	}

	var sb strings.Builder
	RenderStatus(&sb, st)
	out := sb.String()
	for _, want := range []string{"reference servers", "▸ mail1", "▸ mail2", "testbed:", "dnsmasq", "testbed_mailnet"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered output missing %q", want)
		}
	}
	assertColumnsAlign(t, out)
}

func runeIndex(s, substr string) int {
	i := strings.Index(s, substr)
	if i < 0 {
		return -1
	}
	return utf8.RuneCountInString(s[:i])
}
