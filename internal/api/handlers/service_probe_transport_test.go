package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type serviceProbeTestDB struct {
	wait     bool
	calls    int
	deadline bool
}

func (c *serviceProbeTestDB) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c *serviceProbeTestDB) Driver() driver.Driver                        { return c }
func (c *serviceProbeTestDB) Open(string) (driver.Conn, error)             { return c, nil }
func (c *serviceProbeTestDB) Close() error                                 { return nil }
func (c *serviceProbeTestDB) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("probe must not prepare SQL")
}
func (c *serviceProbeTestDB) Begin() (driver.Tx, error) {
	return nil, errors.New("probe must not mutate data")
}
func (c *serviceProbeTestDB) Ping(ctx context.Context) error {
	c.calls++
	_, c.deadline = ctx.Deadline()
	if c.wait {
		<-ctx.Done()
	}
	return ctx.Err()
}
func serviceProbeTestGorm(t *testing.T, c *serviceProbeTestDB) *gorm.DB {
	t.Helper()
	pool := sql.OpenDB(c)
	t.Cleanup(func() { pool.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return db
}
func checkServiceProbeLatency(t *testing.T, result TestProbeResult) {
	t.Helper()
	latency, err := time.ParseDuration(result.Latency)
	if err != nil || latency < 0 {
		t.Fatal("probe latency missing or invalid")
	}
}

func TestServiceProbeLocalTCPListeners(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	accepted := make(chan error, 4)
	go func() {
		for range 4 {
			conn, err := listener.Accept()
			if err != nil {
				accepted <- err
				return
			}
			conn.Close()
			accepted <- nil
		}
	}()
	cfg := &config.Config{Environment: "test", SMTPPortInbound: port, SMTPPortSubmissionTLS: port, IMAPTLSPort: port, POP3TLSPort: port}
	h := NewTestHandler(nil, cfg)
	rr := httptest.NewRecorder()
	testRoutes(h).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/admin/test/probe", strings.NewReader(`{"services":["smtp","SMTP_SUBMISSION","imap","pop3"],"host":"127.0.0.1","timeout":"1s"}`)))
	if rr.Code != 200 {
		t.Fatalf("status%d:%s", rr.Code, rr.Body)
	}
	var body struct{ Data map[string]TestProbeResult }
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 4 {
		t.Fatal("unrequested service was probed or service result missing")
	}
	for _, service := range []string{"smtp", "smtp_submission", "imap", "pop3"} {
		r, ok := body.Data[service]
		if !ok || r.Status != "up" || r.Address != net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) || r.Error != "" {
			t.Fatalf("wrong configured TCP result for%s:%+v", service, r)
		}
		checkServiceProbeLatency(t, r)
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for range 4 {
		select {
		case err := <-accepted:
			if err != nil {
				t.Fatal(err)
			}
		case <-timer.C:
			t.Fatal("configured listener did not receive all four probes")
		}
	}
}

func TestServiceProbeCancelledDefaults(t *testing.T) {
	c := &serviceProbeTestDB{}
	db := serviceProbeTestGorm(t, c)
	cfg := &config.Config{Environment: "test", SMTPPortInbound: 1, IMAPTLSPort: 2, POP3TLSPort: 3}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/test/probe", strings.NewReader(`{"host":"127.0.0.1"}`)).WithContext(ctx)
	rr := httptest.NewRecorder()
	testRoutes(NewTestHandler(db, cfg)).ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status%d:%s", rr.Code, rr.Body)
	}
	var body struct{ Data map[string]TestProbeResult }
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 5 {
		t.Fatal("default services changed")
	}
	for _, service := range []string{"smtp", "imap", "pop3", "dns", "db"} {
		r, ok := body.Data[service]
		if !ok || r.Error == "" {
			t.Fatalf("cancelled%s reported success", service)
		}
		checkServiceProbeLatency(t, r)
	}
	if body.Data["dns"].Domain != "mail1.test" || c.calls != 0 {
		t.Fatal("cancelled request changed DNS target or started a database ping")
	}
}

func TestServiceProbeDatabaseTimeout(t *testing.T) {
	c := &serviceProbeTestDB{wait: true}
	db := serviceProbeTestGorm(t, c)
	result, err := probeDB(t.Context(), db, 10*time.Millisecond)
	if err != nil || result.Status != "error" || result.Error != context.DeadlineExceeded.Error() || c.calls != 1 || !c.deadline {
		t.Fatalf("database timeout not propagated:result%+v err%v calls%d", result, err, c.calls)
	}
	checkServiceProbeLatency(t, result)
}

func TestServiceProbeRefusalPrecedesEveryProbe(t *testing.T) {
	c := &serviceProbeTestDB{}
	db := serviceProbeTestGorm(t, c)
	rr := httptest.NewRecorder()
	testRoutes(NewTestHandler(db, &config.Config{Environment: "test"})).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/admin/test/probe", strings.NewReader(`{"services":["db","unknown"]}`)))
	if rr.Code != 422 || c.calls != 0 {
		t.Fatalf("unknown service ran a partial probe:status%d calls%d", rr.Code, c.calls)
	}
}

func TestServiceProbeDatabaseHandleFailure(t *testing.T) {
	_, err := probeDB(t.Context(), &gorm.DB{Config: &gorm.Config{}}, time.Second)
	if !errors.Is(err, gorm.ErrInvalidDB) {
		t.Fatalf("database handle failure not distinguished:%v", err)
	}
}

// A bounded local UDP fixture exercises the real Go resolver and registered
// response mapping. It never consults an external DNS server.
func TestServiceProbeLocalDNSRecords(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(strconv.FormatBool(failed), func(t *testing.T) {
			pc, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer pc.Close()
			exchange := make(chan error, 1)
			go func() {
				buf := make([]byte, 1500)
				n, addr, err := pc.ReadFrom(buf)
				if err != nil {
					exchange <- err
					return
				}
				q := buf[:n]
				if len(q) < 12 {
					exchange <- errors.New("short DNS request")
					return
				}
				i := 12
				labels := []string{}
				for i < len(q) && q[i] != 0 {
					size := int(q[i])
					i++
					if size > 63 || i+size > len(q) {
						exchange <- errors.New("invalid DNS question")
						return
					}
					labels = append(labels, string(q[i:i+size]))
					i += size
				}
				if i+5 > len(q) || strings.Join(labels, ".") != "mail1.test" || binary.BigEndian.Uint16(q[i+1:i+3]) != 15 {
					exchange <- errors.New("probe changed MX question or DNS target")
					return
				}
				end := i + 5
				response := make([]byte, 12)
				copy(response[:2], q[:2])
				binary.BigEndian.PutUint16(response[2:4], 0x8180)
				binary.BigEndian.PutUint16(response[4:6], 1)
				response = append(response, q[12:end]...)
				if failed {
					binary.BigEndian.PutUint16(response[2:4], 0x8183)
				} else {
					binary.BigEndian.PutUint16(response[6:8], 2)
					for j, host := range []string{"mx.mail1.test", "backup.mail1.test"} {
						data := []byte{0, byte((j + 1) * 10)}
						for _, label := range strings.Split(host, ".") {
							data = append(data, byte(len(label)))
							data = append(data, []byte(label)...)
						}
						data = append(data, 0)
						response = append(response, 0xc0, 0x0c, 0, 15, 0, 1, 0, 0, 0, 60, byte(len(data)>>8), byte(len(data)))
						response = append(response, data...)
					}
				}
				_, err = pc.WriteTo(response, addr)
				exchange <- err
			}()
			h := NewTestHandler(nil, &config.Config{Environment: "test"})
			h.resolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "udp", pc.LocalAddr().String())
			}}
			rr := httptest.NewRecorder()
			testRoutes(h).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/admin/test/probe", strings.NewReader(`{"services":["dns"],"timeout":"1s"}`)))
			if rr.Code != 200 {
				t.Fatalf("status%d:%s", rr.Code, rr.Body)
			}
			var body struct{ Data map[string]TestProbeResult }
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			result, ok := body.Data["dns"]
			if !ok || len(body.Data) != 1 || result.Domain != "mail1.test" || result.Address != "" {
				t.Fatal("DNS probe result fields changed")
			}
			checkServiceProbeLatency(t, result)
			if failed {
				if result.Status != "error" || result.Error == "" || result.MXRecords != nil {
					t.Fatal("failed DNS query reported records")
				}
			} else {
				if result.Status != "ok" || result.Error != "" || result.MXRecords == nil || strings.Join(*result.MXRecords, ",") != "mx.mail1.test. (pref 10),backup.mail1.test. (pref 20)" {
					t.Fatal("MX records or preferences changed")
				}
			}
			select {
			case err := <-exchange:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("local DNS exchange did not complete")
			}
		})
	}
}
