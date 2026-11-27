package mime

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

type openAttachmentFunc func() (io.ReadCloser, error)

func (f openAttachmentFunc) Open() (io.ReadCloser, error) { return f() }

func TestSerializeToMatchesLegacyWire(t *testing.T) {
	for _, tc := range serializeWireCases() {
		t.Run(tc.name, func(t *testing.T) {
			var dst bytes.Buffer
			sources := func(parts []pipeline.Attachment) []AttachmentSource {
				var out []AttachmentSource
				for _, part := range parts {
					raw, err := base64.StdEncoding.DecodeString(part.Content)
					if err != nil {
						t.Fatal(err)
					}
					out = append(out, openAttachmentFunc(func() (io.ReadCloser, error) {
						if !strings.Contains(dst.String(), "Content-Transfer-Encoding: base64\r\n") {
							t.Fatal("attachment opened before its part headers were written")
						}
						return io.NopCloser(iotest.OneByteReader(bytes.NewReader(raw))), nil
					}))
				}
				return out
			}
			var boundary int
			opt := SerializeOptions{
				Boundary:          func() string { boundary++; return wireBoundary(boundary) },
				AttachmentSources: sources(tc.email.Attachments),
				InlineSources:     sources(tc.email.Inline),
			}
			if err := SerializeTo(&dst, tc.email, opt); err != nil {
				t.Fatal(err)
			}
			if want := expandWire(tc.wire); dst.String() != want {
				t.Fatalf("wire output (%d bytes)\n got: %q\nwant: %q", dst.Len(), dst.String(), want)
			}
			t.Logf("matched %d legacy wire bytes", dst.Len())
		})
	}
}

func TestSerializeToBase64LineEnd(t *testing.T) {
	const boundary = "test-boundary"
	for _, rawSize := range []int{0, 56, 57, 58, 114, 115} {
		email := &pipeline.EmailJSON{Attachments: []pipeline.Attachment{{ContentType: "application/octet-stream"}}}
		opts := SerializeOptions{
			Boundary: func() string { return boundary },
			AttachmentSources: []AttachmentSource{openAttachmentFunc(func() (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader(strings.Repeat("x", rawSize))), nil
			})},
		}
		var got strings.Builder
		if err := SerializeTo(&got, email, opts); err != nil {
			t.Fatal(err)
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", rawSize)))
		if len(encoded) > 76 {
			encoded = encoded[:76] + "\r\n" + encoded[76:]
		}
		if len(encoded) > 154 {
			encoded = encoded[:154] + "\r\n" + encoded[154:]
		}
		want := "Content-Type: application/octet-stream\r\n\r\n" + encoded + "\r\n--" + boundary + "--\r\n"
		if !strings.HasSuffix(got.String(), want) {
			t.Errorf("%d input bytes: got %q, want suffix %q", rawSize, got.String(), want)
		}
	}
}

type attachmentReadCloser struct {
	io.Reader
	closed   bool
	closeErr error
}

func (r *attachmentReadCloser) Close() error {
	r.closed = true
	return r.closeErr
}

type attachmentErrorWriter struct {
	opened bool
	err    error
}

func (w *attachmentErrorWriter) Write(p []byte) (int, error) {
	if w.opened {
		return 0, w.err
	}
	return len(p), nil
}

func TestSerializeToAttachmentErrors(t *testing.T) {
	broken := errors.New("attachment failed")
	for _, stage := range []string{"open", "read", "close", "write"} {
		t.Run(stage, func(t *testing.T) {
			email := &pipeline.EmailJSON{Attachments: []pipeline.Attachment{{}}}
			read := &attachmentReadCloser{Reader: strings.NewReader(strings.Repeat("x", 4097))}
			failedWriter := &attachmentErrorWriter{err: broken}
			var dst io.Writer = io.Discard
			switch stage {
			case "read":
				read.Reader = iotest.ErrReader(broken)
			case "close":
				read.closeErr = broken
			case "write":
				dst = failedWriter
			}
			opt := SerializeOptions{AttachmentSources: []AttachmentSource{openAttachmentFunc(func() (io.ReadCloser, error) {
				if stage == "open" {
					return nil, broken
				}
				failedWriter.opened = true
				return read, nil
			})}}
			if err := SerializeTo(dst, email, opt); !errors.Is(err, broken) {
				t.Fatalf("error = %v, want %v", err, broken)
			}
			if stage != "open" && !read.closed {
				t.Fatal("source left open")
			}
		})
	}
}

func TestSerializeToBoundsAttachmentAllocation(t *testing.T) {
	email := &pipeline.EmailJSON{Headers: pipeline.Headers{Date: wireDate}, Attachments: []pipeline.Attachment{{}}}
	for _, size := range []int{1 << 20, 32 << 20} {
		// strings.Reader implements WriterTo, which would otherwise offer a copy
		// of the entire string to the encoder. Keep the source outside the measure.
		data := strings.Repeat("x", size)
		opt := SerializeOptions{AttachmentSources: []AttachmentSource{openAttachmentFunc(func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(data)), nil
		})}}
		var err error
		allocated := bytesAllocated(func() { err = SerializeTo(io.Discard, email, opt) })
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%d attachment bytes allocated %d bytes", size, allocated)
		if allocated > 1<<20 {
			t.Fatalf("rendering allocated %d bytes, want at most 1 MiB", allocated)
		}
	}
}
