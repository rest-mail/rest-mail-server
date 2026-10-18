package attachmentstore

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/attachmentstore/s3test"
)

func TestS3PrivateObjectRoundTripAndRanges(t *testing.T) {
	f := s3test.New(t)
	s := NewS3(f.Config)
	data := []byte("attachment\x00with binary bytes\xff")
	ref, checksum, err := s.Put(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if ref != "attachments/"+checksum || len(checksum) != 64 {
		t.Fatalf("bad content address: %q", ref)
	}
	file, err := s.Open(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	got, err := io.ReadAll(file)
	if err != nil || string(got) != string(data) {
		t.Fatalf("round trip: %q %v", got, err)
	}
	if _, err := file.Seek(2, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, err = io.ReadAll(file)
	if err != nil || string(got) != string(data[2:]) {
		t.Fatalf("seek: %q %v", got, err)
	}
	for _, tc := range []struct {
		rangeValue string
		status     int
		expected   string
	}{
		{"bytes=2-5", http.StatusPartialContent, string(data[2:6])},
		{"bytes=-3", http.StatusPartialContent, string(data[len(data)-3:])},
		{"bytes=999-", http.StatusRequestedRangeNotSatisfiable, ""},
	} {
		t.Run(tc.rangeValue, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/attachment", nil)
			r.Header.Set("Range", tc.rangeValue)
			w.Header().Set("Content-Type", "application/octet-stream")
			http.ServeContent(w, r, "attachment", file.Modified, file)
			if w.Code != tc.status || (tc.expected != "" && w.Body.String() != tc.expected) {
				t.Fatalf("range: %d %q", w.Code, w.Body.String())
			}
		})
	}
}

func TestS3MissingObjectAndReferenceConfinement(t *testing.T) {
	f := s3test.New(t)
	s := NewS3(f.Config)
	if _, err := s.Open(context.Background(), "attachments/"+strings.Repeat("0", 64)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing object: %v", err)
	}
	before := f.Requests.Load()
	for _, ref := range []string{"/etc/passwd", "../other-key", "other/" + strings.Repeat("0", 64), "attachments/../secret", "https://other/bucket"} {
		if _, err := s.Open(context.Background(), ref); err == nil {
			t.Fatalf("accepted reference %q", ref)
		}
	}
	if f.Requests.Load() != before {
		t.Fatal("invalid references reached object storage")
	}
}

func TestS3CanceledUploadAndEmptyObject(t *testing.T) {
	f := s3test.New(t)
	s := NewS3(f.Config)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := s.Put(ctx, []byte("cancelled")); err == nil {
		t.Fatal("canceled upload succeeded")
	}
	ref, _, err := s.Put(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Open(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if data, err := io.ReadAll(r); err != nil || len(data) != 0 {
		t.Fatalf("empty object: %q %v", data, err)
	}
}
