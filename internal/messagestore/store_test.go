package messagestore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/attachmentstore"
	"github.com/rest-mail/rest-mail-server/internal/attachmentstore/s3test"
	"github.com/rest-mail/rest-mail-server/internal/config"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	fake := s3test.New(t)
	fake.Config.S3Prefix = "messages/"
	return &Store{bucket: attachmentstore.NewS3(fake.Config), spoolDir: t.TempDir()}
}

func TestRoundTrip(t *testing.T) {
	{
		store := newStore(t)
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
		if err != nil || again.Ref == object.Ref || again.Checksum != object.Checksum {
			t.Fatalf("the same bytes twice are two objects: %+v, %+v, %v", object, again, err)
		}
		// Removing one message never touches another's.
		if err := store.Delete(ctx, again.Ref); err != nil {
			t.Fatal(err)
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
	}
}

func TestNamesAreInOrderAndUnguessable(t *testing.T) {
	earlier, later := newName(time.UnixMilli(1000)), newName(time.UnixMilli(2000))
	if !objectName.MatchString(earlier) || !objectName.MatchString(later) {
		t.Fatalf("%q, %q", earlier, later)
	}
	if earlier >= later {
		t.Fatalf("%q sorts after %q", earlier, later)
	}
	if same := newName(time.UnixMilli(1000)); same == earlier {
		t.Fatalf("two objects at one moment share %q", same)
	}
	if !strings.HasPrefix(newName(time.Date(2026, 11, 2, 23, 0, 0, 0, time.FixedZone("", -3600))), "2026/11/03/") {
		t.Fatal("the day is not UTC's")
	}
}

func TestEmptyAndExactlyTheLimit(t *testing.T) {
	{
		store := newStore(t)
		ctx := context.Background()
		if object, err := store.Put(ctx, strings.NewReader(""), 8); err != nil || object.Size != 0 {
			t.Fatalf("empty: %+v, %v", object, err)
		}
		if object, err := store.Put(ctx, strings.NewReader("12345678"), 8); err != nil || object.Size != 8 {
			t.Fatalf("at the limit: %+v, %v", object, err)
		}
	}
}

func TestOverTheLimitKeepsNothing(t *testing.T) {
	store := newStore(t)
	if _, err := store.Put(context.Background(), strings.NewReader("123456789"), 8); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v", err)
	}
	if left, err := os.ReadDir(store.spoolDir); err != nil || len(left) != 0 {
		t.Fatalf("left behind %v, %v", left, err)
	}
}

func TestCancelledPutKeepsNothing(t *testing.T) {
	store := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Put(ctx, strings.NewReader("message"), 64); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if left, _ := os.ReadDir(store.spoolDir); len(left) != 0 {
		t.Fatalf("left behind %v", left)
	}
}

func TestReferencesCannotEscape(t *testing.T) {
	store := newStore(t)
	for _, ref := range []string{"../secret", "/etc/passwd", "", "ab/cd", "messages/../attachments/x", "attachments/2026/11/02/00000000000a-" + strings.Repeat("A", 26), "messages/2026/11/02/../../../secret"} {
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

// The point of the store: a big message costs disk, and memory stays flat. The fake bucket holds
// what it is given, so this measures the part that is the store's own: reaching the temporary
// file, which is what a signed upload is sent from.
func TestABigMessageDoesNotGoThroughMemory(t *testing.T) {
	const size, budget = 64 << 20, 8 << 20
	store := &Store{spoolDir: t.TempDir()}
	ctx := context.Background()
	if used := allocated(func() {
		file, got, _, done, err := store.spool(ctx, &endless{left: size}, size)
		if err != nil {
			t.Fatal(err)
		}
		defer done()
		if got != size {
			t.Fatalf("size %d", got)
		}
		if n, err := io.Copy(io.Discard, file); err != nil || n != size {
			t.Fatalf("read back %d, %v", n, err)
		}
	}); used > budget {
		t.Fatalf("spooling %d bytes allocated %d", int64(size), used)
	}
	// The limit is held as the bytes pass, not after reading them all.
	if used := allocated(func() {
		if _, _, _, _, err := store.spool(ctx, &endless{left: 1 << 40}, 1<<20); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("got %v", err)
		}
	}); used > budget {
		t.Fatalf("refusing allocated %d", used)
	}
}

func TestNewNeedsABucket(t *testing.T) {
	messages := config.MessageStorageConfig{S3Prefix: "messages/"}
	if _, err := New(messages, config.AttachmentStorageConfig{Type: "filesystem"}); err == nil {
		t.Fatal("it took a filesystem")
	}
	fake := s3test.New(t)
	store, err := New(messages, fake.Config)
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
}
