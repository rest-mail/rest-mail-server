package filters

// #2107 AC02: the javascript filter's request holds the message once, and is
// refused before upload when the sidecar would refuse it with 413.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// oldExecuteRequest is how the filter built its request before: the message
// marshalled, then marshalled again inside SidecarExecute.
func oldExecuteRequest(t testing.TB, script string, email *pipeline.EmailJSON, timeoutMS int) []byte {
	t.Helper()
	message, err := json.Marshal(email)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(SidecarExecute{Script: script, Email: message, TimeoutMS: timeoutMS})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func requestTestFilter(t testing.TB, script string, sidecar JSSidecar) *jsFilter {
	t.Helper()
	f, err := NewJavaScriptWithSidecar(sidecar)([]byte(`{"script":` + string(mustJSON(t, script)) + `,"timeout_ms":750}`))
	if err != nil {
		t.Fatal(err)
	}
	return f.(*jsFilter)
}

func mustJSON(t testing.TB, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func requestTestEmails() map[string]*pipeline.EmailJSON {
	attachment := pipeline.Attachment{Filename: "résumé ☃.pdf", ContentType: "application/pdf", Size: 9, Disposition: "attachment", Content: "SGVsbG8gd29ybGQ="}
	return map[string]*pipeline.EmailJSON{
		"empty": {},
		"ascii": {
			Envelope: pipeline.Envelope{MailFrom: "ana@example.com", RcptTo: []string{"dana@restmail.test"}},
			Headers: pipeline.Headers{
				From: pipeline.Addresses(pipeline.Address{Name: "Ana", Address: "ana@example.com"}), Subject: "Report",
				MessageID: "<r1@example.com>",
			},
			Body: pipeline.Body{ContentType: "text/plain", Content: "The report is attached."},
		},
		"non-ascii": {
			Headers: pipeline.Headers{Subject: "café 日本語 \U0001F600   ", From: pipeline.Addresses(pipeline.Address{Name: "Zoë", Address: "zoe@example.com"}), To: pipeline.ParsedAddresses("=?UTF-8?Q?Jos=C3=A9?= <jose@example.com>, \"A, B\" <ab@example.com>", &mime.WordDecoder{})},
			Body:    pipeline.Body{ContentType: "text/plain; charset=utf-8", Content: "naïve über \u0000 \x01 tab\t quote\" back\\slash"},
		},
		"html": {
			Body: pipeline.Body{ContentType: "text/html", Content: `<p class="x">a & b &lt; c</p><script>alert('</script>')</script>`},
		},
		"attachments": {
			Body:        pipeline.Body{ContentType: "multipart/mixed", Parts: []pipeline.Body{{ContentType: "text/plain", Content: "hi"}, {ContentType: "text/html", Content: "<b>hi</b>"}}},
			Attachments: []pipeline.Attachment{attachment},
			Inline:      []pipeline.Attachment{{ContentType: "image/png", ContentID: "<cid@x>", Disposition: "inline", Content: "iVBORw0KGgo="}},
			Metadata:    map[string]string{"k": "v & <w>", "z": "é"},
		},
	}
}

func TestExecuteRequest_ByteIdenticalToTwoStepMarshal(t *testing.T) {
	scripts := []string{
		"function filter(e){return {type:'action',action:'continue'}}",
		"function filter(e){ if (e.body.content.includes('<b>') && a & b) { return \"é \" } }\n// \U0001F600 \\ \"q\"",
	}
	for name, email := range requestTestEmails() {
		for i, script := range scripts {
			f := requestTestFilter(t, script, JSSidecar{})
			got, err := f.request(email)
			if err != nil {
				t.Fatal(err)
			}
			want := oldExecuteRequest(t, script, email, 750)
			if !bytes.Equal(got.bytes(), want) {
				t.Errorf("%s/script%d: request differs from the two-step marshal\n got: %.300s\nwant: %.300s", name, i, got.bytes(), want)
			}
			if got.size() != int64(len(want)) {
				t.Errorf("%s/script%d: size %d, want %d", name, i, got.size(), len(want))
			}
			// What the sidecar reads must be the same document.
			raw, _ := io.ReadAll(got.reader())
			if !bytes.Equal(raw, want) {
				t.Errorf("%s/script%d: the reader does not yield the body", name, i)
			}
		}
	}
	// A zero timeout is left out, as SidecarExecute's omitempty leaves it out.
	body, _ := executeRequest("s", []byte(`{}`), 0)
	if want, _ := json.Marshal(SidecarExecute{Script: "s", Email: json.RawMessage(`{}`)}); !bytes.Equal(body.bytes(), want) {
		t.Errorf("zero timeout: got %s want %s", body.bytes(), want)
	}
}

// bigEmail is an email whose JSON is about size bytes, with characters the encoder
// has to escape.
func bigEmail(size int) *pipeline.EmailJSON {
	chunk := "line of <b>text</b> & café \"quoted\"\n"
	return &pipeline.EmailJSON{Body: pipeline.Body{ContentType: "text/html", Content: strings.Repeat(chunk, size/len(chunk))}}
}

// leastAlloc is the least fn allocates over several runs. encoding/json keeps its
// encoding buffers in a sync.Pool that a collection empties, so one run may have to
// grow a buffer of the message's size several times and the next may find one; the
// least is the cost of the copies the code makes itself.
func leastAlloc(fn func()) uint64 {
	runtime.GC()
	least := ^uint64(0)
	for i := 0; i < 8; i++ {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		fn()
		runtime.ReadMemStats(&after)
		if n := after.TotalAlloc - before.TotalAlloc; n < least {
			least = n
		}
	}
	return least
}

func TestExecuteRequest_HoldsTheMessageOnce(t *testing.T) {
	email := bigEmail(4608 << 10) // about 8 MiB of JSON once escaped
	f := requestTestFilter(t, "function filter(e){}", JSSidecar{})

	var body sidecarRequestBody
	request := leastAlloc(func() {
		var err error
		if body, err = f.request(email); err != nil {
			t.Fatal(err)
		}
	})
	var old []byte
	twoStep := leastAlloc(func() { old = oldExecuteRequest(t, "function filter(e){}", email, 750) })
	if !bytes.Equal(body.bytes(), old) {
		t.Fatal("the requests differ")
	}
	size := float64(body.size())
	t.Logf("request %d bytes: built with %.2fx its size allocated; the two-step marshal allocates %.2fx", body.size(), float64(request)/size, float64(twoStep)/size)
	if ratio := float64(request) / size; ratio > 1.5 {
		t.Errorf("building the request allocated %.2fx its size, want at most 1.5x: the message is copied again", ratio)
	}
	if ratio := float64(twoStep) / size; ratio < 1.9 {
		t.Errorf("the reference two-step marshal allocated only %.2fx its size: the test no longer tells one copy from two", ratio)
	}
}

func TestJSFilter_RequestOverTheLimitIsRefusedBeforeUpload(t *testing.T) {
	var requests, bytesSeen atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		n, _ := io.Copy(io.Discard, r.Body)
		bytesSeen.Add(n)
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = w.Write([]byte(`{"error":"request body too large (limit 4096 bytes)"}`))
	}))
	defer srv.Close()

	// What the sidecar's own 413 comes to for the filter: a filter that does not
	// know the limit uploads, and the stub answers as the sidecar does.
	unaware := requestTestFilter(t, "function filter(e){}", JSSidecar{URL: srv.URL, Token: testSidecarToken, MaxBodyBytes: 1 << 30})
	viaSidecar, err := unaware.Execute(context.Background(), bigEmail(8192))
	if err != nil || requests.Load() != 1 {
		t.Fatalf("setup: the sidecar's 413 was not reached (err %v, requests %d)", err, requests.Load())
	}

	requests.Store(0)
	bytesSeen.Store(0)
	// A request over the limit: nothing reaches the server, and the filter's
	// outcome is the one a 413 gives.
	aware := requestTestFilter(t, "function filter(e){}", JSSidecar{URL: srv.URL, Token: testSidecarToken, MaxBodyBytes: 4096})
	res, err := aware.Execute(context.Background(), bigEmail(8192))
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 || bytesSeen.Load() != 0 {
		t.Fatalf("an over-limit request reached the sidecar: %d requests, %d bytes", requests.Load(), bytesSeen.Load())
	}
	if res.Action != viaSidecar.Action || res.Log != viaSidecar.Log {
		t.Errorf("refusal differs from the sidecar's 413 outcome:\n got %+v\nwant %+v", res, viaSidecar)
	}
}

func TestJSFilter_RequestWithinTheLimitIsSent(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		if r.ContentLength != int64(len(got)) {
			t.Errorf("Content-Length %d, body %d bytes", r.ContentLength, len(got))
		}
		_, _ = w.Write([]byte(`{"result":{"type":"action","action":"continue","log":{"detail":"ok"}},"logs":[]}`))
	}))
	defer srv.Close()
	email := requestTestEmails()["non-ascii"]
	f := requestTestFilter(t, "function filter(e){}", JSSidecar{URL: srv.URL, Token: testSidecarToken, MaxBodyBytes: 1 << 20})
	res, err := f.Execute(context.Background(), email)
	if err != nil || res.Action != pipeline.ActionContinue {
		t.Fatalf("res %+v err %v", res, err)
	}
	if want := oldExecuteRequest(t, "function filter(e){}", email, 750); !bytes.Equal(got, want) {
		t.Errorf("the sidecar received\n%s\nwant\n%s", got, want)
	}
}

func TestJSSidecar_DefaultLimitIs32MiB(t *testing.T) {
	if got := (JSSidecar{}).maxBodyBytes(); got != 32<<20 {
		t.Errorf("default limit %d, want %d", got, 32<<20)
	}
	if got := (JSSidecar{MaxBodyBytes: 7}).maxBodyBytes(); got != 7 {
		t.Errorf("limit %d, want 7", got)
	}
}
