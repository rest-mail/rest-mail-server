package dnsbl

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/restmail/restmail/internal/config"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// An explicitly configured database must work: connection and migration
// failures are failures, not skipped tests that could pass without exercising SQL.
func openEventsDB(t *testing.T) *gorm.DB {
	t.Helper()
	if os.Getenv("DB_HOST") == "" || os.Getenv("DB_NAME") == "" {
		t.Skip("set DB_HOST and DB_NAME to run the DNSBL database tests")
	}
	port, err := strconv.Atoi(os.Getenv("DB_PORT"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := rmdb.Connect(&config.Config{
		DBHost: os.Getenv("DB_HOST"), DBPort: port, DBName: os.Getenv("DB_NAME"),
		DBUser: os.Getenv("DB_USER"), DBPass: os.Getenv("DB_PASS"),
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := rmdb.AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasTable("dnsbl_listing_events") {
		t.Fatal("startup migration did not create dnsbl_listing_events")
	}
	if !db.Migrator().HasIndex(&models.DNSBLListingEvent{}, "idx_dnsbl_pair") {
		t.Fatal("startup migration did not create the restart-seed index")
	}
	return db
}

func TestEventObserverDatabaseLifecycle(t *testing.T) {
	db := openEventsDB(t)
	// Roll back the test's events, leaving any pre-existing history alone.
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	rec := &recorder{}
	obs := NewEventObserver(tx, rec)
	sc := &scripted{status: StatusListed}
	cfg := testConfig(obs, sendingIP)
	cfg.Providers[0].Name = "events-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	checker := checkFunc(func(ctx context.Context, ip netip.Addr, p Provider) (Result, error) {
		res, err := sc.Check(ctx, ip, p)
		res.Query = QueryName(ip, p.Zone)
		return res, err
	})
	m, clock := newTestMonitor(t, checker, cfg)
	count := func(want int) []models.DNSBLListingEvent {
		t.Helper()
		var rows []models.DNSBLListingEvent
		if err := tx.Where("provider = ?", cfg.Providers[0].Name).Order("id").Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if len(rows) != want {
			t.Fatalf("events = %+v; want %d rows", rows, want)
		}
		return rows
	}
	// A first clean answer writes nothing: there is no listing to resolve.
	sc.set(StatusNotListed)
	if err := m.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	count(0)
	clock.mu.Lock()
	clock.now = clock.now.Add(10 * time.Minute)
	clock.mu.Unlock()
	sc.set(StatusListed)
	// A listing writes once. Repeated listings and non-answers write nothing.
	if err := m.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows := count(1)
	first := rows[0]
	if first.Kind != "listed" || !first.Since.Equal(clock.Now()) || !first.At.Equal(first.Since) ||
		first.IP != sendingIP || first.Zone != cfg.Providers[0].Zone || first.Query != QueryName(netip.MustParseAddr(sendingIP), first.Zone) ||
		string(first.Codes) != `["127.0.0.2"]` || first.Reason != "listed for testing" {
		t.Fatalf("listing evidence = %+v", first)
	}
	for _, status := range []Status{StatusListed, StatusIndeterminate, StatusProviderError, StatusUnsupported} {
		sc.set(status)
		if err := m.pass(context.Background()); err != nil {
			t.Fatal(err)
		}
		count(1)
	}
	// Reconstruct the monitor from SQL, as API startup does. Its first listed
	// check must not announce the existing listing again.
	known, err := obs.Known(context.Background(), cfg)
	if err != nil || len(known) != 1 {
		t.Fatalf("Known = %+v, %v", known, err)
	}
	cfg.Known = known
	sc.set(StatusListed)
	restarted, restartClock := newTestMonitor(t, checker, cfg)
	if err := restarted.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	count(1)
	if _, changes := rec.snapshot(); len(changes) != 1 {
		t.Fatalf("restart announced existing listing: %+v", changes)
	}
	// A recovery after restart resolves the original listing, not startup time.
	restartClock.mu.Lock()
	restartClock.now = clock.Now().Add(time.Hour)
	restartClock.mu.Unlock()
	sc.set(StatusNotListed)
	if err := restarted.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows = count(2)
	if rows[1].Kind != "delisted" || !rows[1].Since.Equal(first.Since) || !rows[1].At.Equal(restartClock.Now()) || string(rows[1].Codes) != "[]" {
		t.Fatalf("resolved event = %+v", rows[1])
	}
	// A clean seed remains clean after another restart. A later listing is
	// a new incident with a new first-seen time.
	cfg.Known, err = obs.Known(context.Background(), cfg)
	if err != nil || len(cfg.Known) != 1 || cfg.Known[0].Result.Status != StatusNotListed {
		t.Fatalf("resolved seed = %+v, %v", cfg.Known, err)
	}
	clean, cleanClock := newTestMonitor(t, checker, cfg)
	if err := clean.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	count(2)
	cleanClock.mu.Lock()
	cleanClock.now = restartClock.Now().Add(time.Hour)
	cleanClock.mu.Unlock()
	sc.set(StatusListed)
	if err := clean.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows = count(3)
	if rows[2].Kind != "listed" || !rows[2].Since.Equal(cleanClock.Now()) {
		t.Fatalf("new listing = %+v", rows[2])
	}
	// Reusing a provider name with a different zone must not reuse its history.
	cfg.Providers[0].Zone = "different.example.org"
	if known, err := obs.Known(context.Background(), cfg); err != nil || len(known) != 0 {
		t.Fatalf("changed-zone seed = %+v, %v", known, err)
	}

	// A real PostgreSQL error must propagate through the observer and stop Run;
	// no transition is forwarded and no state is accepted. An unreadable seed
	// must also fail rather than be treated as empty history.
	if err := tx.Exec("SELECT 1/0").Error; err == nil {
		t.Fatal("expected PostgreSQL to abort the test transaction")
	}
	_, before := rec.snapshot()
	failed, _ := newTestMonitor(t, checker, testConfig(obs, sendingIP))
	if err := failed.Run(context.Background()); err == nil {
		t.Fatal("database write failure did not stop Run")
	}
	if len(failed.state) != 0 {
		t.Fatal("database write failure accepted listing state")
	}
	if _, after := rec.snapshot(); len(after) != len(before) {
		t.Fatal("unpersisted transition was forwarded")
	}
	if _, err := obs.Known(context.Background(), cfg); err == nil {
		t.Fatal("database read failure returned an empty seed")
	}
}

type failingObserver struct {
	recorder
	err error
}

func (o *failingObserver) Changed(tr Transition) error {
	if o.err != nil {
		return o.err
	}
	return o.recorder.Changed(tr)
}

func TestMonitorObserverFailurePreservesState(t *testing.T) {
	sc := &scripted{status: StatusListed}
	writeErr := errors.New("database unavailable")
	obs := &failingObserver{err: writeErr}
	m, _ := newTestMonitor(t, sc, testConfig(obs, sendingIP))
	if err := m.Run(context.Background()); !errors.Is(err, writeErr) {
		t.Fatalf("Run = %v, want write error", err)
	}
	if len(m.state) != 0 {
		t.Fatal("failed write accepted the listing")
	}
	obs.err = nil
	if err := m.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	obs.err = writeErr
	sc.set(StatusNotListed)
	if err := m.Run(context.Background()); !errors.Is(err, writeErr) {
		t.Fatalf("Run = %v, want write error", err)
	}
	for _, state := range m.state {
		if state.result.Status != StatusListed {
			t.Fatal("failed recovery write cleared listing")
		}
	}
	obs.err = nil
	if err := m.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, changes := obs.snapshot(); len(changes) != 2 || changes[1].Kind != TransitionRecovered {
		t.Fatalf("retried transitions = %+v", changes)
	}
}
