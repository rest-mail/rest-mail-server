package dmarcreceive

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
)

func exampleXML(namespace string) string {
	return `<feedback` + namespace + `><report_metadata><org_name>Reporter</org_name><email>reports@reporter.test</email><report_id>daily-1</report_id><date_range><begin>1793577600</begin><end>1793663999</end></date_range></report_metadata><policy_published><domain>hosted.test</domain><p>none</p></policy_published><record><row><source_ip>192.0.2.1</source_ip><count>12</count><policy_evaluated><disposition>none</disposition><dkim>pass</dkim><spf>fail</spf></policy_evaluated></row><identifiers><header_from>hosted.test</header_from></identifiers><auth_results><dkim><domain>hosted.test</domain><selector>mail</selector><result>pass</result></dkim><spf><domain>other.test</domain><result>fail</result><scope>mfrom</scope></spf></auth_results></record></feedback>`
}

func gzipData(t *testing.T, raw string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func zipData(t *testing.T, entries ...string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, raw := range entries {
		entry, err := writer.Create("report.xml")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestParseFormats(t *testing.T) {
	for _, namespace := range []string{"", ` xmlns="urn:ietf:params:xml:ns:dmarc-2.0"`} {
		raw := exampleXML(namespace)
		if namespace != "" {
			raw = strings.Replace(raw, "<p>none</p>", "<p>quarantine</p><np>reject</np><discovery_method>treewalk</discovery_method><testing>y</testing>", 1)
			raw = strings.Replace(raw, "<disposition>none</disposition>", "<disposition>pass</disposition>", 1)
			raw = strings.Replace(raw, "<scope>mfrom</scope>", "<scope>mfrom</scope><human_result>SPF did not pass</human_result>", 1)
		}
		for media, data := range map[string][]byte{"text/xml": []byte(raw), "application/gzip": gzipData(t, raw), "application/zip": zipData(t, raw)} {
			t.Run(media+namespace, func(t *testing.T) {
				reports, err := Parse(media, data)
				if err != nil {
					t.Fatal(err)
				}
				if len(reports) != 1 || reports[0].ReportID != "daily-1" || reports[0].Records[0].Row.Count != 12 || reports[0].PolicyPublished.Domain != "hosted.test" {
					t.Fatalf("unexpected reports: %+v", reports)
				}
				if namespace != "" && (reports[0].Records[0].Row.PolicyEvaluated.Disposition != "pass" || reports[0].PolicyPublished.NP != "reject" || reports[0].PolicyPublished.DiscoveryMethod != "treewalk" || reports[0].PolicyPublished.Testing != "y") {
					t.Fatalf("lost RFC 9990 fields: %+v", reports[0])
				}
				if namespace != "" && reports[0].Records[0].AuthResults.SPF[0].HumanResult != "SPF did not pass" {
					t.Fatal("lost SPF diagnostic result")
				}
			})
		}
	}
}

func TestParseHostile(t *testing.T) {
	raw := exampleXML("")
	row := raw[strings.Index(raw, "<record>") : strings.Index(raw, "</record>")+len("</record>")]
	tooMany := strings.Replace(raw, row, strings.Repeat(row, MaxRows+1), 1)
	for name, test := range map[string]struct {
		media string
		data  []byte
	}{
		"plain size":        {"text/xml", []byte(strings.Repeat("x", MaxBytes+1))},
		"gzip bomb":         {"application/gzip", gzipData(t, strings.Repeat("x", MaxBytes+1))},
		"zip bomb":          {"application/zip", zipData(t, strings.Repeat("x", MaxBytes+1))},
		"zip total":         {"application/zip", zipData(t, strings.Repeat("x", MaxBytes/2+1), strings.Repeat("x", MaxBytes/2+1))},
		"zip entries":       {"application/zip", zipData(t, make([]string, MaxZipEntries+1)...)},
		"rows":              {"text/xml", []byte(tooMany)},
		"external entity":   {"text/xml", []byte(`<!DOCTYPE feedback [<!ENTITY xxe SYSTEM "file:///etc/passwd">]>` + strings.Replace(raw, "Reporter", "&xxe;", 1))},
		"entity bomb":       {"text/xml", []byte(`<!DOCTYPE feedback [<!ENTITY a "boom">]>` + raw)},
		"namespace":         {"text/xml", []byte(exampleXML(` xmlns="urn:attacker"`))},
		"trailing document": {"text/xml", []byte(raw + raw)},
		"negative count":    {"text/xml", []byte(strings.Replace(raw, "<count>12", "<count>-1", 1))},
		"oversize field":    {"text/xml", []byte(strings.Replace(raw, "Reporter", strings.Repeat("x", 1025), 1))},
		"invalid IP":        {"text/xml", []byte(strings.Replace(raw, "192.0.2.1", "garbage", 1))},
		"reversed period":   {"text/xml", []byte(strings.Replace(raw, "1793663999", "1", 1))},
		"truncated gzip":    {"application/gzip", []byte{31, 139}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(test.media, test.data); err == nil {
				t.Fatal("accepted hostile report")
			}
		})
	}
}

func TestParseDomainNormalizationAndEpoch(t *testing.T) {
	raw := strings.Replace(exampleXML(""), "<domain>hosted.test", "<domain>BÜCHER.test.", 1)
	raw = strings.Replace(raw, "<begin>1793577600", "<begin>0", 1)
	reports, err := Parse("text/xml", []byte(raw))
	if err != nil || reports[0].PolicyPublished.Domain != "xn--bcher-kva.test" || reports[0].StartDate.Unix() != 0 {
		t.Fatalf("valid IDN or epoch report refused: %+v %v", reports, err)
	}
	raw = strings.Replace(raw, "<begin>0</begin>", "", 1)
	if _, err := Parse("text/xml", []byte(raw)); err == nil {
		t.Fatal("missing period boundary accepted")
	}
}

// TestAC01InflatedReportsAreSpooledAndRemoved is #2107 AC01 for the dmarc-report-mail path.
// Each gzip or zip document is inflated to a file Parse owns, read from that file, and removed
// before Parse returns: after success, after a refused document, and after an oversized one.
// Plain XML is already in the message and is not copied.
func TestAC01InflatedReportsAreSpooledAndRemoved(t *testing.T) {
	raw := exampleXML("")
	exact := strings.Replace(raw, "</feedback>", strings.Repeat(" ", MaxBytes-len(raw))+"</feedback>", 1)
	if len(exact) != MaxBytes {
		t.Fatalf("exact-limit fixture is %d bytes, want %d", len(exact), MaxBytes)
	}
	broken := strings.Replace(raw, "192.0.2.1", "garbage", 1)
	for name, test := range map[string]struct {
		media   string
		data    []byte
		spools  int
		reports int
	}{
		"gzip":                 {"application/gzip", gzipData(t, raw), 1, 1},
		"gzip at exact limit":  {"application/gzip", gzipData(t, exact), 1, 1},
		"zip with two entries": {"application/zip", zipData(t, raw, strings.Replace(raw, "daily-1", "daily-2", 1)), 2, 2},
		"gzip refused report":  {"application/gzip", gzipData(t, broken), 1, 0},
		"zip refused second":   {"application/zip", zipData(t, raw, broken), 2, 0},
		"gzip one byte over":   {"application/gzip", gzipData(t, exact+" "), 1, 0},
		"plain XML":            {"text/xml", []byte(raw), 0, 1},
	} {
		t.Run(name, func(t *testing.T) {
			var seen []string
			spoolCreated = func(path string) {
				if _, err := os.Stat(path); err != nil {
					t.Errorf("spool file %s missing while parsing: %v", path, err)
				}
				seen = append(seen, path)
			}
			t.Cleanup(func() { spoolCreated = nil })
			reports, err := Parse(test.media, test.data)
			if test.reports == 0 && err == nil {
				t.Fatal("accepted a report that must be refused")
			}
			if test.reports != 0 && (err != nil || len(reports) != test.reports) {
				t.Fatalf("got %d reports, error %v; want %d reports", len(reports), err, test.reports)
			}
			if len(seen) != test.spools {
				t.Fatalf("parse created %d spool files, want %d", len(seen), test.spools)
			}
			for _, path := range seen {
				if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("spool file %s left after Parse returned: %v", path, err)
				}
			}
		})
	}
}
