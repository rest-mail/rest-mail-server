// Package s3test serves a small S3 wire fixture for attachment integration tests.
package s3test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/config"
)

type Server struct {
	Config   config.AttachmentStorageConfig
	Requests atomic.Int64
	Failure  atomic.Int32
	mu       sync.Mutex
	objects  map[string][]byte
}

func New(t *testing.T) *Server {
	t.Helper()
	f := &Server{objects: make(map[string][]byte)}
	s := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(s.Close)
	f.Config = config.AttachmentStorageConfig{
		Type: "s3", S3Endpoint: s.URL, S3Region: "garage", S3Bucket: "mail-attachments",
		S3AccessKeyID: "GK000000000000000000000001", S3SecretAccessKey: strings.Repeat("a", 64),
		S3ForcePathStyle: true, S3Prefix: "attachments/",
	}
	return f
}

func (f *Server) serve(w http.ResponseWriter, r *http.Request) {
	f.Requests.Add(1)
	if status := f.Failure.Load(); status != 0 {
		w.WriteHeader(int(status))
		_, _ = io.WriteString(w, `<Error><Code>AccessDenied</Code></Error>`)
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential="+f.Config.S3AccessKeyID+"/") {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
		f.list(w, r.URL.Query().Get("prefix"))
		return
	}
	ref, ok := strings.CutPrefix(r.URL.Path, "/"+f.Config.S3Bucket+"/")
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		data, err := io.ReadAll(r.Body)
		hash := sha256.Sum256(data)
		// Exercise the real SDK's ordinary SigV4 payload, with no aws-chunked
		// trailer protocol that a compatible server may not implement.
		if err != nil || r.Header.Get("X-Amz-Content-Sha256") != hex.EncodeToString(hash[:]) || strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.objects[ref] = data
		w.Header().Set("ETag", fmt.Sprintf(`"%x"`, hash))
	case http.MethodHead, http.MethodGet:
		data, ok := f.objects[ref]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `<Error><Code>NoSuchKey</Code></Error>`)
			return
		}
		hash := sha256.Sum256(data)
		w.Header().Set("ETag", fmt.Sprintf(`"%x"`, hash))
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, r, ref, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), bytes.NewReader(data))
	case http.MethodDelete:
		delete(f.objects, ref)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// list answers ListObjectsV2 with every object under prefix, each last modified at
// the same fixed time HEAD reports.
func (f *Server) list(w http.ResponseWriter, prefix string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var contents strings.Builder
	for ref, data := range f.objects {
		if strings.HasPrefix(ref, prefix) {
			fmt.Fprintf(&contents, "<Contents><Key>%s</Key><LastModified>2026-01-01T00:00:00Z</LastModified><Size>%d</Size></Contents>", ref, len(data))
		}
	}
	w.Header().Set("Content-Type", "application/xml")
	fmt.Fprintf(w, `<ListBucketResult><IsTruncated>false</IsTruncated>%s</ListBucketResult>`, contents.String())
}
