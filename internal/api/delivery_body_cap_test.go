package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
)

// The gateway deliver route and the RESTMAIL deliver route carry the delivery body cap
// (#660). Each declares the 413, and through the production router a body over the cap
// gets that answer: refused on its declared length before a byte is read, and refused
// on the bytes read when the length is not declared.
func TestDeliveryRoutes_BodyCapIsDeclaredAndAnswered(t *testing.T) {
	jwtSvc := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	cfg := &config.Config{Environment: "test", SMTPMaxMessageSize: 1024}
	routers := NewRouters(newFailingGormDB(t), jwtSvc, cfg, nil)
	t.Cleanup(routers.Close)
	over := bytes.Repeat([]byte(" "), int(cfg.InternalDeliveryBodyLimit())+1)

	paths, _ := describedAPI(t)
	for _, route := range []struct{ operationID, path string }{
		{"deliverMessage", "/api/v1/messages/deliver"},
		{"restmailDeliver", "/restmail/messages"},
	} {
		op, ok := paths[route.path]["post"]
		if !ok || op.OperationID != route.operationID {
			t.Fatalf("POST %s is not described as %s", route.path, route.operationID)
		}
		if _, ok := op.Responses["413"]; !ok {
			t.Errorf("%s declares no 413 for its body cap", route.operationID)
		}

		for _, declared := range []bool{true, false} {
			req := httptest.NewRequest(http.MethodPost, route.path, bytes.NewReader(over))
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = "127.0.0.1:12345"
			if !declared {
				req.ContentLength = -1
			}
			rr := httptest.NewRecorder()
			routers.Public.ServeHTTP(rr, req)
			if rr.Code != http.StatusRequestEntityTooLarge {
				t.Errorf("%s over the cap (length declared: %v): got %d, want 413: %s", route.operationID, declared, rr.Code, rr.Body.String())
				continue
			}
			var refusal struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &refusal); err != nil {
				t.Fatalf("%s: the 413 is not the error envelope: %v: %s", route.operationID, err, rr.Body.String())
			}
			if refusal.Error.Code != "request_too_large" || refusal.Error.Message != "Request body too large" {
				t.Errorf("%s 413 (length declared: %v): got %+v, want request_too_large", route.operationID, declared, refusal.Error)
			}
		}
	}
}
