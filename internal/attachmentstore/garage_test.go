package attachmentstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/restmail/restmail/internal/config"
)

// Run by scripts/garage-attachment-test.py against the actual Garage image.
func TestS3Garage(t *testing.T) {
	endpoint := os.Getenv("RESTMAIL_TEST_GARAGE_ENDPOINT")
	if endpoint == "" {
		if os.Getenv("RESTMAIL_REQUIRE_GARAGE") == "1" {
			t.Fatal("Garage fixture is required")
		}
		t.Skip("run scripts/garage-attachment-test.py for the real Garage integration")
	}
	c := config.AttachmentStorageConfig{Type: "s3", S3Endpoint: endpoint, S3Region: "garage", S3Bucket: "attachments-test", S3AccessKeyID: os.Getenv("RESTMAIL_TEST_GARAGE_ACCESS_KEY"), S3SecretAccessKey: os.Getenv("RESTMAIL_TEST_GARAGE_SECRET_KEY"), S3Prefix: "attachments/", S3ForcePathStyle: true}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s := NewS3(c)
	data := bytes.Repeat([]byte("private binary\x00\xff\r\n"), 100000)
	ref, sum, err := s.Put(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Delete(context.Background(), ref) })
	if ref != c.S3Prefix+sum {
		t.Fatal("wrong content address")
	}
	r, err := s.Open(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("Garage round trip: bytes=%d error=%v", len(got), err)
	}
	if _, err := r.Seek(1048573, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, err = io.ReadAll(r)
	if err != nil || !bytes.Equal(got, data[1048573:]) {
		t.Fatalf("Garage range: bytes=%d error=%v", len(got), err)
	}
	c.S3Bucket = "attachments-denied"
	_, _, err = NewS3(c).Put(ctx, []byte("must not reach another bucket"))
	var response *smithyhttp.ResponseError
	if !errors.As(err, &response) || response.HTTPStatusCode() != http.StatusForbidden {
		t.Fatalf("scoped Garage key did not deny another bucket: %v", err)
	}
	if err := s.Delete(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(ctx, ref); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted object still readable: %v", err)
	}
}
