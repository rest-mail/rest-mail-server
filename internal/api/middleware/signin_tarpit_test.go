package middleware

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/metrics"
)

const tarpitAddress = "alice@example.test"

// recordingTarpit is a tarpit that notes each wait instead of sitting through
// it, on a clock the test moves.
type recordingTarpit struct {
	*SignInTarpit
	mu     sync.Mutex
	waits  []time.Duration
	moment time.Time
}

func newRecordingTarpit() *recordingTarpit {
	rec := &recordingTarpit{SignInTarpit: NewSignInTarpit(), moment: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}
	rec.SetClock(func() time.Time {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return rec.moment
	})
	rec.SetWait(func(_ context.Context, d time.Duration) {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		rec.waits = append(rec.waits, d)
	})
	return rec
}

func (rec *recordingTarpit) advance(d time.Duration) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.moment = rec.moment.Add(d)
}

// fail is one failed sign-in, and the wait it was given.
func (rec *recordingTarpit) fail(address, ip string) time.Duration {
	rec.mu.Lock()
	rec.waits = nil
	rec.mu.Unlock()
	rec.Failed(requestFrom(ip), address)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.waits) == 0 {
		return 0
	}
	return rec.waits[0]
}

func requestFrom(ip string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	req.RemoteAddr = ip + ":40000"
	return req
}

func ipNumber(i int) string { return fmt.Sprintf("198.51.%d.%d", i/250, i%250+1) }

// Guessing from a new address every time gets a fresh allowance per address, and
// still gets slower, up to the cap and no further.
func TestDistributedFailuresGetSlower(t *testing.T) {
	rec := newRecordingTarpit()
	want := []time.Duration{0, 0, 0, 0, 0,
		250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 3 * time.Second, 3 * time.Second, 3 * time.Second}
	for i, wantWait := range want {
		if got := rec.fail(tarpitAddress, ipNumber(i)); got != wantWait {
			t.Errorf("failure %d from its own IP waited %v, want %v", i+1, got, wantWait)
		}
	}
	for i := len(want); i < 200; i++ {
		if got := rec.fail(tarpitAddress, ipNumber(i)); got != addressMaxDelay {
			t.Fatalf("failure %d waited %v, want the cap %v", i+1, got, addressMaxDelay)
		}
	}
}

// One client failing pays for it more than anyone else does, and the same
// account from another IP sees no more than the account-wide cap.
func TestOneIPsFailuresDoNotThrottleAnotherBeyondTheCap(t *testing.T) {
	rec := newRecordingTarpit()
	const attacker, person = "203.0.113.9", "192.0.2.44"
	want := []time.Duration{0, 0, 0, 500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second}
	for i, wantWait := range want {
		if got := rec.fail(tarpitAddress, attacker); got != wantWait {
			t.Errorf("attacker failure %d waited %v, want %v", i+1, got, wantWait)
		}
	}
	for i := 0; i < 50; i++ {
		if got := rec.fail(tarpitAddress, attacker); got != clientMaxDelay {
			t.Fatalf("attacker failure waited %v, want its cap %v", got, clientMaxDelay)
		}
	}
	if got := rec.fail(tarpitAddress, person); got != addressMaxDelay {
		t.Errorf("the person's mistyped password waited %v, want the account-wide cap %v", got, addressMaxDelay)
	}
	if got := rec.fail("bob@example.test", attacker); got != 0 {
		t.Errorf("another account from the same IP waited %v", got)
	}
}

func TestSpellingsOfOneAddressCountTogether(t *testing.T) {
	rec := newRecordingTarpit()
	for i, spelling := range []string{"alice@example.test", "ALICE@example.test", " alice@EXAMPLE.test ", "Alice@Example.Test", "alice@example.test"} {
		rec.fail(spelling, ipNumber(i))
	}
	if got := rec.fail("aLiCe@example.test", ipNumber(5)); got != addressFirstDelay {
		t.Errorf("sixth failure across spellings waited %v, want %v", got, addressFirstDelay)
	}
}

// An IPv6 customer has a /64, so its addresses are one client.
func TestAnIPv6BlockIsOneClient(t *testing.T) {
	rec := newRecordingTarpit()
	for i := 1; i <= 3; i++ {
		rec.fail(tarpitAddress, fmt.Sprintf("[2001:db8:1:2::%d]", i))
	}
	if got := rec.fail(tarpitAddress, "[2001:db8:1:2::ffff]"); got != clientFirstDelay {
		t.Errorf("fourth failure from one /64 waited %v, want %v", got, clientFirstDelay)
	}
	if got := rec.fail(tarpitAddress, "[2001:db8:1:3::1]"); got != 0 {
		t.Errorf("another /64 waited %v", got)
	}
}

func TestClientPrefix(t *testing.T) {
	for ip, want := range map[string]string{
		"192.0.2.1":          "192.0.2.1",
		"::ffff:192.0.2.1":   "192.0.2.1",
		"2001:db8:1:2::abcd": "2001:db8:1:2::/64",
		"fe80::1%eth0":       "fe80::/64",
		"not an address":     "not an address",
	} {
		if got := clientPrefix(ip); got != want {
			t.Errorf("clientPrefix(%q) = %q, want %q", ip, got, want)
		}
	}
}

// Failures are forgotten with time, without anyone having to clear them.
func TestFailuresAreForgotten(t *testing.T) {
	rec := newRecordingTarpit()
	for i := 0; i < 100; i++ {
		rec.fail(tarpitAddress, ipNumber(i))
	}
	rec.advance(time.Hour)
	if got := rec.fail(tarpitAddress, ipNumber(0)); got != 0 {
		t.Errorf("an hour after the attack, a failure waited %v", got)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.clients) != 1 {
		t.Errorf("%d client counts survive, want only the failure just made", len(rec.clients))
	}
}

// As many as may wait are waiting: the next failure gets its answer at once. It
// is neither refused nor left holding a connection.
func TestWaitingIsBounded(t *testing.T) {
	rec := newRecordingTarpit()
	for i := 0; i < addressFreeFailures; i++ {
		rec.fail(tarpitAddress, ipNumber(i))
	}
	release := make(chan struct{})
	var entered sync.WaitGroup
	entered.Add(maxWaitingFailures)
	rec.SetWait(func(context.Context, time.Duration) {
		entered.Done()
		<-release
	})
	var waiting sync.WaitGroup
	for i := 0; i < maxWaitingFailures; i++ {
		waiting.Add(1)
		go func() {
			defer waiting.Done()
			rec.Failed(requestFrom(ipNumber(i)), tarpitAddress)
		}()
	}
	entered.Wait()
	if got := testutil.ToFloat64(metrics.SignInFailuresWaiting); got != maxWaitingFailures {
		t.Errorf("waiting gauge = %v, want %d", got, maxWaitingFailures)
	}

	before := testutil.ToFloat64(metrics.SignInFailuresUnslowed)
	done := make(chan struct{})
	go func() {
		rec.Failed(requestFrom("203.0.113.1"), tarpitAddress)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("a failure past the bound waited")
	}
	if got := testutil.ToFloat64(metrics.SignInFailuresUnslowed) - before; got != 1 {
		t.Errorf("unslowed counter moved by %v, want 1", got)
	}

	close(release)
	waiting.Wait()
	if got := testutil.ToFloat64(metrics.SignInFailuresWaiting); got != 0 {
		t.Errorf("waiting gauge after release = %v", got)
	}
	var waited bool
	rec.SetWait(func(context.Context, time.Duration) { waited = true })
	rec.Failed(requestFrom("203.0.113.2"), tarpitAddress)
	if !waited {
		t.Error("the slots were not given back")
	}
}

// A client that hangs up ends its wait, but has already been counted.
func TestHangingUpEndsTheWaitButNotTheCount(t *testing.T) {
	tarpit := NewSignInTarpit()
	for i := 0; i < clientFreeFailures; i++ {
		tarpit.Failed(requestFrom("203.0.113.3"), tarpitAddress)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	tarpit.Failed(requestFrom("203.0.113.3").WithContext(ctx), tarpitAddress)
	if elapsed := time.Since(start); elapsed > clientFirstDelay/2 {
		t.Errorf("a hung-up client still waited %v", elapsed)
	}
	if got := tarpit.charge(tarpitAddress, "203.0.113.3"); got != 2*clientFirstDelay {
		t.Errorf("the next failure earned %v: the hung-up one was not counted", got)
	}
}

// The alert counts an episode, not each failure, and can fire again for a new one.
func TestAnAccountUnderAttackIsReportedOncePerEpisode(t *testing.T) {
	rec := newRecordingTarpit()
	before := testutil.ToFloat64(metrics.SignInAccountsUnderAttack)
	for i := 0; i < underAttackFailures-1; i++ {
		rec.fail(tarpitAddress, ipNumber(i))
	}
	if got := testutil.ToFloat64(metrics.SignInAccountsUnderAttack) - before; got != 0 {
		t.Fatalf("reported after %d failures", underAttackFailures-1)
	}
	for i := 0; i < 50; i++ {
		rec.fail(tarpitAddress, ipNumber(i))
	}
	if got := testutil.ToFloat64(metrics.SignInAccountsUnderAttack) - before; got != 1 {
		t.Fatalf("reported %v times in one episode", got)
	}
	rec.advance(time.Hour)
	rec.fail(tarpitAddress, ipNumber(1))
	for i := 0; i < underAttackFailures; i++ {
		rec.fail(tarpitAddress, ipNumber(i))
	}
	if got := testutil.ToFloat64(metrics.SignInAccountsUnderAttack) - before; got != 2 {
		t.Errorf("second episode: reported %v times in all, want 2", got)
	}
	if got := rec.fail("quiet@example.test", ipNumber(1)); got != 0 {
		t.Errorf("another address waited %v", got)
	}
}

func TestTheTablesAreBounded(t *testing.T) {
	rec := newRecordingTarpit()
	for i := 0; i < maxTrackedKeys+100; i++ {
		rec.charge(fmt.Sprintf("spray-%d@example.test", i), "203.0.113.4")
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.addresses) > maxTrackedKeys || len(rec.clients) > maxTrackedKeys {
		t.Errorf("tables hold %d and %d keys, bound %d", len(rec.addresses), len(rec.clients), maxTrackedKeys)
	}
}

func TestANilTarpitDoesNothing(t *testing.T) {
	var tarpit *SignInTarpit
	tarpit.Failed(requestFrom("203.0.113.5"), tarpitAddress)
}

func TestMaskSignInAddress(t *testing.T) {
	for address, want := range map[string]string{
		"alice@example.test": "a***@example.test",
		"nodomain":           "n***",
		"":                   "***",
	} {
		if got := maskSignInAddress(address); got != want {
			t.Errorf("maskSignInAddress(%q) = %q, want %q", address, got, want)
		}
	}
}

// Opening a gateway session is not a way round the wait. A guess waits, a
// correct credential does not, and a secret that matched but cannot be used
// here is no guess.
func TestSessionPasswordsWaitOnlyForAGuess(t *testing.T) {
	rec := newRecordingTarpit()
	f := &fakeAppPasswords{}
	a := NewSessionPasswords(f.verify, nil, nil)
	a.SetTarpit(rec.SignInTarpit)
	handler := a.Authenticate()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	do := func(password, ip string) (int, []time.Duration) {
		rec.mu.Lock()
		rec.waits = nil
		rec.mu.Unlock()
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, passwordRequest("user@example.com", password, ip))
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return rr.Code, rec.waits
	}

	for i := 0; i < addressFreeFailures; i++ {
		if code, waits := do("guess", ipNumber(i)); code != http.StatusUnauthorized || len(waits) != 0 {
			t.Fatalf("free guess %d: %d, %v", i+1, code, waits)
		}
	}
	if code, waits := do("guess", ipNumber(99)); code != http.StatusUnauthorized || len(waits) != 1 || waits[0] != addressFirstDelay {
		t.Errorf("guess past the allowance: %d, %v", code, waits)
	}
	if code, waits := do(testAppPassword, ipNumber(100)); code != http.StatusNoContent || len(waits) != 0 {
		t.Errorf("correct credential during the attack: %d, %v", code, waits)
	}

	id := uint(7)
	f.refusal = &auth.CredentialRefusal{Reason: auth.CredentialNeedsAppPassword, CredentialID: &id}
	if code, waits := do("right but unusable", ipNumber(101)); code != http.StatusUnauthorized || len(waits) != 0 {
		t.Errorf("a secret that matched: %d, %v", code, waits)
	}
}
