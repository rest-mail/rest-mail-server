package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type metricsContractConnection struct {
	capabilityExampleConnection
	queries *atomic.Int64
	fail    bool
}

func (connection metricsContractConnection) Connect(context.Context) (driver.Conn, error) {
	return connection, nil
}

func (connection metricsContractConnection) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	connection.queries.Add(1)
	if connection.fail {
		return nil, errors.New("private metrics database failure")
	}
	switch {
	case strings.Contains(query, `"dkim_selector","dkim_created_at"`) && strings.Contains(query, `FROM "domains"`):
		return &authReadSnapshotRows{
			columns: []string{"id", "name", "dkim_selector", "dkim_created_at", "dkim_rotation_interval_days", "has_key"},
			rows:    [][]driver.Value{{int64(1), "metrics.example.test", "mail", time.Now().Add(-time.Hour), int64(0), true}},
		}, nil
	case strings.Contains(query, `FROM "dkim_rotation_keys"`):
		return &authReadSnapshotRows{
			columns: []string{"domain_id", "slot", "created_at", "published_at", "retire_after", "ttl_seconds"},
		}, nil
	case strings.Contains(query, "FROM certificates GROUP BY domain_id"):
		return &authReadSnapshotRows{
			columns: []string{"min"},
			rows:    [][]driver.Value{{time.Now().Add(30 * 24 * time.Hour)}},
		}, nil
	}
	return nil, errors.New("unexpected query in metrics contract proof")
}

func TestMetricsContractExamplesThroughProductionRouter(t *testing.T) {
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name           string
		pair           string
		remote         string
		allow          string
		proxies        []string
		forwarded      string
		realIP         string
		gatewayOff     bool
		securityOff    bool
		databaseFailed bool
	}{
		{name: "default_loopback", pair: "available", remote: "127.0.0.1:12345"},
		{name: "default_ipv6_loopback", pair: "available", remote: "[::1]:12345"},
		{name: "default_private", pair: "available", remote: "10.1.2.3:12345"},
		{name: "default_ipv6_private", pair: "available", remote: "[fd00::1]:12345"},
		{name: "default_public", pair: "outside_allowlist", remote: "203.0.113.7:12345"},
		{name: "unknown_peer", pair: "outside_allowlist", remote: "not-an-address"},
		{name: "spoofed_forwarded", pair: "outside_allowlist", remote: "203.0.113.7:12345", forwarded: "127.0.0.1"},
		{name: "spoofed_real_ip", pair: "outside_allowlist", remote: "203.0.113.7:12345", realIP: "127.0.0.1"},
		{name: "custom_allowed", pair: "available", remote: "203.0.113.7:12345", allow: "203.0.113.0/24"},
		{name: "custom_denied", pair: "outside_allowlist", remote: "127.0.0.1:12345", allow: "203.0.113.0/24"},
		{name: "gateway_metrics_off_allowed", pair: "available", remote: "127.0.0.1:12345", gatewayOff: true},
		{name: "gateway_metrics_off_denied", pair: "outside_allowlist", remote: "203.0.113.7:12345", gatewayOff: true},
		{name: "gateway_metrics_off_collection_failure", pair: "internal_error", remote: "127.0.0.1:12345", gatewayOff: true, databaseFailed: true},
		{name: "trusted_proxy_allowed", pair: "available", remote: "10.0.0.9:12345", proxies: []string{"10.0.0.0/24"}, forwarded: "127.0.0.1"},
		{name: "trusted_proxy_denied", pair: "outside_allowlist", remote: "10.0.0.9:12345", proxies: []string{"10.0.0.0/24"}, forwarded: "203.0.113.7"},
		{name: "untrusted_proxy_denied", pair: "outside_allowlist", remote: "203.0.113.7:12345", proxies: []string{"10.0.0.0/24"}, forwarded: "127.0.0.1"},
		{name: "security_headers_off", pair: "outside_allowlist", remote: "203.0.113.7:12345", securityOff: true},
		{name: "collection_failure", pair: "internal_error", remote: "127.0.0.1:12345", databaseFailed: true},
		{name: "denied_before_collection_failure", pair: "outside_allowlist", remote: "203.0.113.7:12345", databaseFailed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			pair, ok := pairs["prometheusMetrics"][test.pair]
			if !ok {
				t.Fatal("the named metrics pair is missing")
			}
			examplepairs.Proven(t, "prometheusMetrics", test.pair)
			wantRequirement := map[string]string{
				"available":         "",
				"internal_error":    "fault=database",
				"outside_allowlist": "network=outside_metrics_allowlist",
			}[test.pair]
			if got := requirementOf(pair); got != wantRequirement || (wantRequirement == "" && len(pair.Requires) != 0) {
				t.Fatalf("metrics pair %s requires %q, want %q", test.pair, got, wantRequirement)
			}
			t.Setenv("METRICS_ALLOWED_CIDRS", test.allow)
			var queries atomic.Int64
			pool := sql.OpenDB(metricsContractConnection{queries: &queries, fail: test.databaseFailed})
			t.Cleanup(func() { _ = pool.Close() })
			database, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{
				DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent),
			})
			if err != nil {
				t.Fatal(err)
			}
			cfg := &config.Config{Environment: "test", SecurityHeadersEnabled: !test.securityOff,
				ProxyProtocolTrustedCIDRs: test.proxies,
				SMTPMetricsPort:           config.DefaultGatewayMetricsPort,
				IMAPMetricsPort:           config.DefaultGatewayMetricsPort,
				POP3MetricsPort:           config.DefaultGatewayMetricsPort}
			if test.gatewayOff {
				cfg.SMTPMetricsPort, cfg.IMAPMetricsPort, cfg.POP3MetricsPort = 0, 0, 0
			}
			jwt := auth.NewJWTService(routerTestSecret, time.Minute, time.Hour)
			routers := NewRouters(database, jwt, cfg, nil)
			t.Cleanup(routers.Close)
			request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			request.RemoteAddr = test.remote
			if test.forwarded != "" {
				request.Header.Set("X-Forwarded-For", test.forwarded)
			}
			if test.realIP != "" {
				request.Header.Set("X-Real-IP", test.realIP)
			}
			recorder := httptest.NewRecorder()
			routers.Public.ServeHTTP(recorder, request)
			media, _, err := mime.ParseMediaType(recorder.Header().Get("Content-Type"))
			if err != nil || recorder.Code != pair.Response.Status || media != pair.Response.MediaType {
				t.Fatalf("got %d %q, want %d %q: %v: %s", recorder.Code, recorder.Header().Get("Content-Type"), pair.Response.Status, pair.Response.MediaType, err, recorder.Body.String())
			}
			// Each inventory collector stops at its first failing query.
			wantQueries := int64(2)
			switch test.pair {
			case "available":
				// The DKIM collector reads the domains, then their rotation keys;
				// the certificate expiry collector reads the soonest expiry.
				wantQueries = 3
				if !strings.Contains(recorder.Body.String(), `restmail_dkim_key_age_seconds{domain="metrics.example.test",selector="mail"}`) {
					t.Fatal("the production collector's metric is missing")
				}
				if !strings.Contains(recorder.Body.String(), "\nrestmail_cert_expiry_days 29.") {
					t.Fatal("the certificate expiry metric is missing")
				}
			case "outside_allowlist":
				wantQueries = 0
				var body string
				if err := json.Unmarshal(pair.Response.Body, &body); err != nil {
					t.Fatal(err)
				}
				if recorder.Body.String() != body || recorder.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
					t.Fatalf("network refusal: %v %q, want %q", recorder.Header(), recorder.Body.String(), body)
				}
			case "internal_error":
				assertPublicExampleBody(t, pair.Response.Body, recorder.Body.Bytes())
			}
			if got := queries.Load(); got != wantQueries {
				t.Fatalf("collector queries = %d, want %d", got, wantQueries)
			}
		})
	}
}

func TestMetricsContractDescribesNetworkRefusal(t *testing.T) {
	paths, _ := describedAPI(t)
	operation := paths["/metrics"]["get"]
	if operation.OperationID != "prometheusMetrics" || len(operation.Security) != 0 || len(operation.Responses) != 3 {
		t.Fatalf("metrics must remain credential-free with exactly 200, 404 and 500: %+v", operation)
	}
	for status, media := range map[string]string{"200": "text/plain", "404": "text/plain", "500": "application/json"} {
		content := operation.Responses[status].Content
		if _, ok := content[media]; !ok || len(content) != 1 {
			t.Errorf("metrics %s content = %v, want only %s", status, content, media)
		}
	}
}
