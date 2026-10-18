package dmarcreport

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-dmarc"
)

func TestMailtoRUA(t *testing.T) {
	uri := func(u string, max uint64) dmarc.ReportURI { return dmarc.ReportURI{URI: u, MaxSize: max} }
	cases := []struct {
		name string
		ruas []dmarc.ReportURI
		want string
	}{
		{"first mailto", []dmarc.ReportURI{uri("mailto:agg@example.test", math.MaxUint64), uri("mailto:other@x.test", math.MaxUint64)}, "agg@example.test"},
		{"scheme in any case", []dmarc.ReportURI{uri("MAILTO:agg@example.test", math.MaxUint64)}, "agg@example.test"},
		{"non-mailto skipped", []dmarc.ReportURI{uri("https://example.test/report", math.MaxUint64), uri("mailto:agg@example.test", math.MaxUint64)}, "agg@example.test"},
		{"too small a limit skipped", []dmarc.ReportURI{uri("mailto:small@example.test", 10), uri("mailto:agg@example.test", math.MaxUint64)}, "agg@example.test"},
		{"none", nil, ""},
	}
	for _, c := range cases {
		if got := mailtoRUA(c.ruas, 100); got != c.want {
			t.Errorf("%s: mailtoRUA = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestBuildReportEmail_AttachmentDecodes(t *testing.T) {
	// The gzipped report must survive the MIME/base64 wrapping.
	original := []byte("<?xml version=\"1.0\"?><feedback>report</feedback>")
	gz, err := dmarc.Gzip(original)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1784786400, 0).UTC()
	raw := buildReportEmail("dmarc@restmail.test", "agg@example.test", "example.test", "restmail.test",
		"rid-1", 1784700000, 1784786400, now, gz)

	// Basic structure.
	for _, want := range []string{
		"Subject: Report Domain: example.test Submitter: restmail.test Report-ID: rid-1",
		"Content-Type: multipart/mixed;",
		"Content-Type: application/gzip",
		`filename="restmail.test!example.test!1784700000!1784786400.xml.gz"`,
		"Date: ",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("report email missing %q", want)
		}
	}

	if got := reportXML(t, raw); got != string(original) {
		t.Errorf("attachment did not round-trip to the original report XML")
	}
}

// reportXML extracts a report email's base64 attachment and decodes and
// gunzips it.
func reportXML(t *testing.T, raw string) string {
	t.Helper()
	idx := strings.Index(raw, "filename=")
	if idx < 0 {
		t.Fatal("no attachment")
	}
	blockStart := strings.Index(raw[idx:], "\r\n\r\n")
	if blockStart < 0 {
		t.Fatal("no attachment body")
	}
	body := raw[idx+blockStart+4:]
	body = body[:strings.Index(body, "\r\n--")]
	b64 := strings.ReplaceAll(body, "\r\n", "")
	gzBytes, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("decode attachment: %v", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gzBytes))
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	got, _ := io.ReadAll(zr)
	return string(got)
}

func TestDomainOf(t *testing.T) {
	if domainOf("a@b.test") != "b.test" {
		t.Error("domainOf failed")
	}
}
