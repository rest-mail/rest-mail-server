package filters

import (
	"context"
	"encoding/base64"
	"io"
	"os"
	"testing"

	"github.com/restmail/restmail/internal/attachmentstore"
	"github.com/restmail/restmail/internal/attachmentstore/s3test"
	"github.com/restmail/restmail/internal/pipeline"
)

func TestExtractAttachmentsS3UsesDeploymentStorage(t *testing.T) {
	fake := s3test.New(t)
	store := attachmentstore.NewS3(fake.Config)
	dir := t.TempDir()
	f, err := NewExtractAttachmentsWithS3(store)([]byte(`{"storage_type":"filesystem","storage_path":"` + dir + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	email := &pipeline.EmailJSON{Attachments: []pipeline.Attachment{{Filename: "../../sender.html", Content: base64.StdEncoding.EncodeToString([]byte("payload")), Size: 7}}, Inline: []pipeline.Attachment{{ContentID: "inline", Content: base64.StdEncoding.EncodeToString([]byte("image")), Size: 5}}}
	result, err := f.Execute(context.Background(), email)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range append(result.Message.Attachments, result.Message.Inline...) {
		if a.Storage != "s3" || a.Content != "" || a.Ref == "" {
			t.Fatalf("not stored as S3 metadata: %+v", a)
		}
		r, err := store.Open(context.Background(), a.Ref)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil || int64(len(data)) != a.Size {
			t.Fatalf("wrong stored bytes: %d %v", len(data), err)
		}
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatal("S3 filter wrote to a local attachment directory")
	}
	fake.Failure.Store(403)
	if _, err := f.Execute(context.Background(), email); err == nil {
		t.Fatal("failed storage silently continued delivery")
	}
}

func TestExtractAttachmentsS3RequiresServerConfiguration(t *testing.T) {
	if _, err := NewExtractAttachments([]byte(`{"storage_type":"s3"}`)); err == nil {
		t.Fatal("S3 label accepted without an object store")
	}
}
