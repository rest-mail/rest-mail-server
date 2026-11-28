package handlers

import (
	"bytes"
	"database/sql/driver"
	"errors"
	"io"
	"net/http"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/attachmentstore"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"github.com/rest-mail/rest-mail-server/internal/messagestore"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"gorm.io/gorm"
)

func setTestMessageSources(t *testing.T, h *MessageHandler) *MessageHandler {
	t.Helper()
	sources, err := messagestore.NewSources(h.db, &config.Config{
		Attachments: config.AttachmentStorageConfig{Type: "filesystem"},
		Messages:    config.MessageStorageConfig{Directory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.SetMessageSources(sources)
	return h
}

func testMessageHandler(t *testing.T, db *gorm.DB) *MessageHandler {
	t.Helper()
	return setTestMessageSources(t, NewMessageHandler(db, nil, nil, "", nil))
}

// testSourceLedger only substitutes SQL for handler fault tests. These tests
// still render, upload, and read the source through the real filesystem store.
type testSourceLedger struct {
	size     int64
	checksum string
}

func (l *testSourceLedger) query(q string, args []driver.NamedValue) (driver.Rows, bool) {
	if !strings.Contains(q, `FROM "message_sources"`) {
		return nil, false
	}
	if strings.HasPrefix(q, `SELECT "ref"`) {
		return &deliveryReadRows{columns: []string{"ref"}, values: []driver.Value{args[0].Value}}, true
	}
	return &deliveryReadRows{columns: []string{"size", "checksum"}, values: []driver.Value{l.size, l.checksum}}, true
}

func (l *testSourceLedger) exec(q string, args []driver.NamedValue) {
	if strings.HasPrefix(q, `UPDATE "message_sources" SET "checksum"`) {
		l.checksum, l.size = args[0].Value.(string), args[2].Value.(int64)
	}
}

func composedRaw(t *testing.T, h *MessageHandler, ref *string) string {
	t.Helper()
	if ref == nil {
		t.Fatal("composed message has no stored source reference")
	}
	r, size, err := h.messageSources.Open(t.Context(), *ref)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(r)
	if err := errors.Join(readErr, r.Close()); err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) != size {
		t.Fatalf("source read %d bytes, want %d", len(data), size)
	}
	return string(data)
}

func spoolForwardForTest(h *MessageHandler, r *http.Request, req sendMessageRequest, sent *models.Message, parts []pipeline.Attachment) error {
	return h.spoolComposed(r.Context(), outboundMessage{
		From: req.From, To: req.To, Subject: req.Subject, BodyText: req.BodyText,
		MessageID: sent.MsgID, Date: sent.ReceivedAt, Attachments: parts,
	}, sent, [][]string{req.To}, false, false)
}

func TestComposedSourcePeakHeap(t *testing.T) {
	const big = 24 << 20
	h := NewMessageHandler(nil, nil, nil, "", nil)
	h.SetForwardStorage(2*big, nil)
	h.SetMessageSpoolDir(t.TempDir())
	h.forwardRoot = t.TempDir()
	store := messagestore.NewFilesystem(t.TempDir())
	measure := func(attachmentSize int64, recipientCount int) uint64 {
		t.Helper()
		part := io.LimitReader(repeatingReader("attachment octets "), attachmentSize)
		ref, checksum, size, err := attachmentstore.PutFilesystemReader(t.Context(), h.forwardRoot, part)
		if err != nil || size != attachmentSize {
			t.Fatalf("seed part: size %d, error %v", size, err)
		}
		m := testOutboundMessage()
		m.Attachments = []pipeline.Attachment{{
			Filename: "payload.bin", ContentType: "application/octet-stream",
			Size: size, Storage: "filesystem", Ref: ref, Checksum: checksum,
		}}
		// These are envelope-only recipients. To and Cc are part of the
		// header, whose metadata can grow with the addresses it carries.
		group := make([]string, recipientCount)
		for i := range group {
			group[i] = "private@remote.test"
		}
		runtime.GC()
		previous := debug.SetGCPercent(-1)
		defer debug.SetGCPercent(previous)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		err = h.withComposedSource(t.Context(), m, false, func(source composedSource) error {
			object, err := store.Put(t.Context(), source, source.size)
			if err != nil {
				return err
			}
			if object.Size != source.size || source.size <= attachmentSize ||
				!bytes.HasSuffix(source.headers, []byte("\r\n\r\n")) {
				t.Fatal("stored source size or header metadata is wrong")
			}
			if _, _, _, err := h.putIndependentPart(t.Context(), m.Attachments[0]); err != nil {
				return err
			}
			sent := models.Message{ID: 7, Sender: m.From, SourceRef: &object.Ref}
			seen := 0
			err = queueComposed(&sent, [][]string{group}, false, func(rows []models.OutboundQueue) error {
				seen += len(rows)
				for _, row := range rows {
					if row.SourceRef != sent.SourceRef || row.RawMessage != "" || row.RawOctets != nil {
						t.Fatal("queue row carries its own source")
					}
				}
				return nil
			})
			if seen != len(group) {
				t.Fatalf("queued %d of %d recipients", seen, len(group))
			}
			return err
		})
		runtime.ReadMemStats(&after)
		if err != nil {
			t.Fatal(err)
		}
		// Collections were disabled so the final in-use heap captures the
		// peak even if a large temporary buffer became unreachable mid-send.
		return after.HeapAlloc - before.HeapAlloc
	}

	small := measure(1<<10, 1)
	bigPart := measure(big, 1)
	manyRecipients := measure(big, 8192)
	t.Logf("peak heap above baseline: small %d bytes, large %d bytes, large with fan-out %d bytes", small, bigPart, manyRecipients)
	if bigPart > small+1<<20 || manyRecipients > bigPart+256<<10 {
		t.Fatal("composing or queuing retained bytes proportional to the payload or recipient count")
	}
}

func TestComposedSourceUsesCRLFAndExcludesHiddenFields(t *testing.T) {
	h := NewMessageHandler(nil, nil, nil, "", nil)
	message := testOutboundMessage()
	message.BodyText = "first\nsecond\rthird"
	message.BodyHTML = "<p>first</p>\n<p>second</p>"
	message.Extra = map[string]string{"bCc": "hidden@example.test", "Resent-Bcc": "other@example.test"}
	if err := h.withComposedSource(t.Context(), message, false, func(source composedSource) error {
		body, err := io.ReadAll(source)
		if err != nil {
			return err
		}
		raw := string(body)
		if rmail.HasBareLineEnds(raw) || !strings.Contains(raw, "first\r\nsecond\r\nthird") {
			t.Fatalf("multipart source has unexpected line endings: %q", raw)
		}
		if strings.Contains(raw, "hidden@example.test") || strings.Contains(raw, "other@example.test") {
			t.Fatalf("hidden recipient appeared in %q", raw)
		}
		if !bytes.HasPrefix(body, source.headers) {
			t.Fatal("header metadata differs from the stored source")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestComposedSourceCeilingCountsDKIM(t *testing.T) {
	h, domain, _ := seedSigningDomain(t, "spool-size")
	h.SetForwardStorage(0, nil)
	message := testOutboundMessage()
	message.From = "sender@" + domain
	var size int64
	if err := h.withComposedSource(t.Context(), message, true, func(source composedSource) error {
		if !strings.HasPrefix(string(source.headers), "DKIM-Signature: ") {
			t.Fatal("signed source is missing DKIM")
		}
		size = source.size
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h.maxForwardBytes = size - 1
	if err := h.withComposedSource(t.Context(), message, true, func(composedSource) error {
		t.Fatal("over-limit signed source reached the store")
		return nil
	}); !errors.Is(err, errComposedTooLarge) {
		t.Fatalf("want signed-size refusal, got %v", err)
	}
	h.maxForwardBytes = size
	if err := h.withComposedSource(t.Context(), message, true, func(source composedSource) error {
		if source.size != size {
			t.Fatalf("signed size = %d, want %d", source.size, size)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
