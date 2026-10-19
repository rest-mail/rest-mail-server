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

func TestRESTMAILEndpointForMX(t *testing.T) {
	for _, mx := range []string{"mx1.example.test", "mx1.example.test."} {
		if got, want := restmailEndpointForMX(mx), "https://mx1.example.test/restmail"; got != want {
			t.Errorf("restmailEndpointForMX(%q) = %q, want %q", mx, got, want)
		}
	}
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

// TestTryRESTMAILIgnoresAdvertisedEndpoint is the red-green guard for #291. The
// MX's EHLO advertises an endpoint on a host the attacker controls (an httptest
// TLS server). Before the fix the worker cached that address and POSTed the
// message to it; now the endpoint is derived from the MX host name, so the
// attacker's server sees nothing and the cache holds the derived endpoint.
func TestTryRESTMAILIgnoresAdvertisedEndpoint(t *testing.T) {
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

	var hits atomic.Int32
	attacker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer attacker.Close()

	const domain = "restmail-291.test"
	// .invalid never resolves (RFC 2606), so the derived endpoint fails to dial
	// and the test touches nothing real.
	const mxHost = "mx.restmail-291.invalid"
	gdb.Where("domain = ?", domain).Delete(&models.RESTMAILCapability{})
	t.Cleanup(func() { gdb.Where("domain = ?", domain).Delete(&models.RESTMAILCapability{}) })

	// tlsInsecure would accept the attacker's self-signed certificate and
	// allowPrivateDest the loopback address, so only the endpoint choice stops it.
	w := &Worker{db: gdb, sendDeadline: defaultSendDeadline, tlsInsecure: true, allowPrivateDest: true, hostname: "test.local"}
	item := testItem()
	item.Domain = domain

	addr := fakeRESTMAILMX(t, "RESTMAIL "+attacker.URL+"/restmail")
	upgraded, err := w.tryRESTMAILAt(addr, mxHost, item, nil, false)

	if n := hits.Load(); n != 0 {
		t.Fatalf("worker POSTed %d time(s) to the endpoint the MX advertised; it must derive it from the MX host (#291)", n)
	}
	if !upgraded || err == nil {
		t.Fatalf("upgraded=%v err=%v: want a REST attempt at the derived endpoint, failing to resolve", upgraded, err)
	}
	if !strings.Contains(err.Error(), mxHost) {
		t.Errorf("error %q does not name the derived endpoint host %q", err, mxHost)
	}
	var cap models.RESTMAILCapability
	if err := gdb.Where("domain = ?", domain).First(&cap).Error; err != nil {
		t.Fatalf("capability not cached: %v", err)
	}
	if want := restmailEndpointForMX(mxHost); cap.EndpointURL != want || cap.MXHost != mxHost || !cap.Supported {
		t.Errorf("cached %+v, want supported endpoint %q for MX %q", cap, want, mxHost)
	}
}
