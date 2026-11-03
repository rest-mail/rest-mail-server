package api

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/db/repositories"
)

// The gateways' connection sessions (#1586): a gateway, known by its mTLS
// client certificate on the internal listener, opens a session when a mail
// client signs in, and every call for that connection carries its token.

type gatewayFixture struct {
	*credentialFixture
	internal http.Handler
	app      createdCredential
}

func newGatewayFixture(t *testing.T) *gatewayFixture {
	t.Helper()
	f := newCredentialFixtureWith(t, &config.Config{Environment: "test", InternalMTLSEnabled: true})
	routers := NewRouters(f.db, f.jwt, &config.Config{Environment: "test", InternalMTLSEnabled: true}, nil)
	t.Cleanup(routers.Close)
	if routers.Internal == nil {
		t.Fatal("no internal router with mTLS enabled")
	}
	f.router = routers.Public
	return &gatewayFixture{credentialFixture: f, internal: routers.Internal, app: f.createAppPassword("Mail app")}
}

// as sends a request to the internal listener as the gateway whose client
// certificate is cn ("" for none: the request has no TLS at all).
func (g *gatewayFixture) as(cn, method, path, authorization, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = "192.0.2.20:50000"
	if cn != "" {
		// Only the verified chain and the leaf's name are read past the TLS
		// layer, which the listener itself enforces.
		leaf := &x509.Certificate{Subject: pkix.Name{CommonName: cn}}
		req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}}}
	}
	rr := httptest.NewRecorder()
	g.internal.ServeHTTP(rr, req)
	return rr
}

const (
	imapCN = "rest-mail-imap-gateway"
	pop3CN = "rest-mail-pop3-gateway"
)

func (g *gatewayFixture) open(cn, protocol string) string {
	g.t.Helper()
	rr := g.as(cn, http.MethodPost, "/api/v1/gateway-sessions", basic(g.address, g.app.Password), fmt.Sprintf(`{"protocol":%q}`, protocol))
	if rr.Code != http.StatusCreated {
		g.t.Fatalf("open a session: %d %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Data struct {
			Token        string `json:"token"`
			CredentialID uint   `json:"credential_id"`
		} `json:"data"`
	}
	decodeInto(g.t, rr, &resp)
	if !strings.HasPrefix(resp.Data.Token, "rmgs_") || resp.Data.CredentialID != g.app.ID {
		g.t.Fatalf("open answered %s", rr.Body.String())
	}
	return resp.Data.Token
}

func (g *gatewayFixture) folders(cn, token string) int {
	return g.as(cn, http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d/folders", g.accountID), "Bearer "+token, "").Code
}

func TestGatewaySessionOpenUseClose(t *testing.T) {
	g := newGatewayFixture(t)
	token := g.open(imapCN, "imap")
	if code := g.folders(imapCN, token); code != http.StatusOK {
		t.Fatalf("folders in the session = %d, want 200", code)
	}
	if rr := g.as(imapCN, http.MethodDelete, "/api/v1/gateway-sessions/current", "Bearer "+token, ""); rr.Code != http.StatusNoContent {
		t.Fatalf("close = %d %s", rr.Code, rr.Body.String())
	}
	if code := g.folders(imapCN, token); code != http.StatusUnauthorized {
		t.Fatalf("folders after close = %d, want 401", code)
	}
	// A second close is not an error.
	if rr := g.as(imapCN, http.MethodDelete, "/api/v1/gateway-sessions/current", "Bearer "+token, ""); rr.Code != http.StatusNoContent {
		t.Fatalf("second close = %d", rr.Code)
	}
}

func TestGatewaySessionEndsWhenTheAppPasswordIsRevoked(t *testing.T) {
	g := newGatewayFixture(t)
	token := g.open(imapCN, "imap")
	if code := g.folders(imapCN, token); code != http.StatusOK {
		t.Fatalf("folders = %d", code)
	}
	if err := repositories.NewCredentialRepository(g.db).Revoke(g.accountID, g.app.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if code := g.folders(imapCN, token); code != http.StatusUnauthorized {
		t.Fatalf("folders after revoking the app password = %d, want 401", code)
	}
	var s models.GatewaySession
	if err := g.db.Take(&s).Error; err != nil || s.ClosedAt == nil {
		t.Fatalf("the session was not closed: %v %+v", err, s)
	}
}

func TestGatewaySessionEndsWhenTheAccountsSessionsAreRevoked(t *testing.T) {
	g := newGatewayFixture(t)
	token := g.open(imapCN, "imap")
	if err := repositories.NewRefreshTokenRepository(g.db).RevokeAllForAccount(g.accountID); err != nil {
		t.Fatal(err)
	}
	if code := g.folders(imapCN, token); code != http.StatusUnauthorized {
		t.Fatalf("folders after revoking the account's sessions = %d, want 401", code)
	}
}

func TestGatewaySessionOnlyForItsOwnService(t *testing.T) {
	g := newGatewayFixture(t)
	token := g.open(imapCN, "imap")
	if code := g.folders(pop3CN, token); code != http.StatusUnauthorized {
		t.Errorf("folders with the POP3 gateway's certificate = %d, want 401", code)
	}
	if code := g.folders("someone-else", token); code != http.StatusUnauthorized {
		t.Errorf("folders with a certificate that names no gateway = %d, want 401", code)
	}
	// Nor from the public listener, where no certificate is ever seen.
	if rr := g.do(http.MethodGet, fmt.Sprintf("/api/v1/accounts/%d/folders", g.accountID), "Bearer "+token, ""); rr.Code != http.StatusUnauthorized {
		t.Errorf("folders on the public listener = %d, want 401", rr.Code)
	}
	// The session is still the IMAP gateway's.
	if code := g.folders(imapCN, token); code != http.StatusOK {
		t.Errorf("folders as the IMAP gateway = %d, want 200", code)
	}
}

func TestGatewaySessionOpenIsTheGatewaysOwnProtocol(t *testing.T) {
	g := newGatewayFixture(t)
	if rr := g.as(imapCN, http.MethodPost, "/api/v1/gateway-sessions", basic(g.address, g.app.Password), `{"protocol":"pop3"}`); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("IMAP gateway opening a POP3 session = %d %s, want 422", rr.Code, rr.Body.String())
	}
	if rr := g.as("rest-mail-smtp-delivery", http.MethodPost, "/api/v1/gateway-sessions", basic(g.address, g.app.Password), `{"protocol":"smtp"}`); rr.Code != http.StatusForbidden {
		t.Errorf("SMTP delivery opening a session = %d %s, want 403", rr.Code, rr.Body.String())
	}
	if rr := g.as(imapCN, http.MethodPost, "/api/v1/gateway-sessions", basic(g.address, "wrong"), `{"protocol":"imap"}`); rr.Code != http.StatusUnauthorized {
		t.Errorf("a wrong password = %d, want 401", rr.Code)
	}
}

// IDLE can hold a connection for hours: its session does not expire while in
// use, and only one left idle past GatewaySessionIdle is closed.
func TestGatewaySessionSurvivesALongIdle(t *testing.T) {
	g := newGatewayFixture(t)
	token := g.open(imapCN, "imap")
	if err := g.db.Model(&models.GatewaySession{}).Where("1 = 1").Update("last_used_at", time.Now().Add(-3*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if code := g.folders(imapCN, token); code != http.StatusOK {
		t.Fatalf("folders after three quiet hours = %d, want 200", code)
	}
	if err := g.db.Model(&models.GatewaySession{}).Where("1 = 1").Update("last_used_at", time.Now().Add(-models.GatewaySessionIdle-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if code := g.folders(imapCN, token); code != http.StatusUnauthorized {
		t.Fatalf("folders after the idle limit = %d, want 401", code)
	}
}
