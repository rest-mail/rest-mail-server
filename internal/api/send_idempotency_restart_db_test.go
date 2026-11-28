package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// #2105 inherits the restart criterion from #2110 AC01 (SRC-1644).
func TestSendIdempotencySurvivesRouterRestart(t *testing.T) {
	if os.Getenv("DB_HOST") == "" {
		t.Skip("set DB_HOST for the PostgreSQL restart test")
	}
	useRealGrants(t)

	const (
		key       = "restart-key"
		recipient = "recipient@remote.test"
	)
	tests := []struct {
		name              string
		request           func(*credentialFixture) (path, body string)
		withoutReceipt    int
		sendsAfterControl int64
	}{
		{
			name: "send",
			request: func(f *credentialFixture) (string, string) {
				return "/api/v1/messages/send", fmt.Sprintf(`{"from":%q,"to":[%q],"subject":"Restart test","body_text":"hello"}`, f.address, recipient)
			},
			withoutReceipt:    http.StatusCreated,
			sendsAfterControl: 2,
		},
		{
			name: "send-draft",
			request: func(f *credentialFixture) (string, string) {
				id := seedIdempotencyRestartMessage(f, models.Message{
					Folder: "Drafts", IsDraft: true, Sender: f.address,
					RecipientsTo: models.JSONB(fmt.Sprintf(`[%q]`, recipient)),
				})
				return fmt.Sprintf("/api/v1/messages/draft/%d/send", id), ""
			},
			withoutReceipt:    http.StatusNotFound, // The first send consumed the draft.
			sendsAfterControl: 1,
		},
		{
			name: "forward",
			request: func(f *credentialFixture) (string, string) {
				id := seedIdempotencyRestartMessage(f, models.Message{
					Folder: "INBOX", Sender: "original@remote.test",
					RecipientsTo: models.JSONB(fmt.Sprintf(`[%q]`, f.address)),
				})
				return fmt.Sprintf("/api/v1/messages/%d/forward", id), fmt.Sprintf(`{"from":%q,"to":[%q]}`, f.address, recipient)
			},
			withoutReceipt:    http.StatusCreated,
			sendsAfterControl: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newCredentialFixture(t)
			if err := f.db.FirstOrCreate(&models.QuotaUsage{}, models.QuotaUsage{MailboxID: f.mailboxID}).Error; err != nil {
				t.Fatal(err)
			}
			path, body := tc.request(f)
			request := func(router http.Handler) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
				req.Header.Set("Authorization", f.session)
				req.Header.Set("Idempotency-Key", key)
				if body != "" {
					req.Header.Set("Content-Type", "application/json")
				}
				response := httptest.NewRecorder()
				router.ServeHTTP(response, req)
				return response
			}

			first := request(f.router)
			wantSent(t, tc.name, first)
			assertIdempotencyRestartCounts(f, "first send", 1)
			var sent models.Message
			if err := f.db.Where("mailbox_id = ? AND folder = 'Sent'", f.mailboxID).First(&sent).Error; err != nil {
				t.Fatal(err)
			}
			f.sentSource(sent)
			f.routers.Close()

			// Build the production router and its message store again, using a
			// new pool against the same database and the same storage directory.
			cfg := scratchDBConfig()
			cfg.DBName = f.db.Migrator().CurrentDatabase()
			restartDB, err := rmdb.Connect(&cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { closeScratchConn(restartDB) })
			restart := func() *httptest.ResponseRecorder {
				routers := NewRouters(restartDB, f.jwt, f.cfg, nil)
				defer routers.Close()
				return request(routers.Public)
			}

			replay := restart()
			if replay.Code != first.Code || replay.Body.String() != first.Body.String() {
				t.Fatalf("replay after restart = %d %s, want %d %s", replay.Code, replay.Body, first.Code, first.Body)
			}
			assertIdempotencyRestartCounts(f, "replay after restart", 1)

			// Remove the only receipt in this isolated fixture, then repeat the
			// same restart and request to prove the replay depends on that row.
			deleted := f.db.Where("account_id = ?", f.accountID).Delete(&models.SendIdempotency{})
			if deleted.Error != nil || deleted.RowsAffected != 1 {
				t.Fatalf("delete the receipt: rows=%d, err=%v", deleted.RowsAffected, deleted.Error)
			}
			control := restart()
			if control.Code != tc.withoutReceipt || control.Body.String() == first.Body.String() {
				t.Fatalf("replay without receipt = %d %s, want status %d and a different body from %s",
					control.Code, control.Body, tc.withoutReceipt, first.Body)
			}
			assertIdempotencyRestartCounts(f, "replay without receipt", tc.sendsAfterControl)
		})
	}
}

func seedIdempotencyRestartMessage(f *credentialFixture, message models.Message) uint {
	f.t.Helper()
	message.MailboxID = f.mailboxID
	message.Subject, message.BodyText = "Restart test", "hello"
	message.RecipientsCc = models.JSONB(`[]`)
	if err := f.db.Create(&message).Error; err != nil {
		f.t.Fatal(err)
	}
	return message.ID
}

func assertIdempotencyRestartCounts(f *credentialFixture, phase string, want int64) {
	f.t.Helper()
	for _, check := range []struct {
		name  string
		model any
		where string
	}{
		{"queue rows", &models.OutboundQueue{}, "true"},
		{"Sent copies", &models.Message{}, "folder = 'Sent'"},
		{"stored message sources", &models.MessageSource{}, "true"},
	} {
		var got int64
		if err := f.db.Model(check.model).Where(check.where).Count(&got).Error; err != nil {
			f.t.Fatalf("%s: count %s: %v", phase, check.name, err)
		}
		if got != want {
			f.t.Errorf("%s: %s = %d, want %d", phase, check.name, got, want)
		}
	}
}
