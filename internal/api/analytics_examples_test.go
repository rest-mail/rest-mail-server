package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"github.com/restmail/restmail/internal/examplepairs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/restmail/restmail/internal/auth"
	"github.com/restmail/restmail/internal/config"
)

const analyticsExamplePath = "/api/v1/admin/pipelines/analytics"

type analyticsExampleConnection struct{ capabilityExampleConnection }

func (c analyticsExampleConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c analyticsExampleConnection) Driver() driver.Driver                        { return c }
func (c analyticsExampleConnection) Open(string) (driver.Conn, error)             { return c, nil }
func (c analyticsExampleConnection) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(strings.ToLower(query), "from pipeline_rollups") {
		return nil, errors.New("unexpected query in analytics fixture")
	}
	return &authReadSnapshotRows{columns: []string{"metric_name", "labels", "total"}}, nil
}

func analyticsExampleRouters(t *testing.T) (http.Handler, http.Handler, http.Handler, *auth.JWTService) {
	t.Helper()
	// A controlled empty live snapshot complements the empty durable rollups.
	// Neither the analytics handler nor its clock is replaced.
	gatherer := prometheus.DefaultGatherer
	prometheus.DefaultGatherer = prometheus.NewRegistry()
	t.Cleanup(func() { prometheus.DefaultGatherer = gatherer })
	unavailable, failed, _, jwt := authReadBatchRouters(t)
	pool := sql.OpenDB(analyticsExampleConnection{})
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	routers := NewRouters(db, jwt, &config.Config{Environment: "test", APIMaxBodyBytes: 8}, nil)
	t.Cleanup(routers.Close)
	return unavailable, failed, routers.Public, jwt
}

func TestAnalyticsExamplesMatchRegisteredResponses(t *testing.T) {
	unavailable, failed, available, jwt := analyticsExampleRouters(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	token := mailboxToken(t, jwt)
	pending, err := jwt.GenerateSession(10, "user@example.com", 0, []string{auth.AMRPassword}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		status int
		token  string
		router http.Handler
	}{
		{"available", 200, token, available},
		{"unauthenticated", 401, "", unavailable},
		{"setup_required", 403, pending.AccessToken, failed},
		{"request_too_large", 413, token, failed},
		{"internal_error", 500, token, failed},
		{"session_unavailable", 503, token, unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pair, ok := pairs["getPipelineAnalytics"][tc.name]
			if !ok {
				t.Fatal("named analytics pair is missing")
			}
			query := url.Values{}
			for name, value := range pair.Request.Query {
				query.Set(name, value.(string))
			}
			req := httptest.NewRequest(http.MethodGet, analyticsExamplePath+"?"+query.Encode(), nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			if tc.status == 413 {
				req.ContentLength = 9
			}
			before := time.Now().UTC()
			rr := httptest.NewRecorder()
			tc.router.ServeHTTP(rr, req)
			after := time.Now().UTC()
			if rr.Code != tc.status || rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
				t.Fatalf("unexpected response %d: %s", rr.Code, rr.Body.String())
			}
			actual := rr.Body.Bytes()
			if tc.status == 200 {
				var want, got map[string]interface{}
				if err := json.Unmarshal(pair.Response.Body, &want); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(actual, &got); err != nil {
					t.Fatal(err)
				}
				window := got["data"].(map[string]interface{})["window"].(map[string]interface{})
				since, err := time.Parse(time.RFC3339Nano, window["since"].(string))
				if err != nil {
					t.Fatal(err)
				}
				until, err := time.Parse(time.RFC3339Nano, window["until"].(string))
				if err != nil {
					t.Fatal(err)
				}
				if until.Before(before) || until.After(after) || until.Sub(since) != time.Hour {
					t.Fatalf("window does not cover the requested hour at the actual request time: %v", window)
				}
				// Verify the moving timestamps before comparing the rest exactly.
				illustration := want["data"].(map[string]interface{})["window"].(map[string]interface{})
				window["since"], window["until"] = illustration["since"], illustration["until"]
				actual, err = json.Marshal(got)
				if err != nil {
					t.Fatal(err)
				}
			}
			assertPublicExampleBody(t, pair.Response.Body, actual)
		})
	}
	// Fallback remains the existing runtime policy. Strings that are not valid
	// durations or dates still bind and reach the handler, rather than a422.
	req := httptest.NewRequest(http.MethodGet, analyticsExamplePath+"?since=bad&window=bad", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	available.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("fallback changed: %d %s", rr.Code, rr.Body.String())
	}
	for _, warning := range describedRouters(t).generator("analytics examples").Report() {
		if warning.OperationID == "getPipelineAnalytics" {
			t.Errorf("analytics still has a generator warning: %v", warning)
		}
	}
	paths, _ := describedAPI(t)
	if _, present := paths[analyticsExamplePath]["get"].Responses["422"]; present {
		t.Fatal("infallible string binding still declares422")
	}
}

func TestAnalyticsExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for the real analytics boundary check")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	unavailable, failed, available, jwt := analyticsExampleRouters(t)
	token := mailboxToken(t, jwt)
	pending, err := jwt.GenerateSession(10, "user@example.com", 0, []string{auth.AMRPassword}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		status, token string
		router        http.Handler
	}{
		{"200", token, available}, {"401", "", unavailable}, {"403", pending.AccessToken, failed},
		{"413", token, failed}, {"500", token, failed}, {"503", token, unavailable},
	} {
		t.Run(tc.status, func(t *testing.T) {
			checkAuthReadWithVertrag(t, binary, tc.router, tc.token, tc.status, []string{analyticsExamplePath})
		})
	}
}
