package messagestore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/attachmentstore"
	"github.com/rest-mail/rest-mail-server/internal/attachmentstore/s3test"
	"github.com/rest-mail/rest-mail-server/internal/config"
)

// adapters runs a test against both stores, which have to behave alike.
func adapters(t *testing.T, test func(t *testing.T, store Store)) {
	t.Helper()
	t.Run("filesystem", func(t *testing.T) { test(t, NewFilesystem(t.TempDir())) })
	t.Run("s3", func(t *testing.T) {
		fake := s3test.New(t)
		fake.Config.S3Prefix = "messages/"
		test(t, newS3(attachmentstore.NewS3(fake.Config), t.TempDir()))
	})
}

func TestRoundTrip(t *testing.T) {
	adapters(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		data := []byte("From: a@example.org\r\n\r\nbinary \x00\xff bytes survive\r\n")
		object, err := store.Put(ctx, bytes.NewReader(data), 1024)
		if err != nil {
			t.Fatal(err)
		}
		if object.Size != int64(len(data)) || len(object.Checksum) != 64 {
			t.Fatalf("object %+v", object)
		}
		again, err := store.Put(ctx, bytes.NewReader(data), 1024)
		if err != nil || again != object {
			t.Fatalf("the same bytes are one object: %+v, %v", again, err)
		}
		reader, err := store.Open(ctx, object.Ref)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = reader.Close() }()
		if reader.Size() != object.Size {
			t.Fatalf("size %d", reader.Size())
		}
		got, err := io.ReadAll(reader)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("read %q, %v", got, err)
		}
		// What needs a part reads only that part.
		if _, err := reader.Seek(6, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		part := make([]byte, 13)
		if _, err := io.ReadFull(reader, part); err != nil || string(part) != "a@example.org" {
			t.Fatalf("part %q, %v", part, err)
		}
		if err := store.Delete(ctx, object.Ref); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Open(ctx, object.Ref); !errors.Is(err, ErrNotFound) {
			t.Fatalf("after delete: %v", err)
		}
		if err := store.Delete(ctx, object.Ref); err != nil {
			t.Fatalf("deleting what is gone: %v", err)
		}
	})
}

func TestEmptyAndExactlyTheLimit(t *testing.T) {
	adapters(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		if object, err := store.Put(ctx, strings.NewReader(""), 8); err != nil || object.Size != 0 {
			t.Fatalf("empty: %+v, %v", object, err)
		}
		if object, err := store.Put(ctx, strings.NewReader("12345678"), 8); err != nil || object.Size != 8 {
			t.Fatalf("at the limit: %+v, %v", object, err)
		}
	})
}

func TestOverTheLimitKeepsNothing(t *testing.T) {
	t.Run("filesystem", func(t *testing.T) {
		directory := t.TempDir()
		_, err := NewFilesystem(directory).Put(context.Background(), strings.NewReader("123456789"), 8)
		if !errors.Is(err, ErrTooLarge) {
			t.Fatalf("got %v", err)
		}
		left, err := os.ReadDir(directory)
		if err != nil || len(left) != 0 {
			t.Fatalf("left behind %v, %v", left, err)
		}
	})
	t.Run("s3", func(t *testing.T) {
		fake := s3test.New(t)
		fake.Config.S3Prefix = "messages/"
		spool := t.TempDir()
		_, err := newS3(attachmentstore.NewS3(fake.Config), spool).Put(context.Background(), strings.NewReader("123456789"), 8)
		if !errors.Is(err, ErrTooLarge) {
			t.Fatalf("got %v", err)
		}
		left, err := os.ReadDir(spool)
		if err != nil || len(left) != 0 {
			t.Fatalf("left behind %v, %v", left, err)
		}
	})
}

func TestCancelledPutKeepsNothing(t *testing.T) {
	directory := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewFilesystem(directory).Put(ctx, strings.NewReader("message"), 64); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if left, _ := os.ReadDir(directory); len(left) != 0 {
		t.Fatalf("left behind %v", left)
	}
}

func TestReferencesCannotEscape(t *testing.T) {
	directory := t.TempDir()
	secret := filepath.Join(filepath.Dir(directory), "secret")
	if err := os.WriteFile(secret, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(secret) })
	store := NewFilesystem(directory)
	for _, ref := range []string{"../secret", "/etc/passwd", "", "ab/cd", strings.Repeat("a", 64), "ab/" + strings.Repeat("a", 64)} {
		if _, err := store.Open(context.Background(), ref); !errors.Is(err, ErrInvalidRef) {
			t.Errorf("Open(%q): %v", ref, err)
		}
		if err := store.Delete(context.Background(), ref); !errors.Is(err, ErrInvalidRef) {
			t.Errorf("Delete(%q): %v", ref, err)
		}
	}
}

// endless is a source of any length that holds nothing itself.
type endless struct{ left int64 }

func (e *endless) Read(p []byte) (int, error) {
	if e.left == 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > e.left {
		p = p[:e.left]
	}
	clear(p)
	e.left -= int64(len(p))
	return len(p), nil
}

// allocated is the bytes run allocates.
func allocated(run func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	run()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// The point of the store: a big message costs disk, and memory stays flat.
func TestABigMessageDoesNotGoThroughMemory(t *testing.T) {
	const size, budget = 64 << 20, 8 << 20
	store := NewFilesystem(t.TempDir())
	ctx := context.Background()
	var object Object
	if used := allocated(func() {
		var err error
		if object, err = store.Put(ctx, &endless{left: size}, size); err != nil {
			t.Fatal(err)
		}
	}); used > budget {
		t.Fatalf("writing %d bytes allocated %d", int64(size), used)
	}
	if object.Size != size {
		t.Fatalf("size %d", object.Size)
	}
	if used := allocated(func() {
		reader, err := store.Open(ctx, object.Ref)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = reader.Close() }()
		if n, err := io.Copy(io.Discard, reader); err != nil || n != size {
			t.Fatalf("read %d, %v", n, err)
		}
	}); used > budget {
		t.Fatalf("reading %d bytes allocated %d", int64(size), used)
	}
	// The limit is held as the bytes pass, not after reading them all.
	if used := allocated(func() {
		if _, err := store.Put(ctx, &endless{left: 1 << 40}, 1<<20); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("got %v", err)
		}
	}); used > budget {
		t.Fatalf("refusing allocated %d", used)
	}
}

func TestNewFollowsTheAttachmentsStorageType(t *testing.T) {
	messages := config.MessageStorageConfig{Directory: t.TempDir(), S3Prefix: "messages/"}
	store, err := New(messages, config.AttachmentStorageConfig{Type: "filesystem"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.(*Filesystem); !ok {
		t.Fatalf("got %T", store)
	}
	fake := s3test.New(t)
	store, err = New(messages, fake.Config)
	if err != nil {
		t.Fatal(err)
	}
	object, err := store.Put(context.Background(), strings.NewReader("message"), 64)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(object.Ref, "messages/") {
		t.Fatalf("a message is among the attachments: %q", object.Ref)
	}
	if _, err := New(messages, config.AttachmentStorageConfig{Type: "tape"}); err == nil {
		t.Fatal("an unknown type was taken")
	}
}
