package messageseal

import (
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
)

type repeatedSource struct{ left int64 }

func (source *repeatedSource) Read(buffer []byte) (int, error) {
	if source.left == 0 {
		return 0, io.EOF
	}
	if int64(len(buffer)) > source.left {
		buffer = buffer[:source.left]
	}
	clear(buffer)
	source.left -= int64(len(buffer))
	return len(buffer), nil
}

func TestDigestStreamsWithoutBodySizedAllocation(t *testing.T) {
	body := strings.Repeat("x", 64<<20)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := Digest(&repeatedSource{left: 64 << 20}, body)
	runtime.ReadMemStats(&after)
	if err != nil || len(got) != 64 {
		t.Fatalf("digest %q, %v", got, err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
		t.Fatalf("64 MiB source and body allocated %d bytes", allocated)
	}
}

type failedSource struct{}

func (failedSource) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestDigestDoesNotSealAPartialStream(t *testing.T) {
	got, err := Digest(failedSource{})
	if got != "" || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("partial source sealed: %q, %v", got, err)
	}
}
