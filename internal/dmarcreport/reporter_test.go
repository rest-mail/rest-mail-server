package dmarcreport

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/go-dmarc"
)

func TestParseRUA(t *testing.T) {
	cases := []struct{ record, want string }{
		{"v=DMARC1; p=reject; rua=mailto:agg@example.test", "agg@example.test"},
		{"v=DMARC1; p=none; rua=mailto:agg@example.test!10m, mailto:other@x.test", "agg@example.test"},
		{"v=DMARC1; p=reject; ruf=mailto:forensic@example.test", ""}, // ruf, not rua
		{"v=DMARC1; p=reject", ""},
		{"v=DMARC1; rua=https://example.test/report", ""}, // non-mailto
	}
	for _, c := range cases {
		if got := parseRUA(c.record); got != c.want {
			t.Errorf("parseRUA(%q) = %q, want %q", c.record, got, c.want)
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

	// Extract the base64 attachment body and decode -> gunzip -> original.
	idx := strings.Index(raw, "filename=")
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
	if !bytes.Equal(got, original) {
		t.Errorf("attachment did not round-trip to the original report XML")
	}
}

func TestDomainOf(t *testing.T) {
	if domainOf("a@b.test") != "b.test" {
		t.Error("domainOf failed")
	}
}
