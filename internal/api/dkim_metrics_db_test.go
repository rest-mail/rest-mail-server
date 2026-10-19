package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
	"github.com/restmail/restmail/internal/db/models"
)

func TestMetrics_DKIMAgeSharesTheExistingEndpoint(t *testing.T) {
	database := createScratchDB(t, "dkim_metrics")
	if err := database.AutoMigrate(&models.Domain{}); err != nil {
		t.Fatal(err)
	}
	created := time.Now().Add(-time.Hour)
	if err := database.Create(&models.Domain{
		Name: "metric-route.test", DKIMSelector: "mail",
		DKIMPrivateKey: "private-route-sentinel", DKIMCreatedAt: &created,
	}).Error; err != nil {
		t.Fatal(err)
	}
	jwt := auth.NewJWTService(routerTestSecret, 5*time.Minute, 24*time.Hour)
	routers := NewRouters(database, jwt, &config.Config{Environment: "test"}, nil)
	t.Cleanup(routers.Close)
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	routers.Public.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("scrape status %d: %s", rr.Code, rr.Body.String())
	}
	for _, want := range []string{
		`restmail_dkim_key_age_seconds{domain="metric-route.test",selector="mail"}`,
		"# TYPE restmail_dkim_key_age_seconds gauge",
		"restmail_queue_size",
	} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Errorf("scrape missing %s", want)
		}
	}
	if strings.Contains(rr.Body.String(), "private-route-sentinel") {
		t.Fatal("scrape exposed private key material")
	}

	req.RemoteAddr = "203.0.113.10:12345"
	rr = httptest.NewRecorder()
	routers.Public.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("outside allowlist: status %d, want 404", rr.Code)
	}
}
