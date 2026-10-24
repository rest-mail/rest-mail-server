package nxdomain

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// fakeDNS is a DNS server on a local UDP port that answers each name with
// the response code answers gives it, NXDOMAIN when it has none. Before the
// answer it sends one with another ID when spoof is set.
func fakeDNS(t *testing.T, answers map[string]dnsmessage.RCode, spoof bool) string {
	t.Helper()
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	go func() {
		buf := make([]byte, 4096)
		for {
			n, addr, err := server.ReadFrom(buf)
			if err != nil {
				return
			}
			var query dnsmessage.Message
			if err := query.Unpack(buf[:n]); err != nil || len(query.Questions) != 1 {
				continue
			}
			rcode, ok := answers[strings.ToLower(query.Questions[0].Name.String())]
			if !ok {
				rcode = dnsmessage.RCodeNameError
			}
			reply := func(id uint16, rcode dnsmessage.RCode) {
				response := dnsmessage.Message{
					Header:    dnsmessage.Header{ID: id, Response: true, RecursionAvailable: true, RCode: rcode},
					Questions: query.Questions,
				}
				if wire, err := response.Pack(); err == nil {
					_, _ = server.WriteTo(wire, addr)
				}
			}
			if spoof {
				reply(query.ID+1, dnsmessage.RCodeSuccess)
			}
			reply(query.ID, rcode)
		}
	}()
	return server.LocalAddr().String()
}

// NXDOMAIN is a domain that does not exist; success with no records is one
// that does (an empty non-terminal, or a name with only other types); any
// other code is a failure (RFC 8020).
func TestExists(t *testing.T) {
	addr := fakeDNS(t, map[string]dnsmessage.RCode{
		"empty.example.test.":    dnsmessage.RCodeSuccess,
		"broken.example.test.":   dnsmessage.RCodeServerFailure,
		"refusing.example.test.": dnsmessage.RCodeRefused,
	}, false)
	c := &Client{Servers: []string{addr}, Timeout: time.Second}
	for domain, want := range map[string]bool{"empty.example.test": true, "EMPTY.example.test.": true, "gone.example.test": false} {
		if got, err := c.Exists(context.Background(), domain); err != nil || got != want {
			t.Errorf("%s: %v, %v; want %v", domain, got, err, want)
		}
	}
	for _, domain := range []string{"broken.example.test", "refusing.example.test"} {
		if _, err := c.Exists(context.Background(), domain); err == nil {
			t.Errorf("%s: no error", domain)
		}
	}
	if _, err := c.Exists(context.Background(), strings.Repeat("a", 64)+".example.test"); err == nil {
		t.Error("a label of 64 bytes was asked")
	}
}

// A server that fails passes the question to the next, and an answer that is
// not to the question asked is passed over.
func TestExistsAsksTheNextServer(t *testing.T) {
	broken := fakeDNS(t, map[string]dnsmessage.RCode{"gone.example.test.": dnsmessage.RCodeServerFailure}, false)
	spoofed := fakeDNS(t, nil, true)
	silent, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = silent.Close() }()
	c := &Client{Servers: []string{silent.LocalAddr().String(), broken, spoofed}, Timeout: 200 * time.Millisecond}
	if got, err := c.Exists(context.Background(), "gone.example.test"); err != nil || got {
		t.Errorf("%v, %v; want the third server's NXDOMAIN", got, err)
	}
	if _, err := (&Client{}).Exists(context.Background(), "example.test"); err == nil {
		t.Error("no servers, and no error")
	}
}

// The servers come from resolv.conf's nameserver lines, IPv6 and zones
// included, and are the local ones when it names none.
func TestServersFrom(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "resolv.conf")
	text := "# generated\nsearch default.svc.cluster.local\nnameserver 10.96.0.10\nnameserver 2001:db8::53\nnameserver fe80::1%eth0\nnameserver not-an-ip\noptions ndots:5\n"
	if err := os.WriteFile(conf, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	want := []string{"10.96.0.10:53", "[2001:db8::53]:53", "[fe80::1%eth0]:53"}
	if got := serversFrom(conf); !reflect.DeepEqual(got, want) {
		t.Errorf("%q, want %q", got, want)
	}
	if got := serversFrom(filepath.Join(dir, "missing")); !reflect.DeepEqual(got, defaultServers) {
		t.Errorf("missing file: %q", got)
	}
}
