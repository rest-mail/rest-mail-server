package handlers

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
)

// scanImportBody reads an import's spooled request.
func scanImportBody(file io.Reader, source *importSource) (importBodyScan, error) {
	return scanSpooledBody(file, &spooledBodySpec{source: source, declared: importDeclaredFields})
}

// A text field and a RESTMAIL message hold what encoding/json would make of the string, whatever
// the string holds, and wherever a read happens to fall in it.
func TestTextStreamAgreesWithEncodingJSON(t *testing.T) {
	decode := func(quoted []byte, size int) (string, bool) {
		s := &jsonScanner{src: bufio.NewReaderSize(bytes.NewReader(quoted), size)}
		if first, err := s.next(); err != nil || first != '"' {
			return "", false
		}
		var out bytes.Buffer
		if err := s.textRest(func(data []byte) error { out.Write(data); return nil }); err != nil {
			return "", false
		}
		_, err := s.skipSpace()
		return out.String(), errors.Is(err, errNotJSON)
	}
	check := func(quoted []byte) {
		t.Helper()
		var want string
		wantErr := json.Unmarshal(quoted, &want)
		for _, size := range []int{16, 4096} {
			got, ok := decode(quoted, size)
			if ok != (wantErr == nil) {
				t.Fatalf("%q with a buffer of %d: valid=%t, encoding/json said %v", quoted, size, ok, wantErr)
			}
			if ok && got != want {
				t.Fatalf("%q with a buffer of %d: decoded %q, want %q", quoted, size, got, want)
			}
		}
	}
	for _, quoted := range []string{
		`""`, `"a"`, `"`, `"a`, `"\`, `"\"`, `"\\"`, `"\/"`, `"\b\f\n\r\t"`, `"\a"`, `"\'"`,
		`"\u"`, `"\u12"`, `"\u123g"`, `"\u00e9"`, `"\u0000"`, `"\u007f"`, `"\u0080"`, `"\uFFFD"`, `"\uffff"`,
		`"\ud83d\ude00"`, `"\uD83D\uDE00"`, `"\ud83d"`, `"\ude00"`, `"\ude00\ud83d"`, `"\ud83d\u0041"`, `"\ud83dA"`, `"\ud83d\ud83d\ude00"`, `"\ud83d\n"`, `"\ud83d\u00"`, `"\ud83d\uZZZZ"`, `"\ud83d\`, `"\ud83d\u`,
		"\"\xff\"", "\"\xc3\"", "\"\xc3\xa9\"", "\"\xe2\x82\"", "\"\xe2\x82\xac\"", "\"\xf0\x9f\x98\"", "\"\xf0\x9f\x98\x80\"", "\"\xf0\x9f\"", "\"\xf8\x88\x80\x80\x80\"", "\"\xc0\x80\"", "\"\xed\xa0\x80\"", "\"\xff", "\"\xc3", "\"\xe2\x82",
		"\"\x00\"", "\"\x1f\"", "\"\x7f\"", "\"a\nb\"", "\"a\tb\"",
		`"a"b`, `"a" `,
	} {
		check([]byte(quoted))
	}
	for _, length := range []int{jsonTextChunk - 1, jsonTextChunk, jsonTextChunk + 1, 2*jsonTextChunk + 3} {
		for _, tail := range []string{``, `é`, `😀`, `\u00e9`, `\ud83d\ude00`, "\xc3", "\xf0\x9f"} {
			check([]byte(`"` + strings.Repeat("x", length) + tail + `y"`))
		}
	}
	random := rand.New(rand.NewSource(1437))
	pieces := []string{"a", "é", "😀", `\n`, `\"`, `\\`, `\/`, `\u0041`, `\u00e9`, `\ud83d`, `\ude00`, `\u0000`, "\xff", "\xc3", "\xe2\x82", "\xf0\x9f\x98", "\x80", `\`, `\u`, `"`, "\n"}
	for range importRandomCases(5000) {
		var quoted bytes.Buffer
		quoted.WriteByte('"')
		for range random.Intn(12) {
			quoted.WriteString(pieces[random.Intn(len(pieces))])
		}
		quoted.WriteByte('"')
		check(quoted.Bytes())
	}
}

// A run of symbols decodes as those symbols added one by one do, wherever the runs fall.
func TestBase64RunsAgreeWithSymbols(t *testing.T) {
	random := rand.New(rand.NewSource(1437))
	alphabet := []byte("ABCDwxyz0189+/===!\xff ")
	for range importRandomCases(5000) {
		input := make([]byte, random.Intn(40))
		for i := range input {
			input[i] = alphabet[random.Intn(len(alphabet))]
		}
		if random.Intn(2) == 0 {
			input = []byte(base64.StdEncoding.EncodeToString(input))
		}
		var bySymbol, byRun []byte
		one := newBase64Stream(8, func(data []byte) error { bySymbol = append(bySymbol, data...); return nil })
		for _, char := range input {
			if err := one.add(char); err != nil {
				t.Fatal(err)
			}
		}
		if err := one.finish(); err != nil {
			t.Fatal(err)
		}
		runs := newBase64Stream(8, func(data []byte) error { byRun = append(byRun, data...); return nil })
		for rest := input; len(rest) > 0; {
			taken := 1 + random.Intn(len(rest))
			if err := runs.addRun(rest[:taken]); err != nil {
				t.Fatal(err)
			}
			rest = rest[taken:]
		}
		if err := runs.finish(); err != nil {
			t.Fatal(err)
		}
		if one.invalid != runs.invalid || (!one.invalid && !bytes.Equal(bySymbol, byRun)) {
			t.Fatalf("%q: by symbol %q, %t; by run %q, %t", input, bySymbol, one.invalid, byRun, runs.invalid)
		}
	}
}

// bindDeliveryBody answers what a route's reading of a body comes to: the refusal, as the wire has
// it, or the message and the text fields.
func bindDeliveryBody(t *testing.T, route, contentType string, body []byte) (refusal, message, text, html string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	var spool *deliverySpool
	var err error
	var boundText, boundHTML string
	if route == "gateway" {
		var req deliverMessageBody
		spool, err = spoolDelivery(r, &req, spooledBodySpec{declared: deliveryDeclaredFields, ignored: deliveryImportOnlyFields}, func() []byte { return req.RawMessage })
		boundText, boundHTML = req.BodyText, req.BodyHTML
	} else {
		var in restmailDeliverInput
		spool, err = spoolDelivery(r, &in, spooledBodySpec{declared: restmailDeclaredFields}, func() []byte { return in.Body.RawMessage })
		boundText, boundHTML = in.Body.BodyText, in.Body.BodyHTML
	}
	if err != nil {
		return refusalText(err), "", "", ""
	}
	defer spool.remove()
	if text, err = spool.text(deliveryBodyText, boundText); err != nil {
		t.Fatal(err)
	}
	if html, err = spool.text(deliveryBodyHTML, boundHTML); err != nil {
		t.Fatal(err)
	}
	if message, err = spool.raw.text(); err != nil {
		t.Fatal(err)
	}
	return "", message, text, html
}

// Whatever a body holds, the stand-in gets the answer the whole body gets from the binder, and
// the files hold what the binder would have read.
func TestDeliveryStandInAnswersAsTheWholeBodyDoes(t *testing.T) {
	bodies := []string{
		`{"address":"a@b.test","raw_message":"UVE=","body_text":"text","body_html":"html"}`,
		`{"to":["a@b.test"],"raw_message":"UVE=","body_text":"text","body_html":"html"}`,
		`{"raw_message":"UVE=","raw_message":"QUI="}`, `{"raw_message":"!!","raw_message":"QUI="}`, `{"raw_message":"QUI=","raw_message":"!!"}`, `{"raw_message":1,"raw_message":"QUI="}`,
		`{"raw_message":null,"raw_message":"QUI="}`, `{"raw_message":"QUI=","raw_message":null}`, `{"raw_message":[65,66]}`, `{"raw_message":[65,300]}`, `{"raw_message":[]}`,
		`{"raw_message":""}`, `{"raw_message":"\n"}`, `{"raw_message":"Q\u0051\u003d\u003D"}`, `{"raw_message":"QQ\r\n=="}`, `{"raw_message":"Q\/=="}`, `{"raw_message":"\u00e9"}`, `{"raw_message":"\ud83d\ude00"}`,
		"{\"raw_message\":\"\xff\"}", "{\"raw_message\":\"Q\x01\"}", `{"raw_message":null}`, `{"raw_message":true}`, `{"raw_message":{}}`, `{"raw\u005fmessage":"QQ=="}`, `{"RAW_MESSAGE":"QQ=="}`,
		`{"raw_message":"a\u0000b"}`, `{"raw_message":"Subject: caf\u00e9\r\n\r\nb"}`,
		`{"body_text":""}`, `{"body_text":"\u0000"}`, `{"body_text":"a\u0000b"}`, `{"body_html":"a\u0000"}`, `{"body_text":null}`, `{"body_text":1}`, `{"body_text":[]}`, `{"body_text":{}}`, `{"body_text":true}`,
		`{"body_text":"one","body_text":"two"}`, `{"body_text":"\u0000","body_text":"two"}`, `{"body_text":"one","body_text":"\u0000"}`, `{"body_text":1,"body_text":"two"}`, `{"body_text":"one","body_text":1}`, `{"body_text":"one","body_text":null}`, `{"body_text":null,"body_text":"two"}`,
		`{"body_html":"one","body_text":"two","body_html":"three"}`, `{"body\u005ftext":"x"}`, `{"BODY_TEXT":"x"}`, "{\"body_text\":\"\xff\"}", `{"body_text":"\ud83d"}`, `{"body_text":"\q"}`, `{"body_text":"`, `{"body_text":"a`,
		`{"address":"\u0000"}`, `{"subject":"\u0000"}`, `{"folder":" x"}`, `{"folder":"a"}`, `{"folder":1}`, `{"mailbox_id":"x"}`, `{"mailbox_id":-1}`, `{"mailbox_id":1.5}`,
		`{"recipients_to":["\u0000"]}`, `{"recipients_cc":{"\u0000":1}}`, `{"recipients_to":`, `{"received_at":"yesterday"}`, `{"is_read":1}`, `{"require_tls":"x"}`,
		`{"to":[]}`, `{"to":"x"}`, `{"to":[1]}`, `{"recipients":[]}`, `{"recipients":["a"]}`, `{"from":1}`, `{"headers":{"a":1}}`, `{"headers":1}`, `{"date":1}`, `{"sender":1}`,
		`{"x":"y"}`, `{"x":{"deep":[1,2,3]},"y":null}`, `{"raw_message":"QQ==","x":1}`,
		`{}`, `[]`, `[1,2]`, `null`, ` null `, `1`, `"x"`, `true`, ``, ` `, "\n\t ", `{`, `{"raw_message"`, `{"raw_message":`, `{"raw_message":"QQ==`, `{"raw_message":"QQ=="`, `{"raw_message":"QQ==",`, `{"raw_message":"QQ=="}x`, `{"raw_message":"QQ==",}`, `{"raw_message":"QQ=="} {}`, `nul`,
	}
	random := rand.New(rand.NewSource(1437))
	alphabet := []byte("{}[],:\"\\/ \n0123-.etrufalsn!=QA\xff\x00")
	seeds := [][]byte{
		[]byte(`{"address":"a@b.test","sender":"s","raw_message":"UVE9PQ==","body_text":"t\u00e9\n","body_html":"h","folder":"Archive","recipients_to":["x"],"is_read":true}`),
		[]byte(`{"from":"a@b.test","to":["c@d.test"],"raw_message":"U3ViamVjdDogcQ0KDQpi","body_text":"t\n","body_html":"h","headers":{"a":["b"]}}`),
	}
	for range importRandomCases(5000) {
		data := append([]byte(nil), seeds[random.Intn(len(seeds))]...)
		for range 1 + random.Intn(2) {
			at := random.Intn(len(data))
			char := alphabet[random.Intn(len(alphabet))]
			switch random.Intn(3) {
			case 0:
				data = append(data[:at:at], append([]byte{char}, data[at:]...)...)
			case 1:
				data = append(data[:at:at], data[at+1:]...)
			default:
				data[at] = char
			}
		}
		bodies = append(bodies, string(data))
	}
	whole := func(route, contentType, body string) (refusal, message, text, html string) {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		if route == "gateway" {
			// The delivery route takes no notice of the import-only fields older callers still
			// send (#1474), so the whole body it is held to is the one without them.
			var req deliverMessageBody
			if err := endpoint.BindRequest(r, &req, withoutFields([]byte(body), deliveryImportOnlyFields)); err != nil {
				return refusalText(err), "", "", ""
			}
			return "", string(req.RawMessage), req.BodyText, req.BodyHTML
		}
		var in restmailDeliverInput
		if err := endpoint.BindRequest(r, &in, []byte(body)); err != nil {
			return refusalText(err), "", "", ""
		}
		return "", string(in.Body.RawMessage), in.Body.BodyText, in.Body.BodyHTML
	}
	for _, route := range []string{"gateway", "restmail"} {
		for _, contentType := range []string{"application/json", "", "text/plain"} {
			for i, body := range bodies {
				if i > 120 && contentType != "application/json" {
					break // every written case with every content type, the random ones with one
				}
				gotRefusal, gotMessage, gotText, gotHTML := bindDeliveryBody(t, route, contentType, []byte(body))
				wantRefusal, wantMessage, wantText, wantHTML := whole(route, contentType, body)
				if gotRefusal != wantRefusal {
					t.Fatalf("%s, %q as %q:\n got %s\nwant %s", route, body, contentType, gotRefusal, wantRefusal)
				}
				if gotMessage != wantMessage || gotText != wantText || gotHTML != wantHTML {
					t.Fatalf("%s, %q: read %q, %q, %q, want %q, %q, %q", route, body, gotMessage, gotText, gotHTML, wantMessage, wantText, wantHTML)
				}
			}
		}
	}
}

// The section is the bytes up to and including the first empty line, and a parse from it says what
// a parse of the whole message says about headers. What's after it is left alone.
func TestHeaderSectionIsWhatStripAuthResultsWalks(t *testing.T) {
	spoolDirectory(t)
	for _, tc := range []struct{ message, want string }{
		{"", ""},
		{"\r\nbody", "\r\n"},
		{"\nbody", "\n"},
		{"A: b\r\n\r\nbody\r\n\r\nmore", "A: b\r\n\r\n"},
		{"A: b\n\nbody", "A: b\n\n"},
		{"A: b\r\n c\r\n\r\nbody", "A: b\r\n c\r\n\r\n"},
		{"A: b\r\n \r\nC: d\r\n\r\nbody", "A: b\r\n \r\nC: d\r\n\r\n"},
		{"A: b", "A: b"},
		{"A: b\r\n", "A: b\r\n"},
		{"A: b\r\rC: d\r\n\r\nbody", "A: b\r\rC: d\r\n\r\n"},
		{"Authentication-Results: restmail; spf=pass\r\nA: b\r\n\r\nAuthentication-Results: restmail; x\r\n", "A: b\r\n\r\n"},
		{"Authentication-Results: restmail; spf=pass", ""},
		{"Authentication-Results: other; spf=pass\r\n\r\nbody", "Authentication-Results: other; spf=pass\r\n\r\n"},
	} {
		source, err := newSpooledText() // as a delivery's raw message is
		if err != nil {
			t.Fatal(err)
		}
		defer source.remove()
		if err := source.write([]byte(tc.message)); err != nil {
			t.Fatal(err)
		}
		got, err := (&deliverySpool{raw: source}).headerSection()
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Fatalf("%q: section %q, want %q", tc.message, got, tc.want)
		}
		if whole, err := (&deliverySpool{raw: source}).message(); err != nil || !strings.HasPrefix(whole, got) {
			t.Fatalf("%q: the message %q doesn't start with its section %q, %v", tc.message, whole, got, err)
		}
	}
}

// A signature that verification would find is one the check on the section finds.
func TestHeaderNamesDKIMSignatureFindsWhatVerificationFinds(t *testing.T) {
	for section, want := range map[string]bool{
		"":                           false,
		"From: a\r\n\r\n":            false,
		"DKIM-Signature: v=1\r\n":    true,
		"dkim-signature : v=1\r\n":   true,
		"DKIM-SIGNATURE:v=1\r\n\r\n": true,
		"X-Note: dkim-signature\r\n": true, // read for nothing, but never skipped
	} {
		if got := headerNamesDKIMSignature(section); got != want {
			t.Fatalf("%q: %t", section, got)
		}
	}
}
