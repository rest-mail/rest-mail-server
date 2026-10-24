package dmarcreport

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-dmarc"
)

// A report goes to every mailto: rua= address, in order (RFC 9990 §3.5);
// other schemes are left out (RFC 9989 §4.7).
func TestMailtoRUAs(t *testing.T) {
	cases := []struct {
		name string
		ruas []string
		want []string
	}{
		{"every mailto", []string{"mailto:agg@example.test", "mailto:other@x.test"}, []string{"agg@example.test", "other@x.test"}},
		{"scheme in any case", []string{"MAILTO:agg@example.test"}, []string{"agg@example.test"}},
		{"non-mailto skipped", []string{"https://example.test/report", "mailto:agg@example.test"}, []string{"agg@example.test"}},
		{"headers dropped", []string{"mailto:agg@example.test?subject=dmarc"}, []string{"agg@example.test"}},
		{"none", nil, nil},
	}
	for _, c := range cases {
		if got := mailtoRUAs(c.ruas); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: mailtoRUAs = %q, want %q", c.name, got, c.want)
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
	filename, _ := dmarc.ReportFilename("restmail.test", "example.test", dmarc.DateRange{Begin: 1784700000, End: 1784786400}, "", true)
	subject, _ := dmarc.ReportSubject("example.test", "restmail.test", "rid-1")
	raw := buildReportEmail("dmarc@restmail.test", "agg@example.test", subject, filename, "rid-1", "example.test", "restmail.test", now, gz)

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
