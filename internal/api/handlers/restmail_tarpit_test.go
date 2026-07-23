package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// TestEscalatingDelay covers the pure delay model used by the negative-lookup
// tarpit: base * misses, capped at max, with the first miss paying the base
// floor and non-positive inputs yielding zero.
func TestEscalatingDelay(t *testing.T) {
	const (
		base = time.Second
		max  = 3 * time.Second
	)
	cases := []struct {
		name string
		n    int
		want time.Duration
	}{
		{"zero misses", 0, 0},
		{"negative misses", -3, 0},
		{"first miss is the base floor", 1, 1 * time.Second},
		{"second miss escalates", 2, 2 * time.Second},
		{"third miss hits the cap", 3, 3 * time.Second},
		{"past cap stays capped", 9, 3 * time.Second},
		{"huge count no overflow", 1_000_000_000, 3 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := escalatingDelay(base, max, tc.n); got != tc.want {
				t.Errorf("escalatingDelay(%v, %v, %d) = %v, want %v", base, max, tc.n, got, tc.want)
			}
		})
	}

	if got := escalatingDelay(0, max, 5); got != 0 {
		t.Errorf("base=0 must disable, got %v", got)
	}
	if got := escalatingDelay(base, 0, 5); got != 0 {
		t.Errorf("max=0 must disable, got %v", got)
	}
}

// newTestTarpit builds a tarpit with a controllable clock and a no-op sleeper so
// record's escalation/decay/keying is asserted without real waits.
func newTestTarpit(base, max time.Duration, now func() time.Time) *negLookupTarpit {
	tp := newNegLookupTarpit(RestmailTarpitConfig{Enabled: true, Base: base, Max: max})
	tp.now = now
	tp.sleep = func(context.Context, time.Duration) {}
	return tp
}

// TestNegLookupTarpitRecordEscalatesAndCaps proves consecutive misses from one
// source escalate linearly and cap at max.
func TestNegLookupTarpitRecordEscalatesAndCaps(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	tp := newTestTarpit(time.Second, 3*time.Second, func() time.Time { return clock })

	want := []time.Duration{1 * time.Second, 2 * time.Second, 3 * time.Second, 3 * time.Second}
	for i, w := range want {
		if got := tp.record("203.0.113.9"); got != w {
			t.Fatalf("miss %d: record = %v, want %v", i+1, got, w)
		}
	}
}

// TestNegLookupTarpitRecordPerSourceKeying proves each source IP has its own
// independent streak.
func TestNegLookupTarpitRecordPerSourceKeying(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	tp := newTestTarpit(time.Second, 10*time.Second, func() time.Time { return clock })

	if got := tp.record("10.0.0.1"); got != 1*time.Second {
		t.Fatalf("A miss 1 = %v, want 1s", got)
	}
	if got := tp.record("10.0.0.1"); got != 2*time.Second {
		t.Fatalf("A miss 2 = %v, want 2s", got)
	}
	// A different source starts fresh, unaffected by A's streak.
	if got := tp.record("10.0.0.2"); got != 1*time.Second {
		t.Fatalf("B miss 1 = %v, want 1s (independent of A)", got)
	}
}

// TestNegLookupTarpitRecordDecays proves a source whose streak has been idle
// past the decay window starts fresh.
func TestNegLookupTarpitRecordDecays(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	tp := newTestTarpit(time.Second, 10*time.Second, func() time.Time { return clock })

	if got := tp.record("192.0.2.5"); got != 1*time.Second {
		t.Fatalf("miss 1 = %v, want 1s", got)
	}
	if got := tp.record("192.0.2.5"); got != 2*time.Second {
		t.Fatalf("miss 2 = %v, want 2s", got)
	}
	// Idle past the decay window: the streak resets to the base floor.
	clock = clock.Add(restmailTarpitDecay + time.Second)
	if got := tp.record("192.0.2.5"); got != 1*time.Second {
		t.Fatalf("miss after decay = %v, want 1s (streak should reset)", got)
	}
}

// TestNegLookupTarpitDelayDisabled proves a disabled or misconfigured tarpit
// never sleeps.
func TestNegLookupTarpitDelayDisabled(t *testing.T) {
	// Disabled.
	tp := newNegLookupTarpit(RestmailTarpitConfig{Enabled: false, Base: time.Second, Max: 3 * time.Second})
	slept := false
	tp.sleep = func(context.Context, time.Duration) { slept = true }
	if d := tp.delay(context.Background(), "1.2.3.4"); d != 0 || slept {
		t.Fatalf("disabled: d=%v slept=%v, want 0/false", d, slept)
	}

	// Nil receiver is safe.
	var nilTp *negLookupTarpit
	if d := nilTp.delay(context.Background(), "1.2.3.4"); d != 0 {
		t.Fatalf("nil tarpit delay = %v, want 0", d)
	}

	// Enabled but base<=0 disables at the delay gate.
	tp2 := newNegLookupTarpit(RestmailTarpitConfig{Enabled: true, Base: 0, Max: 3 * time.Second})
	slept2 := false
	tp2.sleep = func(context.Context, time.Duration) { slept2 = true }
	if d := tp2.delay(context.Background(), "1.2.3.4"); d != 0 || slept2 {
		t.Fatalf("base<=0: d=%v slept=%v, want 0/false", d, slept2)
	}
}

// TestNegLookupTarpitDelayAbortsOnCancel proves the sleep aborts when the
// request context is cancelled (client disconnect), using the real sleeper.
func TestNegLookupTarpitDelayAbortsOnCancel(t *testing.T) {
	tp := newNegLookupTarpit(RestmailTarpitConfig{Enabled: true, Base: 10 * time.Second, Max: 20 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	tp.delay(ctx, "203.0.113.1")
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("delay did not abort on cancelled context, took %v", elapsed)
	}
}

// TestRestmailClientIP checks IP extraction from RemoteAddr.
func TestRestmailClientIP(t *testing.T) {
	cases := map[string]string{
		"203.0.113.4:5555":  "203.0.113.4",
		"[2001:db8::1]:443": "2001:db8::1",
		"malformed-no-port": "malformed-no-port",
	}
	for remote, want := range cases {
		r := httptest.NewRequest(http.MethodGet, "/restmail/mailboxes?address=x@y", nil)
		r.RemoteAddr = remote
		if got := restmailClientIP(r); got != want {
			t.Errorf("restmailClientIP(%q) = %q, want %q", remote, got, want)
		}
	}
}

// openMailboxTestDB connects to the unit-test Postgres and migrates the tables
// CheckMailbox reads. It skips (never fails) when no database is reachable,
// matching the repo's depless-local / DB-in-CI convention.
func openMailboxTestDB(t *testing.T) *gorm.DB {
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
		t.Skipf("restmail tarpit DB test skipped: no database reachable (%v)", err)
	}
	if err := gdb.AutoMigrate(&models.Domain{}, &models.Mailbox{}); err != nil {
		t.Skipf("restmail tarpit DB test skipped: migrate failed (%v)", err)
	}
	return gdb
}

// TestCheckMailboxTarpitsNegativeButNotPositive proves the wiring: a lookup for
// a non-existent mailbox invokes the (recorded) tarpit sleep, while a lookup for
// a real mailbox returns promptly with no added delay. Skips without a DB.
func TestCheckMailboxTarpitsNegativeButNotPositive(t *testing.T) {
	db := openMailboxTestDB(t)

	suffix := time.Now().UnixNano()
	domainName := fmt.Sprintf("tarpit-%d.example", suffix)
	realAddr := fmt.Sprintf("real-%d@%s", suffix, domainName)

	domain := models.Domain{Name: domainName, Active: true}
	if err := db.Create(&domain).Error; err != nil {
		t.Fatalf("seed domain: %v", err)
	}
	mbox := models.Mailbox{DomainID: domain.ID, LocalPart: "real", Address: realAddr, Password: "x", Active: true}
	if err := db.Create(&mbox).Error; err != nil {
		t.Fatalf("seed mailbox: %v", err)
	}
	t.Cleanup(func() {
		db.Where("id = ?", mbox.ID).Delete(&models.Mailbox{})
		db.Where("id = ?", domain.ID).Delete(&models.Domain{})
	})

	h := NewRestmailHandler(db, nil, nil, RestmailTarpitConfig{Enabled: true, Base: 50 * time.Millisecond, Max: time.Second})
	var mu sync.Mutex
	var calls []time.Duration
	h.tarpit.sleep = func(_ context.Context, d time.Duration) {
		mu.Lock()
		calls = append(calls, d)
		mu.Unlock()
	}
	sleptCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(calls)
	}

	// Negative lookup: exists:false and the source is tarpitted once.
	reqN := httptest.NewRequest(http.MethodGet, "/restmail/mailboxes?address=ghost@"+domainName, nil)
	reqN.RemoteAddr = "203.0.113.77:40000"
	rrN := httptest.NewRecorder()
	h.CheckMailbox(rrN, reqN)
	if got := decodeExists(t, rrN); got != false {
		t.Fatalf("negative lookup exists = %v, want false", got)
	}
	if n := sleptCount(); n != 1 {
		t.Fatalf("negative lookup tarpit sleeps = %d, want 1", n)
	}

	// Positive lookup: exists:true, prompt, no new tarpit sleep.
	reqP := httptest.NewRequest(http.MethodGet, "/restmail/mailboxes?address="+realAddr, nil)
	reqP.RemoteAddr = "203.0.113.77:40001"
	rrP := httptest.NewRecorder()
	h.CheckMailbox(rrP, reqP)
	if got := decodeExists(t, rrP); got != true {
		t.Fatalf("positive lookup exists = %v, want true", got)
	}
	if n := sleptCount(); n != 1 {
		t.Fatalf("positive lookup added a tarpit sleep (count now %d), want it to stay 1", n)
	}
}

// decodeExists pulls the "exists" boolean out of a CheckMailbox JSON response.
func decodeExists(t *testing.T, rr *httptest.ResponseRecorder) bool {
	t.Helper()
	var body struct {
		Data struct {
			Exists bool `json:"exists"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rr.Body.String(), err)
	}
	return body.Data.Exists
}
