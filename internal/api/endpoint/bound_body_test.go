package endpoint

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// boundUpload is a route's request whose body is one long string, as a Sieve script, a
// certificate or a message's text is.
type boundUpload struct {
	Body struct {
		Script string `json:"script"`
	} `body:""`
}

// generatedBody is n bytes of b, made as they are read, so a large body costs the test no
// memory. read counts what the binder read of it.
type generatedBody struct {
	b    byte
	n    int64
	read int64
}

func (g *generatedBody) Read(p []byte) (int, error) {
	if g.n <= 0 {
		return 0, io.EOF
	}
	p = p[:min(int64(len(p)), g.n)]
	for i := range p {
		p[i] = g.b
	}
	g.n -= int64(len(p))
	g.read += int64(len(p))
	return len(p), nil
}

// uploadBody is a JSON body whose script is size bytes, made as it is read.
func uploadBody(size int64) (io.Reader, *generatedBody) {
	script := &generatedBody{b: 'a', n: size}
	return io.MultiReader(strings.NewReader(`{"script":"`), script, strings.NewReader(`"}`)), script
}

// bindUpload binds r as a route with a body cap of limit, and answers what it bound, the
// bytes allocated while it ran, and the error.
func bindUpload(t *testing.T, r *http.Request, limit int64) (boundUpload, uint64, error) {
	t.Helper()
	if limit > 0 {
		r.Body = http.MaxBytesReader(httptest.NewRecorder(), r.Body, limit)
	}
	var in boundUpload
	shape := requestShape(reflect.TypeOf(in))
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err := bind(r, &in, shape)
	runtime.ReadMemStats(&after)
	return in, after.TotalAlloc - before.TotalAlloc, err
}

// spoolDir makes the temporary directory one the test can see is left empty.
func spoolDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	return dir
}

func assertNoSpoolLeft(t *testing.T, dir string) {
	t.Helper()
	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) > 0 {
		t.Errorf("%d files left in the spool directory", len(left))
	}
}

func refusalStatus(err error) int {
	var refusal *Error
	if errors.As(err, &refusal) {
		return refusal.Status
	}
	return 0
}

// A body sent in chunks past the cap is refused as it arrives, holding the first part of it and
// no more, however far past the cap it goes.
func TestBindRefusesABodyPastTheCapAsItArrives(t *testing.T) {
	dir := spoolDir(t)
	const limit = 8 << 20
	for _, size := range []int64{16 << 20, 256 << 20} {
		body, script := uploadBody(size)
		r := httptest.NewRequest(http.MethodPut, "/", body)
		r.Header.Set("Content-Type", "application/json")
		_, allocated, err := bindUpload(t, r, limit)
		if refusalStatus(err) != http.StatusRequestEntityTooLarge {
			t.Fatalf("%d bytes: got %v, want the 413", size, err)
		}
		t.Logf("%d MiB over an 8 MiB cap: allocated %d KiB, read %d MiB", size>>20, allocated>>10, script.read>>20)
		if allocated > 1<<20 {
			t.Errorf("%d bytes: allocated %d bytes refusing it, want under 1 MiB", size, allocated)
		}
		if script.read > limit+(1<<20) {
			t.Errorf("%d bytes: read %d bytes of it, want reading to stop at the cap", size, script.read)
		}
	}
	assertNoSpoolLeft(t, dir)
}

// A body in a media type the route doesn't take is refused holding none of it. Its length
// known, the rest isn't read. Sent in chunks, the rest is read and dropped, so one past the cap
// is still the 413 that reading it whole gave.
func TestBindRefusesAnUnacceptedMediaTypeWithoutHoldingTheBody(t *testing.T) {
	dir := spoolDir(t)
	const size = 64 << 20

	body, script := uploadBody(size)
	r := httptest.NewRequest(http.MethodPut, "/", body)
	r.Header.Set("Content-Type", "text/plain")
	r.ContentLength = size + int64(len(`{"script":""}`))
	_, allocated, err := bindUpload(t, r, 0)
	var refusal *Error
	if !errors.As(err, &refusal) || refusal.Status != http.StatusBadRequest || !strings.HasPrefix(refusal.Message, "Unsupported media type: text/plain") {
		t.Fatalf("got %v, want the unsupported media type", err)
	}
	t.Logf("64 MiB with its length, wrong type: allocated %d KiB, read %d KiB", allocated>>10, script.read>>10)
	if allocated > 1<<20 || script.read > boundBodyInMemory {
		t.Errorf("allocated %d and read %d bytes, want under 1 MiB and no more than the first part", allocated, script.read)
	}

	body, script = uploadBody(size)
	r = httptest.NewRequest(http.MethodPut, "/", body)
	r.Header.Set("Content-Type", "text/plain")
	_, allocated, err = bindUpload(t, r, 0)
	if !errors.As(err, &refusal) || refusal.Status != http.StatusBadRequest {
		t.Fatalf("in chunks: got %v, want the unsupported media type", err)
	}
	t.Logf("64 MiB in chunks, wrong type: allocated %d KiB, read %d MiB", allocated>>10, script.read>>20)
	if allocated > 1<<20 {
		t.Errorf("in chunks: allocated %d bytes, want under 1 MiB", allocated)
	}

	body, _ = uploadBody(size)
	r = httptest.NewRequest(http.MethodPut, "/", body)
	r.Header.Set("Content-Type", "text/plain")
	if _, _, err := bindUpload(t, r, 8<<20); refusalStatus(err) != http.StatusRequestEntityTooLarge {
		t.Fatalf("in chunks past the cap: got %v, want the 413", err)
	}
	assertNoSpoolLeft(t, dir)
}

// A large body the binder takes is read once into a buffer of its size, and bound as it was
// sent. Its file is gone when binding ends.
func TestBindReadsALargeBodyOnce(t *testing.T) {
	dir := spoolDir(t)
	for _, size := range []int64{1 << 20, 16 << 20} {
		body, _ := uploadBody(size)
		r := httptest.NewRequest(http.MethodPut, "/", body)
		r.Header.Set("Content-Type", "application/json")
		in, allocated, err := bindUpload(t, r, 32<<20)
		if err != nil || int64(len(in.Body.Script)) != size || strings.Trim(in.Body.Script, "a") != "" {
			t.Fatalf("%d bytes: bound %d bytes, %v", size, len(in.Body.Script), err)
		}
		t.Logf("%d MiB bound: allocated %d KiB, %.2f times its size", size>>20, allocated>>10, float64(allocated)/float64(size))
		// Reading it whole cost 5.2 times its size: a buffer grown to it, and then the
		// decoder's own. Read once, it is the decoder's and the body once.
		if limit := uint64(size)*9/2 + 1<<20; allocated > limit {
			t.Errorf("%d bytes: allocated %d, want at most %d", size, allocated, limit)
		}
		again, err := io.ReadAll(r.Body)
		if err != nil || int64(len(again)) != size+int64(len(`{"script":""}`)) {
			t.Errorf("%d bytes: the handler reads %d bytes of the body again, %v", size, len(again), err)
		}
	}
	assertNoSpoolLeft(t, dir)
}

// Either side of the first part, a body binds as reading it whole did. Only one past it needs
// the disk, so with no disk a small body still binds and a large one fails as the server's.
func TestBindAnswersTheSameEitherSideOfTheFirstPart(t *testing.T) {
	for _, size := range []int64{0, 1, boundBodyInMemory - 13, boundBodyInMemory - 12, boundBodyInMemory - 11, boundBodyInMemory, 2 * boundBodyInMemory} {
		body := `{"script":"` + strings.Repeat("b", int(size)) + `"}`
		r := httptest.NewRequest(http.MethodPut, "/", io.MultiReader(strings.NewReader(body)))
		r.Header.Set("Content-Type", "application/json")
		in, _, err := bindUpload(t, r, 0)
		if err != nil || in.Body.Script != strings.Repeat("b", int(size)) {
			t.Fatalf("%d bytes: bound %d bytes, %v", size, len(in.Body.Script), err)
		}
	}

	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "gone"))
	small := httptest.NewRequest(http.MethodPut, "/", io.MultiReader(strings.NewReader(`{"script":"x"}`)))
	if in, _, err := bindUpload(t, small, 0); err != nil || in.Body.Script != "x" {
		t.Fatalf("a small body with no disk: %v", err)
	}
	large, _ := uploadBody(2 * boundBodyInMemory)
	_, _, err := bindUpload(t, httptest.NewRequest(http.MethodPut, "/", large), 0)
	var refusal *Error
	if err == nil || errors.As(err, &refusal) {
		t.Fatalf("a large body with no disk: got %v, want the server's own error", err)
	}

	// An empty body is no body in any media type: never the unsupported media type.
	empty := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(nil))
	empty.Header.Set("Content-Type", "text/plain")
	if _, _, err := bindUpload(t, empty, 0); err == nil || errors.As(err, &refusal) && strings.HasPrefix(refusal.Message, "Unsupported") {
		t.Fatalf("an empty body: got %v, want what a body-less request gets", err)
	}
}
