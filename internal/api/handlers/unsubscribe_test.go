package handlers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/bootstrap"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// testOneClickPoster trusts srv's certificate and lets the POST reach srv's own loopback
// address and nothing else the real guard refuses.
func testOneClickPoster(srv *httptest.Server, lookup func(context.Context, string) ([]net.IPAddr, error)) *oneClickPoster {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	allowed := net.ParseIP(srv.Listener.Addr().(*net.TCPAddr).IP.String())
	if lookup == nil {
		lookup = func(context.Context, string) ([]net.IPAddr, error) { return []net.IPAddr{{IP: allowed}}, nil }
	}
	return &oneClickPoster{
		blocked:   func(ip net.IP) bool { return !ip.Equal(allowed) && blockedUnsubscribeIP(ip) },
		lookup:    lookup,
		tlsConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		timeout:   defaultOneClickPoster().timeout,
	}
}

// One request posts List-Unsubscribe=One-Click once, as a form, with no cookie or credential
// (RFC 8058 §3.1).
func TestOneClickPoster_PostsTheRFC8058BodyOnce(t *testing.T) {
	var hits atomic.Int32
	var body, contentType, cookie, authorization string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		raw, _ := io.ReadAll(r.Body)
		body, contentType = string(raw), r.Header.Get("Content-Type")
		cookie, authorization = r.Header.Get("Cookie"), r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	status, err := testOneClickPoster(srv, nil).post(context.Background(), srv.URL+"/u?t=abc")
	if err != nil || status != http.StatusOK {
		t.Fatalf("post = %d, %v", status, err)
	}
	if hits.Load() != 1 {
		t.Fatalf("the target was posted %d times, want once", hits.Load())
	}
	if body != "List-Unsubscribe=One-Click" || contentType != "application/x-www-form-urlencoded" {
		t.Fatalf("body %q, content type %q", body, contentType)
	}
	if cookie != "" || authorization != "" {
		t.Fatalf("cookie %q, authorization %q sent", cookie, authorization)
	}
}

// A name that resolves to a private address is refused before anything is dialed, and so is
// one with a public and a private address.
func TestOneClickPoster_RefusesAPrivateTarget(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port

	for _, addrs := range [][]string{{"10.0.0.5"}, {"93.184.216.34", "192.168.1.1"}, {"169.254.169.254"}, {"::1"}, {"100.64.0.1"}} {
		lookup := func(context.Context, string) ([]net.IPAddr, error) {
			var out []net.IPAddr
			for _, a := range addrs {
				out = append(out, net.IPAddr{IP: net.ParseIP(a)})
			}
			return out, nil
		}
		target := "https://news.example:" + strconv.Itoa(port) + "/u"
		_, err := testOneClickPoster(srv, lookup).post(context.Background(), target)
		if !errors.Is(err, errUnsubscribeBlock) {
			t.Fatalf("%v: err = %v, want the target refused", addrs, err)
		}
	}
	for _, target := range []string{"https://127.0.0.2/u", "https://[fd00::1]/u", "http://" + srv.Listener.Addr().String() + "/u", "https://user:pw@news.example/u"} {
		if _, err := testOneClickPoster(srv, nil).post(context.Background(), target); !errors.Is(err, errUnsubscribeBlock) {
			t.Fatalf("%s: err = %v, want the target refused", target, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("a refused target was reached %d times", hits.Load())
	}
}

// A redirect to a private address is refused, and the private address is never dialed.
func TestOneClickPoster_RefusesARedirectToAPrivateAddress(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://10.0.0.1/internal", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	_, err := testOneClickPoster(srv, nil).post(context.Background(), srv.URL+"/u")
	if !errors.Is(err, errUnsubscribeBlock) {
		t.Fatalf("err = %v, want the redirect refused", err)
	}
}

// The dial hook checks the address actually dialed, so a name that resolved to a public
// address at check time and to a private one at dial time is still refused.
func TestOneClickPoster_DialRefusesPrivateAddresses(t *testing.T) {
	p := defaultOneClickPoster()
	for _, address := range []string{"10.0.0.1:443", "127.0.0.1:443", "[::1]:443", "169.254.169.254:443", "[::ffff:10.0.0.1]:443"} {
		if err := p.dialControl("tcp", address, nil); !errors.Is(err, errUnsubscribeBlock) {
			t.Fatalf("%s: err = %v", address, err)
		}
	}
	if err := p.dialControl("tcp", "93.184.216.34:443", nil); err != nil {
		t.Fatalf("a public address was refused: %v", err)
	}
}

// seedUnsubscribeMessage stores a received message in a mailbox of a domain of its own,
// carrying headers, and answers the handler, the mailbox and the message.
func seedUnsubscribeMessage(t *testing.T, prefix string, headers map[string][]string) (*MessageHandler, models.Mailbox, models.Message) {
	t.Helper()
	h, _, _ := seedSigningDomain(t, prefix)
	domain := fmt.Sprintf("%s-%d.test", prefix, time.Now().UnixNano())
	if err := h.db.Create(&models.Domain{Name: domain}).Error; err != nil {
		t.Fatalf("seed the domain: %v", err)
	}
	mailbox := seedSendingMailbox(t, h.db, domain, "reader")
	if err := h.db.AutoMigrate(accountModels...); err != nil {
		t.Skipf("unsubscribe test skipped: migrate failed (%v)", err)
	}
	if err := rmdb.MigrateAuditLog(h.db); err != nil {
		t.Skipf("unsubscribe test skipped: audit log migrate failed (%v)", err)
	}
	if err := bootstrap.EnsureRBAC(h.db); err != nil {
		t.Fatalf("seed the roles: %v", err)
	}
	// The account owns its mailbox, so it may read its mail and send from it.
	grantOwnerRole(t, h.db, mailbox.ID, mailbox.Address)
	encoded, err := json.Marshal(headers)
	if err != nil {
		t.Fatal(err)
	}
	msg := models.Message{MailboxID: mailbox.ID, Folder: "INBOX", Subject: "weekly news", BodyText: "hello",
		Sender: "news@remote.test", RecipientsTo: models.JSONB(`["reader@` + domain + `"]`), RecipientsCc: models.JSONB(`[]`),
		Headers: models.JSONB(encoded)}
	if err := h.db.Create(&msg).Error; err != nil {
		t.Fatalf("seed the message: %v", err)
	}
	return h, mailbox, msg
}

// A mailto: unsubscribe is sent from the message's account through the send path: it is
// queued to the target with the URI's subject and body, and the URI's to hfield adds no one.
func TestUnsubscribeMailto_SendsThroughTheSendPath(t *testing.T) {
	h, mailbox, msg := seedUnsubscribeMessage(t, "unsubscribe-mailto", map[string][]string{
		"List-Unsubscribe": {"<mailto:leave@remote.test?subject=remove%20me&body=please&to=extra@evil.test>"},
	})
	claims := &auth.Claims{AccountID: mailbox.ID, CredentialKind: "app_password"}
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/messages/%d/unsubscribe/mailto", msg.ID), strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	composeRoute(h)(rr, withClaims(req, claims))
	if rr.Code != http.StatusCreated {
		t.Fatalf("unsubscribe by mailto: %d %s", rr.Code, rr.Body.String())
	}
	var sent models.Message
	if err := json.Unmarshal(dataOf(t, rr), &sent); err != nil {
		t.Fatal(err)
	}
	var queued []models.OutboundQueue
	if err := h.db.Where("message_id = ?", sent.ID).Find(&queued).Error; err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || queued[0].Recipient != "leave@remote.test" || queued[0].Sender != mailbox.Address {
		t.Fatalf("queued = %+v, want one copy from %s to leave@remote.test", queued, mailbox.Address)
	}
	fields := headerFields(queued[0].RawMessage)
	if got := fields["subject"]; len(got) != 1 || got[0] != "remove me" {
		t.Fatalf("Subject = %q", got)
	}
	if !strings.Contains(queued[0].RawMessage, "please") {
		t.Fatalf("the URI's body is not in the message:\n%s", queued[0].RawMessage)
	}

	// The send path's own checks apply: a from the session may not send as is refused.
	req = httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/messages/%d/unsubscribe/mailto", msg.ID), strings.NewReader(`{"from":"someone@elsewhere.test"}`))
	rr = httptest.NewRecorder()
	composeRoute(h)(rr, withClaims(req, claims))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("unsubscribe from a foreign address: %d %s", rr.Code, rr.Body.String())
	}
}

// The options route lists one-click only with List-Unsubscribe-Post, and the one-click route
// refuses a target that resolves to a private address without posting.
func TestUnsubscribeRoutes_OneClick(t *testing.T) {
	h, mailbox, msg := seedUnsubscribeMessage(t, "unsubscribe-oneclick", map[string][]string{
		"List-Unsubscribe":      {"<https://10.0.0.7/u?t=1>, <mailto:leave@remote.test>"},
		"List-Unsubscribe-Post": {"List-Unsubscribe=One-Click"},
	})
	claims := &auth.Claims{AccountID: mailbox.ID, CredentialKind: "app_password"}
	get := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/messages/%d/unsubscribe", msg.ID), nil)
	rr := httptest.NewRecorder()
	composeRoute(h)(rr, withClaims(get, claims))
	if rr.Code != http.StatusOK {
		t.Fatalf("options: %d %s", rr.Code, rr.Body.String())
	}
	var options UnsubscribeOptions
	if err := json.Unmarshal(dataOf(t, rr), &options); err != nil {
		t.Fatal(err)
	}
	if options.OneClick != "https://10.0.0.7/u?t=1" || len(options.Mailto) != 1 || options.Mailto[0].To != "leave@remote.test" {
		t.Fatalf("options = %+v", options)
	}

	post := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/messages/%d/unsubscribe/one-click", msg.ID), nil)
	rr = httptest.NewRecorder()
	composeRoute(h)(rr, withClaims(post, claims))
	if rr.Code != http.StatusUnprocessableEntity || errorCodeOf(rr) != "unsubscribe_target_refused" {
		t.Fatalf("one-click to a private address: %d %s", rr.Code, rr.Body.String())
	}
}
