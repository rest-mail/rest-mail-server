package dmarc

import (
	"bytes"
	"compress/gzip"
	"encoding/xml"
	"io"
	"testing"

	"github.com/restmail/restmail/internal/db/models"
)

func TestAggregateRecords_GroupsAndCounts(t *testing.T) {
	recs := []models.DMARCAggregateRecord{
		{Domain: "example.test", SourceIP: "10.0.0.1", HeaderFrom: "a@example.test", Disposition: "none", DKIMResult: "pass", DKIMAligned: true, SPFResult: "pass", SPFAligned: true},
		{Domain: "example.test", SourceIP: "10.0.0.1", HeaderFrom: "a@example.test", Disposition: "none", DKIMResult: "pass", DKIMAligned: true, SPFResult: "pass", SPFAligned: true},
		{Domain: "example.test", SourceIP: "10.0.0.2", HeaderFrom: "a@example.test", Disposition: "reject", DKIMResult: "fail", DKIMAligned: false, SPFResult: "fail", SPFAligned: false},
	}
	rows := AggregateRecords(recs)
	if len(rows) != 2 {
		t.Fatalf("want 2 aggregated rows, got %d", len(rows))
	}
	if rows[0].Row.SourceIP != "10.0.0.1" || rows[0].Row.Count != 2 {
		t.Errorf("first row: %+v", rows[0].Row)
	}
	if rows[0].Row.PolicyEvaluated.DKIM != "pass" || rows[0].Row.PolicyEvaluated.Disposition != "none" {
		t.Errorf("first row eval: %+v", rows[0].Row.PolicyEvaluated)
	}
	if rows[1].Row.Count != 1 || rows[1].Row.PolicyEvaluated.Disposition != "reject" || rows[1].Row.PolicyEvaluated.DKIM != "fail" {
		t.Errorf("second row: %+v", rows[1].Row)
	}
}

func TestEvaluated_RequiresPassAndAlign(t *testing.T) {
	if evaluated("pass", false) != "fail" {
		t.Error("pass-but-unaligned must evaluate to fail")
	}
	if evaluated("pass", true) != "pass" {
		t.Error("pass+aligned must evaluate to pass")
	}
	if evaluated("fail", true) != "fail" {
		t.Error("fail must evaluate to fail")
	}
}

func TestBuildReport_ValidXML(t *testing.T) {
	meta := ReportMetadata{
		OrgName:   "restmail.test",
		Email:     "dmarc@restmail.test",
		ReportID:  "report-1",
		DateRange: DateRange{Begin: 1784700000, End: 1784786400},
	}
	policy := PolicyPublished{Domain: "example.test", ADKIM: "r", ASPF: "r", P: "reject", PCT: 100}
	recs := []models.DMARCAggregateRecord{
		{Domain: "example.test", SourceIP: "10.0.0.1", HeaderFrom: "a@example.test", Disposition: "none", DKIMResult: "pass", DKIMAligned: true, SPFResult: "pass", SPFAligned: true},
	}
	xmlBytes, err := BuildReport(meta, policy, recs)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(xmlBytes, []byte("<?xml")) {
		t.Error("missing XML declaration")
	}
	// Round-trip: unmarshal and check the key fields survived.
	var fb Feedback
	if err := xml.Unmarshal(xmlBytes, &fb); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if fb.ReportMetadata.OrgName != "restmail.test" || fb.PolicyPublished.Domain != "example.test" || fb.PolicyPublished.P != "reject" {
		t.Errorf("metadata/policy not preserved: %+v / %+v", fb.ReportMetadata, fb.PolicyPublished)
	}
	if len(fb.Records) != 1 || fb.Records[0].Row.SourceIP != "10.0.0.1" || fb.Records[0].Row.Count != 1 {
		t.Errorf("record not preserved: %+v", fb.Records)
	}
	if len(fb.Records[0].AuthResults.DKIM) != 1 || fb.Records[0].AuthResults.DKIM[0].Result != "pass" {
		t.Errorf("auth_results dkim not preserved: %+v", fb.Records[0].AuthResults)
	}
}

func TestGzip_RoundTrip(t *testing.T) {
	data := []byte("<?xml version=\"1.0\"?><feedback>hello</feedback>")
	gz, err := Gzip(data)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if !bytes.Equal(got, data) {
		t.Errorf("gzip round-trip mismatch")
	}
}
