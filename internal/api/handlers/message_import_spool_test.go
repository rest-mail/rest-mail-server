package handlers

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/attachmentstore"
	"github.com/rest-mail/rest-mail-server/internal/attachmentstore/s3test"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"gorm.io/gorm"
)

// importRandomCases is how many random inputs a comparison below runs: a tenth of them by
// default, the same ones first, and all of them with RESTMAIL_IMPORT_LONG_PROOF=1, which CI
// sets. The default keeps a local run quick. The tests' tables hold an input for every
// statement the rest reach, in the scanner and the binder.
func importRandomCases(all int) int {
	if os.Getenv("RESTMAIL_IMPORT_LONG_PROOF") == "1" {
		return all
	}
	return all / 10
}

// The scanner has to say what json.Valid says about anything at all, because only a body that
// passes reaches any other rule.
func TestJSONScannerAgreesWithJSONValid(t *testing.T) {
	valid := func(data []byte) bool {
		s := &jsonScanner{src: bufio.NewReader(bytes.NewReader(data))}
		first, err := s.skipSpace()
		if err != nil {
			return false
		}
		if err := s.value(first); err != nil {
			return false
		}
		_, err = s.skipSpace()
		return errors.Is(err, errNotJSON)
	}
	check := func(data []byte) {
		t.Helper()
		if got, want := valid(data), json.Valid(data); got != want {
			t.Fatalf("%q: scanner says %t, json.Valid says %t", data, got, want)
		}
	}
	for _, body := range []string{
		``, ` `, `{`, `}`, `[`, `]`, `{}`, `[]`, `[,]`, `{,}`, `[1,]`, `{"a":1,}`, `{"a"}`, `{"a":}`, `{a:1}`, `{"a":1 "b":2}`, `[1 2]`, `{"a":1}x`, `{} {}`, `[] 1`,
		`0`, `-0`, `-`, `01`, `-01`, `1.`, `.5`, `1.5`, `1e5`, `1E+5`, `1e-5`, `1e`, `1e+`, `1.e5`, `+1`, `0x10`, `1.5.5`, `123456789012345678901234567890`, `0e0`, `00`,
		`true`, `false`, `null`, `tru`, `nul`, `True`, `NULL`, `truee`, `nulll`, `true false`,
		`""`, `"`, `"a`, `"\`, `"\"`, `"\\"`, `"\/"`, `"\b\f\n\r\t"`, `"\a"`, `"\u"`, `"\u12"`, `"\u123g"`, `"\u00e9"`, `"\uD83D\uDE00"`, `"\uD83D"`, "\"a\tb\"", "\"a\nb\"", "\"\x00\"", "\"\x1f\"", "\"\x7f\"", "\"\xff\"", "\"\xc3\xa9\"",
		" \t\r\n{} \t\r\n", "\u00a0{}", "\ufeff{}", "\x0b{}", "\x0c{}",
		`{"a":{"b":[1,2,{"c":null}]},"d":"e"}`, `[[[[]]]]`, `[[[[]]]`, `[{]}`, `{[}]`, `{"a":[}]}`, `[{"a"`, `{"a"`, `{"a":1,`, `[1,`, `{"a":{"b"`, `{"a":{"b":1,`,
	} {
		check([]byte(body))
	}
	for _, depth := range []int{jsonMaxDepth - 1, jsonMaxDepth, jsonMaxDepth + 1} {
		check([]byte(strings.Repeat("[", depth) + strings.Repeat("]", depth)))
		check([]byte(strings.Repeat(`{"a":`, depth-1) + "{}" + strings.Repeat("}", depth-1)))
	}
	random := rand.New(rand.NewSource(1437))
	alphabet := []byte("{}[],:\"\\/ \t\r\n0123456789-+.eEtrufalsn\x00\x1f\x7f\xc3\xa9\xffbx")
	seeds := []string{`{"a":[1,-2.5e3,true,false,null,"s\n\u00e9"],"b":{"c":{}}}`, `[0,"\"",[],{}]`, `-12.5E-3`, `"\uD83D\uDE00"`}
	for range importRandomCases(50000) {
		data := []byte(seeds[random.Intn(len(seeds))])
		for range 1 + random.Intn(3) {
			at := random.Intn(len(data) + 1)
			char := alphabet[random.Intn(len(alphabet))]
			switch random.Intn(3) {
			case 0:
				data = append(data[:at:at], append([]byte{char}, data[at:]...)...)
			case 1:
				if at < len(data) {
					data = append(data[:at:at], data[at+1:]...)
				}
			default:
				if at < len(data) {
					data[at] = char
				}
			}
		}
		check(data)
	}
}

// Valid is whatever DecodeString takes, however the symbols fall across chunks, and it decodes
// to what DecodeString decodes to.
func TestBase64StreamAgreesWithDecodeString(t *testing.T) {
	alphabet := []byte("ABCDwxyz0189+/===\r\n!\x00\xff ")
	random := rand.New(rand.NewSource(1437))
	cases := []string{"", "\n", "\r\n", "QQ==", "QUI=", "QUJD", "Q", "QQ", "QQ=", "QQ==\n", "Q\nQ\n=\r\n=", "QQ==QQ==", "QUJDQQ==", "QQ==\nQ", "QUJDQUJD", "Q===", "====", "=", "QQ=A", "QUI=QUI=", "QUJD=", "QUJDQ", "QR==", "QUJ="}
	for range importRandomCases(20000) {
		text := make([]byte, random.Intn(40))
		for i := range text {
			text[i] = alphabet[random.Intn(len(alphabet))]
		}
		cases = append(cases, string(text))
		// Mostly valid: a good encoding with a little done to it.
		good := []byte(base64.StdEncoding.EncodeToString(text))
		if len(good) > 0 && random.Intn(2) == 0 {
			good[random.Intn(len(good))] = alphabet[random.Intn(len(alphabet))]
		}
		cases = append(cases, string(good))
	}
	for _, chunk := range []int{4, 8, 16, base64Chunk} {
		for _, input := range cases {
			want, wantErr := base64.StdEncoding.DecodeString(input)
			var got []byte
			stream := newBase64Stream(chunk, func(data []byte) error { got = append(got, data...); return nil })
			for i := range len(input) {
				if err := stream.add(input[i]); err != nil {
					t.Fatal(err)
				}
			}
			if err := stream.finish(); err != nil {
				t.Fatal(err)
			}
			if stream.invalid != (wantErr != nil) {
				t.Fatalf("chunk %d, %q: invalid=%t, DecodeString said %v", chunk, input, stream.invalid, wantErr)
			}
			if wantErr == nil && !bytes.Equal(got, want) {
				t.Fatalf("chunk %d, %q: decoded %q, want %q", chunk, input, got, want)
			}
		}
	}
}

// bindImportBody answers what the route's reading of a body comes to: the refusal, as the wire
// has it, or the message.
func bindImportBody(t *testing.T, id, contentType string, body []byte, limit int64) (refusal string, message []byte, tooLarge bool) {
	t.Helper()
	request := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		route := chi.NewRouteContext()
		route.URLParams.Add("id", id)
		return r.WithContext(contextWithRoute(r, route))
	}
	source, err := newImportSource(limit)
	if err != nil {
		t.Fatal(err)
	}
	defer source.remove()
	scanned, err := scanImportBody(bytes.NewReader(body), source)
	if err != nil {
		t.Fatal(err)
	}
	standIn := scanned.standIn
	if scanned.bindWhole {
		standIn = body
	}
	var in importMessageRequest
	if err := endpoint.BindRequest(request(), &in, standIn); err != nil {
		return refusalText(err), nil, false
	}
	if !scanned.streamed {
		if err := source.replace(in.Body.RawMessage); err != nil {
			t.Fatal(err)
		}
	}
	if source.tooLarge() {
		return "", nil, true
	}
	reader, err := source.reader()
	if err != nil {
		t.Fatal(err)
	}
	message, err = io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if source.checksum != sha256.Sum256(message) {
		t.Fatalf("%q: the checksum isn't the message's", body)
	}
	if int64(len(message)) != source.size {
		t.Fatalf("%q: size %d for %d bytes", body, source.size, len(message))
	}
	return "", message, false
}

func contextWithRoute(r *http.Request, route *chi.Context) context.Context {
	return context.WithValue(r.Context(), chi.RouteCtxKey, route)
}

func refusalText(err error) string {
	var refusal *endpoint.Error
	if !errors.As(err, &refusal) {
		return "not a refusal: " + err.Error()
	}
	encoded, _ := json.Marshal(refusal)
	return string(encoded)
}

// Whatever a body holds, the stand-in gets the answer the whole body gets from the binder, and
// the file holds what the binder would have decoded.
func TestImportStandInAnswersAsTheWholeBodyDoes(t *testing.T) {
	message := base64.StdEncoding.EncodeToString([]byte("Subject: x\r\n\r\nbody\xff\x00\r\n"))
	date := `"internal_date":"2020-01-02T03:04:05Z"`
	bodies := []string{
		`{"raw_message":"` + message + `",` + date + `}`,
		`{"raw_message":"` + message + `","folder":"Archive","flags":["\\Seen"],` + date + `}`,
		`{` + date + `,"raw_message":"` + message + `"}`,
		`{"raw_message":"QQ==","raw_message":"QUI=",` + date + `}`,
		`{"raw_message":"!!","raw_message":"QUI=",` + date + `}`,
		`{"raw_message":"QUI=","raw_message":"!!",` + date + `}`,
		`{"raw_message":1,"raw_message":"QUI=",` + date + `}`,
		`{"raw_message":null,"raw_message":"QUI=",` + date + `}`,
		`{"raw_message":"QUI=","raw_message":null,` + date + `}`,
		`{"raw_message":[65,66],` + date + `}`,
		`{"raw_message":[65,300],` + date + `}`,
		`{"raw_message":[],` + date + `}`,
		`{"raw_message":"",` + date + `}`,
		`{"raw_message":"\n",` + date + `}`,
		`{"raw_message":"Q\u0051\u003d\u003D",` + date + `}`,
		`{"raw_message":"QQ\r\n==",` + date + `}`,
		`{"raw_message":"Q\/==",` + date + `}`,
		`{"raw_message":"\u00e9",` + date + `}`,
		`{"raw_message":"\ud83d\ude00",` + date + `}`,
		"{\"raw_message\":\"\xff\"," + date + `}`,
		"{\"raw_message\":\"Q\x01\"," + date + `}`,
		`{"raw_message":null,` + date + `}`,
		`{"raw_message":true,` + date + `}`,
		`{"raw_message":{},` + date + `}`,
		`{"raw\u005fmessage":"QQ==",` + date + `}`,
		`{"RAW_MESSAGE":"QQ==",` + date + `}`,
		`{"raw_message":"QQ==","RAW_MESSAGE":"QUI=",` + date + `}`,
		`{"raw_message":"QQ==","x":"` + strings.Repeat("y", 100) + `",` + date + `}`,
		`{"raw_message":"QQ==","x":{"deep":[1,2,3]},"y":null,` + date + `}`,
		` {"raw_message":"QQ==",` + date + `}`,
		"\r\n\t{\"raw_message\":\"QQ==\"," + date + "} \n",
		`{ "raw_message" : "QQ==" , ` + date + ` }`,
		`{ }`,
		`{"raw_message":"QQ==","flags.x":1,` + date + `}`,
		`{"raw_message":"QQ==","internal_date.x":1,` + date + `}`,
		`{"raw_message":"QQ==","flags":["a"`,
		`{"raw_message":"QQ==","x":[1`,
		`{"raw_message":"QQ==","x":tru,` + date + `}`,
		`{"raw_message":"QQ==","x":[1,],` + date + `}`,
		`{"raw_message":"QQ==","x":01,` + date + `}`,
		`{"raw_message":"QQ==","x":"\q",` + date + `}`,
		`{"raw_message":"QQ==","x":,` + date + `}`,
		`{"raw_message":"QQ==","\xff":1,` + date + `}`,
		`{"raw_message":"QQ==","folder":1,` + date + `}`,
		`{"raw_message":"QQ==","folder":null,` + date + `}`,
		`{"raw_message":"QQ==","folder":"a","folder":"b",` + date + `}`,
		`{"raw_message":"QQ==","folder":1,"folder":"b",` + date + `}`,
		`{"raw_message":"QQ==","flags":"x",` + date + `}`,
		`{"raw_message":"QQ==","flags":[1],` + date + `}`,
		`{"raw_message":"QQ==","internal_date":"yesterday"}`,
		`{"raw_message":"QQ==","internal_date":1,` + date + `}`,
		`{"raw_message":"QQ=="}`,
		`{` + date + `}`, `{}`, `[]`, `[1,2]`, `null`, ` null `, `1`, `"x"`, `true`, ``, ` `, "\n\t ", "\u00a0", `{`, `{"raw_message"`, `{"raw_message":`, `{"raw_message":"QQ==`, `{"raw_message":"QQ=="`, `{"raw_message":"QQ==",`, `{"raw_message":"QQ=="}x`, `{"raw_message":"QQ==",}`,
		`{"raw_message":"QQ=="} {}`, `nul`, strings.Repeat("[", jsonMaxDepth+1),
		`{"x":` + strings.Repeat("[", jsonMaxDepth) + strings.Repeat("]", jsonMaxDepth) + `,` + date + `}`,
		`{"x":` + strings.Repeat("[", jsonMaxDepth-1) + strings.Repeat("]", jsonMaxDepth-1) + `,` + date + `}`,
	}
	random := rand.New(rand.NewSource(1437))
	alphabet := []byte("{}[],:\"\\/ \n0123-.etrufalsn!=QA\xff\x00")
	seed := []byte(`{"raw_message":"UVE9PQ==","folder":"Archive","flags":["\\Seen","$k"],` + date + `}`)
	for range importRandomCases(20000) {
		data := append([]byte(nil), seed...)
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
	for _, id := range []string{"12", "0"} {
		for _, contentType := range []string{"", "application/json", "application/json; charset=utf-8", "text/plain", "a b/c; ="} {
			for i, body := range bodies {
				if i > 200 && (id != "12" || contentType != "application/json") {
					break // every case with every request line, and the random ones with one
				}
				gotRefusal, gotMessage, gotTooLarge := bindImportBody(t, id, contentType, []byte(body), 4096)

				var whole importMessageRequest
				r := httptest.NewRequest(http.MethodPost, "/", nil)
				if contentType != "" {
					r.Header.Set("Content-Type", contentType)
				}
				route := chi.NewRouteContext()
				route.URLParams.Add("id", id)
				r = r.WithContext(contextWithRoute(r, route))
				wantRefusal := ""
				if err := endpoint.BindRequest(r, &whole, []byte(body)); err != nil {
					wantRefusal = refusalText(err)
				}
				if gotRefusal != wantRefusal {
					t.Fatalf("%q as %q, account %s:\n got %s\nwant %s", body, contentType, id, gotRefusal, wantRefusal)
				}
				if wantRefusal != "" || gotTooLarge {
					continue
				}
				if !bytes.Equal(gotMessage, whole.Body.RawMessage) {
					t.Fatalf("%q: message %q, want %q", body, gotMessage, whole.Body.RawMessage)
				}
			}
		}
	}
}

func spoolDirectory(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	t.Setenv("TMPDIR", directory)
	return directory
}

func assertNoSpool(t *testing.T, directory string) {
	t.Helper()
	left, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}

func importRequestJSON(raw []byte, extra string) []byte {
	return fmt.Appendf(nil, `{"raw_message":"%s","internal_date":"2020-01-02T03:04:05Z"%s}`, base64.StdEncoding.EncodeToString(raw), extra)
}

// Allocated across a request: what it read into memory, whether or not it was still there at the
// end. The route's whole point is that this doesn't grow with the message when it isn't stored.
func allocatedBy(serve func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	serve()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// The limit is half the message, so one copy of it fails, and a refusal costs about 0.2 MiB whatever
// the size, so the message need only be large beside that. The request is read
// a byte at a time, which under the race detector and coverage takes more than a microsecond a
// byte, so a larger message buys this test only time: 32 MiB took seven minutes.
func TestImportRefusalsDoNotHoldTheMessage(t *testing.T) {
	const size = 1 << 20
	spool := spoolDirectory(t)
	routes := importMessageRoutes(NewMessageImportHandler(nil, 2*size, nil, nil))
	routesSmall := importMessageRoutes(NewMessageImportHandler(nil, size/2, nil, nil))
	message := append([]byte("Message-ID: <big@example.test>\r\nSubject: big\r\n\r\n"), bytes.Repeat([]byte("x"), size)...)
	for _, tc := range []struct {
		name   string
		routes http.Handler
		body   []byte
		status int
	}{
		// Within the request cap, so that this is the limit on the decoded message.
		{"past the decoded limit", routesSmall, importRequestJSON(message[:size/2+1], ""), 413},
		{"no credentials", routes, importRequestJSON(message, ""), 401},
		{"a field refused by the handler", routes, importRequestJSON(message, `,"flags":["bad keyword"]`), 422},
		{"a field refused by the binder", routes, importRequestJSON(message, `,"flags":1`), 422},
		{"a field no one declared", routes, importRequestJSON(message, `,"x":1`), 422},
		{"not base64", routes, bytes.Replace(importRequestJSON(message, ""), []byte("="), []byte("!"), 1), 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/12/messages/import", bytes.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			allocated := allocatedBy(func() { tc.routes.ServeHTTP(w, r) })
			t.Logf("refusing %d bytes allocated %d", size, allocated)
			if w.Code != tc.status {
				t.Fatalf("answered %d %s", w.Code, w.Body)
			}
			if tc.status == 413 && !strings.Contains(w.Body.String(), "message_too_large") {
				t.Fatalf("answered %s", w.Body)
			}
			if allocated > size/2 {
				t.Fatalf("allocated %d bytes refusing a %d byte message", allocated, size)
			}
			assertNoSpool(t, spool)
		})
	}
}

// Every way out of the route leaves no file: a refusal at each stage, a client gone mid-request,
// and a panic.
func TestImportLeavesNoFile(t *testing.T) {
	spool := spoolDirectory(t)
	routes := importMessageRoutes(NewMessageImportHandler(nil, 1024, nil, nil))
	good := importRequestJSON([]byte("Subject: x\r\n\r\nbody\r\n"), "")
	for _, tc := range []struct {
		name   string
		path   string
		body   io.Reader
		status int
	}{
		{"not JSON", "12", strings.NewReader(`{`), 400},
		{"empty", "12", strings.NewReader(``), 400},
		{"past the request cap", "12", strings.NewReader(strings.Repeat("x", 80000)), 413},
		{"past the decoded limit", "12", bytes.NewReader(importRequestJSON(bytes.Repeat([]byte("x"), 2000), "")), 413},
		{"bad account", "0", bytes.NewReader(good), 422},
		{"no credentials", "12", bytes.NewReader(good), 401},
		{"raw_message twice", "12", strings.NewReader(`{"raw_message":"QQ==","raw_message":"!"}`), 422},
		{"client gone", "12", io.MultiReader(strings.NewReader(`{"raw_message":"QQ`), iotest.ErrReader(errors.New("connection reset"))), 400},
		{"client gone at once", "12", iotest.ErrReader(errors.New("connection reset")), 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/"+tc.path+"/messages/import", tc.body)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			routes.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("answered %d %s", w.Code, w.Body)
			}
			assertNoSpool(t, spool)
		})
	}
	t.Run("panic", func(t *testing.T) {
		// A nil database panics at the first query, past the point every file exists.
		r := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/12/messages/import", bytes.NewReader(good))
		r = r.WithContext(context.WithValue(r.Context(), middleware.ClaimsKey, &auth.Claims{AccountID: 12}))
		defer func() {
			if recover() == nil {
				t.Fatal("expected the nil database to panic")
			}
			assertNoSpool(t, spool)
		}()
		routes.ServeHTTP(httptest.NewRecorder(), r)
	})
}

// The server's own disk failing is not the client's fault, so it is not a client's refusal.
func TestImportDiskFailureIsNotARefusal(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir()+"/gone")
	h := NewMessageImportHandler(nil, 1024, nil, nil)
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(importRequestJSON([]byte("x"), "")))
	_, err := h.importSpooledMessage(r, struct{}{})
	var refusal *endpoint.Error
	if err == nil || errors.As(err, &refusal) {
		t.Fatalf("got %v", err)
	}
}

func TestImportDeclaredFieldsAreTheType(t *testing.T) {
	want := map[string]bool{"folder": true, "flags": true, "internal_date": true}
	if !reflect.DeepEqual(importDeclaredFields, want) {
		t.Fatalf("declared %v, want %v", importDeclaredFields, want)
	}
}

type panickingImportBroker struct{}

func (panickingImportBroker) Publish(uint, SSEEvent) { panic("broker") }

func importedAccount(t *testing.T) (*gorm.DB, uint) {
	t.Helper()
	db := importProofDB(t)
	fx := seedAccountsFixture(t, db)
	if err := db.Create(&models.QuotaUsage{MailboxID: fx.user.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := grants.EnsureImport(db, &fx.user); err != nil {
		t.Fatal(err)
	}
	return db, fx.user.ID
}

func importedRequest(account uint, message string) *http.Request {
	r := authzRequest(http.MethodPost, fmt.Sprintf("/api/v1/accounts/%d/messages/import", account), account, nil, string(importRequestJSON([]byte(message), "")))
	r.Header.Set("Content-Type", "application/json")
	return r
}

const importedWithAttachment = "Message-ID: <files@example.test>\r\nFrom: old@example.test\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n" +
	"--B\r\nContent-Type: text/plain\r\n\r\nbody\r\n" +
	"--B\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=a.pdf\r\n\r\npdf bytes\r\n--B--\r\n"

// A message that is stored leaves no file, and neither does one that panics after it is stored.
func TestImportStoredLeavesNoFile(t *testing.T) {
	db, account := importedAccount(t)
	spool := spoolDirectory(t)
	h := NewMessageImportHandler(db, 1<<20, nil, nil)
	h.directory = t.TempDir()
	routes := importMessageRoutes(h)
	for _, want := range []string{`"imported":true`, `"imported":false`} {
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, importedRequest(account, importedWithAttachment))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
			t.Fatalf("answered %d %s, want %s", w.Code, w.Body, want)
		}
		assertNoSpool(t, spool)
	}

	h = NewMessageImportHandler(db, 1<<20, nil, panickingImportBroker{})
	h.directory = t.TempDir()
	routes = importMessageRoutes(h)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected the broker to panic")
			}
		}()
		routes.ServeHTTP(httptest.NewRecorder(), importedRequest(account, "Message-ID: <panic@example.test>\r\n\r\nbody\r\n"))
	}()
	assertNoSpool(t, spool)
}

// A part bound for the object store goes through a file of its own first, under the reference
// and checksum Put gives the same bytes, and the file doesn't stay.
func TestImportStoresAttachmentsInTheObjectStore(t *testing.T) {
	db, account := importedAccount(t)
	spool := spoolDirectory(t)
	fake := s3test.New(t)
	store := attachmentstore.NewS3(fake.Config)
	w := httptest.NewRecorder()
	importMessageRoutes(NewMessageImportHandler(db, 1<<20, store, nil)).ServeHTTP(w, importedRequest(account, importedWithAttachment))
	if w.Code != http.StatusOK {
		t.Fatalf("answered %d %s", w.Code, w.Body)
	}
	assertNoSpool(t, spool)
	ref, checksum, err := store.Put(context.Background(), []byte("pdf bytes"))
	if err != nil {
		t.Fatal(err)
	}
	var attachments []models.Attachment
	if err := db.Find(&attachments).Error; err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 || attachments[0].StorageType != "s3" || attachments[0].StorageRef != ref || attachments[0].Checksum != checksum || attachments[0].SizeBytes != 9 {
		t.Fatalf("stored %+v, want reference %s", attachments, ref)
	}
}

// A store that fails is the server's failure, and the message isn't taken without its parts.
func TestImportFailsWhenAnAttachmentCannotBeStored(t *testing.T) {
	db, account := importedAccount(t)
	spool := spoolDirectory(t)
	h := NewMessageImportHandler(db, 1<<20, nil, nil)
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	h.directory = blocked
	w := httptest.NewRecorder()
	importMessageRoutes(h).ServeHTTP(w, importedRequest(account, importedWithAttachment))
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "file") {
		t.Fatalf("answered %d %s", w.Code, w.Body)
	}
	var count int64
	if err := db.Model(&models.Message{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("stored %d messages, %v", count, err)
	}
	assertNoSpool(t, spool)
}
