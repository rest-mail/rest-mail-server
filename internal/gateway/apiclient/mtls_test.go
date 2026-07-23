package apiclient

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/restmail/restmail/internal/mtls"
	"github.com/restmail/restmail/internal/mtls/mtlstest"
)

// newInternalMTLSServer starts an httptest server that requires a verified
// internal client certificate and serves only the two machine routes. It
// returns the server plus the CA + client keypair paths.
func newInternalMTLSServer(t *testing.T) (*httptest.Server, *mtlstest.Paths) {
	t.Helper()
	dir := t.TempDir()
	p, err := mtlstest.Write(dir, "127.0.0.1")
	if err != nil {
		t.Fatalf("write mTLS material: %v", err)
	}
	serverCfg, err := mtls.ServerTLSConfig(p.CACert, p.ServerCert, p.ServerKey)
	if err != nil {
		t.Fatalf("ServerTLSConfig: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/mailboxes", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `{"data":{"exists":true,"mailbox_id":7,"address":"a@b.test"}}`)
	})
	mux.HandleFunc("/api/v1/messages/deliver", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `{"data":{"id":9,"mailbox_id":7,"subject":"hi"}}`)
	})
	ts := httptest.NewUnstartedServer(mux)
	ts.TLS = serverCfg
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return ts, p
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

// TestClient_TwoListeners_PerEndpointRouting is the regression test for the
// outage: routing is PER-ENDPOINT. With internal mTLS enabled the two tokenless
// machine routes must go to the internal (mTLS) listener, while Login and the
// Bearer-token user routes must keep going to the PUBLIC listener. The old
// whole-base-URL switch sent everything to the 2-route internal listener → 404
// on every user route → IMAP/POP3 retrieval and SMTP submission broke.
func TestClient_TwoListeners_PerEndpointRouting(t *testing.T) {
	dir := t.TempDir()
	p, err := mtlstest.Write(dir, "127.0.0.1")
	if err != nil {
		t.Fatalf("write mTLS material: %v", err)
	}

	// PUBLIC listener (plain http): Login + Bearer-token user routes.
	pub := http.NewServeMux()
	pub.HandleFunc("/api/v1/auth/login", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `{"data":{"access_token":"tok-123","expires_in":900,"user":{"id":1,"email":"u@e.test","display_name":"U"}}}`)
	})
	pub.HandleFunc("/api/v1/accounts/1/folders/INBOX/messages", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `{"data":[{"id":1,"mailbox_id":1,"folder":"INBOX","subject":"hi"}],"pagination":{"cursor":"","has_more":false,"total":1}}`)
	})
	pub.HandleFunc("/api/v1/messages/1", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `{"data":{"id":1,"mailbox_id":1,"subject":"hi","body_text":"yo"}}`)
	})
	pubTS := httptest.NewServer(pub)
	defer pubTS.Close()

	// INTERNAL listener (mTLS): only the two machine routes.
	internal := http.NewServeMux()
	internal.HandleFunc("/api/mailboxes", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `{"data":{"exists":true,"mailbox_id":7,"address":"a@b.test"}}`)
	})
	internal.HandleFunc("/api/v1/messages/deliver", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `{"data":{"id":9,"mailbox_id":7,"subject":"hi"}}`)
	})
	serverCfg, err := mtls.ServerTLSConfig(p.CACert, p.ServerCert, p.ServerKey)
	if err != nil {
		t.Fatalf("ServerTLSConfig: %v", err)
	}
	intTS := httptest.NewUnstartedServer(internal)
	intTS.TLS = serverCfg
	intTS.StartTLS()
	defer intTS.Close()

	clientTLS, err := mtls.ClientTLSConfig(p.CACert, p.ClientCert, p.ClientKey)
	if err != nil {
		t.Fatalf("ClientTLSConfig: %v", err)
	}
	c := New(pubTS.URL, WithInternalMTLS(intTS.URL, clientTLS))

	// PUBLIC routes must succeed against the public listener.
	login, err := c.Login("u@e.test", "pw")
	if err != nil {
		t.Fatalf("Login (public) failed: %v", err)
	}
	if login.Data.AccessToken != "tok-123" {
		t.Fatalf("Login token = %q, want tok-123", login.Data.AccessToken)
	}
	msgs, err := c.ListMessages("tok-123", 1, "INBOX")
	if err != nil {
		t.Fatalf("ListMessages (public) failed: %v", err)
	}
	if len(msgs.Data) != 1 || msgs.Data[0].ID != 1 {
		t.Fatalf("ListMessages unexpected: %+v", msgs.Data)
	}
	det, err := c.GetMessage("tok-123", 1)
	if err != nil {
		t.Fatalf("GetMessage (public) failed: %v", err)
	}
	if det.Data.ID != 1 {
		t.Fatalf("GetMessage id = %d, want 1", det.Data.ID)
	}

	// MACHINE routes must succeed over the internal mTLS listener.
	mb, err := c.CheckMailbox("a@b.test")
	if err != nil {
		t.Fatalf("CheckMailbox (internal mTLS) failed: %v", err)
	}
	if !mb.Data.Exists || mb.Data.MailboxID != 7 {
		t.Fatalf("CheckMailbox unexpected: %+v", mb.Data)
	}
	dv, err := c.DeliverMessage(&DeliverRequest{Address: "a@b.test", Sender: "s@x.test", Subject: "hi", BodyText: "yo"})
	if err != nil {
		t.Fatalf("DeliverMessage (internal mTLS) failed: %v", err)
	}
	if dv.Data.ID != 9 {
		t.Fatalf("DeliverMessage id = %d, want 9", dv.Data.ID)
	}

	// Proof of the split: a user route is NOT served on the internal listener,
	// so routing user traffic there (the old bug) would 404.
	raw := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS}}
	resp, err := raw.Get(intTS.URL + "/api/v1/messages/1")
	if err != nil {
		t.Fatalf("raw internal GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("internal listener served a user route (status %d) — it must only serve the 2 machine routes", resp.StatusCode)
	}
}

// TestClient_DefaultOff_SingleListener proves default-off behavior: with no
// WithInternalMTLS option, the machine routes use the same public client/base
// URL as everything else (no second destination).
func TestClient_DefaultOff_SingleListener(t *testing.T) {
	var gotPaths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		switch r.URL.Path {
		case "/api/mailboxes":
			writeJSON(w, `{"data":{"exists":true,"mailbox_id":1,"address":"a@b.test"}}`)
		case "/api/v1/auth/login":
			writeJSON(w, `{"data":{"access_token":"t"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	c := New(ts.URL)
	if _, err := c.CheckMailbox("a@b.test"); err != nil {
		t.Fatalf("CheckMailbox default-off failed: %v", err)
	}
	if _, err := c.Login("u@e.test", "pw"); err != nil {
		t.Fatalf("Login default-off failed: %v", err)
	}
	// Both hit the single server.
	if len(gotPaths) != 2 {
		t.Fatalf("expected both calls on one server, got paths %v", gotPaths)
	}
}

// TestClient_InternalMTLS_MachineRoutesSucceed is the focused happy-path for the
// internal mTLS destination.
func TestClient_InternalMTLS_MachineRoutesSucceed(t *testing.T) {
	ts, p := newInternalMTLSServer(t)
	clientTLS, err := mtls.ClientTLSConfig(p.CACert, p.ClientCert, p.ClientKey)
	if err != nil {
		t.Fatalf("ClientTLSConfig: %v", err)
	}
	c := New(ts.URL, WithInternalMTLS(ts.URL, clientTLS))

	resp, err := c.CheckMailbox("a@b.test")
	if err != nil {
		t.Fatalf("CheckMailbox with client cert failed: %v", err)
	}
	if !resp.Data.Exists || resp.Data.MailboxID != 7 {
		t.Fatalf("unexpected response: %+v", resp.Data)
	}
}

// TestClient_WithoutClientCert_Rejected proves that an internal destination
// which trusts the server CA but presents NO client certificate is refused.
func TestClient_WithoutClientCert_Rejected(t *testing.T) {
	ts, p := newInternalMTLSServer(t)

	pool := x509.NewCertPool()
	caPEM, err := os.ReadFile(p.CACert)
	if err != nil {
		t.Fatalf("read CA: %v", err)
	}
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("append CA")
	}
	// Trusts the server, but offers no client certificate.
	c := New(ts.URL, WithInternalMTLS(ts.URL, &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}))

	if _, err := c.CheckMailbox("a@b.test"); err == nil {
		t.Fatal("expected CheckMailbox to fail without a client certificate, got nil error")
	}
}
