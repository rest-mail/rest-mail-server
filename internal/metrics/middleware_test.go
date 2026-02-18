package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ── responseWriter ───────────────────────────────────────────────────

func TestNewResponseWriter_DefaultStatusCode(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := newResponseWriter(rec)

	if rw.statusCode != http.StatusOK {
		t.Errorf("expected default status 200, got %d", rw.statusCode)
	}
}

func TestResponseWriter_WriteHeaderSetsCode(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := newResponseWriter(rec)

	rw.WriteHeader(http.StatusNotFound)

	if rw.statusCode != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", rw.statusCode)
	}
}

func TestResponseWriter_SecondWriteHeaderIgnored(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := newResponseWriter(rec)

	rw.WriteHeader(http.StatusCreated)
	rw.WriteHeader(http.StatusInternalServerError)

	// The first call sets written=true, so the second call should not
	// change statusCode.
	if rw.statusCode != http.StatusCreated {
		t.Errorf("expected status 201 (first call wins), got %d", rw.statusCode)
	}
}

func TestResponseWriter_WriteMarksWritten(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := newResponseWriter(rec)

	if rw.written {
		t.Fatal("expected written=false before any write")
	}

	_, err := rw.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !rw.written {
		t.Error("expected written=true after Write")
	}
}

func TestResponseWriter_WritePassesDataThrough(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := newResponseWriter(rec)

	n, err := rw.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 5 {
		t.Errorf("expected 5 bytes written, got %d", n)
	}
	if rec.Body.String() != "hello" {
		t.Errorf("expected body 'hello', got %q", rec.Body.String())
	}
}

func TestResponseWriter_Unwrap(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := newResponseWriter(rec)

	unwrapped := rw.Unwrap()
	if unwrapped != rec {
		t.Error("Unwrap should return the underlying ResponseWriter")
	}
}

func TestResponseWriter_WriteHeaderThenWriteKeepsFirstCode(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := newResponseWriter(rec)

	rw.WriteHeader(http.StatusAccepted)
	_, _ = rw.Write([]byte("data"))

	if rw.statusCode != http.StatusAccepted {
		t.Errorf("expected status 202, got %d", rw.statusCode)
	}
}

func TestResponseWriter_WriteThenWriteHeaderKeepsWrittenTrue(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := newResponseWriter(rec)

	_, _ = rw.Write([]byte("data"))
	// Write sets written=true, so WriteHeader should not update statusCode.
	rw.WriteHeader(http.StatusBadRequest)

	if rw.statusCode != http.StatusOK {
		t.Errorf("expected default status 200 (Write came first), got %d", rw.statusCode)
	}
}

// ── normalisePath ────────────────────────────────────────────────────

func TestNormalisePath_ShortPathReturnedAsIs(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/api/v1/messages", "/api/v1/messages"},
		{"/health", "/health"},
		{"/api/v1/auth/login", "/api/v1/auth/login"},
		{"/", "/"},
	}

	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodGet, tt.path, nil)
		got := normalisePath(r)
		if got != tt.want {
			t.Errorf("normalisePath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestNormalisePath_LongPathTruncated(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/api/v1/mailboxes/42/messages", "/api/v1/mailboxes/42/..."},
		{"/a/b/c/d/e/f/g", "/a/b/c/d/..."},
		{"/one/two/three/four/five", "/one/two/three/four/..."},
	}

	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodGet, tt.path, nil)
		got := normalisePath(r)
		if got != tt.want {
			t.Errorf("normalisePath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestNormalisePath_ExactlyFourSegments(t *testing.T) {
	// 4 segments should NOT be truncated (only 5+ triggers truncation)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil)
	got := normalisePath(r)
	if got != "/api/v1/auth/login" {
		t.Errorf("normalisePath with 4 segments = %q, want /api/v1/auth/login", got)
	}
}
