package queue

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// fakeRESTMAILMX starts an SMTP listener that answers EHLO with restmailLine
// (for example "RESTMAIL https://evil.test/restmail") and returns its address.
func fakeRESTMAILMX(t *testing.T, restmailLine string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				r := bufio.NewReader(c)
				fmt.Fprint(c, "220 mx.test ESMTP\r\n")
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
					case strings.HasPrefix(cmd, "EHLO"):
						fmt.Fprintf(c, "250-mx.test\r\n250 %s\r\n", restmailLine)
					case cmd == "QUIT":
						fmt.Fprint(c, "221 bye\r\n")
						return
					default:
						fmt.Fprint(c, "502 not implemented\r\n")
					}
				}
			}(c)
		}
	}()
	return ln.Addr().String()
}

// TestProbeRESTMAILKeywordIsAHint verifies #291: the probe reports only whether
// the MX speaks REST. A bare keyword with no address still counts, and whatever
// address the keyword carries never leaves the probe.
func TestProbeRESTMAILKeywordIsAHint(t *testing.T) {
	w := &Worker{sendDeadline: defaultSendDeadline, allowPrivateDest: true, hostname: "test.local"}
	for _, line := range []string{"RESTMAIL", "RESTMAIL https://attacker.test/restmail"} {
		probed, supported := w.probeRESTMAIL(fakeRESTMAILMX(t, line), "mx.test")
		if !probed || !supported {
			t.Errorf("EHLO %q: probed=%v supported=%v, want both true", line, probed, supported)
		}
	}
}

// restmailTestDB connects to the test database and clears the capability row
// for domain before and after the test.
func restmailTestDB(t *testing.T, domain string) *gorm.DB {
	t.Helper()
	cfg := &config.Config{
		DBHost: envOr("DB_HOST", "localhost"),
		DBPort: envIntOr("DB_PORT", 5432),
		DBName: envOr("DB_NAME", "restmail"),
		DBUser: envOr("DB_USER", "restmail"),
		DBPass: envOr("DB_PASS", "restmail"),
	}
	gdb, err := rmdb.Connect(cfg)
	if err != nil {
		t.Skipf("RESTMAIL endpoint DB test skipped: no database reachable (%v)", err)
	}
	if err := rmdb.AutoMigrate(gdb); err != nil {
		t.Skipf("RESTMAIL endpoint DB test skipped: migrate failed (%v)", err)
	}
	gdb.Where("domain = ?", domain).Delete(&models.RESTMAILCapability{})
	t.Cleanup(func() { gdb.Where("domain = ?", domain).Delete(&models.RESTMAILCapability{}) })
	return gdb
}

// TestTryRESTMAILIgnoresAdvertisedEndpoint is the red-green guard for #291. The
// MX's EHLO advertises an endpoint on a host the attacker controls (an httptest
// TLS server). Before the fix the worker cached that address and POSTed the
// message to it; now the endpoint is asked of the MX host's discovery endpoint,
// so the attacker's server sees nothing and the cache holds no endpoint.
func TestTryRESTMAILIgnoresAdvertisedEndpoint(t *testing.T) {
	const domain = "restmail-291.test"
	gdb := restmailTestDB(t, domain)

	var hits atomic.Int32
	attacker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer attacker.Close()

	// .invalid never resolves (RFC 2606), so discovery at the MX host fails to
	// dial and the test touches nothing real.
	const mxHost = "mx.restmail-291.invalid"

	// tlsInsecure would accept the attacker's self-signed certificate and
	// allowPrivateDest the loopback address, so only the endpoint choice stops it.
	w := &Worker{db: gdb, sendDeadline: defaultSendDeadline, tlsInsecure: true, allowPrivateDest: true, hostname: "test.local"}
	item := testItem()
	item.Domain = domain

	addr := fakeRESTMAILMX(t, "RESTMAIL "+attacker.URL+"/restmail")
	upgraded, err := w.tryRESTMAILAt(addr, restmailDiscoveryURL(mxHost, domain), mxHost, item, nil, false)

	if n := hits.Load(); n != 0 {
		t.Fatalf("worker sent %d request(s) to the endpoint the MX advertised; it must ask the MX host (#291)", n)
	}
	if !upgraded || err == nil {
		t.Fatalf("upgraded=%v err=%v: want a REST attempt at the MX host's discovery endpoint, failing to resolve", upgraded, err)
	}
	if !strings.Contains(err.Error(), mxHost) {
		t.Errorf("error %q does not name the MX host %q", err, mxHost)
	}
	var cap models.RESTMAILCapability
	if err := gdb.Where("domain = ?", domain).First(&cap).Error; err != nil {
		t.Fatalf("capability not cached: %v", err)
	}
	if cap.EndpointURL != "" || cap.MXHost != mxHost || !cap.Supported {
		t.Errorf("cached %+v, want supported for MX %q with no stored endpoint", cap, mxHost)
	}
}

// TestTryRESTMAILDeliversToDiscoveredEndpoint: the message goes to the endpoint
// the verified discovery answer names, which may be another host (#291).
func TestTryRESTMAILDeliversToDiscoveredEndpoint(t *testing.T) {
	const domain = "restmail-291-fleet.test"
	gdb := restmailTestDB(t, domain)

	var posted atomic.Value
	final := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posted.Store(r.Method + " " + r.URL.Path)
		w.WriteHeader(http.StatusCreated)
	}))
	defer final.Close()
	var askedDomain atomic.Value
	discovery, w := discoveryServer(t, func(rw http.ResponseWriter, r *http.Request) {
		askedDomain.Store(r.URL.Query().Get("domain"))
		fmt.Fprintf(rw, `{"endpoint":%q}`, final.URL+"/restmail")
	})
	w.db = gdb
	// Final-hop verification still follows tlsInsecure until it is always on.
	w.tlsInsecure = true
	item := testItem()
	item.Domain = domain

	addr := fakeRESTMAILMX(t, "RESTMAIL https://attacker.test/restmail")
	upgraded, err := w.tryRESTMAILAt(addr, discovery.URL+"/.well-known/restmail?domain="+domain, "mx.test", item, nil, false)
	if !upgraded || err != nil {
		t.Fatalf("upgraded=%v err=%v, want REST delivery", upgraded, err)
	}
	if got, _ := askedDomain.Load().(string); got != domain {
		t.Errorf("discovery asked for domain %q, want %q", got, domain)
	}
	if got, _ := posted.Load().(string); got != "POST /restmail/messages" {
		t.Errorf("final endpoint got %q, want POST /restmail/messages", got)
	}
}

// TestTryRESTMAILVerifiedNotHosted: a verified 404 from discovery means the MX
// does not take REST mail for the domain, so the worker records that and uses
// SMTP without a REST attempt.
func TestTryRESTMAILVerifiedNotHosted(t *testing.T) {
	const domain = "restmail-291-gone.test"
	gdb := restmailTestDB(t, domain)

	discovery, w := discoveryServer(t, func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusNotFound)
	})
	w.db = gdb
	item := testItem()
	item.Domain = domain

	addr := fakeRESTMAILMX(t, "RESTMAIL")
	upgraded, err := w.tryRESTMAILAt(addr, discovery.URL+"/.well-known/restmail?domain="+domain, "mx.test", item, nil, false)
	if upgraded || err != nil {
		t.Fatalf("upgraded=%v err=%v, want no REST attempt", upgraded, err)
	}
	var cap models.RESTMAILCapability
	if err := gdb.Where("domain = ?", domain).First(&cap).Error; err != nil {
		t.Fatalf("capability not cached: %v", err)
	}
	if cap.Supported || cap.MXHost != "mx.test" {
		t.Errorf("cached %+v, want unsupported for mx.test", cap)
	}
}
