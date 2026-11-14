package api

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"gorm.io/gorm"

	acmeclient "github.com/rest-mail/rest-mail-server/internal/acme"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/bootstrap"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/mtls/mtlstest"
)

// The configuration matrix (#661): REST Mail's own wiring of a feature, in the
// configurations it applies to. The components have their own suites; these checks
// fail when an environment variable stops reaching the router, or a feature is wired
// to the wrong listener or state.
//
// Each named configuration is an environment overlay read by the real config.Load,
// held to what cmd/api refuses to start on, and served by the real production routers.
// A check's env adds the setting that row turns on or off. This covers the rows that
// live at the router. It does not run the topology overlays or Vertrag in each
// configuration, and it says nothing on which routes the served description lists.

const (
	matrixJWTSecret   = "configuration-matrix-jwt-secret-0123456789abcdef"
	matrixMasterKey   = "configuration-matrix-master-key-0123456789abcdef"
	matrixPushTopic   = "test.restmail.configuration-matrix"
	matrixPublicPeer  = "203.0.113.7:12345"
	matrixGatewayHost = "mail.matrix-gateway.test"
)

// matrixVariables are cleared before each overlay, so a setting in the test's own
// environment cannot decide a check. DB_* stay: the scratch databases read them.
var matrixVariables = []string{
	"ACME_EMAIL", "ACME_ENABLED",
	"API_TLS_TERMINATED_BY_PROXY",
	"AUTH_RATE_LIMIT_BURST", "AUTH_RATE_LIMIT_ENABLED", "AUTH_RATE_LIMIT_RPS",
	"DB_SSLMODE", "ENVIRONMENT", "GATEWAY_HOSTNAME",
	"INTERNAL_MTLS_CA_CERT", "INTERNAL_MTLS_CLIENT_CERT", "INTERNAL_MTLS_CLIENT_KEY", "INTERNAL_MTLS_ENABLED",
	"INTERNAL_MTLS_SERVER_CERT", "INTERNAL_MTLS_SERVER_KEY", "INTERNAL_ROUTES_ALLOW_PUBLIC",
	"JS_FILTER_TOKEN", "JS_FILTER_URL", "JWT_SECRET", "MASTER_KEY", "METRICS_ALLOWED_CIDRS",
	"PIPELINE_TEST_RATE_LIMIT_BURST", "PIPELINE_TEST_RATE_LIMIT_ENABLED", "PIPELINE_TEST_RATE_LIMIT_RPS",
	"PROXY_PROTOCOL_TRUSTED_CIDRS",
	"RESTMAIL_APNS_KEY_FILE", "RESTMAIL_APNS_KEY_ID", "RESTMAIL_APNS_TEAM_ID", "RESTMAIL_APNS_TOPIC",
	"RESTMAIL_OWNER_KEYS", "RESTMAIL_STATUS_KEYS", "SECURITY_HEADERS_ENABLED",
	"TLSRPT_RATE_LIMIT_BURST", "TLSRPT_RATE_LIMIT_ENABLED", "TLSRPT_RATE_LIMIT_RPS",
	"TOTP_2FA_ENABLED",
}

func matrixMTLSEnvironment(t *testing.T) map[string]string {
	t.Helper()
	paths, err := mtlstest.Write(t.TempDir(), "127.0.0.1")
	if err != nil {
		t.Fatalf("write mTLS material: %v", err)
	}
	return map[string]string{
		"INTERNAL_MTLS_ENABLED":     "true",
		"INTERNAL_MTLS_CA_CERT":     paths.CACert,
		"INTERNAL_MTLS_SERVER_CERT": paths.ServerCert,
		"INTERNAL_MTLS_SERVER_KEY":  paths.ServerKey,
		"INTERNAL_MTLS_CLIENT_CERT": paths.ClientCert,
		"INTERNAL_MTLS_CLIENT_KEY":  paths.ClientKey,
	}
}

func matrixAPNsEnvironment(t *testing.T) map[string]string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "matrix-apns.p8")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"RESTMAIL_APNS_KEY_FILE": keyFile,
		"RESTMAIL_APNS_KEY_ID":   "MATRIXKEY1",
		"RESTMAIL_APNS_TEAM_ID":  "MATRIXTEAM",
		"RESTMAIL_APNS_TOPIC":    matrixPushTopic,
	}
}

// namedConfigurations are #661's five, each a small overlay on an unset environment.
var namedConfigurations = map[string]func(t *testing.T) map[string]string{
	"default": func(*testing.T) map[string]string {
		return map[string]string{
			"ENVIRONMENT": "development", "JWT_SECRET": matrixJWTSecret, "MASTER_KEY": matrixMasterKey,
			"GATEWAY_HOSTNAME": matrixGatewayHost,
		}
	},
	"internal-mtls": func(t *testing.T) map[string]string {
		env := matrixMTLSEnvironment(t)
		env["ENVIRONMENT"], env["JWT_SECRET"], env["MASTER_KEY"] = "development", matrixJWTSecret, matrixMasterKey
		env["GATEWAY_HOSTNAME"] = matrixGatewayHost
		return env
	},
	"production": func(t *testing.T) map[string]string {
		env := matrixMTLSEnvironment(t)
		env["ENVIRONMENT"], env["JWT_SECRET"], env["MASTER_KEY"] = "production", matrixJWTSecret, matrixMasterKey
		env["GATEWAY_HOSTNAME"] = matrixGatewayHost
		env["API_TLS_TERMINATED_BY_PROXY"] = "true"
		env["DB_SSLMODE"] = "require"
		return env
	},
	"push": func(t *testing.T) map[string]string {
		env := matrixAPNsEnvironment(t)
		env["ENVIRONMENT"], env["JWT_SECRET"], env["MASTER_KEY"] = "development", matrixJWTSecret, matrixMasterKey
		env["GATEWAY_HOSTNAME"] = matrixGatewayHost
		return env
	},
	// No master key, no sidecar, no scanners and two-factor off. Nothing listens
	// on port 1, so the sidecar is refused at once rather than after a DNS wait.
	"minimal": func(*testing.T) map[string]string {
		return map[string]string{
			"ENVIRONMENT": "development", "JWT_SECRET": matrixJWTSecret,
			"TOTP_2FA_ENABLED": "false", "JS_FILTER_URL": "http://127.0.0.1:1",
		}
	},
}

// matrixServer is one configuration as the API process builds it.
type matrixServer struct {
	cfg     *config.Config
	routers *Routers
	jwt     *auth.JWTService
	db      *gorm.DB
}

func startMatrixConfiguration(t *testing.T, name string, env map[string]string, database func(*testing.T) *gorm.DB) *matrixServer {
	t.Helper()
	overlay, ok := namedConfigurations[name]
	if !ok {
		t.Fatalf("no named configuration %q", name)
	}
	if database == nil {
		database = newFailingGormDB
	}
	// Before the overlay: the scratch database is reached through the test's own DB_*.
	db := database(t)
	for _, variable := range matrixVariables {
		t.Setenv(variable, "") // restores the value when the test ends
		if err := os.Unsetenv(variable); err != nil {
			t.Fatal(err)
		}
	}
	for _, values := range []map[string]string{overlay(t), env} {
		for variable, value := range values {
			t.Setenv(variable, value)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	// What cmd/api checks before it builds the routers.
	if err := cfg.ValidateListenerSecurity(config.RoleAPI); err != nil {
		t.Fatalf("the API would refuse to start: %v", err)
	}
	if err := CheckPush(cfg); err != nil {
		t.Fatalf("the API would refuse to start: %v", err)
	}
	if _, err := bootstrap.ParseStatusKeys(cfg.StatusKeys); err != nil {
		t.Fatalf("the API would refuse to start: %v", err)
	}
	var acme *acmeclient.Client
	if cfg.ACMEEnabled {
		acme = acmeclient.NewClient(acmeclient.ClientConfig{DB: db, MasterKey: cfg.MasterKey, Email: cfg.ACMEEmail, Directory: cfg.ACMEDirectory, Staging: cfg.ACMEStaging})
	}
	jwt := auth.NewJWTService(cfg.JWTSecret, cfg.JWTAccessExpiry, cfg.JWTRefreshExpiry)
	routers := NewRouters(db, jwt, cfg, nil, acme)
	t.Cleanup(routers.Close)
	return &matrixServer{cfg: cfg, routers: routers, jwt: jwt, db: db}
}

func (s *matrixServer) do(method, path, token, body string) *httptest.ResponseRecorder {
	return doRequest(s.routers.Public, method, path, token, body)
}

// from sends a request from peer, with a forwarded client when one is named.
func (s *matrixServer) from(peer, forwarded, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.RemoteAddr = peer
	if forwarded != "" {
		request.Header.Set("X-Forwarded-For", forwarded)
	}
	recorder := httptest.NewRecorder()
	s.routers.Public.ServeHTTP(recorder, request)
	return recorder
}

func (s *matrixServer) admin(t *testing.T) string {
	t.Helper()
	return adminToken(t, s.jwt, []string{"*"})
}

func matrixWantStatus(t *testing.T, rr *httptest.ResponseRecorder, want int, what string) {
	t.Helper()
	if rr.Code != want {
		t.Errorf("%s: got %d, want %d: %s", what, rr.Code, want, rr.Body)
	}
}

// matrixWantReached fails when a routing or authorization answer stood in for the handler's.
// The handler's own answer depends on the check's database, so it is not named here.
func matrixWantReached(t *testing.T, rr *httptest.ResponseRecorder, what string) {
	t.Helper()
	switch rr.Code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed:
		t.Errorf("%s: got %d, want the handler's answer: %s", what, rr.Code, rr.Body)
	}
}

func matrixWantAbsent(t *testing.T, rr *httptest.ResponseRecorder, what string) {
	t.Helper()
	if rr.Code != http.StatusNotFound && rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("%s: got %d, want no such route: %s", what, rr.Code, rr.Body)
	}
}

var matrixSecurityHeaders = []string{"Strict-Transport-Security", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy", "Content-Security-Policy"}

// matrixHeaderRequests answer from the handler, the router's own 404, and the
// metrics gate's 404, which comes before most of the middleware.
func matrixHeaderRequests(s *matrixServer) map[string]*httptest.ResponseRecorder {
	return map[string]*httptest.ResponseRecorder{
		"handler":     s.from(matrixPublicPeer, "", "/api/health"),
		"no route":    s.from(matrixPublicPeer, "", "/no-such-route"),
		"metrics 404": s.from(matrixPublicPeer, "", "/metrics"),
	}
}

func matrixCheckSecurityHeadersOn(t *testing.T, s *matrixServer) {
	for what, rr := range matrixHeaderRequests(s) {
		for _, header := range matrixSecurityHeaders {
			if rr.Header().Get(header) == "" {
				t.Errorf("%s: %s is missing", what, header)
			}
		}
	}
}

func matrixCheckSecurityHeadersOff(t *testing.T, s *matrixServer) {
	for what, rr := range matrixHeaderRequests(s) {
		for _, header := range matrixSecurityHeaders {
			// net/http's Error sets nosniff itself on the two 404s, whatever the
			// configuration says; only the handler's answer can show it off.
			if header == "X-Content-Type-Options" && what != "handler" {
				continue
			}
			if got := rr.Header().Get(header); got != "" {
				t.Errorf("%s: %s = %q, want none", what, header, got)
			}
		}
	}
}

func matrixDeviceBody(appID string) string {
	return `{"platform":"apns","token":"` + strings.Repeat("ab", 32) + `","app_id":"` + appID + `"}`
}

func matrixCheckPushOff(t *testing.T, s *matrixServer) {
	matrixWantStatus(t, s.do(http.MethodPost, "/api/v1/devices", s.admin(t), matrixDeviceBody(matrixPushTopic)), http.StatusNotFound, "POST /api/v1/devices")
	matrixWantStatus(t, s.do(http.MethodGet, "/api/v1/devices", s.admin(t), ""), http.StatusNotFound, "GET /api/v1/devices")
}

func matrixCheckPushOn(t *testing.T, s *matrixServer) {
	matrixWantStatus(t, s.do(http.MethodPost, "/api/v1/devices", "", matrixDeviceBody(matrixPushTopic)), http.StatusUnauthorized, "signed out")
	// The handler holds RESTMAIL_APNS_TOPIC: another app is refused before the failing
	// database, and this server's own goes past that check to it.
	other := s.do(http.MethodPost, "/api/v1/devices", s.admin(t), matrixDeviceBody("com.example.other"))
	matrixWantStatus(t, other, http.StatusUnprocessableEntity, "another app's id")
	if !strings.Contains(other.Body.String(), matrixPushTopic) {
		t.Errorf("another app's id: the refusal does not name %s: %s", matrixPushTopic, other.Body)
	}
	matrixWantStatus(t, s.do(http.MethodPost, "/api/v1/devices", s.admin(t), matrixDeviceBody(matrixPushTopic)), http.StatusInternalServerError, "this server's app id, on a failing database")
}

const matrixChallengePath = "/.well-known/acme-challenge/matrix-token"

func matrixCheckACMEOff(t *testing.T, s *matrixServer) {
	matrixWantStatus(t, s.do(http.MethodGet, matrixChallengePath, "", ""), http.StatusNotFound, "GET")
	// With no route at all, POST is a plain 404 too: the route's own answer would be 405.
	matrixWantStatus(t, s.do(http.MethodPost, matrixChallengePath, "", ""), http.StatusNotFound, "POST")
}

func matrixCheckACMEOn(t *testing.T, s *matrixServer) {
	matrixWantStatus(t, s.do(http.MethodGet, matrixChallengePath, "", ""), http.StatusNotFound, "an unknown token")
	matrixWantStatus(t, s.do(http.MethodPost, matrixChallengePath, "", ""), http.StatusMethodNotAllowed, "POST to the registered route")
}

func matrixCheckInternalRoutesOnPublic(t *testing.T, s *matrixServer) {
	if s.routers.Internal != nil {
		t.Error("an internal handler exists with internal mTLS off")
	}
	matrixWantReached(t, s.do(http.MethodGet, "/api/mailboxes?address=a@matrix.test", "", ""), "check-address on the public router")
	matrixWantReached(t, s.do(http.MethodPost, "/api/v1/messages/deliver", "", `{}`), "deliver on the public router")
}

// matrixCheckInternalRoutesBehindMTLS serves the internal handler as cmd/api does, on the
// server TLS configuration the environment names.
func matrixCheckInternalRoutesBehindMTLS(t *testing.T, s *matrixServer) {
	matrixWantAbsent(t, s.do(http.MethodGet, "/api/mailboxes?address=a@matrix.test", "", ""), "check-address on the public router")
	matrixWantAbsent(t, s.do(http.MethodPost, "/api/v1/messages/deliver", "", `{}`), "deliver on the public router")
	if s.routers.Internal == nil {
		t.Fatal("no internal handler with internal mTLS on")
	}
	serverTLS, err := s.cfg.InternalMTLSServerTLS()
	if err != nil {
		t.Fatalf("the API would refuse to start: %v", err)
	}
	server := httptest.NewUnstartedServer(s.routers.Internal)
	server.TLS = serverTLS
	server.StartTLS()
	t.Cleanup(server.Close)

	clientTLS, err := s.cfg.InternalMTLSClientTLS()
	if err != nil {
		t.Fatal(err)
	}
	gateway := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS}, Timeout: 30 * time.Second}
	t.Cleanup(gateway.CloseIdleConnections)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/mailboxes?address=a@matrix.test", ""},
		{http.MethodPost, "/api/v1/messages/deliver", `{}`},
	} {
		request, err := http.NewRequest(tc.method, server.URL+tc.path, strings.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := gateway.Do(request)
		if err != nil {
			t.Fatalf("%s %s with the gateway's certificate: %v", tc.method, tc.path, err)
		}
		if err := response.Body.Close(); err != nil {
			t.Error(err)
		}
		switch response.StatusCode {
		case http.StatusUnauthorized, http.StatusNotFound, http.StatusMethodNotAllowed:
			t.Errorf("%s %s with the gateway's certificate: got %d, want the handler's answer", tc.method, tc.path, response.StatusCode)
		}
	}

	noCertificate := clientTLS.Clone()
	noCertificate.Certificates = nil
	noCertificate.GetClientCertificate = nil
	stranger := &http.Client{Transport: &http.Transport{TLSClientConfig: noCertificate}, Timeout: 10 * time.Second}
	t.Cleanup(stranger.CloseIdleConnections)
	if response, err := stranger.Get(server.URL + "/api/mailboxes?address=a@matrix.test"); err == nil {
		if err := response.Body.Close(); err != nil {
			t.Error(err)
		}
		t.Errorf("no client certificate: got %d, want a refused handshake", response.StatusCode)
	}
}

const matrixEnrollPath = "/api/v1/auth/2fa/enroll"

func matrixCheckTwoFactorOff(t *testing.T, s *matrixServer) {
	rr := s.do(http.MethodPost, matrixEnrollPath, s.admin(t), "")
	matrixWantStatus(t, rr, http.StatusForbidden, "enroll")
	if !strings.Contains(rr.Body.String(), "disabled on this server") {
		t.Errorf("enroll: the refusal does not say two-factor is off: %s", rr.Body)
	}
}

func matrixCheckTwoFactorNoMasterKey(t *testing.T, s *matrixServer) {
	matrixWantStatus(t, s.do(http.MethodPost, matrixEnrollPath, s.admin(t), ""), http.StatusServiceUnavailable, "enroll")
}

// matrixCheckTwoFactorOn: past both gates the handler reads the account, and this check's
// database fails. The lifecycle on a real database is TestOwnTwoFactorExamples.
func matrixCheckTwoFactorOn(t *testing.T, s *matrixServer) {
	matrixWantStatus(t, s.do(http.MethodPost, matrixEnrollPath, s.admin(t), ""), http.StatusInternalServerError, "enroll")
}

// matrixTestOperations are the seven test endpoints, each sent what its handler answers
// quickly: a refused body where it has one, the failing database where it does not.
var matrixTestOperations = []struct{ method, path, body string }{
	{http.MethodPost, "/api/v1/admin/test/send", `{}`},
	{http.MethodGet, "/api/v1/admin/test/verify", ""},
	{http.MethodPost, "/api/v1/admin/test/probe", `{"services":["db"]}`},
	{http.MethodPost, "/api/v1/admin/test/reset", `{}`},
	{http.MethodPost, "/api/v1/admin/test/seed", `{}`},
	{http.MethodPost, "/api/v1/admin/test/snapshot", `{}`},
	{http.MethodPost, "/api/v1/admin/test/snapshot/restore", `{}`},
}

// matrixCheckTestEndpointsAbsent: production omits them even for the wildcard capability. #652's
// 2026-10-26 clarification supersedes the 403 #661's table still says.
func matrixCheckTestEndpointsAbsent(t *testing.T, s *matrixServer) {
	for _, operation := range matrixTestOperations {
		matrixWantStatus(t, s.do(operation.method, operation.path, s.admin(t), operation.body), http.StatusNotFound, operation.method+" "+operation.path)
	}
}

func matrixCheckTestEndpointsPresent(t *testing.T, s *matrixServer) {
	for _, operation := range matrixTestOperations {
		matrixWantReached(t, s.do(operation.method, operation.path, s.admin(t), operation.body), operation.method+" "+operation.path)
	}
}

// matrixThrottles are the API's *_RATE_LIMIT_* families, with a request each one counts.
var matrixThrottles = []struct {
	prefix, method, path string
	admin                bool
}{
	{"AUTH", http.MethodPost, "/api/v1/auth/login", false},
	{"PIPELINE_TEST", http.MethodPost, "/api/v1/admin/pipelines/test", true},
	{"TLSRPT", http.MethodPost, "/.well-known/smtp-tlsrpt", false},
}

func matrixThrottleEnvironment(enabled string) map[string]string {
	env := map[string]string{}
	for _, throttle := range matrixThrottles {
		env[throttle.prefix+"_RATE_LIMIT_ENABLED"] = enabled
		env[throttle.prefix+"_RATE_LIMIT_BURST"] = "2"
		env[throttle.prefix+"_RATE_LIMIT_RPS"] = "0.0001"
	}
	return env
}

func matrixCheckThrottles(want bool) func(*testing.T, *matrixServer) {
	return func(t *testing.T, s *matrixServer) {
		for _, throttle := range matrixThrottles {
			token := ""
			if throttle.admin {
				token = s.admin(t)
			}
			throttled := false
			for range 6 {
				throttled = throttled || s.do(throttle.method, throttle.path, token, `{}`).Code == http.StatusTooManyRequests
			}
			if throttled != want {
				t.Errorf("%s: six requests past a burst of 2: throttled = %v, want %v", throttle.prefix, throttled, want)
			}
		}
	}
}

const matrixDiscoveryPath = "/.well-known/restmail?domain=matrix.test"

func matrixCheckDiscoveryUnconfigured(t *testing.T, s *matrixServer) {
	matrixWantStatus(t, s.do(http.MethodGet, matrixDiscoveryPath, "", ""), http.StatusServiceUnavailable, "discovery")
}

// matrixCheckDiscoveryAnswers: the endpoint is the configured hostname, never the request's Host.
func matrixCheckDiscoveryAnswers(t *testing.T, s *matrixServer) {
	if err := s.db.Create(&models.Domain{Name: "matrix.test", Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, matrixDiscoveryPath, nil)
	request.Host = "attacker.example"
	recorder := httptest.NewRecorder()
	s.routers.Public.ServeHTTP(recorder, request)
	matrixWantStatus(t, recorder, http.StatusOK, "a hosted domain")
	var got struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, recorder.Body)
	}
	if want := "https://" + matrixGatewayHost + "/restmail"; got.Endpoint != want {
		t.Errorf("endpoint = %q, want %q", got.Endpoint, want)
	}
}

const (
	matrixValidatePath  = "/api/v1/admin/custom-filters/validate"
	matrixValidateBody  = `{"script":"function filter(e){return e}"}`
	matrixSidecarToken  = "configuration-matrix-sidecar-token"
	matrixTokenRefusal  = "JS_FILTER_TOKEN"
	matrixSidecarAnswer = `"valid":true`
)

// matrixSidecar answers only the token the deployment's sidecar holds. It stands in for
// the sidecar, whose own checks are its suite's: what is tested is what the API sends.
func matrixSidecar(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+matrixSidecarToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"valid":true}`))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func matrixCheckSidecarAccepts(t *testing.T, s *matrixServer) {
	rr := s.do(http.MethodPost, matrixValidatePath, s.admin(t), matrixValidateBody)
	matrixWantStatus(t, rr, http.StatusOK, "validate")
	if !strings.Contains(rr.Body.String(), matrixSidecarAnswer) {
		t.Errorf("validate: not the sidecar's verdict: %s", rr.Body)
	}
}

func matrixCheckSidecarRefusesToken(t *testing.T, s *matrixServer) {
	rr := s.do(http.MethodPost, matrixValidatePath, s.admin(t), matrixValidateBody)
	matrixWantStatus(t, rr, http.StatusServiceUnavailable, "validate")
	if !strings.Contains(rr.Body.String(), matrixTokenRefusal) {
		t.Errorf("validate: the refusal does not name %s: %s", matrixTokenRefusal, rr.Body)
	}
}

func matrixCheckSidecarUnreachable(t *testing.T, s *matrixServer) {
	matrixWantStatus(t, s.do(http.MethodPost, matrixValidatePath, s.admin(t), matrixValidateBody), http.StatusServiceUnavailable, "validate")
}

func matrixMetricsDB(t *testing.T) *gorm.DB {
	t.Helper()
	var queries atomic.Int64
	var connector driver.Connector = metricsContractConnection{queries: &queries}
	return requirementDB(t, connector)
}

func matrixCheckMetricsDefaultAllowlist(t *testing.T, s *matrixServer) {
	matrixWantStatus(t, s.from("127.0.0.1:12345", "", "/metrics"), http.StatusOK, "loopback")
	matrixWantStatus(t, s.from(matrixPublicPeer, "", "/metrics"), http.StatusNotFound, "a public peer")
	matrixWantStatus(t, s.from(matrixPublicPeer, "127.0.0.1", "/metrics"), http.StatusNotFound, "a public peer naming loopback")
}

func matrixCheckMetricsConfiguredAllowlist(t *testing.T, s *matrixServer) {
	matrixWantStatus(t, s.from(matrixPublicPeer, "", "/metrics"), http.StatusOK, "a listed peer")
	matrixWantStatus(t, s.from("127.0.0.1:12345", "", "/metrics"), http.StatusNotFound, "loopback, now unlisted")
	matrixWantStatus(t, s.from("10.0.0.9:12345", "203.0.113.7", "/metrics"), http.StatusOK, "a trusted proxy for a listed client")
	matrixWantStatus(t, s.from("10.0.0.9:12345", "198.51.100.9", "/metrics"), http.StatusNotFound, "a trusted proxy for an unlisted client")
	matrixWantStatus(t, s.from("198.51.100.9:12345", "203.0.113.7", "/metrics"), http.StatusNotFound, "an untrusted peer naming a listed client")
}

const (
	matrixChallengeRoute = "/api/v1/auth/owner/challenge"
	matrixStatusRoute    = "/api/v1/auth/owner/status"
)

// matrixKey is an SSH key as RESTMAIL_OWNER_KEYS and RESTMAIL_STATUS_KEYS list it, and
// the private half that signs with it.
func matrixKey(t *testing.T, seed byte) (authorized string, signer ssh.Signer) {
	t.Helper()
	key := ed25519.NewKeyFromSeed(append(make([]byte, ed25519.SeedSize-1), seed))
	signer, err := ssh.NewSignerFromSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), signer
}

// matrixStatusAnswer signs a fresh challenge for the status and sends it.
func matrixStatusAnswer(t *testing.T, s *matrixServer, signer ssh.Signer) *httptest.ResponseRecorder {
	t.Helper()
	rr := s.do(http.MethodPost, matrixChallengeRoute, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("challenge: got %d: %s", rr.Code, rr.Body)
	}
	var issued struct {
		Data struct {
			Challenge string `json:"challenge"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &issued); err != nil {
		t.Fatalf("%v: %s", err, rr.Body)
	}
	signature, err := signer.Sign(rand.Reader, bootstrap.StatusMessage(issued.Data.Challenge))
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{
		"challenge":  issued.Data.Challenge,
		"public_key": string(ssh.MarshalAuthorizedKey(signer.PublicKey())),
		"signature":  base64.StdEncoding.EncodeToString(ssh.Marshal(signature)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return s.do(http.MethodPost, matrixStatusRoute, "", string(body))
}

func matrixNoKeysDB(t *testing.T) *gorm.DB {
	t.Helper()
	return requirementDB(t, ownerNoKeysConnection{})
}

func matrixCheckNoOwnerKeys(t *testing.T, s *matrixServer) {
	rr := s.do(http.MethodPost, matrixChallengeRoute, "", "")
	matrixWantStatus(t, rr, http.StatusConflict, "challenge")
	if !strings.Contains(rr.Body.String(), "no_owner_keys") {
		t.Errorf("challenge: not no_owner_keys: %s", rr.Body)
	}
}

func matrixCheckStatusKey(t *testing.T, s *matrixServer) {
	_, listed := matrixKey(t, 1)
	_, unlisted := matrixKey(t, 2)
	matrixWantStatus(t, matrixStatusAnswer(t, s, listed), http.StatusOK, "a listed status key")
	matrixWantStatus(t, matrixStatusAnswer(t, s, unlisted), http.StatusForbidden, "an unlisted key")
}

// matrixCheckOwnerKey registers RESTMAIL_OWNER_KEYS as cmd/api does before it serves.
func matrixCheckOwnerKey(t *testing.T, s *matrixServer) {
	if err := bootstrap.SeedOwnerKeys(s.db, s.cfg.OwnerKeys); err != nil {
		t.Fatalf("the API would refuse to start: %v", err)
	}
	matrixCheckStatusKey(t, s)
}

func matrixScratchDB(t *testing.T) *gorm.DB {
	t.Helper()
	return openCredentialScratchDB(t)
}

func matrixEnv(env map[string]string) func(*testing.T) map[string]string {
	return func(*testing.T) map[string]string { return env }
}

// configurationChecks is the matrix: #661's row, the configurations it applies to, and
// a variant's setting when the named configuration alone does not reach it.
var configurationChecks = []struct {
	row            string
	variant        string
	configurations []string
	env            func(*testing.T) map[string]string
	database       func(*testing.T) *gorm.DB
	check          func(*testing.T, *matrixServer)
}{
	{row: "push devices", variant: "off", configurations: []string{"default", "internal-mtls", "production", "minimal"}, check: matrixCheckPushOff},
	{row: "push devices", variant: "on", configurations: []string{"push"}, check: matrixCheckPushOn},

	{row: "acme", variant: "off", configurations: []string{"default", "production", "minimal"}, check: matrixCheckACMEOff},
	{row: "acme", variant: "on", configurations: []string{"default", "production"},
		env: matrixEnv(map[string]string{"ACME_ENABLED": "true", "ACME_EMAIL": "acme@matrix.test"}), check: matrixCheckACMEOn},

	{row: "internal listener", variant: "off", configurations: []string{"default", "push", "minimal"}, check: matrixCheckInternalRoutesOnPublic},
	{row: "internal listener", variant: "on", configurations: []string{"internal-mtls", "production"}, check: matrixCheckInternalRoutesBehindMTLS},

	{row: "two-factor", variant: "off", configurations: []string{"minimal"}, check: matrixCheckTwoFactorOff},
	{row: "two-factor", variant: "on", configurations: []string{"default", "internal-mtls", "production", "push"}, check: matrixCheckTwoFactorOn},
	{row: "two-factor", variant: "no master key", configurations: []string{"default"},
		env: matrixEnv(map[string]string{"MASTER_KEY": ""}), check: matrixCheckTwoFactorNoMasterKey},

	{row: "production lock", variant: "locked", configurations: []string{"production"}, check: matrixCheckTestEndpointsAbsent},
	{row: "production lock", variant: "open", configurations: []string{"default", "internal-mtls", "push", "minimal"}, check: matrixCheckTestEndpointsPresent},

	{row: "security headers", variant: "on", configurations: []string{"default", "internal-mtls", "production", "push", "minimal"}, check: matrixCheckSecurityHeadersOn},
	{row: "security headers", variant: "off", configurations: []string{"default", "production"},
		env: matrixEnv(map[string]string{"SECURITY_HEADERS_ENABLED": "false"}), check: matrixCheckSecurityHeadersOff},

	{row: "rate limits", variant: "on", configurations: []string{"default", "production"}, env: matrixEnv(matrixThrottleEnvironment("true")), check: matrixCheckThrottles(true)},
	{row: "rate limits", variant: "off", configurations: []string{"default", "production"}, env: matrixEnv(matrixThrottleEnvironment("false")), check: matrixCheckThrottles(false)},

	{row: "restmail discovery", variant: "configured", configurations: []string{"default", "production"}, database: matrixScratchDB, check: matrixCheckDiscoveryAnswers},
	{row: "restmail discovery", variant: "unset", configurations: []string{"minimal"}, check: matrixCheckDiscoveryUnconfigured},
	{row: "restmail discovery", variant: "empty", configurations: []string{"default"},
		env: matrixEnv(map[string]string{"GATEWAY_HOSTNAME": ""}), check: matrixCheckDiscoveryUnconfigured},

	{row: "javascript filters", variant: "right token", configurations: []string{"default", "production"},
		env: func(t *testing.T) map[string]string {
			return map[string]string{"JS_FILTER_URL": matrixSidecar(t), "JS_FILTER_TOKEN": matrixSidecarToken}
		}, check: matrixCheckSidecarAccepts},
	{row: "javascript filters", variant: "wrong token", configurations: []string{"default"},
		env: func(t *testing.T) map[string]string {
			return map[string]string{"JS_FILTER_URL": matrixSidecar(t), "JS_FILTER_TOKEN": "not-the-token"}
		}, check: matrixCheckSidecarRefusesToken},
	{row: "javascript filters", variant: "no sidecar", configurations: []string{"minimal"}, check: matrixCheckSidecarUnreachable},

	{row: "metrics and trusted proxies", variant: "default allowlist", configurations: []string{"default", "production"}, database: matrixMetricsDB, check: matrixCheckMetricsDefaultAllowlist},
	{row: "metrics and trusted proxies", variant: "configured", configurations: []string{"default", "production"}, database: matrixMetricsDB,
		env: matrixEnv(map[string]string{"METRICS_ALLOWED_CIDRS": "203.0.113.0/24", "PROXY_PROTOCOL_TRUSTED_CIDRS": "10.0.0.0/24"}), check: matrixCheckMetricsConfiguredAllowlist},

	{row: "owner and status keys", variant: "no keys", configurations: []string{"default", "minimal"}, database: matrixNoKeysDB, check: matrixCheckNoOwnerKeys},
	{row: "owner and status keys", variant: "status key", configurations: []string{"default", "production"}, database: matrixScratchDB,
		env: func(t *testing.T) map[string]string {
			listed, _ := matrixKey(t, 1)
			return map[string]string{"RESTMAIL_STATUS_KEYS": listed}
		}, check: matrixCheckStatusKey},
	{row: "owner and status keys", variant: "owner key", configurations: []string{"default", "production"}, database: matrixScratchDB,
		env: func(t *testing.T) map[string]string {
			listed, _ := matrixKey(t, 1)
			return map[string]string{"RESTMAIL_OWNER_KEYS": listed}
		}, check: matrixCheckOwnerKey},
}

func TestConfigurationMatrix(t *testing.T) {
	for _, entry := range configurationChecks {
		for _, name := range entry.configurations {
			t.Run(entry.row+"/"+entry.variant+"/"+name, func(t *testing.T) {
				var env map[string]string
				if entry.env != nil {
					env = entry.env(t)
				}
				entry.check(t, startMatrixConfiguration(t, name, env, entry.database))
			})
		}
	}
}

// A named configuration no check runs in, or a check in a configuration nobody named,
// would look covered and not be.
func TestConfigurationMatrixRunsInEveryNamedConfiguration(t *testing.T) {
	ran := map[string]bool{}
	for _, entry := range configurationChecks {
		for _, name := range entry.configurations {
			if _, ok := namedConfigurations[name]; !ok {
				t.Errorf("%s/%s runs in %q, which is not a named configuration", entry.row, entry.variant, name)
			}
			ran[name] = true
		}
	}
	for name := range namedConfigurations {
		if !ran[name] {
			t.Errorf("no check runs in the %s configuration", name)
		}
	}
}
