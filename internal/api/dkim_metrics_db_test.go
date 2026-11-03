package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

func TestMetrics_DKIMAgeSharesTheExistingEndpoint(t *testing.T) {
	database := createScratchDB(t, "dkim_metrics")
	if err := database.AutoMigrate(&models.Domain{}, &models.DKIMRotationKey{}, &models.Certificate{}); err != nil {
		t.Fatal(err)
	}
	created := time.Now().Add(-time.Hour)
	domain := models.Domain{
		Name: "metric-route.test", DKIMSelector: "mail",
		DKIMPrivateKey: "private-route-sentinel", DKIMCreatedAt: &created, DKIMRotationIntervalDays: 90,
	}
	if err := database.Create(&domain).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&models.DKIMRotationKey{
		DomainID: domain.ID, Slot: models.DKIMRotationStaged, Selector: "next",
		PrivateKey: "private-staged-sentinel", TTLSeconds: 3600, CreatedAt: created,
	}).Error; err != nil {
		t.Fatal(err)
	}
	// The certificate expiry gauge is served from the same registry (#1630).
	if err := database.Create(&models.Certificate{
		DomainID: domain.ID, CertPEM: "-", KeyPEM: "-", Issuer: "test",
		NotBefore: created, NotAfter: time.Now().Add(3 * 24 * time.Hour),
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
		`restmail_dkim_rotation_interval_seconds{domain="metric-route.test"}`,
		`restmail_dkim_rotation_state{domain="metric-route.test",state="awaiting_publication"} 1`,
		`restmail_dkim_rotation_step_started_seconds{domain="metric-route.test"}`,
		"restmail_queue_size",
		"# TYPE restmail_cert_expiry_days gauge",
		"\nrestmail_cert_expiry_days 2.9",
	} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Errorf("scrape missing %s", want)
		}
	}
	if strings.Contains(rr.Body.String(), "private-route-sentinel") || strings.Contains(rr.Body.String(), "private-staged-sentinel") {
		t.Fatal("scrape exposed private key material")
	}

	req.RemoteAddr = "203.0.113.10:12345"
	rr = httptest.NewRecorder()
	routers.Public.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("outside allowlist: status %d, want 404", rr.Code)
	}
}
