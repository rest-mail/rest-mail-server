package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/attachmentstore"
	"github.com/rest-mail/rest-mail-server/internal/attachmentstore/s3test"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

func storedForwardPart(t *testing.T, h *MessageHandler, data []byte) pipeline.Attachment {
	t.Helper()
	ref, sum, err := attachmentstore.PutFilesystem(t.Context(), h.forwardRoot, data)
	if err != nil {
		t.Fatal(err)
	}
	return pipeline.Attachment{Filename: "part.bin", ContentType: "application/octet-stream", Size: int64(len(data)), Storage: "filesystem", Ref: ref, Checksum: sum}
}

// A stored part is checked as it is read: one that is shorter, longer or other than its
// row says is refused with the forward's attachment error, and one that is whole answers
// its checksum (#2107).
func TestCheckForwardPartRefusesAChangedObject(t *testing.T) {
	h := &MessageHandler{forwardRoot: t.TempDir(), maxForwardBytes: 1 << 20}
	data := []byte("attachment bytes")
	part := storedForwardPart(t, h, data)
	if sum, err := h.checkForwardPart(t.Context(), part); err != nil || sum != part.Checksum {
		t.Fatalf("whole part: %q %v", sum, err)
	}
	unsummed := part
	unsummed.Checksum = ""
	if sum, err := h.checkForwardPart(t.Context(), unsummed); err != nil || sum != part.Checksum {
		t.Fatalf("a part without a checksum is not given one: %q %v", sum, err)
	}
	for name, change := range map[string]func(p *pipeline.Attachment){
		"shorter":  func(p *pipeline.Attachment) { p.Size++ },
		"longer":   func(p *pipeline.Attachment) { p.Size--; p.Checksum = "" },
		"other":    func(p *pipeline.Attachment) { p.Checksum = hex.EncodeToString(make([]byte, 32)) },
		"missing":  func(p *pipeline.Attachment) { p.Ref += ".gone" },
		"unknown":  func(p *pipeline.Attachment) { p.Storage = "tape" },
		"negative": func(p *pipeline.Attachment) { p.Size = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := part
			change(&changed)
			if _, err := h.checkForwardPart(t.Context(), changed); err != errForwardAttachment {
				t.Fatalf("err = %v, want the attachment error", err)
			}
		})
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := h.checkForwardPart(cancelled, part); err != errForwardAttachment {
		t.Fatalf("a cancelled read: %v", err)
	}
}

// The pipeline's filters open the parts the send handed in, and only those: a reference a
// filter made up, or one with another size, is not opened.
func TestReferencedPartsOpenOnlyThePartsHandedIn(t *testing.T) {
	h := &MessageHandler{forwardRoot: t.TempDir()}
	data := []byte("handed in")
	part := storedForwardPart(t, h, data)
	other := storedForwardPart(t, h, []byte("not handed in"))
	open := h.referencedParts([]pipeline.Attachment{part})
	if !open.Holds(part) {
		t.Fatal("the handed-in part is not held")
	}
	r, err := open.Open(t.Context(), part)
	if err != nil {
		t.Fatalf("handed-in part: %v", err)
	}
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read %q %v", got, err)
	}
	resized := part
	resized.Size++
	for name, att := range map[string]pipeline.Attachment{"not handed in": other, "another size": resized} {
		if open.Holds(att) {
			t.Fatalf("%s: held", name)
		}
		if r, err := open.Open(t.Context(), att); err == nil {
			_ = r.Close()
			t.Fatalf("%s: opened", name)
		}
	}
}

// A part's independent copy is streamed from where it is and checked on the way, on the
// filesystem and in S3; a part that is not what it says is refused, and stores nothing
// that a row then refers to.
func TestPutIndependentPartStreamsAndChecks(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 4096)
	sum := sha256.Sum256(data)
	for _, store := range []string{"filesystem", "s3"} {
		t.Run(store, func(t *testing.T) {
			h := &MessageHandler{forwardRoot: t.TempDir()}
			if store == "s3" {
				h.forwardStore = attachmentstore.NewS3(s3test.New(t).Config)
			}
			part := storedForwardPart(t, h, data)
			storage, ref, checksum, err := h.putIndependentPart(t.Context(), part)
			if err != nil || storage != store || checksum != hex.EncodeToString(sum[:]) || ref == part.Ref {
				t.Fatalf("copy: %q %q %q %v", storage, ref, checksum, err)
			}
			copied, err := h.openForwardPart(t.Context(), pipeline.Attachment{Storage: storage, Ref: ref})
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(copied)
			_ = copied.Close()
			if err != nil || !bytes.Equal(got, data) {
				t.Fatal("the copy is not the part")
			}
			// The same part as content, as a transform hands it back.
			content := pipeline.Attachment{Size: part.Size, Content: base64.StdEncoding.EncodeToString(data)}
			if _, _, checksum, err := h.putIndependentPart(t.Context(), content); err != nil || checksum != part.Checksum {
				t.Fatalf("content copy: %q %v", checksum, err)
			}
			wrong := part
			wrong.Checksum = hex.EncodeToString(make([]byte, 32))
			if _, _, _, err := h.putIndependentPart(t.Context(), wrong); !errors.Is(err, errForwardAttachment) {
				t.Fatalf("a part with another checksum was copied: %v", err)
			}
			short := part
			short.Size++
			if _, _, _, err := h.putIndependentPart(t.Context(), short); err == nil {
				t.Fatal("a part shorter than its size was copied")
			}
		})
	}
}

// Held parts are copies of their own, kept as references: the held row carries no bytes,
// and the copies outlive the originals (#2107).
func TestRetainHeldPartsKeepsReferencesToCopies(t *testing.T) {
	h := &MessageHandler{forwardRoot: t.TempDir(), maxForwardBytes: 64}
	data := []byte("held attachment")
	part := storedForwardPart(t, h, data)
	empty := storedForwardPart(t, h, nil)
	retained, err := h.retainHeldParts(t.Context(), []pipeline.Attachment{part, empty})
	if err != nil {
		t.Fatal(err)
	}
	for i, held := range retained {
		original := []pipeline.Attachment{part, empty}[i]
		if held.Content != "" || held.Ref == "" || held.Ref == original.Ref || held.Checksum != original.Checksum || held.Size != original.Size {
			t.Fatalf("held part %d: %+v", i, held)
		}
	}
	if err := os.Remove(part.Ref); err != nil {
		t.Fatal(err)
	}
	if sum, err := h.checkForwardPart(t.Context(), retained[0]); err != nil || sum != part.Checksum {
		t.Fatalf("the held copy did not outlive the original: %v", err)
	}
	h.maxForwardBytes = int64(len(data)) - 1
	if _, err := h.retainHeldParts(t.Context(), []pipeline.Attachment{retained[0]}); err != errForwardTooLarge {
		t.Fatalf("a hold over the forward limit: %v", err)
	}
	if got, err := h.retainHeldParts(t.Context(), nil); err != nil || got != nil {
		t.Fatalf("no parts: %v %v", got, err)
	}
}
