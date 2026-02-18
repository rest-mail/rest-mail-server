package gateway_test

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	proxyproto "github.com/pires/go-proxyproto"
	smtpgw "github.com/restmail/restmail/internal/gateway/smtp"
)

// --- PROXY protocol v1 header parsing tests ---

func TestProxyV1HeaderParseTCP4(t *testing.T) {
	// Format: "PROXY TCP4 <src> <dst> <srcport> <dstport>\r\n"
	raw := "PROXY TCP4 192.168.1.100 10.0.0.1 56324 25\r\n"
	reader := bufio.NewReader(strings.NewReader(raw))
	header, err := proxyproto.Read(reader)
	if err != nil {
		t.Fatalf("expected no error parsing v1 TCP4 header, got: %v", err)
	}
	if header.Version != 1 {
		t.Fatalf("expected version 1, got %d", header.Version)
	}
	src, dst, ok := header.TCPAddrs()
	if !ok {
		t.Fatal("expected TCPAddrs to succeed")
	}
	if src.IP.String() != "192.168.1.100" {
		t.Fatalf("expected source IP 192.168.1.100, got %s", src.IP)
	}
	if src.Port != 56324 {
		t.Fatalf("expected source port 56324, got %d", src.Port)
	}
	if dst.IP.String() != "10.0.0.1" {
		t.Fatalf("expected destination IP 10.0.0.1, got %s", dst.IP)
	}
	if dst.Port != 25 {
		t.Fatalf("expected destination port 25, got %d", dst.Port)
	}
}

func TestProxyV1HeaderParseTCP6(t *testing.T) {
	raw := "PROXY TCP6 2001:db8::1 2001:db8::2 56324 25\r\n"
	reader := bufio.NewReader(strings.NewReader(raw))
	header, err := proxyproto.Read(reader)
	if err != nil {
		t.Fatalf("expected no error parsing v1 TCP6 header, got: %v", err)
	}
	if header.Version != 1 {
		t.Fatalf("expected version 1, got %d", header.Version)
	}
	src, dst, ok := header.TCPAddrs()
	if !ok {
		t.Fatal("expected TCPAddrs to succeed")
	}
	if src.IP.String() != "2001:db8::1" {
		t.Fatalf("expected source IP 2001:db8::1, got %s", src.IP)
	}
	if src.Port != 56324 {
		t.Fatalf("expected source port 56324, got %d", src.Port)
	}
	if dst.IP.String() != "2001:db8::2" {
		t.Fatalf("expected destination IP 2001:db8::2, got %s", dst.IP)
	}
	if dst.Port != 25 {
		t.Fatalf("expected destination port 25, got %d", dst.Port)
	}
}

func TestProxyV1HeaderInvalid(t *testing.T) {
	// Missing the trailing \r\n makes it not a valid v1 header
	raw := "PROXY TCP4 bad-data\r\n"
	reader := bufio.NewReader(strings.NewReader(raw))
	_, err := proxyproto.Read(reader)
	if err == nil {
		t.Fatal("expected error for malformed v1 header, got nil")
	}
}

// --- PROXY v2 header generation and parsing round-trip ---

func TestProxyV2HeaderRoundTrip(t *testing.T) {
	srcAddr := &net.TCPAddr{IP: net.ParseIP("203.0.113.50"), Port: 44123}
	dstAddr := &net.TCPAddr{IP: net.ParseIP("198.51.100.1"), Port: 587}
	header := proxyproto.HeaderProxyFromAddrs(2, srcAddr, dstAddr)

	buf, err := header.Format()
	if err != nil {
		t.Fatalf("failed to format v2 header: %v", err)
	}

	reader := bufio.NewReader(strings.NewReader(string(buf)))
	parsed, err := proxyproto.Read(reader)
	if err != nil {
		t.Fatalf("failed to parse v2 header: %v", err)
	}
	if parsed.Version != 2 {
		t.Fatalf("expected version 2, got %d", parsed.Version)
	}
	src, dst, ok := parsed.TCPAddrs()
	if !ok {
		t.Fatal("expected TCPAddrs to succeed for v2")
	}
	if src.IP.String() != "203.0.113.50" {
		t.Fatalf("expected source 203.0.113.50, got %s", src.IP)
	}
	if dst.Port != 587 {
		t.Fatalf("expected dst port 587, got %d", dst.Port)
	}
}

// --- Listener-level integration tests using WrapWithProxyProtocol ---

// echoServer accepts a single connection on the given listener, reads one line,
// prefixes it with the connection's RemoteAddr, and writes it back.
func echoServer(listener net.Listener, done chan<- string) {
	conn, err := listener.Accept()
	if err != nil {
		done <- fmt.Sprintf("accept error: %v", err)
		return
	}
	defer conn.Close()

	remote := conn.RemoteAddr().String()
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		done <- fmt.Sprintf("read error: %v", err)
		return
	}
	line = strings.TrimRight(line, "\r\n")

	// Return the remote address as seen by the server plus the line content.
	done <- fmt.Sprintf("remote=%s data=%s", remote, line)
}

func TestWrapWithProxyProtocol_TrustedCIDR_PropagatatesRealIP(t *testing.T) {
	// Set up a plain TCP listener on an ephemeral port.
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer base.Close()

	// Wrap with PROXY protocol trusting localhost (127.0.0.0/8).
	wrapped, err := smtpgw.WrapWithProxyProtocol(base, []string{"127.0.0.0/8"})
	if err != nil {
		t.Fatalf("WrapWithProxyProtocol failed: %v", err)
	}

	done := make(chan string, 1)
	go echoServer(wrapped, done)

	// Connect from localhost (which is trusted) and send a PROXY v1 header
	// claiming the real client is 203.0.113.50:44123.
	conn, err := net.DialTimeout("tcp", base.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	defer conn.Close()

	// Send PROXY v1 header first.
	proxyHeader := "PROXY TCP4 203.0.113.50 198.51.100.1 44123 25\r\n"
	_, err = conn.Write([]byte(proxyHeader))
	if err != nil {
		t.Fatalf("failed to write proxy header: %v", err)
	}

	// Send application data.
	_, err = conn.Write([]byte("EHLO test.example.com\r\n"))
	if err != nil {
		t.Fatalf("failed to write data: %v", err)
	}

	select {
	case result := <-done:
		// The server should see the real client IP from the PROXY header.
		if !strings.Contains(result, "203.0.113.50") {
			t.Fatalf("expected server to see real client IP 203.0.113.50, got: %s", result)
		}
		if !strings.Contains(result, "EHLO test.example.com") {
			t.Fatalf("expected server to receive application data, got: %s", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for server response")
	}
}

func TestWrapWithProxyProtocol_UntrustedCIDR_IgnoresHeader(t *testing.T) {
	// Set up a plain TCP listener on an ephemeral port.
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer base.Close()

	// Wrap with PROXY protocol trusting only 10.0.0.0/8, NOT localhost.
	wrapped, err := smtpgw.WrapWithProxyProtocol(base, []string{"10.0.0.0/8"})
	if err != nil {
		t.Fatalf("WrapWithProxyProtocol failed: %v", err)
	}

	done := make(chan string, 1)
	go echoServer(wrapped, done)

	// Connect from localhost (which is NOT trusted) and attempt to send a PROXY header.
	conn, err := net.DialTimeout("tcp", base.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	defer conn.Close()

	// Send a PROXY header even though we're not trusted. It should be IGNORED
	// (treated as raw data), and RemoteAddr should stay as 127.0.0.1.
	proxyHeader := "PROXY TCP4 203.0.113.50 198.51.100.1 44123 25\r\n"
	_, err = conn.Write([]byte(proxyHeader))
	if err != nil {
		t.Fatalf("failed to write proxy header: %v", err)
	}
	// Also send some data after (though the proxy header may be treated as data).
	_, err = conn.Write([]byte("EHLO test.example.com\r\n"))
	if err != nil {
		t.Fatalf("failed to write data: %v", err)
	}

	select {
	case result := <-done:
		// Server should see 127.0.0.1 since the connection is not trusted.
		if !strings.Contains(result, "127.0.0.1") {
			t.Fatalf("expected server to see local IP 127.0.0.1, got: %s", result)
		}
		// The PROXY header should have been treated as data because it was ignored.
		if strings.Contains(result, "203.0.113.50") {
			t.Fatalf("server should NOT see spoofed IP 203.0.113.50, got: %s", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for server response")
	}
}

func TestWrapWithProxyProtocol_NoHeader_StillWorks(t *testing.T) {
	// Set up a plain TCP listener on an ephemeral port.
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer base.Close()

	// Wrap with PROXY protocol trusting only 10.0.0.0/8.
	// Since connections from 127.0.0.1 are NOT trusted, the policy is IGNORE,
	// meaning no PROXY header is expected and connections work normally.
	wrapped, err := smtpgw.WrapWithProxyProtocol(base, []string{"10.0.0.0/8"})
	if err != nil {
		t.Fatalf("WrapWithProxyProtocol failed: %v", err)
	}

	done := make(chan string, 1)
	go echoServer(wrapped, done)

	// Connect and send data directly without any PROXY header.
	conn, err := net.DialTimeout("tcp", base.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	defer conn.Close()

	_, err = conn.Write([]byte("EHLO direct.example.com\r\n"))
	if err != nil {
		t.Fatalf("failed to write data: %v", err)
	}

	select {
	case result := <-done:
		// RemoteAddr should be 127.0.0.1 since no proxy header was sent.
		if !strings.Contains(result, "127.0.0.1") {
			t.Fatalf("expected 127.0.0.1 in result, got: %s", result)
		}
		if !strings.Contains(result, "EHLO direct.example.com") {
			t.Fatalf("expected application data in result, got: %s", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for server response")
	}
}

func TestWrapWithProxyProtocol_IPv6Header(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer base.Close()

	wrapped, err := smtpgw.WrapWithProxyProtocol(base, []string{"127.0.0.0/8"})
	if err != nil {
		t.Fatalf("WrapWithProxyProtocol failed: %v", err)
	}

	done := make(chan string, 1)
	go echoServer(wrapped, done)

	conn, err := net.DialTimeout("tcp", base.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	defer conn.Close()

	// Send a PROXY v1 TCP6 header with an IPv6 client address.
	proxyHeader := "PROXY TCP6 2001:db8::1 2001:db8::2 56789 25\r\n"
	_, err = conn.Write([]byte(proxyHeader))
	if err != nil {
		t.Fatalf("failed to write proxy header: %v", err)
	}
	_, err = conn.Write([]byte("EHLO ipv6.example.com\r\n"))
	if err != nil {
		t.Fatalf("failed to write data: %v", err)
	}

	select {
	case result := <-done:
		if !strings.Contains(result, "2001:db8::1") {
			t.Fatalf("expected server to see IPv6 client 2001:db8::1, got: %s", result)
		}
		if !strings.Contains(result, "EHLO ipv6.example.com") {
			t.Fatalf("expected application data, got: %s", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for server response")
	}
}

func TestWrapWithProxyProtocol_V2Binary(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer base.Close()

	wrapped, err := smtpgw.WrapWithProxyProtocol(base, []string{"127.0.0.0/8"})
	if err != nil {
		t.Fatalf("WrapWithProxyProtocol failed: %v", err)
	}

	done := make(chan string, 1)
	go echoServer(wrapped, done)

	conn, err := net.DialTimeout("tcp", base.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	defer conn.Close()

	// Build a PROXY v2 binary header.
	srcAddr := &net.TCPAddr{IP: net.ParseIP("198.51.100.22"), Port: 33210}
	dstAddr := &net.TCPAddr{IP: net.ParseIP("198.51.100.1"), Port: 25}
	header := proxyproto.HeaderProxyFromAddrs(2, srcAddr, dstAddr)
	headerBytes, err := header.Format()
	if err != nil {
		t.Fatalf("failed to format v2 header: %v", err)
	}

	_, err = conn.Write(headerBytes)
	if err != nil {
		t.Fatalf("failed to write v2 header: %v", err)
	}
	_, err = conn.Write([]byte("EHLO v2test.example.com\r\n"))
	if err != nil {
		t.Fatalf("failed to write data: %v", err)
	}

	select {
	case result := <-done:
		if !strings.Contains(result, "198.51.100.22") {
			t.Fatalf("expected server to see real client IP 198.51.100.22, got: %s", result)
		}
		if !strings.Contains(result, "EHLO v2test.example.com") {
			t.Fatalf("expected application data, got: %s", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for server response")
	}
}

// --- Helper function unit tests ---

func TestParseCIDRs_Valid(t *testing.T) {
	// WrapWithProxyProtocol internally parses CIDRs. Valid CIDRs should not error.
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer base.Close()

	cidrs := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "::1/128", "fd00::/8"}
	_, err = smtpgw.WrapWithProxyProtocol(base, cidrs)
	if err != nil {
		t.Fatalf("expected valid CIDRs to succeed, got: %v", err)
	}
}

func TestParseCIDRs_Invalid(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer base.Close()

	cidrs := []string{"not-a-cidr"}
	_, err = smtpgw.WrapWithProxyProtocol(base, cidrs)
	if err == nil {
		t.Fatal("expected error for invalid CIDR, got nil")
	}
	if !strings.Contains(err.Error(), "invalid CIDR") {
		t.Fatalf("expected 'invalid CIDR' in error message, got: %v", err)
	}
}

func TestWrapWithProxyProtocol_EmptyCIDRs(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	defer base.Close()

	// Empty CIDRs means all connections get IGNORE policy.
	wrapped, err := smtpgw.WrapWithProxyProtocol(base, []string{})
	if err != nil {
		t.Fatalf("expected empty CIDRs to succeed, got: %v", err)
	}

	done := make(chan string, 1)
	go echoServer(wrapped, done)

	conn, err := net.DialTimeout("tcp", base.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	defer conn.Close()

	_, err = conn.Write([]byte("EHLO no-proxy.example.com\r\n"))
	if err != nil {
		t.Fatalf("failed to write data: %v", err)
	}

	select {
	case result := <-done:
		if !strings.Contains(result, "127.0.0.1") {
			t.Fatalf("expected 127.0.0.1 in result, got: %s", result)
		}
		if !strings.Contains(result, "EHLO no-proxy.example.com") {
			t.Fatalf("expected data in result, got: %s", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for server response")
	}
}
