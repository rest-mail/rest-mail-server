// Package reloadtest proves that a gateway listener picks up a renewed
// certificate from disk without a restart, and that a broken renewal keeps the
// previous certificate serving and shows up in the reload metrics.
//
// It builds the serving TLS configuration the way the gateway commands do (a
// tlsutil.FileCertLoader behind tls.Config.GetCertificate) so each listener's
// test drives its real accept and STARTTLS paths over it. The loader checks the
// files on every handshake instead of every few seconds, which keeps the tests
// fast without changing what is served.
package reloadtest

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/rest-mail/rest-mail-server/internal/gateway/tlsutil"
	"github.com/rest-mail/rest-mail-server/internal/mtls/mtlstest"
	"github.com/rest-mail/rest-mail-server/internal/tlsfiles"
)

// Pair is a certificate and key on disk, served through a reloading config.
type Pair struct {
	CertPath string
	KeyPath  string
	Config   *tls.Config
}

// New writes a throwaway pair for host and returns the serving config for it.
func New(t *testing.T, host string) *Pair {
	t.Helper()
	dir := t.TempDir()
	p := &Pair{CertPath: filepath.Join(dir, "tls.crt"), KeyPath: filepath.Join(dir, "tls.key")}
	cert, key := issue(t, host)
	writeFile(t, p.KeyPath, key)
	writeFile(t, p.CertPath, cert)
	loader, err := tlsfiles.New(p.CertPath, p.KeyPath, 0)
	if err != nil {
		t.Fatalf("load initial pair: %v", err)
	}
	primary := &tlsutil.FileCertLoader{Loader: loader}
	p.Config = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: primary.GetCertificate}
	return p
}

// Serial is the serial number of the certificate currently in CertPath.
func (p *Pair) Serial(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(p.CertPath)
	if err != nil {
		t.Fatal(err)
	}
	return serialOf(t, data)
}

// Renew publishes a new pair the way an external certificate manager does:
// each file is written beside its target and renamed over it, key first.
func (p *Pair) Renew(t *testing.T, host string) string {
	t.Helper()
	cert, key := issue(t, host)
	writeFile(t, p.KeyPath, key)
	writeFile(t, p.CertPath, cert)
	return serialOf(t, cert)
}

// Break replaces the certificate with a file that does not load.
func (p *Pair) Break(t *testing.T) {
	t.Helper()
	writeFile(t, p.CertPath, []byte("not a certificate"))
}

// PeerSerial is the serial number of the leaf certificate a client was shown.
func PeerSerial(t *testing.T, state tls.ConnectionState) string {
	t.Helper()
	if len(state.PeerCertificates) == 0 {
		t.Fatal("server presented no certificate")
	}
	return state.PeerCertificates[0].SerialNumber.String()
}

// Prove runs the renewal scenario against one listener. connect opens a new
// connection, completes the TLS handshake through the listener's own path
// (implicit TLS or STARTTLS) and returns the client's view of it.
func Prove(t *testing.T, host string, p *Pair, connect func(t *testing.T) tls.ConnectionState) {
	t.Helper()
	if got, want := PeerSerial(t, connect(t)), p.Serial(t); got != want {
		t.Fatalf("first connection served serial %s, want the installed certificate %s", got, want)
	}

	renewed := p.Renew(t, host)
	if got := PeerSerial(t, connect(t)); got != renewed {
		t.Fatalf("after renewal a new connection served serial %s, want the renewed certificate %s", got, renewed)
	}

	failing := Metric(t, "restmail_tls_cert_reload_failing")
	failures := Metric(t, "restmail_tls_cert_reload_failures_total")
	p.Break(t)
	if got := PeerSerial(t, connect(t)); got != renewed {
		t.Fatalf("after a broken renewal a new connection served serial %s, want the last good certificate %s", got, renewed)
	}
	if got := Metric(t, "restmail_tls_cert_reload_failing") - failing; got != 1 {
		t.Fatalf("restmail_tls_cert_reload_failing moved by %v after a broken renewal, want 1", got)
	}
	if got := Metric(t, "restmail_tls_cert_reload_failures_total") - failures; got != 1 {
		t.Fatalf("restmail_tls_cert_reload_failures_total moved by %v after a broken renewal, want 1", got)
	}

	fixed := p.Renew(t, host)
	if got := PeerSerial(t, connect(t)); got != fixed {
		t.Fatalf("after a repaired renewal a new connection served serial %s, want %s", got, fixed)
	}
	if got := Metric(t, "restmail_tls_cert_reload_failing") - failing; got != 0 {
		t.Fatalf("restmail_tls_cert_reload_failing stayed %v above its baseline after a repaired renewal", got)
	}
}

// FreePort returns a loopback TCP port that was free a moment ago.
func FreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// Step is one exchange of a cleartext protocol dialogue: Send (if not empty)
// is written as a line, then lines are read until one starts with Until.
type Step struct {
	Send  string
	Until string
}

// ImplicitTLS dials a port that starts TLS on connect (465, 993, 995).
func ImplicitTLS(t *testing.T, port int, host string) tls.ConnectionState {
	t.Helper()
	conn, err := dial(t, port)
	if err != nil {
		t.Fatalf("dial %d: %v", port, err)
	}
	defer func() { _ = conn.Close() }()
	client := tls.Client(conn, clientConfig(host))
	if err := client.Handshake(); err != nil {
		t.Fatalf("TLS handshake on port %d: %v", port, err)
	}
	return client.ConnectionState()
}

// StartTLS dials a cleartext port, runs the dialogue that ends in the
// protocol's TLS upgrade command, then completes the handshake.
func StartTLS(t *testing.T, port int, host string, dialogue ...Step) tls.ConnectionState {
	t.Helper()
	conn, err := dial(t, port)
	if err != nil {
		t.Fatalf("dial %d: %v", port, err)
	}
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	for _, step := range dialogue {
		if step.Send != "" {
			if _, err := conn.Write([]byte(step.Send + "\r\n")); err != nil {
				t.Fatalf("send %q: %v", step.Send, err)
			}
		}
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				t.Fatalf("waiting for %q after %q: %v", step.Until, step.Send, err)
			}
			if strings.HasPrefix(line, step.Until) {
				break
			}
		}
	}
	if r.Buffered() > 0 {
		t.Fatal("server sent data after the upgrade reply")
	}
	client := tls.Client(conn, clientConfig(host))
	if err := client.Handshake(); err != nil {
		t.Fatalf("TLS handshake after upgrade on port %d: %v", port, err)
	}
	return client.ConnectionState()
}

func dial(t *testing.T, port int) (net.Conn, error) {
	t.Helper()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	var conn net.Conn
	var err error
	// The listener goroutine may not be accepting yet on the first dial.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		conn, err = net.DialTimeout("tcp", addr, time.Second)
		if err == nil || time.Now().After(deadline) {
			break
		}
	}
	if err == nil {
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	}
	return conn, err
}

func clientConfig(host string) *tls.Config {
	// The certificates are throwaway; the test compares serial numbers, so
	// chain verification would only retest crypto/tls.
	return &tls.Config{ServerName: host, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} //nolint:gosec // test client compares serials
}

// Metric reads an unlabelled counter or gauge from the default registry, the
// one each gateway serves on /metrics.
func Metric(t *testing.T, name string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, m := range family.GetMetric() {
			if g := m.GetGauge(); g != nil {
				return g.GetValue()
			}
			if c := m.GetCounter(); c != nil {
				return c.GetValue()
			}
		}
	}
	t.Fatalf("metric %s is not registered", name)
	return 0
}

func issue(t *testing.T, host string) (certPEM, keyPEM []byte) {
	t.Helper()
	paths, err := mtlstest.Write(t.TempDir(), host)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err = os.ReadFile(paths.ServerCert)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err = os.ReadFile(paths.ServerKey)
	if err != nil {
		t.Fatal(err)
	}
	return certPEM, keyPEM
}

func serialOf(t *testing.T, certPEM []byte) string {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("certificate file holds no PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert.SerialNumber.String()
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path+".new", data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
}
