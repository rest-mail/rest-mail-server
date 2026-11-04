package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var gatewayDeliveryExamples = []string{
	"malformed_json", "body_array", "no_mailbox", "raw_message_invalid",
	"address_empty", "recipient_missing",
	"recipients_nested_nul", "address_nul", "sender_nul", "sender_name_nul", "subject_nul", "body_text_nul", "body_html_nul",
	"message_id_nul", "in_reply_to_nul", "references_nul", "client_ip_nul", "helo_name_nul", "tls_version_nul",
	"recipients_to_nul", "recipients_cc_nul",
}

func TestGatewayDeliveryExamples(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	pool := sql.OpenDB(mailboxLookupConnection{})
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	jwt := auth.NewJWTService(routerTestSecret, time.Minute, time.Hour)
	routers := NewRouters(db, jwt, &config.Config{Environment: "test"}, nil)
	t.Cleanup(routers.Close)

	for _, name := range gatewayDeliveryExamples {
		t.Run("deliverMessage/"+name, func(t *testing.T) {
			examplepairs.Proven(t, "deliverMessage", name)
			pair, ok := pairs["deliverMessage"][name]
			if !ok {
				t.Fatalf("no deliverMessage pair %s", name)
			}
			body := string(pair.Request.Body)
			if name == "malformed_json" {
				body = "{" // The pair's body is the raw truncated JSON byte, sent as it is.
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/messages/deliver", strings.NewReader(body))
			req.RemoteAddr = "127.0.0.1:12345"
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			routers.Public.ServeHTTP(rr, req)
			assertRequirementResponse(t, pair, rr)
		})
	}
}
