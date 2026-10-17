package handlers

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/restmail/restmail/internal/auth"
	rmdb "github.com/restmail/restmail/internal/db"
	"github.com/restmail/restmail/internal/db/models"
	"gorm.io/gorm"
)

// mailboxReads is a connection of its own to the test database that counts
// the queries it makes on the mailboxes table.
func mailboxReads(t *testing.T) (*gorm.DB, func() int64) {
	t.Helper()
	db, err := rmdb.Connect(testDBConfig())
	if err != nil {
		t.Skipf("session cache DB test skipped: no database reachable (%v)", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	var n atomic.Int64
	if err := db.Callback().Query().After("gorm:query").Register("test:count_mailbox_reads", func(tx *gorm.DB) {
		if tx.Statement.Table == "mailboxes" {
			n.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	return db, n.Load
}

// The check every request makes reads the database once per account per
// SessionCacheTTL, not once per request.
func TestSessionCache_ReadsAnAccountOncePerTTL(t *testing.T) {
	mb := seedMailbox(t, openRevocationTestDB(t), "cache")
	db, reads := mailboxReads(t)
	now := time.Now()
	cache := NewSessionCache(db, SessionCacheTTL)
	cache.now = func() time.Time { return now }

	for i := 0; i < 100; i++ {
		if ok, err := cache.Current(mb.ID, mb.CredentialEpoch); err != nil || !ok {
			t.Fatalf("request %d: current = %v, %v", i, ok, err)
		}
	}
	// A token from another epoch is refused from the same entry.
	if ok, _ := cache.Current(mb.ID, mb.CredentialEpoch+1); ok {
		t.Error("a token from another epoch is current")
	}
	// So is an account that does not exist, which is remembered too.
	for i := 0; i < 10; i++ {
		if ok, err := cache.Current(mb.ID+1_000_000, 0); err != nil || ok {
			t.Fatalf("an account that does not exist: current = %v, %v", ok, err)
		}
	}
	if got := reads(); got != 2 {
		t.Fatalf("%d reads of the mailboxes table for 111 checks of 2 accounts, want 2", got)
	}

	now = now.Add(SessionCacheTTL)
	if ok, _ := cache.Current(mb.ID, mb.CredentialEpoch); !ok {
		t.Error("not current after the cache's lifetime")
	}
	if got := reads(); got != 3 {
		t.Errorf("%d reads after the cache's lifetime, want 3", got)
	}
}

// Ending an account's sessions through this process is seen at the very next
// check, however long the cache would have kept the account.
func TestSessionCache_ForgetSeesEndedSessionsAtOnce(t *testing.T) {
	db := openRevocationTestDB(t)
	mb := seedMailbox(t, db, "forget")
	cache := NewSessionCache(db, time.Hour)
	if ok, _ := cache.Current(mb.ID, mb.CredentialEpoch); !ok {
		t.Fatal("a fresh session is not current")
	}

	if err := db.Transaction(func(tx *gorm.DB) error { return endSessions(tx, mb.ID) }); err != nil {
		t.Fatal(err)
	}
	cache.Forget(mb.ID)
	if ok, _ := cache.Current(mb.ID, mb.CredentialEpoch); ok {
		t.Error("a token from before the sessions ended is still current")
	}
	if ok, _ := cache.Current(mb.ID, mb.CredentialEpoch+1); !ok {
		t.Error("a token from after the sessions ended is not current")
	}

	if err := db.Model(&models.Mailbox{}).Where("id = ?", mb.ID).Update("active", false).Error; err != nil {
		t.Fatal(err)
	}
	cache.Forget(mb.ID)
	if ok, _ := cache.Current(mb.ID, mb.CredentialEpoch+1); ok {
		t.Error("a disabled account's session is current")
	}
}

// A refresh token from before its account's epoch moved on is refused even
// while its ledger row is still active, as after `restmail-api
// revoke-sessions` bumped the epoch from another process: it must not mint an
// access token at the new epoch. The row is left as it was.
func TestRefresh_RefusesATokenFromAnOlderEpoch(t *testing.T) {
	db := openRevocationTestDB(t)
	jwt := auth.NewJWTService("rev-secret", 15*time.Minute, 7*24*time.Hour)
	h := newDBAuthHandler(db, jwt)
	mb := seedMailbox(t, db, "epoch")

	old, err := jwt.GenerateSession(mb.ID, mb.Address, mb.CredentialEpoch, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	seedLedger(t, db, old, mb.ID)
	if err := db.Model(&models.Mailbox{}).Where("id = ?", mb.ID).
		Update("credential_epoch", gorm.Expr("credential_epoch + 1")).Error; err != nil {
		t.Fatal(err)
	}

	if rr := doRefresh(h, old.RefreshToken); rr.Code != http.StatusUnauthorized {
		t.Fatalf("refresh from the old epoch: %d %s, want 401", rr.Code, rr.Body.String())
	}
	if got := ledgerStatus(t, db, old.RefreshJTI); got != models.RefreshTokenActive {
		t.Errorf("ledger row after a refused refresh = %q, want it untouched", got)
	}

	current, err := jwt.GenerateSession(mb.ID, mb.Address, mb.CredentialEpoch+1, []string{auth.AMRPassword}, false)
	if err != nil {
		t.Fatal(err)
	}
	seedLedger(t, db, current, mb.ID)
	rr := doRefresh(h, current.RefreshToken)
	if rr.Code != http.StatusOK {
		t.Fatalf("refresh from the current epoch: %d %s", rr.Code, rr.Body.String())
	}
	if claims := validateBody(t, jwt, rr); claims.CredentialEpoch != mb.CredentialEpoch+1 {
		t.Errorf("refreshed at epoch %d, want %d", claims.CredentialEpoch, mb.CredentialEpoch+1)
	}
}

// eventStream opens the account's event stream on h with token, and returns
// its lines as they arrive; the channel closes when the stream ends.
func eventStream(t *testing.T, h *EventHandler, accountID uint, token string) <-chan string {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/api/v1/accounts/{id}/events", h.Events)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/accounts/"+strconv.FormatUint(uint64(accountID), 10)+"/events", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("open the stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("open the stream: %d", resp.StatusCode)
	}
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		defer resp.Body.Close()
		s := bufio.NewScanner(resp.Body)
		for s.Scan() {
			lines <- s.Text()
		}
	}()
	return lines
}

// untilEnd reads lines until the stream ends, failing if it does not end
// within a few seconds; it returns what came.
func untilEnd(t *testing.T, lines <-chan string) []string {
	t.Helper()
	var got []string
	timeout := time.After(5 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return got
			}
			got = append(got, line)
		case <-timeout:
			t.Fatalf("the stream is still open; it sent %q", got)
		}
	}
}

// waitFor reads lines until one has prefix, failing after a few seconds.
func waitFor(t *testing.T, lines <-chan string, prefix string) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("the stream ended before %q", prefix)
			}
			if strings.HasPrefix(line, prefix) {
				return
			}
		case <-timeout:
			t.Fatalf("no %q from the stream", prefix)
		}
	}
}

// An event stream opened before the account's sessions end is sent nothing
// after: it ends at the next event it would have sent, or at its next
// keepalive when there is none.
func TestEvents_OpenStreamEndsWithItsSession(t *testing.T) {
	db := openRevocationTestDB(t)
	jwt := auth.NewJWTService("sse-secret", 15*time.Minute, 7*24*time.Hour)

	open := func(t *testing.T, keepalive time.Duration) (*SSEBroker, models.Mailbox, *atomic.Bool, <-chan string) {
		mb := seedMailbox(t, db, "sse")
		var ended atomic.Bool
		broker := NewSSEBroker()
		h := NewEventHandler(db, broker, jwt, func(accountID uint, epoch int) (bool, error) {
			return accountID == mb.ID && !ended.Load(), nil
		})
		h.keepalive = keepalive
		pair, err := jwt.GenerateSession(mb.ID, mb.Address, mb.CredentialEpoch, []string{auth.AMRPassword}, false)
		if err != nil {
			t.Fatal(err)
		}
		return broker, mb, &ended, eventStream(t, h, mb.ID, pair.AccessToken)
	}

	t.Run("at the next event", func(t *testing.T) {
		broker, mb, ended, lines := open(t, time.Hour)
		broker.Publish(mb.ID, SSEEvent{Type: "new_message", Data: map[string]string{"subject": "before"}})
		waitFor(t, lines, "event: new_message")

		ended.Store(true)
		broker.Publish(mb.ID, SSEEvent{Type: "new_message", Data: map[string]string{"subject": "after"}})
		for _, line := range untilEnd(t, lines) {
			if strings.Contains(line, "after") {
				t.Errorf("an ended session was sent %q", line)
			}
		}
	})

	t.Run("at the next keepalive", func(t *testing.T) {
		_, _, ended, lines := open(t, 20*time.Millisecond)
		waitFor(t, lines, ": keepalive")
		ended.Store(true)
		untilEnd(t, lines)
	})
}
