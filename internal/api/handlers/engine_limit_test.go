package handlers

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/pipeline/filters"
)

// The store-path limit holds with a pipeline that reads the address lists, so that keeping a list
// unread on the way to the row hasn't only moved what it costs. A Sieve script reads To and Cc for
// every message.
func TestDeliverHoldsTheLimitThroughTheEngine(t *testing.T) {
	const id = "From: old@example.test\r\nSubject: s\r\nDate: Thu, 02 Jan 2020 03:04:05 +0000\r\n"
	db := importProofDB(t)
	if err := db.AutoMigrate(&models.Pipeline{}, &models.OutboundQueue{}, &models.SieveScript{}); err != nil {
		t.Fatal(err)
	}
	domain := models.Domain{Name: "engine-limit.test", Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	mailbox := models.Mailbox{DomainID: domain.ID, LocalPart: "to", Address: "to@engine-limit.test", Active: true, QuotaBytes: 1 << 40}
	if err := db.Create(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.QuotaUsage{MailboxID: mailbox.ID}).Error; err != nil {
		t.Fatal(err)
	}
	seedReceiveFixture(t, db, mailbox)
	encoded, err := json.Marshal([]pipeline.FilterConfig{sieveStep("")})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Pipeline{DomainID: domain.ID, Direction: "inbound", Filters: encoded, Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	const script = `require ["fileinto"]; if address :is "to" "nobody@nowhere.test" { fileinto "Never"; }`
	if err := db.Create(&models.SieveScript{MailboxID: mailbox.ID, Script: script, ScriptHash: filters.HashSieveScript(script), Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	route := deliveryRoute(NewMessageHandler(db, nil, pipeline.NewEngine(withQuietVacation(pipeline.DefaultRegistry), slog.Default()), "", nil))
	send := func(message string) (*httptest.ResponseRecorder, uint64) {
		body := fmt.Sprintf(`{"address":"%[1]s","sender":"old@example.test","sender_name":"Old","subject":"s","message_id":"m@example.test","recipients_to":["%[1]s"],"client_ip":"192.0.2.1","helo_name":"mail.example.test","received_tls":true,"tls_version":"TLS1.3","tls_cipher":"TLS_AES_128_GCM_SHA256","raw_message":"%[2]s"}`, mailbox.Address, base64.StdEncoding.EncodeToString([]byte(message)))
		r := httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		route(w, r)
		runtime.ReadMemStats(&after)
		return w, after.TotalAlloc - before.TotalAlloc
	}
	if w, _ := send("Message-ID: <warm@example.test>\r\n" + id + "\r\nb\r\n"); w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	shapes := hostileFieldShapes(id)
	for name, message := range shapes {
		if !strings.HasPrefix(name, "address list") {
			continue
		}
		t.Run(name, func(t *testing.T) {
			best, length := uint64(1)<<63, 0
			for attempt := range 3 {
				stored := fmt.Sprintf("Message-ID: <engine-%s-%d@example.test>\r\n", strings.ReplaceAll(name, " ", "-"), attempt) + message
				length = len(stored)
				w, allocated := send(stored)
				if w.Code != http.StatusCreated {
					t.Fatalf("%d %.300s", w.Code, w.Body)
				}
				best = min(best, allocated)
			}
			t.Logf("delivering %d bytes allocated %d, %.1f times over", length, best, float64(best)/float64(length))
			if best > 8*uint64(length) {
				t.Errorf("delivering %d bytes allocated %d", length, best)
			}
		})
	}
}
