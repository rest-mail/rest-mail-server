package fleet

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/restmail/restmail/internal/instance"
)

// componentMeta is the per-component presentation knowledge: whether the
// component is fronted by the reverse proxy, and which ports it listens on when
// the manifest does not say. Ports the manifest DOES declare always win.
var componentMeta = map[string]struct {
	Proxy    string // proxy path segment, empty when not an HTTP service
	Internal []int  // container-only ports, when not declared in the manifest
}{
	"postgres":      {Internal: []int{5432}},
	"js-filter":     {Internal: []int{3100}},
	"api":           {Proxy: "api"},
	"smtp-gateway":  {},
	"imap-gateway":  {},
	"pop3-gateway":  {},
	"webmail":       {Proxy: "webmail", Internal: []int{3000}},
	"admin":         {Proxy: "admin", Internal: []int{3000}},
	"website":       {Proxy: "website"},
	"reference-ref": {},
}

// ServiceRow is one component's line in the status table.
type ServiceRow struct {
	Service  string
	State    State
	ProxyURL string // "" renders as an em dash
	Notes    string
}

// ConfigView is one config definition and the live state of its instance.
type ConfigView struct {
	Name    string
	Project string
	Rows    []ServiceRow
}

// Orphan is a running rest-mail service container that no config claims.
type Orphan struct {
	Container string
	State     State
	Ports     string
}

// ReferenceView is one reference mail server (Postfix/Dovecot/rspamd), which is
// not a rest-mail config: it is owned by rest-mail/reference-mailserver and
// identified only by its container names, mailref-<cfg>-<daemon>.
type ReferenceView struct {
	Name string
	Rows []ServiceRow
}

// TestbedView is the shared substrate every stack above sits on.
type TestbedView struct {
	Rows           []ServiceRow
	Network        string
	NetworkPresent bool
}

// Status is the whole fleet: every config, the reference stacks and substrate
// alongside them, plus containers nothing claims.
type Status struct {
	Configs    []ConfigView
	References []ReferenceView
	Testbed    TestbedView
	Orphans    []Orphan
	// Prefixes is the set of project prefixes the configs claim, which is what
	// orphan classification is relative to.
	Prefixes []string
}

// referenceDaemons is the ordered daemon set of a reference stack, with what
// each listens on inside the network — they publish nothing to the host.
var referenceDaemons = []struct{ Name, Notes string }{
	{"postfix", "internal :25 :465 :587"},
	{"dovecot", "internal :143 :993 :110 :995 :24"},
	{"rspamd", "internal :11332 :11333 :11334"},
	{"fail2ban", "internal (no listeners)"},
	{"postgres", "internal :5432"},
}

// DefaultNetwork is used for the substrate row when no manifest declares one.
const DefaultNetwork = "testbed_mailnet"

var mailrefName = regexp.MustCompile(`^mailref-([^-]+)-(.+)$`)

var serviceSuffix = regexp.MustCompile(
	`-(api|postgres|js-filter|smtp-gateway|imap-gateway|pop3-gateway|webmail|admin|website)$`)

// BuildStatus assembles the fleet view. Every docker read goes through d, so
// this is exercised in tests with a fake.
func BuildStatus(ctx context.Context, d Docker, sels []Selection) (Status, error) {
	var st Status
	claimed := map[string]bool{}

	for _, sel := range sels {
		m, err := sel.Load()
		if err != nil {
			return st, err
		}
		project := m.Project
		if project == "" {
			project = sel.Name
		}
		claimed[project] = true
		// The substrate network is a manifest fact; the first config to declare
		// one names it for the testbed block below.
		if st.Testbed.Network == "" && m.Network != "" {
			st.Testbed.Network = m.Network
		}
		view := ConfigView{Name: sel.Name, Project: project}
		for _, comp := range m.Components {
			c, err := d.Inspect(ctx, project+"-"+comp.Name)
			if err != nil {
				return st, err
			}
			view.Rows = append(view.Rows, ServiceRow{
				Service:  comp.Name,
				State:    c.State,
				ProxyURL: proxyURL(m, comp.Name),
				Notes:    notes(m, comp),
			})
		}
		st.Configs = append(st.Configs, view)
	}
	for p := range claimed {
		st.Prefixes = append(st.Prefixes, p)
	}
	sort.Strings(st.Prefixes)

	all, err := d.List(ctx)
	if err != nil {
		return st, err
	}

	// Reference stacks come from container names only. Reading the peer repo's
	// configs/ directory would make a dev clone an input to a runtime view, so a
	// stack that has never been started simply does not appear.
	var refNames []string
	seenRef := map[string]bool{}
	for _, c := range all {
		if m := mailrefName.FindStringSubmatch(c.Name); m != nil && !seenRef[m[1]] {
			seenRef[m[1]] = true
			refNames = append(refNames, m[1])
		}
	}
	sort.Strings(refNames)
	for _, name := range refNames {
		view := ReferenceView{Name: name}
		for _, dm := range referenceDaemons {
			c, err := d.Inspect(ctx, "mailref-"+name+"-"+dm.Name)
			if err != nil {
				return st, err
			}
			view.Rows = append(view.Rows, ServiceRow{Service: dm.Name, State: c.State, Notes: dm.Notes})
		}
		st.References = append(st.References, view)
	}

	// Substrate: the testbed's DNS plus the network everything shares.
	dns, err := d.Inspect(ctx, "testbed-dnsmasq")
	if err != nil {
		return st, err
	}
	st.Testbed.Rows = []ServiceRow{
		{Service: "dnsmasq", State: dns.State, Notes: "internal :53/tcp :53/udp"},
	}
	if st.Testbed.Network == "" {
		st.Testbed.Network = DefaultNetwork
	}
	if st.Testbed.NetworkPresent, err = d.NetworkExists(ctx, st.Testbed.Network); err != nil {
		return st, err
	}

	for _, c := range all {
		if c.State != StateUp || !serviceSuffix.MatchString(c.Name) {
			continue
		}
		if strings.HasPrefix(c.Name, "mailref-") || strings.HasPrefix(c.Name, "testbed-") {
			continue
		}
		owned := false
		for p := range claimed {
			if strings.HasPrefix(c.Name, p+"-") {
				owned = true
				break
			}
		}
		if !owned {
			st.Orphans = append(st.Orphans, Orphan{Container: c.Name, State: c.State, Ports: c.Ports})
		}
	}
	return st, nil
}

func proxyURL(m *instance.Manifest, component string) string {
	meta, ok := componentMeta[component]
	if !ok || meta.Proxy == "" || m.ProxyHost == "" {
		return ""
	}
	return "http://" + m.ProxyHost + "/" + meta.Proxy
}

// notes describes where a component listens. Ports the manifest declares are
// host-published unless the manifest is mailnet_only; anything else is the
// component's fixed container port, marked internal.
func notes(m *instance.Manifest, comp instance.Component) string {
	var declared []int
	if comp.Port != 0 {
		declared = append(declared, comp.Port)
	}
	for _, k := range sortedKeys(comp.Ports) {
		declared = append(declared, comp.Ports[k])
	}
	if len(declared) > 0 {
		prefix := ""
		if m.MailnetOnly {
			prefix = "internal "
		}
		return prefix + joinPorts(declared)
	}
	if meta, ok := componentMeta[comp.Name]; ok && len(meta.Internal) > 0 {
		return "internal " + joinPorts(meta.Internal)
	}
	return ""
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func joinPorts(ports []int) string {
	parts := make([]string, len(ports))
	for i, p := range ports {
		parts[i] = fmt.Sprintf(":%d", p)
	}
	return strings.Join(parts, " ")
}

// ── rendering ──────────────────────────────────────────────────────────────
//
// Widths are counted in runes, so the multi-byte state glyphs line up by
// construction — the shell version had to hand-pad around printf's byte-based
// padding.

const emDash = "—"

func pad(s string, w int) string {
	if n := utf8.RuneCountInString(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// RenderStatus writes the human-facing fleet table.
func RenderStatus(w io.Writer, st Status) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Legend:   ● up   ○ down   · absent   — not applicable")
	fmt.Fprintln(w, "  Columns:  PROXY URL      reverse-proxy vhost; HTTP services only")
	fmt.Fprintln(w, "            PORTS / NOTES  published on the host, unless marked \"internal\"")
	fmt.Fprintln(w)

	fmt.Fprintln(w, "  configs:  one block per config/<domain>")
	if len(st.Configs) == 0 {
		fmt.Fprintln(w, "    (none under config/ — restmail config scaffold <domain>)")
	}
	for _, cfg := range st.Configs {
		fmt.Fprintf(w, "\n    ▸ %s  (project %s)\n", cfg.Name, cfg.Project)
		renderTable(w, cfg.Rows)
	}

	// Reference stacks are peer-owned (rest-mail/reference-mailserver) and are
	// discovered from containers, so only ones that have been started show up.
	fmt.Fprintln(w, "\n  reference servers:  discovered from mailref-* containers")
	if len(st.References) == 0 {
		fmt.Fprintln(w, "    (none running — task -d .workspace/reference-mailserver up CONFIG=mail1)")
	}
	for _, ref := range st.References {
		fmt.Fprintf(w, "\n    ▸ %s\n", ref.Name)
		renderTable(w, ref.Rows)
	}

	fmt.Fprintln(w, "\n  testbed:")
	rows := append([]ServiceRow{}, st.Testbed.Rows...)
	net := StateAbsent
	if st.Testbed.NetworkPresent {
		net = StateExists
	}
	network := st.Testbed.Network
	if network == "" {
		network = DefaultNetwork
	}
	rows = append(rows, ServiceRow{
		Service: network,
		State:   net,
		Notes:   "docker network shared by everything above",
	})
	renderTable(w, rows)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "  orphan containers:  running instances whose config is gone")
	prefixes := strings.Join(st.Prefixes, " ")
	if prefixes == "" {
		prefixes = "(none — config/ holds no definitions)"
	}
	fmt.Fprintf(w, "    claimed prefixes: %s\n", prefixes)
	if len(st.Orphans) == 0 {
		fmt.Fprintln(w, "    (none)")
		return
	}
	fmt.Fprintf(w, "      %s %s %s\n", pad("CONTAINER", 30), pad("STATE", 9), "PORTS")
	fmt.Fprintf(w, "      %s %s %s\n",
		strings.Repeat("─", 30), strings.Repeat("─", 9), strings.Repeat("─", 13))
	for _, o := range st.Orphans {
		ports := o.Ports
		if ports == "" {
			ports = emDash
		}
		fmt.Fprintf(w, "      %s %s %s\n",
			pad(o.Container, 30), pad(o.State.Glyph()+" "+string(o.State), 9), ports)
	}
}

// renderTable writes one service table: header, rule, rows. Widths come from
// the content, so a name longer than its header (smtp-gateway) widens the
// column instead of shunting every column after it out of alignment, and rune
// counting keeps the multi-byte state glyphs aligned.
func renderTable(w io.Writer, rows []ServiceRow) {
	sw, tw, uw := len("SERVICE"), len("STATE"), len("PROXY URL")
	for _, r := range rows {
		sw = max(sw, utf8.RuneCountInString(r.Service))
		tw = max(tw, utf8.RuneCountInString(r.stateCell()))
		uw = max(uw, utf8.RuneCountInString(orDash(r.ProxyURL)))
	}
	writeRow(w, sw, tw, uw, "SERVICE", "STATE", "PROXY URL", "PORTS / NOTES")
	fmt.Fprintf(w, "      %s %s %s %s\n",
		strings.Repeat("─", sw), strings.Repeat("─", tw),
		strings.Repeat("─", uw), strings.Repeat("─", len("PORTS / NOTES")))
	for _, r := range rows {
		writeRow(w, sw, tw, uw, r.Service, r.stateCell(), orDash(r.ProxyURL), r.Notes)
	}
}

func (r ServiceRow) stateCell() string { return r.State.Glyph() + " " + string(r.State) }

func writeRow(w io.Writer, sw, tw, uw int, service, state, url, notes string) {
	fmt.Fprintf(w, "      %s %s %s %s\n", pad(service, sw), pad(state, tw), pad(url, uw), notes)
}

// orDash renders an inapplicable value as an em dash.
func orDash(s string) string {
	if s == "" {
		return emDash
	}
	return s
}
