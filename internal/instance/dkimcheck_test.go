package instance

import "testing"

// manifestWith builds a minimal manifest: a primary domain (with the given
// top-level DKIM selector) plus one additional served domain (with its own
// selector). A blank selector means "declare no selector" — the provisioner
// default then applies.
func manifestWith(primary, primarySel, extra, extraSel string) *Manifest {
	m := &Manifest{Domain: primary}
	m.DKIM.Selector = primarySel
	if extra != "" {
		de := DomainEntry{Name: extra}
		de.DKIM.Selector = extraSel
		m.Domains = []DomainEntry{de}
	}
	return m
}

func TestCheckDKIMSelectorDrift_NoDrift(t *testing.T) {
	m := manifestWith("primary.test", "s1", "extra.test", "s2")
	provisioned := map[string]string{
		"primary.test": "s1",
		"extra.test":   "s2",
	}
	if drift := m.CheckDKIMSelectorDrift(provisioned, "default"); len(drift) != 0 {
		t.Fatalf("expected no drift, got %v", drift)
	}
}

func TestCheckDKIMSelectorDrift_BlankSelectorUsesDefault(t *testing.T) {
	// A manifest that declares no selector expects the provisioner default; a DB
	// key under that default is consistent, a DB key under anything else drifts.
	m := manifestWith("primary.test", "", "", "")

	if drift := m.CheckDKIMSelectorDrift(map[string]string{"primary.test": "default"}, "default"); len(drift) != 0 {
		t.Fatalf("expected no drift when DB uses the default selector, got %v", drift)
	}

	drift := m.CheckDKIMSelectorDrift(map[string]string{"primary.test": "selectorX"}, "default")
	if len(drift) != 1 {
		t.Fatalf("expected 1 drift, got %d (%v)", len(drift), drift)
	}
	if drift[0].Expected != "default" || drift[0].Actual != "selectorX" {
		t.Errorf("drift = %+v, want Expected=default Actual=selectorX", drift[0])
	}
}

func TestCheckDKIMSelectorDrift_Mismatch(t *testing.T) {
	m := manifestWith("primary.test", "s1", "extra.test", "s2")
	// extra.test provisioned under the WRONG selector.
	provisioned := map[string]string{
		"primary.test": "s1",
		"extra.test":   "wrong",
	}
	drift := m.CheckDKIMSelectorDrift(provisioned, "default")
	if len(drift) != 1 {
		t.Fatalf("expected 1 drift, got %d (%v)", len(drift), drift)
	}
	if drift[0].Domain != "extra.test" || drift[0].Expected != "s2" || drift[0].Actual != "wrong" || drift[0].Missing {
		t.Errorf("drift = %+v, want {extra.test s2 wrong false}", drift[0])
	}
}

func TestCheckDKIMSelectorDrift_MissingKey(t *testing.T) {
	m := manifestWith("primary.test", "s1", "extra.test", "s2")
	// extra.test has no provisioned key at all (absent from the map).
	provisioned := map[string]string{"primary.test": "s1"}
	drift := m.CheckDKIMSelectorDrift(provisioned, "default")
	if len(drift) != 1 {
		t.Fatalf("expected 1 drift, got %d (%v)", len(drift), drift)
	}
	if drift[0].Domain != "extra.test" || !drift[0].Missing || drift[0].Expected != "s2" {
		t.Errorf("drift = %+v, want extra.test missing expected=s2", drift[0])
	}
	// An empty provisioned selector is treated the same as missing.
	drift = m.CheckDKIMSelectorDrift(map[string]string{"primary.test": "s1", "extra.test": ""}, "default")
	if len(drift) != 1 || !drift[0].Missing {
		t.Fatalf("empty selector should count as missing, got %v", drift)
	}
}
