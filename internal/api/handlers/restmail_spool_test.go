package handlers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"
	"unicode/utf8"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// decodeJSONText reads body as one JSON string with the scanner, from src, and answers what it
// made of it, or false where the scanner says it isn't one.
func decodeJSONText(t *testing.T, body []byte, src *bufio.Reader) ([]byte, bool) {
	t.Helper()
	var got []byte
	s := &jsonScanner{src: src}
	first, err := s.skipSpace()
	if err != nil || first != '"' {
		return nil, false
	}
	if err := s.textRest(func(data []byte) error {
		if len(data) > 2*jsonTextChunk+utf8.UTFMax {
			t.Fatalf("%q: held %d bytes", body, len(data))
		}
		got = append(got, data...)
		return nil
	}); err != nil {
		if !errors.Is(err, errNotJSON) {
			t.Fatal(err)
		}
		return nil, false
	}
	if _, err := s.skipSpace(); !errors.Is(err, errNotJSON) {
		return nil, false
	}
	return got, true
}

// A string decodes to what encoding/json makes of it, however hostile, and wherever a read or a
// chunk happens to fall.
func TestJSONTextStreamAgreesWithEncodingJSON(t *testing.T) {
	check := func(content string) {
		t.Helper()
		body := []byte(`"` + content + `"`)
		var want string
		wantErr := json.Unmarshal(body, &want)
		for _, src := range []*bufio.Reader{
			bufio.NewReaderSize(bytes.NewReader(body), 16),
			bufio.NewReaderSize(bytes.NewReader(body), 17),
			bufio.NewReaderSize(bytes.NewReader(body), 4096),
			bufio.NewReaderSize(bytes.NewReader(body), 64<<10),
			bufio.NewReaderSize(iotest.OneByteReader(bytes.NewReader(body)), 16),
			bufio.NewReaderSize(iotest.HalfReader(bytes.NewReader(body)), 64<<10),
		} {
			got, ok := decodeJSONText(t, body, src)
			if ok != (wantErr == nil) {
				t.Fatalf("%q: scanner says %t, encoding/json says %v", body, ok, wantErr)
			}
			if ok && string(got) != want {
				t.Fatalf("%q: decoded %q, encoding/json %q", body, got, want)
			}
		}
	}
	hostile := []string{
		``, `a`, `\"`, `\\`, `\/`, `\b\f\n\r\t`,
		// Invalid escapes, and the ones encoding/json only knows of past its scanner.
		`\`, `\a`, `\'`, `\x41`, `\0`, `\U0041`, `\u`, `\u1`, `\u12`, `\u123`, `\u123g`, `\ug123`, `\u 123`, `\uD83D\`, `\uD83D\u`, `\uD83D\uDE0`, `\uD83D\uDE0g`,
		// Escapes that hold what a string can't.
		`\u0000`, `\u0001`, `\u001f`, `\u0022`, `\u005c`, `\u007f`, `\u0080`, `\u00e9`, `\u00E9`, `\u07ff`, `\u0800`, `\uFFFD`, `\uFFFE`, `\uFFFF`,
		// Surrogates, alone, in pairs and out of them.
		`\uD83D\uDE00`, `\ud83d\ude00`, `\uD800\uDC00`, `\uDBFF\uDFFF`, `\uD83D`, `\uDE00`, `\uDE00\uD83D`, `\uD83D\uD83D`, `\uD83D\uD83D\uDE00`, `\uDE00\uDE00`, `\uD83D\uDE00\uDE00`, `\uD83D\uD83D\uD83D`,
		`\uD83Da`, `a\uD83D`, `\uD83D\n`, `\uD83D\\`, `\uD83D\"`, `\uD83D\u0041`, `\uD83D\uFFFD`, `\uD83D` + "\xc3\xa9", `\uD83D` + "\xff", `\uDE00a`, "\xf0\x9f" + `\uDE00`, "\xed\xa0\x80" + `\uDE00`,
		// Raw octets: valid, cut short, overlong, surrogates in UTF-8, past the last code point.
		"\u00e9", "\u20ac", "\U0001F600", "\ufffd", "\xff", "\xfe\xff", "\xc3", "\xc3a", "\xe2\x82", "\xe2\x82a", "\xf0\x9f\x98", "\xf0\x9f\x98a", "\xc0\x80", "\xc1\xbf", "\xe0\x80\x80", "\xe0\x9f\xbf", "\xed\xa0\x80", "\xed\xbf\xbf", "\xf0\x8f\xbf\xbf", "\xf4\x90\x80\x80", "\xf5\x80\x80\x80", "\x80", "\xbf", "\x80\x80\x80\x80\x80", "\xf0\xf0\xf0\xf0\xf0", "\xe2\xe2\x82\xac",
		"\xc3" + `\n`, "\xe2\x82" + `\u0041`, "\xf0\x9f\x98" + `\\`, "\xc3\u00e9", `\u00c3` + "\xa9",
		// Raw control characters, which are never allowed. Only the string's own end is tested
		// here, since a quote inside it ends it.
		"\x00", "\x01", "\x1f", "a\nb", "a\tb", "\x7f", "\xc3\x00", `\uD83D` + "\x01",
		// A character on each side of where a chunk ends.
		strings.Repeat("a", 3) + "\U0001F600", strings.Repeat("a", 13) + "\U0001F600", strings.Repeat("a", 14) + "\U0001F600", strings.Repeat("a", 15) + `\uD83D\uDE00`, strings.Repeat("a", 60) + "\U0001F600", strings.Repeat("a", 61) + "\xf0\x9f\x98",
		strings.Repeat("a", jsonTextChunk-2) + "\U0001F600", strings.Repeat("a", jsonTextChunk-3) + `\uD83D\uDE00`, strings.Repeat("a", jsonTextChunk-1) + "\xf0\x9f\x98", strings.Repeat("a", jsonTextChunk) + `\n`, strings.Repeat("a", jsonTextChunk+1) + "\xc3",
		strings.Repeat("a", 64<<10-1) + "\xc3\xa9", strings.Repeat("a", 64<<10-3) + "\U0001F600b", strings.Repeat("a", 64<<10) + `\uD83D\uDE00`, strings.Repeat(`line\r\n`, 40000), strings.Repeat("a", 2*jsonTextChunk-1) + `\n` + strings.Repeat("b", jsonTextChunk-1) + `\n` + strings.Repeat("c", jsonTextChunk+7),
	}
	for _, content := range hostile {
		check(content)
	}
	// A quote left open, or cut in an escape: never a string, whatever came before.
	for _, body := range []string{`"`, `"a`, `"\`, `"\"`, `"\u`, `"\u00`, `"\uD83D`, `"\uD83D\`, `"\uD83D\u`, `"\uD83D\uDE0`, "\"\xc3", "\"\xe2\x82"} {
		if json.Valid([]byte(body)) {
			t.Fatalf("%q is valid", body)
		}
		if _, ok := decodeJSONText(t, []byte(body), bufio.NewReaderSize(bytes.NewReader([]byte(body)), 16)); ok {
			t.Fatalf("%q read as a string", body)
		}
	}
	random := rand.New(rand.NewSource(1915))
	pieces := []string{`\uD83D`, `\uDE00`, `\uD800`, `\uDFFF`, `\u0041`, `\u0000`, `\uFFFD`, `\u00e9`, `\n`, `\\`, `\/`, `\"`, `\`, `\u`, `\x`, "a", "\r", "\xc3", "\xa9", "\xe2", "\x82", "\xac", "\xf0", "\x9f", "\x98", "\x80", "\xff", "\xed", "\xa0", "\u00e9", "\U0001F600", "\ufffd", "\x00", "\x7f"}
	for range importRandomCases(50000) {
		var content strings.Builder
		for range random.Intn(12) {
			content.WriteString(pieces[random.Intn(len(pieces))])
		}
		check(content.String())
	}
}

// restmailBindAnswer is what binding a body comes to: the refusal, as the wire has it, or the body.
type restmailBindAnswer struct {
	refusal string
	body    restmailDeliverBody
}

func restmailBindRequest(body []byte) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func restmailBoundWhole(body []byte) restmailBindAnswer {
	var in restmailDeliverInput
	if err := endpoint.BindRequest(restmailBindRequest(body), &in, body); err != nil {
		return restmailBindAnswer{refusal: refusalText(err)}
	}
	return restmailBindAnswer{body: in.Body}
}

func restmailBoundSpooled(t *testing.T, body []byte) restmailBindAnswer {
	t.Helper()
	var in restmailDeliverInput
	spool, err := spoolDelivery(restmailBindRequest(body), &in, spooledBodySpec{rawIsText: true, declared: restmailDeclaredFields}, func() []byte { return []byte(in.Body.RawMessage) })
	if err != nil {
		return restmailBindAnswer{refusal: refusalText(err)}
	}
	defer spool.remove()
	if in.Body.RawMessage, err = spool.raw.text(); err != nil {
		t.Fatal(err)
	}
	if in.Body.BodyText, err = spool.text(deliveryBodyText, in.Body.BodyText); err != nil {
		t.Fatal(err)
	}
	if in.Body.BodyHTML, err = spool.text(deliveryBodyHTML, in.Body.BodyHTML); err != nil {
		t.Fatal(err)
	}
	return restmailBindAnswer{body: in.Body}
}

// Whatever a body holds, the stand-in gets the answer the whole body gets from the binder, and
// the file holds what the binder would have decoded.
func TestRestmailStandInAnswersAsTheWholeBodyDoes(t *testing.T) {
	to := `"to":["a@b.test"]`
	bodies := []string{
		``, ` `, `{`, `}`, `{}`, `[]`, `null`, `1`, `"x"`, `{ }`,
		`{"raw_message":"x"}`, `{"raw_message":""}`, `{"raw_message":"\n"}`, `{"raw_message":"\u0000"}`, `{"raw_message":"\ud83d"}`, `{"raw_message":"\ud83d\ude00"}`,
		"{\"raw_message\":\"\xff\"}", "{\"raw_message\":\"\xc3\"}", "{\"raw_message\":\"a\x01\"}", `{"raw_message":"\x"}`, `{"raw_message":"\u12"}`, `{"raw_message":"`,
		`{"raw_message":"x",` + to + `}`, `{` + to + `,"raw_message":"x"}`, `{"raw_message":"",` + to + `}`, `{"raw_message":"","to":[]}`, `{"raw_message":"x","to":[]}`, `{"to":[]}`, `{` + to + `}`,
		`{"raw_message":null}`, `{"raw_message":null,` + to + `}`, `{"raw_message":1}`, `{"raw_message":1,` + to + `}`, `{"raw_message":true,` + to + `}`, `{"raw_message":{},` + to + `}`, `{"raw_message":[65],` + to + `}`,
		`{"raw_message":"a","raw_message":"b"}`, `{"raw_message":"a","raw_message":""}`, `{"raw_message":"","raw_message":"a"}`, `{"raw_message":1,"raw_message":"a"}`, `{"raw_message":"a","raw_message":1}`, `{"raw_message":"a","raw_message":null}`, `{"raw_message":null,"raw_message":"a"}`,
		`{"raw\u005fmessage":"x"}`, `{"RAW_MESSAGE":"x"}`, `{"Raw_Message":"x",` + to + `}`, `{"raw_message":"x","RAW_MESSAGE":"y"}`,
		`{"raw_message":"x","extra":1}`, `{"raw_message":"x","extra":{"deep":[1,2]},` + to + `}`, `{"raw_message":"x","y":null}`,
		`{"raw_message":"x","from":"a@b.test","sender":"s@b.test","subject":"s","date":"d","body_text":"t","body_html":"h","message_id":"m","in_reply_to":"i","references":"r","recipients":["c@d.test"],"headers":{"X":["y"]}}`,
		`{"raw_message":"x","to":"a@b.test"}`, `{"raw_message":"x","to":1}`, `{"raw_message":"x","to":[1]}`, `{"raw_message":"x","to":null}`, `{"raw_message":"x","from":1}`, `{"raw_message":"x","headers":1}`, `{"raw_message":"x","recipients":[]}`, `{"raw_message":"","recipients":["a@b.test"]}`, `{"raw_message":"","recipients":[]}`,
		`{"raw_message":"x","to":["a@b.test"`, `{"raw_message":"x","x":[1,]}`, `{"raw_message":"x",}`, `{"raw_message":"x"} x`, `{"raw_message" "x"}`, `{"raw_message":}`,
		" \r\n{\"raw_message\":\"x\"}\t ", `{ "raw_message" : "x" , ` + to + ` }`,
	}
	for _, body := range bodies {
		want, got := restmailBoundWhole([]byte(body)), restmailBoundSpooled(t, []byte(body))
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%q:\nspooled %+v\nwhole   %+v", body, got, want)
		}
	}
	random := rand.New(rand.NewSource(1915))
	alphabet := []byte("{}[],:\"\\ 0a\x00\x01\xc3\xa9\xffu")
	seeds := []string{`{"raw_message":"s\n\ud83d\ude00\u00e9",` + to + `,"from":"a@b.test"}`, `{"subject":"q","raw_message":"From: a@b.test\r\n\r\nb","recipients":["c@d.test"]}`, `{"raw_message":"","to":[]}`, `{"raw_message":"a","raw_message":"b"}`}
	for range importRandomCases(5000) {
		data := []byte(seeds[random.Intn(len(seeds))])
		for range 1 + random.Intn(3) {
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
			if len(data) == 0 {
				data = []byte(`{}`)
			}
		}
		want, got := restmailBoundWhole(data), restmailBoundSpooled(t, data)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%q:\nspooled %+v\nwhole   %+v", data, got, want)
		}
	}
}

// restmailLimitFixture is a mailbox and the route, in a temporary directory only this test
// spools to.
func restmailLimitFixture(t *testing.T) (http.Handler, models.Mailbox, func(*testing.T) models.Message) {
	t.Helper()
	db := importProofDB(t)
	if err := db.AutoMigrate(&models.Pipeline{}, &models.OutboundQueue{}); err != nil {
		t.Fatal(err)
	}
	domain := models.Domain{Name: "restmail-limit.test", Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	mailbox := models.Mailbox{DomainID: domain.ID, LocalPart: "to", Address: "to@restmail-limit.test", Active: true, QuotaBytes: 1 << 40}
	if err := db.Create(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.QuotaUsage{MailboxID: mailbox.ID}).Error; err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(t, db, mailbox)
	h := NewRestmailHandler(db, nil, nil, RestmailTarpitConfig{}, RestmailDeliverAuthConfig{TrustedCIDRs: []string{"192.0.2.10/32"}})
	last := func(t *testing.T) models.Message {
		t.Helper()
		var stored models.Message
		if err := db.Where("mailbox_id = ?", mailbox.ID).Order("id DESC").First(&stored).Error; err != nil {
			t.Fatal(err)
		}
		return stored
	}
	return restmailRoutes(h), mailbox, last
}

// What the federation client sends: the envelope and the source.
func restmailClientBody(t *testing.T, to, message string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"from": "old@example.test", "to": []string{to}, "raw_message": message})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func postRestmail(routes http.Handler, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, restmailMessagesPath, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "192.0.2.10:1234"
	w := httptest.NewRecorder()
	routes.ServeHTTP(w, r)
	return w
}

// RESTMAIL holds the store-path limit on what the federation client sends: its request is a file
// until the message is read, once, and that one read parses it once and gives the row its text.
// Every shape stores text, because the limit only means something if the text was stored too.
// Long address lists and many parameters on one field are still over it, which #1915 records.
func TestRestmailHoldsTheLimitOnHostileShapes(t *testing.T) {
	const size = 1 << 20
	const id = "From: old@example.test\r\nSubject: s\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\n"
	spool := t.TempDir()
	t.Setenv("TMPDIR", spool)
	routes, mailbox, last := restmailLimitFixture(t)
	// What the first delivery in a process builds and keeps isn't the message's to answer for.
	if w := postRestmail(routes, restmailClientBody(t, mailbox.Address, "Message-ID: <warm@example.test>\r\n"+id+"\r\nb\r\n")); w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	// A message nested in another isn't that message's text, so the shapes with parts carry a text
	// part beside them, of half the message.
	mixed := "Content-Type: multipart/mixed; boundary=B\r\n\r\n--B\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + strings.Repeat("\u00f6", size/4) + "\r\n"
	for name, message := range map[string]string{
		"plain ASCII":              id + "Content-Type: text/plain\r\n\r\n" + strings.Repeat("hello world\r\n", size/13),
		"8-bit text, no charset":   id + "Content-Type: text/plain\r\n\r\n" + strings.Repeat("\u00f6", size/2) + "\r\n",
		"60 nested messages":       id + mixed + "--B\r\n" + strings.Repeat("Content-Type: message/rfc822\r\n\r\n", 60) + "Subject: \u00f6\r\n\r\n" + strings.Repeat("\u00f6", size/4) + "\r\n--B--\r\n",
		"1000 typed parts":         id + mixed + strings.Repeat("--B\r\nContent-Type: a/b\r\n\r\n\u00f6"+strings.Repeat("y", size/2000)+"\r\n", 999) + "--B--\r\n",
		"typed parts in a message": id + mixed + "--B\r\nContent-Type: message/rfc822\r\n\r\nContent-Type: multipart/mixed; boundary=C\r\n\r\n" + strings.Repeat("--C\r\nContent-Type: a/b\r\n\r\nx\r\n", size/56) + "--C--\r\n--B--\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			// TotalAlloc counts the whole process, so a run gets another try before it fails: a
			// message that really costs too much costs too much every time.
			best, length := uint64(1)<<63, 0
			var want string
			for attempt := range 3 {
				want = fmt.Sprintf("Message-ID: <%s-%d@example.test>\r\n", strings.ReplaceAll(name, " ", "-"), attempt) + message
				length = len(want)
				body := restmailClientBody(t, mailbox.Address, want)
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				w := postRestmail(routes, body)
				runtime.ReadMemStats(&after)
				if w.Code != http.StatusCreated {
					t.Fatalf("%d %s", w.Code, w.Body)
				}
				var answer struct{ Data RestmailDelivery }
				if err := json.Unmarshal(w.Body.Bytes(), &answer); err != nil {
					t.Fatal(err)
				}
				if len(answer.Data.Delivered) != 1 || len(answer.Data.Failed) != 0 {
					t.Fatalf("answer %s", w.Body)
				}
				best = min(best, after.TotalAlloc-before.TotalAlloc)
				if best <= 8*uint64(length) {
					break
				}
			}
			t.Logf("delivering %d bytes allocated %d, %.1f times over", length, best, float64(best)/float64(length))
			if best > 8*uint64(length) {
				t.Fatalf("delivering %d bytes allocated %d", length, best)
			}
			stored := last(t)
			if len(stored.BodyText) < size/4 {
				t.Fatalf("stored %d bytes of text", len(stored.BodyText))
			}
			if stored.RawMessage != want {
				t.Fatalf("stored %d bytes of source for %d", len(stored.RawMessage), len(want))
			}
		})
	}
	left, err := os.ReadDir(spool)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("%d files left behind, the first %s", len(left), left[0].Name())
	}
}

// A hostile string stores what encoding/json makes of it, the same way a refusal leaves no file.
func TestRestmailStoresTheStringAsJSONReadsIt(t *testing.T) {
	spool := t.TempDir()
	t.Setenv("TMPDIR", spool)
	routes, mailbox, last := restmailLimitFixture(t)
	head := `Message-ID: <hostile@example.test>\r\nFrom: old@example.test\r\nSubject: s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n`
	content := head + "a\\uD83D\\uDE00b\\uD83Dc\\uDE00d\xc3\xa9e\xc3f\xffg\\u00e9h\\/i\\n"
	body := []byte(`{"from":"old@example.test","to":["` + mailbox.Address + `"],"raw_message":"` + content + `"}`)
	var want struct {
		RawMessage string `json:"raw_message"`
	}
	if err := json.Unmarshal(body, &want); err != nil {
		t.Fatal(err)
	}
	if w := postRestmail(routes, body); w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	stored := last(t)
	if stored.RawMessage != want.RawMessage || !strings.HasSuffix(want.RawMessage, "a\U0001F600b\ufffdc\ufffdd\u00e9e\ufffdf\ufffdg\u00e9h/i\n") {
		t.Fatalf("stored %q, encoding/json read %q", stored.RawMessage, want.RawMessage)
	}
	if stored.RawSize != len(want.RawMessage) {
		t.Fatalf("raw size %d for %d octets", stored.RawSize, len(want.RawMessage))
	}
	if w := postRestmail(routes, []byte(`{"to":["`+mailbox.Address+`"],"raw_message":"cut`)); w.Code != http.StatusBadRequest {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	left, err := os.ReadDir(spool)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("%d files left behind, the first %s", len(left), left[0].Name())
	}
}
