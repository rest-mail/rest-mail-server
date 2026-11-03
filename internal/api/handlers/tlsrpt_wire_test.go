package handlers

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const (
	tlsWireDates     = `"date-range":{"start-datetime":"2020-01-02T03:04:05Z","end-datetime":"2020-01-02T03:04:05Z"}`
	tlsWireValid     = `{"organization-name":"x",` + tlsWireDates + `,"policies":[{"policy":{"policy-domain":"example.com"}}]}`
	tlsWireAtStorage = "reached storage"
)

// tlsWireReport is a valid report with extra top-level text: a reporter's own field.
func tlsWireReport(own string) string {
	return `{"organization-name":"x",` + tlsWireDates + `,` + own + `,"policies":[{"policy":{"policy-domain":"example.com"}}]}`
}

// tlsWirePad is the valid report padded with whitespace to size bytes.
func tlsWirePad(size int) string {
	return tlsWireValid + strings.Repeat(" ", size-len(tlsWireValid))
}

func tlsWireGzip(t *testing.T, body string) string {
	t.Helper()
	return string(gzipped(t, []byte(body)))
}

// tlsWireCorrupt is a gzip stream broken just after its header, followed by enough of
// anything to take the whole body over the cap, so the two refusals are told apart.
func tlsWireCorrupt() string {
	return "\x1f\x8b\x08\x00\x00\x00\x00\x00\x00\x03\x07" + strings.Repeat("z", int(tlsRPTMaxBodyBytes)+64)
}

// tlsWireIncompressible is a gzip stream of random bytes, larger than the cap compressed.
func tlsWireIncompressible(t *testing.T) string {
	t.Helper()
	random := make([]byte, tlsRPTMaxBodyBytes+4096)
	_, _ = rand.New(rand.NewSource(1437)).Read(random)
	return string(gzipped(t, random))
}

// tlsWireAnswer is the route's answer as "status body", on a handler with no database:
// tlsWireAtStorage for a request that got past every refusal and tried to store.
func tlsWireAnswer(t *testing.T, r *http.Request) (answer string) {
	t.Helper()
	defer func() {
		if p := recover(); p != nil {
			answer = tlsWireAtStorage
		}
	}()
	rr := httptest.NewRecorder()
	tlsReportRoutes(NewTLSReportHandler(nil), passThrough).ServeHTTP(rr, r)
	return strconv.Itoa(rr.Code) + " " + strings.TrimSpace(rr.Body.String())
}

// Every refusal this route gave before its body moved to a file, as main answered it when
// this table was written: the answer, and which answer wins when a request has two things
// wrong with it. No temporary file outlives any of them.
func TestReceiveReport_WireUnchanged(t *testing.T) {
	const (
		tooLarge  = `413 {"error":{"code":"request_too_large","message":"Request body too large"}}`
		invalid   = `400 {"error":{"code":"bad_request","message":"Invalid request body"}}`
		notGzip   = `400 {"error":{"code":"bad_request","message":"Invalid gzip-compressed TLS-RPT report"}}`
		labelled  = "application/tlsrpt+gzip"
		ownMedia  = "application/tlsrpt+json"
		bodyField = `422 {"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{"body":`
	)
	unsupported := func(media string) string {
		return `400 {"error":{"code":"bad_request","message":"Unsupported media type: ` + media + `; the body is taken as application/tlsrpt+json, application/json, application/tlsrpt+gzip"}}`
	}
	validation := func(fields string) string {
		return `422 {"error":{"code":"validation_failed","message":"Request body failed validation","details":{"fields":{` + fields + `}}}}`
	}
	notObject := bodyField + `"the body is not a JSON object"}}}}`
	limit := int(tlsRPTMaxBodyBytes)
	overCap := tlsWirePad(limit + 1)
	nest := func(depth int) string { return strings.Repeat("[", depth) + strings.Repeat("]", depth) }
	fullGzip := tlsWireGzip(t, tlsWireValid)
	badTrailer := fullGzip[:len(fullGzip)-5] + "\x00\x00\x00\x00\x00"

	for _, tc := range []struct {
		name, contentType, encoding, body, want string
	}{
		// An empty or blank body.
		{"empty", "", "", "", invalid},
		{"empty, another media type", "text/plain", "", "", unsupported("text/plain")},
		{"empty, another media type in capitals, with a parameter", "TEXT/Plain; charset=x", "", "", unsupported("text/plain")},
		{"empty, a malformed media type", "application/json; charset", "", "", invalid},
		{"whitespace", "", "", " \t\r\n", invalid},
		{"whitespace, another media type", "text/plain", "", "  ", unsupported("text/plain")},
		{"unicode whitespace", "", "", "  ", invalid},

		// The media type.
		{"not JSON", "application/json", "", "not json", invalid},
		{"another media type", "text/plain", "", tlsWireValid, unsupported("text/plain")},
		{"a malformed media type", "a b/c; =", "", tlsWireValid, unsupported("a b/c; =")},
		{"a blank media type", "   ", "", tlsWireValid, unsupported("   ")},
		{"a media type in capitals, with a parameter", "APPLICATION/JSON; charset=utf-8", "", tlsWireValid, tlsWireAtStorage},
		{"its own media type", ownMedia, "", tlsWireValid, tlsWireAtStorage},
		{"no media type", "", "", tlsWireValid, tlsWireAtStorage},

		// The cap, sent plain.
		{"over the cap", ownMedia, "", overCap, tooLarge},
		{"over the cap, another media type", "text/plain", "", overCap, tooLarge},
		{"over the cap, not JSON", ownMedia, "", strings.Repeat("x", limit+1), tooLarge},
		{"exactly the cap", ownMedia, "", tlsWirePad(limit), tlsWireAtStorage},

		// JSON that is not valid, or not a report.
		{"an array", ownMedia, "", `[]`, notObject},
		{"a string", ownMedia, "", `"x"`, notObject},
		{"a number", ownMedia, "", `1`, notObject},
		{"a flag", ownMedia, "", `true`, notObject},
		{"null reaches storage with no report", ownMedia, "", `null`, tlsWireAtStorage},
		{"null after whitespace", ownMedia, "", " \n null ", tlsWireAtStorage},
		{"garbage after a report", ownMedia, "", tlsWireValid + ` x`, invalid},
		{"another value after a report", ownMedia, "", tlsWireValid + ` {}`, invalid},
		{"a comma after a report", ownMedia, "", tlsWireValid + `,`, invalid},
		{"a report cut short", ownMedia, "", tlsWireValid[:40], invalid},
		{"a trailing comma", ownMedia, "", `{"organization-name":"x",}`, invalid},
		{"a rule broken, and invalid later on", ownMedia, "", `{"organization-name":"","x":tru}`, invalid},
		{"a bad escape", ownMedia, "", `{"x":"\q"}`, invalid},
		{"a control character in a string", ownMedia, "", "{\"x\":\"\x01\"}", invalid},
		{"nested as deep as JSON goes", ownMedia, "", `{"x":` + nest(9999) + `,` + tlsWireValid[1:], tlsWireAtStorage},
		{"nested too deep", ownMedia, "", `{"x":` + nest(10000) + `,` + tlsWireValid[1:], invalid},
		{"invalid UTF-8 in a reporter's field", ownMedia, "", tlsWireReport("\"x-own\":\"\xff\""), tlsWireAtStorage},
		{"a number too large for a float64", ownMedia, "", tlsWireReport(`"x-own":1e999`), tlsWireAtStorage},
		{"a reporter's own field of every kind", ownMedia, "", tlsWireReport(`"x-own":{"a":[1,null,true,"s",{"b":{}}],"c":-1.5e3}`), tlsWireAtStorage},

		// The rules.
		{"nothing in it", ownMedia, "", `{}`, validation(`"date-range":"required","organization-name":"Required field","policies":"At least one policy entry is required"`)},
		{"types all wrong", ownMedia, "", `{"organization-name":5,"date-range":"x","contact-info":5,"report-id":[],"policies":5}`,
			validation(`"contact-info":"must be a string","date-range":"must be an object","organization-name":"must be a string","policies":"must be an array","report-id":"must be a string"`)},
		{"a date in the wrong form", ownMedia, "", `{"organization-name":"x","date-range":{"start-datetime":"no"},"policies":[{}]}`,
			validation(`"date-range.end-datetime":"Invalid RFC3339 datetime","date-range.start-datetime":"Invalid RFC3339 datetime"`)},
		{"null where a list goes", ownMedia, "", `{"organization-name":"x",` + tlsWireDates + `,"policies":null}`, validation(`"policies":"must not be null"`)},
		{"no policies", ownMedia, "", `{"organization-name":"x",` + tlsWireDates + `,"policies":[]}`, validation(`"policies":"Must hold 1 to 100 policy entries"`)},
		{"policies an object", ownMedia, "", `{"organization-name":"x",` + tlsWireDates + `,"policies":{"a":1}}`, validation(`"policies":"must be an array"`)},
		{"entries wrong at their indexes", ownMedia, "", `{"organization-name":"x",` + tlsWireDates + `,"policies":[{},1,null,{"policy":5},{"policy":{"mx-host":[1]},"summary":{"total-failure-session-count":"x"},"failure-details":[1]}]}`,
			validation(`"policies[1]":"must be an object","policies[2]":"must not be null","policies[3].policy":"must be an object","policies[4].failure-details[0]":"must be an object","policies[4].policy.mx-host[0]":"must be a string","policies[4].summary.total-failure-session-count":"must be an integer"`)},
		{"too many, one wrong past the limit", ownMedia, "", `{"organization-name":"",` + tlsWireDates + `,"policies":[` + strings.Repeat(`{},`, 101) + `1]}`,
			validation(`"organization-name":"Required field","policies":"Must hold 1 to 100 policy entries","policies[101]":"must be an object"`)},
		{"as many as allowed", ownMedia, "", `{"organization-name":"x",` + tlsWireDates + `,"policies":[` + strings.Repeat(`{},`, 99) + `{}]}`, tlsWireAtStorage},

		// What encoding/json merges, or finds wrong only after the rules passed.
		{"a declared field in other capitals, the wrong type", ownMedia, "", tlsWireReport(`"ORGANIZATION-NAME":5`),
			bodyField + `"json: cannot unmarshal number into Go struct field TLSRPTReport.organization-name of type string"}}}}`},
		{"a declared field in a folded rune", ownMedia, "", tlsWireReport(`"policieſ":5`),
			bodyField + `"json: cannot unmarshal number into Go struct field TLSRPTReport.policies of type []handlers.TLSRPTPolicy"}}}}`},
		{"a nested field in other capitals, the wrong type", ownMedia, "", `{"organization-name":"x",` + tlsWireDates + `,"policies":[{},{"POLICY":5}]}`,
			bodyField + `"json: cannot unmarshal number into Go struct field TLSRPTPolicy.policies.policy of type handlers.TLSRPTPolicyDesc"}}}}`},
		{"a date range's field in other capitals, the wrong type", ownMedia, "", `{"organization-name":"x","date-range":{"start-datetime":"2020-01-02T03:04:05Z","end-datetime":"2020-01-02T03:04:05Z","END-DATETIME":5},"policies":[{}]}`,
			bodyField + `"json: cannot unmarshal number into Go struct field TLSRPTRange.date-range.end-datetime of type string"}}}}`},
		{"a field given twice, the last one good", ownMedia, "", `{"organization-name":"","organization-name":"x",` + tlsWireDates + `,"policies":[{}]}`, tlsWireAtStorage},
		{"a field given twice, the last one wrong", ownMedia, "", `{"organization-name":"x","organization-name":"",` + tlsWireDates + `,"policies":[{}]}`, validation(`"organization-name":"Required field"`)},
		{"policies given twice, the first wrong for decoding", ownMedia, "", `{"organization-name":"x",` + tlsWireDates + `,"policies":[1],"policies":[{}]}`,
			bodyField + `"json: cannot unmarshal number into Go struct field TLSRPTReport.policies of type handlers.TLSRPTPolicy"}}}}`},
		{"policies given twice, the last good", ownMedia, "", `{"organization-name":"x",` + tlsWireDates + `,"policies":[{"summary":{}}],"policies":[{}]}`, tlsWireAtStorage},
		{"policies given twice, the last wrong", ownMedia, "", `{"organization-name":"x",` + tlsWireDates + `,"policies":[{}],"policies":[1]}`, validation(`"policies[0]":"must be an object"`)},

		// A body labelled compressed, spooled and then inflated.
		{"labelled, whitespace", labelled, "", "   ", invalid},
		{"labelled, unicode whitespace", labelled, "", " ", invalid},
		{"labelled, not gzip", labelled, "", "not gzip", notGzip},
		{"labelled, not gzip, over the cap", labelled, "", strings.Repeat("x", limit+1), tooLarge},
		{"labelled, broken and over the cap", labelled, "", tlsWireCorrupt(), tooLarge},
		{"labelled, cut short", labelled, "", fullGzip[:len(fullGzip)-3], notGzip},
		{"labelled, a bad checksum", labelled, "", badTrailer, notGzip},
		{"labelled, garbage after the stream", labelled, "", fullGzip + "garbage", notGzip},
		{"labelled, two streams", labelled, "", tlsWireGzip(t, tlsWireValid[:9]) + tlsWireGzip(t, tlsWireValid[9:]), tlsWireAtStorage},
		{"labelled, inflates to nothing", labelled, "", tlsWireGzip(t, ""), invalid},
		{"labelled, inflates to whitespace", labelled, "", tlsWireGzip(t, "  "), invalid},
		{"labelled, inflates to something not JSON", labelled, "", tlsWireGzip(t, "not json"), invalid},
		{"labelled, inflates to null", labelled, "", tlsWireGzip(t, "null"), bodyField + `"the body is null"}}}}`},
		{"labelled, inflates to an array", labelled, "", tlsWireGzip(t, "[]"), notObject},
		{"labelled, inflates past the cap", labelled, "", tlsWireGzip(t, overCap), tooLarge},
		{"labelled, inflates to exactly the cap", labelled, "", tlsWireGzip(t, tlsWirePad(limit)), tlsWireAtStorage},
		{"labelled, cut short before the cap inflated", labelled, "", tlsWireGzip(t, overCap)[:len(tlsWireGzip(t, overCap))-3], notGzip},
		{"labelled, a bad checksum after the cap inflated", labelled, "", tlsWireGzip(t, overCap+"  ")[:len(tlsWireGzip(t, overCap+"  "))-8] + "\x00\x00\x00\x00\x00\x00\x00\x00", tooLarge},
		{"labelled, inflates to a report breaking a rule", labelled, "", tlsWireGzip(t, `{}`), validation(`"date-range":"required","organization-name":"Required field","policies":"At least one policy entry is required"`)},
		{"labelled, with a parameter", labelled + "; charset=x", "", tlsWireGzip(t, tlsWireValid), tlsWireAtStorage},
		{"labelled, in capitals", "APPLICATION/TLSRPT+GZIP", "", tlsWireGzip(t, tlsWireValid), tlsWireAtStorage},

		// A body that says so another way, inflated as it spools. A stream found broken
		// on the way wins over the cap it would have reached, because it comes first.
		{"encoded, empty", ownMedia, "gzip", "", notGzip},
		{"encoded, not gzip", ownMedia, "gzip", "not gzip", notGzip},
		{"encoded, not gzip, over the cap", ownMedia, "gzip", strings.Repeat("x", limit+1), notGzip},
		{"encoded, broken, over the cap", ownMedia, "gzip", tlsWireCorrupt(), invalid},
		{"encoded, cut short", ownMedia, "gzip", fullGzip[:len(fullGzip)-3], invalid},
		{"encoded, a bad checksum", ownMedia, "gzip", badTrailer, invalid},
		{"encoded, inflates past the cap", ownMedia, "gzip", tlsWireGzip(t, overCap), tooLarge},
		{"encoded, past the cap compressed", ownMedia, "gzip", tlsWireIncompressible(t), tooLarge},
		{"encoded, inflates to nothing", ownMedia, "gzip", tlsWireGzip(t, ""), invalid},
		{"encoded, inflates to null", ownMedia, "gzip", tlsWireGzip(t, "null"), tlsWireAtStorage},
		{"encoded, inflates to something not JSON", ownMedia, "gzip", tlsWireGzip(t, "not json"), invalid},
		{"encoded, another media type", "text/plain", "gzip", fullGzip, unsupported("text/plain")},
		{"encoded, another media type, inflates to nothing", "text/plain", "gzip", tlsWireGzip(t, ""), unsupported("text/plain")},
		{"encoded, a malformed media type", "a b/c; =", "gzip", fullGzip, tlsWireAtStorage},
		{"encoded, no media type", "", "gzip", fullGzip, tlsWireAtStorage},
		{"encoded, in capitals", ownMedia, " GZIP ", fullGzip, tlsWireAtStorage},
		{"encoded and labelled", labelled, "gzip", fullGzip, tlsWireAtStorage},
		{"encoded and labelled, compressed twice", labelled, "gzip", tlsWireGzip(t, fullGzip), invalid},
		{"another encoding, taken as it is", ownMedia, "br", tlsWireValid, tlsWireAtStorage},
		{"sniffed", "application/json", "", fullGzip, tlsWireAtStorage},
		{"sniffed, no media type", "", "", fullGzip, tlsWireAtStorage},
		{"sniffed, its own media type", ownMedia, "", fullGzip, tlsWireAtStorage},
		{"sniffed, another media type", "text/plain", "", fullGzip, unsupported("text/plain")},
		{"sniffed, not gzip after all", ownMedia, "", "\x1f\x8bnot gzip", notGzip},
		{"sniffed, inflates past the cap", ownMedia, "", tlsWireGzip(t, overCap), tooLarge},
		{"sniffed, inflates to null", "", "", tlsWireGzip(t, "null"), tlsWireAtStorage},
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
			if got := tlsWireAnswer(t, r); got != tc.want {
				t.Errorf("answered\n%s\nwant\n%s", got, tc.want)
			}
			assertNothingSpooled(t, spool)
		})
	}
}

func assertNothingSpooled(t *testing.T, spool string) {
	t.Helper()
	left, err := os.ReadDir(spool)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range left {
		t.Errorf("a temporary file remains: %s", entry.Name())
	}
}

// tlsCutOff is a client that goes away after sending some of its body.
type tlsCutOff struct{ sent io.Reader }

func (c *tlsCutOff) Read(p []byte) (int, error) {
	n, err := c.sent.Read(p)
	if errors.Is(err, io.EOF) {
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}

// A client that goes away mid-body gets the 400 a bound body gave, and leaves no file.
func TestReceiveReport_ClientDisconnectLeavesNoFile(t *testing.T) {
	compressed := gzipped(t, buildTLSRPTBody(t, 100))
	for _, tc := range []struct {
		name, contentType, encoding, sent string
	}{
		{"plain", "application/tlsrpt+json", "", tlsWireValid[:40]},
		{"encoded", "application/tlsrpt+json", "gzip", string(compressed[:len(compressed)/2])},
		{"labelled", "application/tlsrpt+gzip", "", string(compressed[:len(compressed)/2])},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spool := t.TempDir()
			t.Setenv("TMPDIR", spool)
			r := httptest.NewRequest(http.MethodPost, tlsReportPath, &tlsCutOff{sent: strings.NewReader(tc.sent)})
			r.Header.Set("Content-Type", tc.contentType)
			if tc.encoding != "" {
				r.Header.Set("Content-Encoding", tc.encoding)
			}
			want := `400 {"error":{"code":"bad_request","message":"Invalid request body"}}`
			if got := tlsWireAnswer(t, r); got != want {
				t.Errorf("answered %s, want %s", got, want)
			}
			assertNothingSpooled(t, spool)
		})
	}
}

// tlsReadGuard fails the test if the route reads past limit bytes of a body.
type tlsReadGuard struct {
	t     *testing.T
	src   io.Reader
	limit int64
	read  int64
}

func (g *tlsReadGuard) Read(p []byte) (int, error) {
	n, err := g.src.Read(p)
	g.read += int64(n)
	if g.read > g.limit {
		g.t.Errorf("read %d bytes of the body, past the cap and one (%d)", g.read, g.limit)
	}
	return n, err
}

// A body over the cap is read no further than it takes to know.
func TestReceiveReport_ReadsNoFurtherThanTheCap(t *testing.T) {
	huge := strings.NewReader(strings.Repeat(" ", 8*int(tlsRPTMaxBodyBytes)))
	r := httptest.NewRequest(http.MethodPost, tlsReportPath, &tlsReadGuard{t: t, src: huge, limit: tlsRPTMaxBodyBytes + 1})
	r.Header.Set("Content-Type", "application/tlsrpt+json")
	want := `413 {"error":{"code":"request_too_large","message":"Request body too large"}}`
	if got := tlsWireAnswer(t, r); got != want {
		t.Errorf("answered %s, want %s", got, want)
	}
}

// tlsAllocated is the least any of a few runs of do allocated, in bytes: what it needs, with
// what anything else in the process allocated at the same time taken out.
func tlsAllocated(do func()) uint64 {
	least := ^uint64(0)
	for range 5 {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		do()
		runtime.ReadMemStats(&after)
		least = min(least, after.TotalAlloc-before.TotalAlloc)
	}
	return least
}

// A report over the cap is refused the same way however it comes, and is never in memory on
// the way: everything the request allocates comes to less than the cap, so neither the body
// nor the inflated report can have been held. Reading either into memory, as this route
// did, allocates more than the cap by itself.
func TestReceiveReport_OverTheCapIsNeverHeld(t *testing.T) {
	bomb := gzipped(t, bytes.Repeat([]byte(" "), 64<<20))
	if int64(len(bomb)) >= tlsRPTMaxBodyBytes {
		t.Fatalf("the bomb is %d bytes compressed, which must be under the cap", len(bomb))
	}
	plain := bytes.Repeat([]byte(" "), int(tlsRPTMaxBodyBytes)*2)
	for _, tc := range []struct {
		name, contentType, encoding string
		body                        []byte
	}{
		{"plain", "application/tlsrpt+json", "", plain},
		{"plain, labelled", "application/tlsrpt+gzip", "", plain},
		{"labelled", "application/tlsrpt+gzip", "", bomb},
		{"encoded", "application/tlsrpt+json", "gzip", bomb},
		{"sniffed", "application/json", "", bomb},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			handler := tlsReportRoutes(NewTLSReportHandler(nil), passThrough)
			allocated := tlsAllocated(func() {
				r := httptest.NewRequest(http.MethodPost, tlsReportPath, bytes.NewReader(tc.body))
				r.Header.Set("Content-Type", tc.contentType)
				if tc.encoding != "" {
					r.Header.Set("Content-Encoding", tc.encoding)
				}
				rr := httptest.NewRecorder()
				handler.ServeHTTP(rr, r)
				if rr.Code != http.StatusRequestEntityTooLarge {
					t.Fatalf("answered %d %s, want 413", rr.Code, rr.Body)
				}
			})
			if allocated >= uint64(tlsRPTMaxBodyBytes) {
				t.Errorf("the refusal allocated %d bytes, as much as the %d-byte cap: the report was held in memory", allocated, tlsRPTMaxBodyBytes)
			}
		})
	}
}
