package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// FuzzDecodeSpooledReportAnswersAsBound holds the streaming decoder to the binder it stands
// in for: for any bytes, the same report or the same refusal. The seeds are every way found
// for the two to differ, and run as a test on every go test.
func FuzzDecodeSpooledReportAnswersAsBound(f *testing.F) {
	dates := `"date-range":{"start-datetime":"2020-01-02T03:04:05Z","end-datetime":"2020-01-02T03:04:05Z"}`
	for _, seed := range []string{
		"", " ", " ", "null", " null ", "[]", "[", "]", "{", "}", `""`, "1", "1e999", "tru", "true", "{} {}", "{},", "[1,]", `{"a":1,}`, `{"a" 1}`, `{1:2}`, "\xff",
		`{}`, tlsWireValid, tlsWireValid + "\n", tlsWireValid + " x",
		`{"organization-name":"x",` + dates + `,"policies":[{}]}`,
		`{"organization-name":"x",` + dates + `,"policies":[]}`,
		`{"organization-name":"x",` + dates + `,"policies":null}`,
		`{"organization-name":"x",` + dates + `,"policies":{}}`,
		`{"organization-name":"x",` + dates + `,"policies":"x"}`,
		`{"organization-name":null,"date-range":null,"contact-info":null,"report-id":null,"policies":[null]}`,
		`{"organization-name":"x",` + dates + `,"policies":[1,null,"x",[],{"policy":5},{"summary":{"total-failure-session-count":"x"}}]}`,
		`{"organization-name":"x",` + dates + `,"policies":[{"failure-details":[1,null,{"a":1}]},{"failure-details":{}}]}`,
		`{"organization-name":"x",` + dates + `,"policies":[{"policy":{"policy-domain":"a.test","mx-host":["a"],"policy-string":["b"],"policy-type":"sts"},"summary":{"total-successful-session-count":1,"total-failure-session-count":2},"failure-details":[{"result-type":"x"}],"x-own":1}],"x-own":{"a":[1,{"b":null}]},"contact-info":"c","report-id":"r"}`,
		`{"ORGANIZATION-NAME":"y","organization-name":"x",` + dates + `,"policies":[{}]}`,
		`{"organization-name":"x","ORGANIZATION-NAME":5,` + dates + `,"policies":[{}]}`,
		`{"organization-name":"x",` + dates + `,"policies":[{}],"POLICIES":null}`,
		`{"organization-name":"x",` + dates + `,"policieſ":5,"policies":[{}]}`,
		`{"organization-name":"x",` + dates + `,"policies":[{}],"policies":[1]}`,
		`{"organization-name":"x",` + dates + `,"policies":[1],"policies":[{}]}`,
		`{"organization-name":"x",` + dates + `,"policies":[{"summary":{"total-failure-session-count":1}},{}],"policies":[{}]}`,
		`{"organization-name":"","organization-name":"x",` + dates + `,` + dates + `,"policies":[{}]}`,
		`{"organization-name":"x","date-range":{"start-datetime":"2020-01-02T03:04:05Z","end-datetime":"2020-01-02T03:04:05Z","END-DATETIME":5},"policies":[{}]}`,
		`{"organization-name":"x",` + dates + `,"policies":[{"POLICY":5}]}`,
		`{"organization-name":"x",` + dates + `,"policies":[{"POLICY":{"policy-domain":"a.test"},"policy":{"policy-type":"sts"}}]}`,
		`{"organization-name":"x",` + dates + `,"x":1e999,"y":"\ud800","policies":[{}]}`,
		`{"organization-name":"x",` + dates + `,"policies":[` + strings.Repeat(`{},`, 100) + `{}]}`,
		`{"organization-name":"",` + dates + `,"policies":[` + strings.Repeat(`{},`, 100) + `1]}`,
		`{"x":` + strings.Repeat("[", 9999) + strings.Repeat("]", 9999) + `}`,
		`{"x":` + strings.Repeat("[", 10000) + strings.Repeat("]", 10000) + `}`,
		strings.Repeat("[", 10000) + strings.Repeat("]", 10000),
		strings.Repeat("[", 10001) + strings.Repeat("]", 10001),
		"{\"x\":\"\x01\"}", "{\"x\":\"\xff\"}", `{"x":"\q"}`, `{"😀":1}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		wantReport := &TLSRPTReport{}
		wantErr := endpoint.BindBody(wantReport, body)

		file, done, err := endpoint.Spool(bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer done()
		gotReport, gotErr := decodeSpooledReport(file, false)

		if !sameTLSReportRefusal(wantErr, gotErr) {
			t.Fatalf("%q: the binder said %#v, the file %#v", body, wantErr, gotErr)
		}
		if wantErr == nil && !reflect.DeepEqual(wantReport, gotReport) {
			t.Fatalf("%q: the binder read %#v, the file %#v", body, wantReport, gotReport)
		}
	})
}

// sameTLSReportRefusal says whether two answers from binding a report are the same on the wire.
func sameTLSReportRefusal(a, b error) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	var first, second *endpoint.Error
	if !errors.As(a, &first) || !errors.As(b, &second) {
		return false
	}
	return first.Status == second.Status && first.Code == second.Code && first.Message == second.Message && reflect.DeepEqual(first.Fields, second.Fields)
}

// tlsCountingReader counts what is read of a compressed report.
type tlsCountingReader struct {
	src  io.Reader
	read int
}

func (c *tlsCountingReader) Read(p []byte) (int, error) {
	n, err := c.src.Read(p)
	c.read += n
	return n, err
}

// A bomb stops inflating at the cap, so at most a byte past it is ever written, and most of
// the compressed stream is never read.
func TestSpoolInflatedReport_StopsAtTheCap(t *testing.T) {
	spool := t.TempDir()
	t.Setenv("TMPDIR", spool)
	bomb := gzipped(t, bytes.Repeat([]byte{0}, 256<<20))
	source := &tlsCountingReader{src: bytes.NewReader(bomb)}
	_, _, err := spoolInflatedReport(source)
	var refusal *endpoint.Error
	if !errors.As(err, &refusal) || refusal.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("got %v, want the 413", err)
	}
	if source.read >= len(bomb)/2 {
		t.Errorf("read %d of %d compressed bytes: inflating did not stop at the cap", source.read, len(bomb))
	}
	assertNothingSpooled(t, spool)
}

// It answers with the inflated report in a file, which done removes.
func TestSpoolInflatedReport_AnswersAFile(t *testing.T) {
	spool := t.TempDir()
	t.Setenv("TMPDIR", spool)
	file, done, err := spoolInflatedReport(bytes.NewReader(gzipped(t, []byte(tlsWireValid))))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(file)
	if err != nil || string(got) != tlsWireValid {
		t.Fatalf("read %q, %v", got, err)
	}
	done()
	assertNothingSpooled(t, spool)
}

// The route spools before anything reads the body, so with nowhere to spool it answers 500,
// whatever is in the body. A route that reads its body into memory answers something else.
func TestReceiveReport_SpoolsBeforeAnythingReadsTheBody(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "gone"))
	for name, body := range map[string]string{"a report": tlsWireValid, "not a report": "not json", "over the cap": strings.Repeat("x", int(tlsRPTMaxBodyBytes)/2)} {
		r := httptest.NewRequest(http.MethodPost, tlsReportPath, strings.NewReader(body))
		rr := httptest.NewRecorder()
		tlsReportRoutes(NewTLSReportHandler(nil), passThrough).ServeHTTP(rr, r)
		if rr.Code != http.StatusInternalServerError {
			t.Errorf("%s: answered %d %s, want the 500 of a disk that can't be written", name, rr.Code, rr.Body)
		}
	}
}

// A handler that panics takes its files with it.
func TestReceiveReport_PanicLeavesNoFile(t *testing.T) {
	spool := t.TempDir()
	t.Setenv("TMPDIR", spool)
	for name, tc := range map[string]struct{ media, body string }{
		"plain":    {"application/tlsrpt+json", tlsWireValid},
		"null":     {"application/tlsrpt+json", "null"},
		"labelled": {"application/tlsrpt+gzip", tlsWireGzip(t, tlsWireValid)},
	} {
		r := httptest.NewRequest(http.MethodPost, tlsReportPath, strings.NewReader(tc.body))
		r.Header.Set("Content-Type", tc.media)
		if got := tlsWireAnswer(t, r); got != tlsWireAtStorage {
			t.Errorf("%s: answered %s", name, got)
		}
		assertNothingSpooled(t, spool)
	}
}

// Compressed however it says so or not at all, a report stores the same rows, each keeping the
// report as it was sent, and leaves no file. One for no hosted domain stores none, also without
// a file left.
func TestReceiveReport_StoresTheSameRowsHoweverSent(t *testing.T) {
	database := openTLSRPTTestDB(t)
	tx := database.Begin()
	t.Cleanup(func() { tx.Rollback() })
	suffix := time.Now().UnixNano()
	hosted := fmt.Sprintf("spool-%d.test", suffix)
	domain := models.Domain{Name: hosted, Active: true}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	raw := `{"organization-name":"Spool Reporter","x-reporter":{"kept":true},"date-range":{"start-datetime":"2026-10-16T00:00:00Z","end-datetime":"2026-10-16T23:59:59Z"},"contact-info":"mailto:a@b.test","report-id":"r-1",` +
		`"policies":[{"policy":{"policy-domain":"` + hosted + `.","policy-type":"sts"},"summary":{"total-successful-session-count":12,"total-failure-session-count":1},"failure-details":[{"result-type":"certificate-expired","x-reason":true}]},` +
		`{"policy":{"policy-domain":"not-hosted.invalid"}},{"policy":{"policy-domain":"` + hosted + `","policy-type":"no-policy-found"}}]} `
	compressed := tlsWireGzip(t, raw)

	type row struct {
		Org, PolicyType, PolicyDomain, FailureDetails, Raw string
		Successful, Failed                                 int64
		Start, End                                         time.Time
	}
	stored := func(t *testing.T) []row {
		t.Helper()
		var rows []models.TLSReport
		if err := tx.Where("domain_id = ?", domain.ID).Order("id").Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		out := make([]row, len(rows))
		for i, r := range rows {
			out[i] = row{r.ReportingOrg, r.PolicyType, r.PolicyDomain, string(r.FailureDetails), r.RawReport, r.TotalSuccessful, r.TotalFailure, r.StartDate.UTC(), r.EndDate.UTC()}
		}
		if err := tx.Where("domain_id = ?", domain.ID).Delete(&models.TLSReport{}).Error; err != nil {
			t.Fatal(err)
		}
		return out
	}

	var plain []row
	for _, tc := range []struct{ name, contentType, encoding, body string }{
		{"plain", "application/tlsrpt+json", "", raw},
		{"plain, application/json", "application/json", "", raw},
		{"no media type", "", "", raw},
		{"labelled", "application/tlsrpt+gzip", "", compressed},
		{"encoded", "application/tlsrpt+json", "gzip", compressed},
		{"encoded and labelled", "application/tlsrpt+gzip", "gzip", compressed},
		{"sniffed", "application/json", "", compressed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spool := t.TempDir()
			t.Setenv("TMPDIR", spool)
			r := httptest.NewRequest(http.MethodPost, tlsReportPath, strings.NewReader(tc.body))
			if tc.contentType != "" {
				r.Header.Set("Content-Type", tc.contentType)
			}
			if tc.encoding != "" {
				r.Header.Set("Content-Encoding", tc.encoding)
			}
			rr := httptest.NewRecorder()
			tlsReportRoutes(NewTLSReportHandler(tx), passThrough).ServeHTTP(rr, r)
			if rr.Code != http.StatusCreated || !strings.Contains(rr.Body.String(), `"stored":2`) {
				t.Fatalf("answered %d %s", rr.Code, rr.Body)
			}
			assertNothingSpooled(t, spool)
			rows := stored(t)
			if len(rows) != 2 || rows[0].Raw != raw || rows[1].Raw != raw || rows[0].Org != "Spool Reporter" || rows[0].PolicyType != "sts" || rows[1].PolicyType != "no-policy" || rows[0].Successful != 12 || rows[0].Failed != 1 || !strings.Contains(rows[0].FailureDetails, "x-reason") {
				t.Fatalf("stored %+v", rows)
			}
			if plain == nil {
				plain = rows
			} else if !reflect.DeepEqual(plain, rows) {
				t.Errorf("stored\n%+v\nwhere sent plain it stored\n%+v", rows, plain)
			}
		})
	}

	t.Run("no hosted domain", func(t *testing.T) {
		spool := t.TempDir()
		t.Setenv("TMPDIR", spool)
		r := httptest.NewRequest(http.MethodPost, tlsReportPath, strings.NewReader(strings.ReplaceAll(raw, hosted, "not-hosted.invalid")))
		rr := httptest.NewRecorder()
		tlsReportRoutes(NewTLSReportHandler(tx), passThrough).ServeHTTP(rr, r)
		want := `{"error":{"code":"no_matching_domains","message":"None of the policy domains in the report match a known domain"}}`
		if rr.Code != http.StatusUnprocessableEntity || strings.TrimSpace(rr.Body.String()) != want {
			t.Fatalf("answered %d %s", rr.Code, rr.Body)
		}
		assertNothingSpooled(t, spool)
	})
}

// A report for no hosted domain is refused without its bytes being read back into memory.
func TestStoreReport_ReadsTheReportOnlyToStoreIt(t *testing.T) {
	database := openTLSRPTTestDB(t)
	tx := database.Begin()
	t.Cleanup(func() { tx.Rollback() })
	report := &TLSRPTReport{OrganizationName: "x", Policies: []TLSRPTPolicy{{Policy: TLSRPTPolicyDesc{PolicyDomain: "not-hosted.invalid"}}}}
	_, err := NewTLSReportHandler(tx).storeReport(report, func() ([]byte, error) {
		t.Error("the report was read with nothing to store")
		return nil, nil
	})
	var refusal *endpoint.Error
	if !errors.As(err, &refusal) || refusal.Code != "no_matching_domains" {
		t.Fatalf("got %v", err)
	}

	domain := models.Domain{Name: fmt.Sprintf("read-once-%d.test", time.Now().UnixNano()), Active: true}
	if err := tx.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	report.Policies = []TLSRPTPolicy{{Policy: TLSRPTPolicyDesc{PolicyDomain: domain.Name}}, {Policy: TLSRPTPolicyDesc{PolicyDomain: domain.Name}}}
	reads := 0
	if _, err := NewTLSReportHandler(tx).storeReport(report, func() ([]byte, error) {
		reads++
		return []byte(`{}`), nil
	}); err != nil {
		t.Fatal(err)
	}
	if reads != 1 {
		t.Errorf("read the report %d times for two rows", reads)
	}
}

// tlsReportEmailPart is a message holding one report part, in the given transfer encoding.
func tlsReportEmailPart(t *testing.T, media, encoding, part string) *mail.Message {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.SetBoundary("report"); err != nil {
		t.Fatal(err)
	}
	header := textproto.MIMEHeader{"Content-Type": {media}}
	if encoding != "" {
		header.Set("Content-Transfer-Encoding", encoding)
	}
	w, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, part); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	message, err := mail.ReadMessage(strings.NewReader("Content-Type: multipart/report; report-type=tlsrpt; boundary=report\r\n\r\n" + body.String()))
	if err != nil {
		t.Fatal(err)
	}
	return message
}

// A report by mail is read as one over HTTP is, and leaves no file however it ends. Past the
// cap, compressed or not, nothing of it is held.
func TestTLSReportEmailSpools(t *testing.T) {
	const (
		plainMedia      = "application/tlsrpt+json"
		compressedMedia = "application/tlsrpt+gzip"
	)
	overCap := tlsWirePad(int(tlsRPTMaxBodyBytes) + 1)
	for _, tc := range []struct {
		name, media, part string
		accepted          bool
	}{
		{"plain", plainMedia, tlsWireValid, true},
		{"compressed", compressedMedia, tlsWireGzip(t, tlsWireValid), true},
		{"plain, exactly the cap", plainMedia, tlsWirePad(int(tlsRPTMaxBodyBytes)), true},
		{"compressed, inflates to exactly the cap", compressedMedia, tlsWireGzip(t, tlsWirePad(int(tlsRPTMaxBodyBytes))), true},
		{"plain, over the cap", plainMedia, overCap, false},
		{"compressed, over the cap", compressedMedia, overCap, false},
		{"compressed, inflates past the cap", compressedMedia, tlsWireGzip(t, overCap), false},
		{"compressed, not gzip", compressedMedia, "not gzip", false},
		{"compressed, cut short", compressedMedia, tlsWireGzip(t, tlsWireValid)[:20], false},
		{"not JSON", plainMedia, "not json", false},
		{"null", plainMedia, "null", false},
		{"null, compressed", compressedMedia, tlsWireGzip(t, "null"), false},
		{"breaks a rule", plainMedia, `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spool := t.TempDir()
			t.Setenv("TMPDIR", spool)
			report, body, err := readTLSReportEmail(tlsReportEmailPart(t, tc.media, "binary", tc.part), "report")
			switch {
			case tc.accepted && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.accepted && (report == nil || !strings.HasPrefix(string(body), tlsWireValid)):
				t.Fatalf("read %v, %.60q", report, body)
			case !tc.accepted && err == nil:
				t.Fatal("accepted")
			}
			assertNothingSpooled(t, spool)
		})
	}
}

// A report by mail that can't be spooled isn't the report's fault: it stays retryable, where
// a report that can't be read is consumed.
func TestTLSReportEmailDiskFailureIsRetryable(t *testing.T) {
	receiver := NewTLSReportEmailReceiver(nil, "reports@local.test")
	message := tlsReportEmail(t, []byte(tlsWireValid), false)

	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "gone"))
	consumed, err := receiver.receive(context.Background(), "reports@local.test", message)
	if !consumed || err == nil {
		t.Fatalf("with no disk: consumed=%v, err=%v; want a retryable error", consumed, err)
	}

	t.Setenv("TMPDIR", t.TempDir())
	unreadable := tlsReportEmail(t, []byte("not json"), false)
	consumed, err = receiver.receive(context.Background(), "reports@local.test", unreadable)
	if !consumed || err != nil {
		t.Fatalf("an unreadable report: consumed=%v, err=%v; want it consumed", consumed, err)
	}
}
