package instance

import "fmt"

// DKIMDrift is a per-domain discrepancy between the DKIM selector an instance
// manifest declares for a served domain and the selector actually provisioned
// for that domain in the database.
//
// It is what CheckDKIMSelectorDrift reports so selector drift is caught loudly
// instead of silently shipping — the failure mode #150 describes, where the
// manifest names one selector while the DB signs (and publishes its DNS record)
// under another, so outbound signing diverges from what the manifest documents.
type DKIMDrift struct {
	Domain   string // served domain name
	Expected string // selector the manifest declares (provisioner default applied)
	Actual   string // selector provisioned in the DB ("" when none is provisioned)
	Missing  bool   // true when the domain has no provisioned DKIM key at all
}

// String renders a one-line, operator-readable description of the drift.
func (d DKIMDrift) String() string {
	if d.Missing {
		return fmt.Sprintf("%s: manifest declares DKIM selector %q but no key is provisioned in the database", d.Domain, d.Expected)
	}
	return fmt.Sprintf("%s: manifest declares DKIM selector %q but the provisioned key uses %q", d.Domain, d.Expected, d.Actual)
}

// CheckDKIMSelectorDrift compares each served domain's manifest-declared DKIM
// selector against the selector actually provisioned in the database.
//
// provisioned maps a served domain's name to the DKIM selector under which its
// key is provisioned in the DB; a domain absent from the map (or mapped to "")
// has no provisioned key. defaultSelector is substituted for any served domain
// whose manifest leaves the selector blank, matching the provisioner default
// (`dkim-provision` selector "default") so the comparison reflects what is
// actually signed.
//
// It returns one DKIMDrift per served domain whose provisioned selector does not
// match the manifest — including domains with no provisioned key. An empty slice
// means the database is consistent with the manifest.
func (m *Manifest) CheckDKIMSelectorDrift(provisioned map[string]string, defaultSelector string) []DKIMDrift {
	var drift []DKIMDrift
	for _, d := range m.ServedDomains() {
		expected := d.Selector
		if expected == "" {
			expected = defaultSelector
		}
		actual := provisioned[d.Name]
		if actual == "" {
			drift = append(drift, DKIMDrift{Domain: d.Name, Expected: expected, Missing: true})
			continue
		}
		if actual != expected {
			drift = append(drift, DKIMDrift{Domain: d.Name, Expected: expected, Actual: actual})
		}
	}
	return drift
}
