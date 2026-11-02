package mime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// memoryStore counts what it is given, and keeps only small parts.
type memoryStore struct {
	parts [][]byte
	sizes []int64
}

func (m *memoryStore) put(_ context.Context, part io.Reader) (StoredPart, error) {
	hash := sha256.New()
	var kept bytes.Buffer
	size, err := io.Copy(io.MultiWriter(hash, limitedWriter{&kept, 1 << 16}), part)
	if err != nil {
		return StoredPart{}, err
	}
	m.parts = append(m.parts, kept.Bytes())
	m.sizes = append(m.sizes, size)
	return StoredPart{Storage: "filesystem", Ref: "ref", Checksum: hex.EncodeToString(hash.Sum(nil)), Size: size}, nil
}

type limitedWriter struct {
	buffer *bytes.Buffer
	left   int
}

func (l limitedWriter) Write(p []byte) (int, error) {
	if room := l.left - l.buffer.Len(); room > 0 {
		l.buffer.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

const streamedMessage = "From: Ann <ann@example.org>\r\nTo: bob@example.org\r\nSubject: =?UTF-8?Q?caf=C3=A9?=\r\n" +
	"Message-ID: <m1@example.org>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=outer\r\n\r\n" +
	"--outer\r\nContent-Type: multipart/alternative; boundary=inner\r\n\r\n" +
	"--inner\r\nContent-Type: text/plain\r\n\r\nplain text\r\n" +
	"--inner\r\nContent-Type: text/html\r\n\r\n<p>html</p>\r\n--inner--\r\n" +
	"--outer\r\nContent-Type: image/png\r\nContent-Disposition: inline\r\nContent-ID: <pic>\r\nContent-Transfer-Encoding: base64\r\n\r\naW1hZ2U=\r\n" +
	"--outer\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=a.pdf\r\nContent-Transfer-Encoding: base64\r\n\r\ncGRm\r\n" +
	"--outer\r\nContent-Type: text/calendar\r\n\r\nBEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:1\r\nSUMMARY:Meet\r\nDTSTART:20260101T100000Z\r\nDTEND:20260101T110000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n" +
	"--outer--\r\n"

func stream(t *testing.T, message string, store *memoryStore, maxText int64) *emailResult {
	t.Helper()
	email, err := ParseStream(context.Background(), strings.NewReader(message), StreamOptions{Store: store.put, MaxText: maxText})
	if err != nil {
		t.Fatal(err)
	}
	return &emailResult{email.Headers.Subject, email.Body.Parts, len(email.CalendarEvents), email.Attachments, email.Inline, email.Metadata}
}

type emailResult struct {
	subject     string
	parts       any
	calendars   int
	attachments any
	inline      any
	metadata    map[string]string
}

// For an ordinary message, ParseStream decomposes it as Parse does, except that the parts are
// stored rather than held.
func TestParseStreamMatchesParse(t *testing.T) {
	want, err := Parse([]byte(streamedMessage))
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{}
	got, err := ParseStream(context.Background(), strings.NewReader(streamedMessage), StreamOptions{Store: store.put, MaxText: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Headers, want.Headers) {
		t.Errorf("headers %+v, want %+v", got.Headers, want.Headers)
	}
	if !reflect.DeepEqual(got.Body, want.Body) {
		t.Errorf("body %+v, want %+v", got.Body, want.Body)
	}
	if !reflect.DeepEqual(got.CalendarEvents, want.CalendarEvents) || len(got.CalendarEvents) != 1 {
		t.Errorf("calendar %+v, want %+v", got.CalendarEvents, want.CalendarEvents)
	}
	if len(got.Attachments) != 1 || len(got.Inline) != 1 {
		t.Fatalf("attachments %+v inline %+v", got.Attachments, got.Inline)
	}
	attachment, inline := got.Attachments[0], got.Inline[0]
	if attachment.Content != "" || inline.Content != "" {
		t.Error("a stored part's content stayed in memory")
	}
	if attachment.Filename != "a.pdf" || attachment.Size != 3 || attachment.Ref != "ref" || attachment.Disposition != "attachment" {
		t.Errorf("attachment %+v", attachment)
	}
	if inline.ContentID != "<pic>" || inline.Size != 5 || inline.Disposition != "inline" {
		t.Errorf("inline %+v", inline)
	}
	if string(store.parts[0]) != "image" || string(store.parts[1]) != "pdf" {
		t.Errorf("stored %q", store.parts)
	}
	if got.Metadata != nil {
		t.Errorf("metadata %v", got.Metadata)
	}
}

func TestParseStreamCapsText(t *testing.T) {
	text := strings.Repeat("x", 100)
	result := stream(t, "Content-Type: text/plain\r\n\r\n"+text, &memoryStore{}, 10)
	if result.metadata[MetadataBodyTruncated] != "true" {
		t.Fatalf("metadata %v", result.metadata)
	}
	email, err := ParseStream(context.Background(), strings.NewReader("Content-Type: text/plain\r\n\r\n"+text), StreamOptions{Store: (&memoryStore{}).put, MaxText: 10})
	if err != nil || email.Body.Content != text[:10] {
		t.Fatalf("content %q, %v", email.Body.Content, err)
	}
	// Exactly the limit is not truncated, and the parts after a capped one are still found.
	if result := stream(t, "Content-Type: text/plain\r\n\r\n"+text[:10], &memoryStore{}, 10); result.metadata != nil {
		t.Fatalf("metadata %v", result.metadata)
	}
	store := &memoryStore{}
	stream(t, "Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\n"+text+
		"\r\n--b\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment\r\n\r\npdf\r\n--b--\r\n", store, 10)
	if len(store.parts) != 1 || string(store.parts[0]) != "pdf" {
		t.Fatalf("stored %q", store.parts)
	}
}

func TestParseStreamStoreFailureIsAnError(t *testing.T) {
	broken := errors.New("disk full")
	_, err := ParseStream(context.Background(), strings.NewReader(streamedMessage), StreamOptions{
		Store:   func(context.Context, io.Reader) (StoredPart, error) { return StoredPart{}, broken },
		MaxText: 1 << 20,
	})
	if !errors.Is(err, broken) {
		t.Fatalf("got %v", err)
	}
}

func TestParseStreamWantsItsOptions(t *testing.T) {
	if _, err := ParseStream(context.Background(), strings.NewReader("a: b\r\n\r\n"), StreamOptions{}); err == nil {
		t.Fatal("it ran without a store or a limit")
	}
}

func TestParseStreamTakesWhatASenderCanMakeUp(t *testing.T) {
	for name, message := range map[string]string{
		"no boundary":      "Content-Type: multipart/mixed\r\n\r\nbody",
		"never closed":     "Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\ntext",
		"bad base64":       "Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment\r\nContent-Transfer-Encoding: base64\r\n\r\n!!!!\r\n--b--\r\n",
		"unknown encoding": "Content-Transfer-Encoding: nonsense\r\n\r\nbody",
		"unknown charset":  "Content-Type: text/plain; charset=nonsense\r\n\r\nbody",
		"no headers":       "just text",
		"empty":            "",
	} {
		t.Run(name, func(t *testing.T) {
			store := &memoryStore{}
			if _, err := ParseStream(context.Background(), strings.NewReader(message), StreamOptions{Store: store.put, MaxText: 1 << 20}); err != nil {
				if _, parseErr := Parse([]byte(message)); parseErr == nil {
					t.Fatalf("Parse takes it and ParseStream refuses it: %v", err)
				}
			}
		})
	}
}

// bigAttachment is a message with an attachment of size bytes, generated as it is read.
func bigAttachment(size int64) io.Reader {
	head := "From: a@example.org\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: application/zip\r\n" +
		"Content-Disposition: attachment; filename=big.zip\r\nContent-Transfer-Encoding: base64\r\n\r\n"
	reader, writer := io.Pipe()
	go func() {
		encoder := base64.NewEncoder(base64.StdEncoding, writer)
		chunk := make([]byte, 57*1024)
		for left := size; left > 0; left -= int64(len(chunk)) {
			if _, err := encoder.Write(chunk[:min(left, int64(len(chunk)))]); err != nil {
				return
			}
		}
		_ = encoder.Close()
		_, _ = io.WriteString(writer, "\r\n--b--\r\n")
		_ = writer.Close()
	}()
	return io.MultiReader(strings.NewReader(head), reader)
}

// The point of it: however big the attachment, parsing costs what the structure costs.
func TestParseStreamDoesNotHoldABigAttachment(t *testing.T) {
	const size = 64 << 20
	discard := func(ctx context.Context, part io.Reader) (StoredPart, error) {
		n, err := io.Copy(io.Discard, part)
		return StoredPart{Storage: "filesystem", Ref: "ref", Size: n}, err
	}
	source := bigAttachment(size)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	peak := uint64(0)
	email, err := ParseStream(context.Background(), source, StreamOptions{MaxText: 1 << 16, Store: func(ctx context.Context, part io.Reader) (StoredPart, error) {
		stored, err := discard(ctx, part)
		runtime.ReadMemStats(&after)
		peak = after.HeapAlloc
		return stored, err
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(email.Attachments) != 1 || email.Attachments[0].Size != size {
		t.Fatalf("attachments %+v", email.Attachments)
	}
	if peak > before.HeapAlloc+(16<<20) {
		t.Fatalf("heap went from %d to %d for a %d byte attachment", before.HeapAlloc, peak, int64(size))
	}
}
