package queue

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// discoveryServer starts a TLS server answering every request with handler and
// returns it with a worker that trusts only its certificate.
func discoveryServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Worker) {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	w := &Worker{sendDeadline: defaultSendDeadline, allowPrivateDest: true, hostname: "test.local", restmailRoots: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}
	return srv, w
}

func TestRESTMAILDiscoveryURL(t *testing.T) {
	for _, mx := range []string{"mx1.example.test", "mx1.example.test."} {
		if got, want := restmailDiscoveryURL(mx, "xn--bcher-kva.example"), "https://mx1.example.test/.well-known/restmail?domain=xn--bcher-kva.example"; got != want {
			t.Errorf("restmailDiscoveryURL(%q) = %q, want %q", mx, got, want)
		}
	}
}

func TestDiscoverRESTMAILEndpoint(t *testing.T) {
	var gotPath, gotDomain string
	srv, w := discoveryServer(t, func(rw http.ResponseWriter, r *http.Request) {
		gotPath, gotDomain = r.URL.Path, r.URL.Query().Get("domain")
		fmt.Fprint(rw, `{"endpoint":"https://fleet-7.example.net/restmail/"}`)
	})
	got, err := w.discoverRESTMAILEndpoint(srv.URL + "/.well-known/restmail?domain=example.test")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if got != "https://fleet-7.example.net/restmail" {
		t.Errorf("endpoint = %q, want the discovered host without trailing slash", got)
	}
	if gotPath != "/.well-known/restmail" || gotDomain != "example.test" {
		t.Errorf("request path=%q domain=%q", gotPath, gotDomain)
	}
}

// TestDiscoverRESTMAILAlwaysVerifiesCertificate: the dev tlsInsecure flag must
// not let an unverified MX answer the discovery question (#291).
func TestDiscoverRESTMAILAlwaysVerifiesCertificate(t *testing.T) {
	srv, w := discoveryServer(t, func(rw http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(rw, `{"endpoint":"https://attacker.test/restmail"}`)
	})
	w.restmailRoots = nil
	w.tlsInsecure = true
	if got, err := w.discoverRESTMAILEndpoint(srv.URL + "/.well-known/restmail?domain=example.test"); err == nil {
		t.Fatalf("discovery from an untrusted certificate returned %q", got)
	}
}

func TestDiscoverRESTMAILStatus(t *testing.T) {
	for _, tc := range []struct {
		status    int
		notHosted bool
	}{
		{http.StatusNotFound, true},
		{http.StatusGone, true},
		{http.StatusFound, false},
		{http.StatusBadRequest, false},
		{http.StatusInternalServerError, false},
		{http.StatusServiceUnavailable, false},
	} {
		srv, w := discoveryServer(t, func(rw http.ResponseWriter, _ *http.Request) {
			rw.Header().Set("Location", "https://attacker.test/.well-known/restmail")
			rw.WriteHeader(tc.status)
		})
		_, err := w.discoverRESTMAILEndpoint(srv.URL + "/.well-known/restmail?domain=example.test")
		if err == nil {
			t.Errorf("status %d: want an error", tc.status)
			continue
		}
		if errors.Is(err, errRESTMAILNotHosted) != tc.notHosted {
			t.Errorf("status %d: err=%v, want not-hosted=%v", tc.status, err, tc.notHosted)
		}
	}
}

func TestDiscoverRESTMAILRejectsBadAnswers(t *testing.T) {
	for _, body := range []string{
		`{"endpoint":"http://mx.example.test/restmail"}`,
		`{"endpoint":"https:///restmail"}`,
		`{"endpoint":"https://user:pw@mx.example.test/restmail"}`,
		`{"endpoint":"https://mx.example.test/restmail?x=1"}`,
		`{"endpoint":"https://mx.example.test/restmail?"}`,
		`{"endpoint":"https://mx.example.test/restmail#x"}`,
		`{"endpoint":"mx.example.test/restmail"}`,
		`{"endpoint":""}`,
		`{}`,
		`not json`,
		`{"endpoint":"https://mx.example.test/restmail","pad":"` + strings.Repeat("x", maxRESTMAILDiscoveryBody) + `"}`,
	} {
		srv, w := discoveryServer(t, func(rw http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(rw, body)
		})
		got, err := w.discoverRESTMAILEndpoint(srv.URL + "/.well-known/restmail?domain=example.test")
		if err == nil {
			t.Errorf("body %.80q: accepted endpoint %q", body, got)
		}
		if errors.Is(err, errRESTMAILNotHosted) {
			t.Errorf("body %.80q: a bad answer must not read as a verified not-hosted", body)
		}
	}
}
