package queue

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/messagestore"
	"gorm.io/gorm"
)

func sourceDeliveryDB(t *testing.T) (*Worker, *gorm.DB) {
	t.Helper()
	db := openResetWorkerDB(t)
	if err := db.AutoMigrate(&models.MessageSource{}, &models.MessageSourceAttempt{}); err != nil {
		t.Fatal(err)
	}
	w := eightBitWorker(t, db)
	cfg := &config.Config{
		Messages:    config.MessageStorageConfig{Directory: t.TempDir()},
		Attachments: config.AttachmentStorageConfig{Type: "filesystem"},
	}
	store, err := messagestore.NewSources(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	w.SetMessageSources(store)
	return w, db
}

func stageQueueSource(t *testing.T, w *Worker, headers, raw string, recipients ...string) []models.OutboundQueue {
	t.Helper()
	source, err := w.sources.Stage(t.Context(), strings.NewReader(raw), int64(len(raw)), []byte(headers))
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]models.OutboundQueue, 0, len(recipients))
	err = w.db.Transaction(func(tx *gorm.DB) error {
		for i, to := range recipients {
			row := models.OutboundQueue{Sender: "sender@example.test", Recipient: to, Domain: "remote.test",
				SourceRef: &source.Ref, NextAttempt: time.Now().Add(-time.Minute).Add(time.Duration(i) * time.Second)}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			row.SourceHeaders = headers
			rows = append(rows, row)
		}
		return w.sources.Publish(t.Context(), tx, source.Ref, int64(len(rows)))
	})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestQueuedReferenceDATAParity(t *testing.T) {
	w, _ := sourceDeliveryDB(t)
	const headers = "From: sender@example.test\r\nSubject: parity"
	cert, roots := relayTestCertificate(t, false)
	for _, shape := range []struct{ name, raw string }{
		{"dots", headers + "\r\n\r\n.\r\n..\r\n"},
		{"LF", headers + "\n\nfoo\n.\nbar\n"},
		{"8bit", headers + "\r\n\r\n\xff café\r\n"},
		{"long", headers + "\r\n\r\n" + strings.Repeat("x", dataBlockSize*2) + "\r\n.\r\n"},
		{"long LF", headers + "\r\n\r\n" + strings.Repeat("x", dataBlockSize*2) + "\n.\n"},
		{"line requiring encoding", headers + "\r\n\r\n" + strings.Repeat("x", 999) + "\n.\n"},
	} {
		t.Run(shape.name, func(t *testing.T) {
			ref := stageQueueSource(t, w, headers, shape.raw, "a@remote.test")[0]
			inline := ref
			inline.SourceRef = nil
			inline.RawMessage = shape.raw
			var wires, relayed []string
			for _, item := range []models.OutboundQueue{inline, ref} {
				peer := newWirePeer(t, true)
				if err := w.deliverSMTP(t.Context(), peer.addr, "mx.remote.test", item, nil, nil); err != nil {
					t.Fatal(err)
				}
				wires = append(wires, relayedOnce(t, peer))
				settings, finish := relayTestPeer(t, cert, relayPeerOptions{})
				settings.roots, settings.username, settings.password = roots, "", ""
				if err := w.deliverViaRelay(t.Context(), settings, item); err != nil {
					t.Fatal(err)
				}
				result := finish()
				relayed = append(relayed, result.data)
			}
			if wires[0] != wires[1] || relayed[0] != relayed[1] {
				t.Fatalf("source and inline SMTP bytes differ (direct %d/%d, relay %d/%d)",
					len(wires[0]), len(wires[1]), len(relayed[0]), len(relayed[1]))
			}
		})
	}
}

func TestQueuedReferenceRetryAndLastRelease(t *testing.T) {
	w, db := sourceDeliveryDB(t)
	const headers = "From: sender@example.test\r\nSubject: retry"
	raw := headers + "\r\n\r\n" + strings.Repeat(".test!\r\n", dataBlockSize/4)
	rows := stageQueueSource(t, w, headers, raw, "a@remote.test", "b@remote.test")
	ref := *rows[0].SourceRef
	cert, roots := relayTestCertificate(t, false)
	now := time.Now()
	assertCount := func(want int64) {
		t.Helper()
		var stored models.MessageSource
		if err := db.Where("ref = ?", ref).Take(&stored).Error; err != nil || stored.ReferenceCount != want {
			t.Fatalf("source references=%d, want %d (%v)", stored.ReferenceCount, want, err)
		}
	}
	for i := range 3 {
		item, err := w.claim(now)
		if err != nil {
			t.Fatal(err)
		}
		if item.pin == "" || item.RawMessage != "" || item.sourceOctets != int64(len(raw)) || item.SourceHeaders != headers {
			t.Fatalf("unexpected claim metadata for row %d", item.ID)
		}
		if i < 2 {
			assertCount(3)
		} else {
			assertCount(2)
		}
		opts := relayPeerOptions{}
		if i == 0 {
			opts.rejectAt, opts.reply = "DATA reply", "451 4.3.0 try again"
		}
		settings, finish := relayTestPeer(t, cert, opts)
		settings.roots, settings.username, settings.password = roots, "", ""
		deliveryErr := w.deliverViaRelay(t.Context(), settings, item.OutboundQueue)
		if received := finish().data; received != raw {
			t.Fatalf("attempt %d sent %d bytes, want %d exact bytes", i+1, len(received), len(raw))
		}
		if i == 0 {
			if deliveryErr == nil {
				t.Fatal("peer refusal was ignored")
			}
			w.recordFailure(t.Context(), item.OutboundQueue, deliveryErr, now)
			if stored := readRow(t, db, item.ID); stored.Status != "deferred" || stored.SourceRef == nil {
				t.Fatalf("retry lost its queue owner: %+v", stored)
			}
			// Make the same recipient the next claim without sleeping for backoff.
			if err := db.Model(&models.OutboundQueue{}).Where("id = ?", item.ID).
				Update("next_attempt", now.Add(-time.Hour)).Error; err != nil {
				t.Fatal(err)
			}
		} else {
			if deliveryErr != nil {
				t.Fatal(deliveryErr)
			}
			affected, err := w.writeAttemptOutcome(t.Context(), item.OutboundQueue, false, map[string]any{"status": "delivered"})
			if err != nil || affected != 1 {
				t.Fatalf("record delivered: rows=%d (%v)", affected, err)
			}
		}
		assertCount(int64(3 - i))
		w.releaseAttemptPin(t.Context(), item.pin)
		assertCount(int64(2 - i))
	}
	removed, err := w.sources.Reap(t.Context())
	if err != nil || removed != 1 {
		t.Fatalf("reaper removed %d, want one (%v)", removed, err)
	}
	if err := db.Where("ref = ?", ref).Take(&models.MessageSource{}).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("source row remains after reap: %v", err)
	}
}

func TestQueuedReferenceRESTMAILExactLength(t *testing.T) {
	w, _ := sourceDeliveryDB(t)
	const headers = "Subject: RESTMAIL"
	raw := headers + "\r\n\r\n" + strings.Repeat("\xff.abc\n", dataBlockSize)
	row := stageQueueSource(t, w, headers, raw, "a@remote.test")[0]
	srv := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		var fields struct {
			From string
			To   []string
			Raw  []byte `json:"raw_message"`
		}
		if r.ContentLength <= int64(len(raw)) || len(r.TransferEncoding) != 0 {
			t.Errorf("no exact content length: %d %v", r.ContentLength, r.TransferEncoding)
		}
		if err := json.NewDecoder(r.Body).Decode(&fields); err != nil ||
			string(fields.Raw) != raw || fields.From != row.Sender || len(fields.To) != 1 || fields.To[0] != row.Recipient {
			t.Errorf("RESTMAIL decoded %d raw bytes with envelope %q to %q: %v", len(fields.Raw), fields.From, fields.To, err)
		}
		rw.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	w.restmailRoots = srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	if err := w.deliverRESTMAILHTTPS(t.Context(), srv.URL, row); err != nil {
		t.Fatal(err)
	}
	w.SetMaxSourceOctets(int64(len(raw) - 1))
	if err := w.deliverRESTMAILHTTPS(t.Context(), srv.URL, row); smtpErrorFrom(err) == nil || smtpErrorFrom(err).Code != 552 {
		t.Fatalf("independent open size check returned %v, want 552", err)
	}
}
