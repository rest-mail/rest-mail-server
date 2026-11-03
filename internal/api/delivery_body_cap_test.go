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

// deliveryRoutes are the two routes that carry the delivery body cap (#660).
var deliveryRoutes = []struct{ operationID, path string }{
	{"deliverMessage", "/api/v1/messages/deliver"},
	{"restmailDeliver", "/restmail/messages"},
}

// deliveryRouter is the production public router with the given SMTP max message size,
// the setting the delivery body cap is sized from.
func deliveryRouter(t *testing.T, smtpMax int64) (http.Handler, int64) {
	t.Helper()
	jwtSvc := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	cfg := &config.Config{Environment: "test", SMTPMaxMessageSize: smtpMax}
	routers := NewRouters(newFailingGormDB(t), jwtSvc, cfg, nil)
	t.Cleanup(routers.Close)
	return routers.Public, cfg.InternalDeliveryBodyLimit()
}

// postDelivery sends size bytes of JSON whitespace to path, with the length declared or
// not. Whitespace makes the handler's decoder read the whole body before it fails, so a
// body over the cap always reaches the cap's reader.
func postDelivery(router http.Handler, path string, size int64, declared bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(bytes.Repeat([]byte(" "), int(size))))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:12345"
	if !declared {
		req.ContentLength = -1
	}
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

type deliveryRefusal struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func refusedForSize(rr *httptest.ResponseRecorder) bool {
	var refusal deliveryRefusal
	_ = json.Unmarshal(rr.Body.Bytes(), &refusal)
	return rr.Code == http.StatusRequestEntityTooLarge || refusal.Error.Code == "request_too_large"
}

// The gateway deliver route and the RESTMAIL deliver route carry the delivery body cap
// (#660). Each declares the 413, and through the production router a body exactly at the
// cap is not refused for its size, while one byte over gets the documented answer: refused
// on its declared length before a byte is read, and refused on the bytes read when the
// length is not declared.
func TestDeliveryRoutes_BodyCapIsDeclaredAndAnswered(t *testing.T) {
	router, limit := deliveryRouter(t, 1024)

	paths, _ := describedAPI(t)
	for _, route := range deliveryRoutes {
		op, ok := paths[route.path]["post"]
		if !ok || op.OperationID != route.operationID {
			t.Fatalf("POST %s is not described as %s", route.path, route.operationID)
		}
		if _, ok := op.Responses["413"]; !ok {
			t.Errorf("%s declares no 413 for its body cap", route.operationID)
		}

		for _, declared := range []bool{true, false} {
			if rr := postDelivery(router, route.path, limit, declared); refusedForSize(rr) {
				t.Errorf("%s at the cap (length declared: %v) was refused for size: %d %s", route.operationID, declared, rr.Code, rr.Body.String())
			}

			rr := postDelivery(router, route.path, limit+1, declared)
			if rr.Code != http.StatusRequestEntityTooLarge {
				t.Errorf("%s over the cap (length declared: %v): got %d, want 413: %s", route.operationID, declared, rr.Code, rr.Body.String())
				continue
			}
			var refusal deliveryRefusal
			if err := json.Unmarshal(rr.Body.Bytes(), &refusal); err != nil {
				t.Fatalf("%s: the 413 is not the error envelope: %v: %s", route.operationID, err, rr.Body.String())
			}
			if refusal.Error.Code != "request_too_large" || refusal.Error.Message != "Request body too large" {
				t.Errorf("%s 413 (length declared: %v): got %+v, want request_too_large", route.operationID, declared, refusal.Error)
			}
		}
	}
}

// The delivery body cap is read from the configured SMTP max message size: a body one
// byte over the cap for a small max is not refused for size by a router whose max is
// larger.
func TestDeliveryRoutes_BodyCapFollowsSMTPMaxMessageSize(t *testing.T) {
	small, smallLimit := deliveryRouter(t, 1024)
	large, largeLimit := deliveryRouter(t, 2048)
	if largeLimit <= smallLimit+1 {
		t.Fatalf("a larger SMTP max gave no larger delivery cap: %d vs %d", largeLimit, smallLimit)
	}

	for _, route := range deliveryRoutes {
		for _, declared := range []bool{true, false} {
			if rr := postDelivery(small, route.path, smallLimit+1, declared); rr.Code != http.StatusRequestEntityTooLarge {
				t.Errorf("%s with the small max (length declared: %v): got %d, want 413", route.operationID, declared, rr.Code)
			}
			if rr := postDelivery(large, route.path, smallLimit+1, declared); refusedForSize(rr) {
				t.Errorf("%s with the large max (length declared: %v) refused a body under its cap: %d %s", route.operationID, declared, rr.Code, rr.Body.String())
			}
		}
	}
}
