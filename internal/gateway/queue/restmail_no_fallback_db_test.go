package queue

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rest-mail/go-mtasts/pkg/mtasts"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

// noFallbackMX is an SMTP peer that advertises RESTMAIL in its EHLO reply when
// restmail is set and otherwise takes mail by SMTP, counting every MAIL command.
type noFallbackMX struct {
	addr     string
	restmail atomic.Bool
	mails    atomic.Int32
}

func newNoFallbackMX(t *testing.T, restmail bool) *noFallbackMX {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	mx := &noFallbackMX{addr: ln.Addr().String()}
	mx.restmail.Store(restmail)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go mx.serve(c)
		}
	}()
	return mx
}

func (mx *noFallbackMX) serve(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	r := bufio.NewReader(c)
	fmt.Fprint(c, "220 mx.test ESMTP\r\n")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
		case strings.HasPrefix(cmd, "EHLO"):
			if mx.restmail.Load() {
				fmt.Fprint(c, "250-mx.test\r\n250 RESTMAIL\r\n")
			} else {
				fmt.Fprint(c, "250 mx.test\r\n")
			}
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			mx.mails.Add(1)
			fmt.Fprint(c, "250 ok\r\n")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			fmt.Fprint(c, "250 ok\r\n")
		case cmd == "DATA":
			fmt.Fprint(c, "354 go\r\n")
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
			}
			fmt.Fprint(c, "250 queued\r\n")
		case cmd == "QUIT":
			fmt.Fprint(c, "221 bye\r\n")
			return
		default:
			fmt.Fprint(c, "250 ok\r\n")
		}
	}
}

// restPeer is the REST side of the same peer: its discovery endpoint answers
// discovery (200 names the peer's own /restmail), and POST /restmail/messages
// answers post.
// A peer with deferFirst set answers that many POSTs first the way a REST Mail
// server's deliver handler answers a message its inbound pipeline defers
// (greylisting a first message): 503, error code "deferred".
type restPeer struct {
	discovery, post atomic.Int32
	posts           atomic.Int32
	deferFirst      atomic.Int32
}

// noFallbackWorker returns a worker whose recipient domain's only MX is
// mx.<domain>, reached at mx for SMTP and at peer for RESTMAIL, and a queue row
// for domain stored in the database.
func noFallbackWorker(t *testing.T, domain string, mx *noFallbackMX, peer *restPeer, set func(*models.OutboundQueue)) (*Worker, *gorm.DB, models.OutboundQueue) {
	t.Helper()
	gdb := restmailTestDB(t, domain)
	var base atomic.Value
	srv, w := discoveryServer(t, func(rw http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/restmail":
			if s := int(peer.discovery.Load()); s != http.StatusOK {
				rw.WriteHeader(s)
				return
			}
			fmt.Fprintf(rw, `{"endpoint":%q}`, base.Load().(string)+"/restmail")
		case "/restmail/messages":
			peer.posts.Add(1)
			if peer.deferFirst.Add(-1) >= 0 {
				endpoint.WriteError(r.Context(), rw, endpoint.Refuse(http.StatusServiceUnavailable, "deferred", "Message deferred, retry later"))
				return
			}
			rw.WriteHeader(int(peer.post.Load()))
			fmt.Fprint(rw, `{"error":"from the peer"}`)
		default:
			rw.WriteHeader(http.StatusNotFound)
		}
	})
	base.Store(srv.URL)
	w.db = gdb
	w.resolver = fixedMXResolver{host: "mx." + domain}
	w.smtpAddr = func(string) string { return mx.addr }
	w.restmailDiscoveryURL = func(_, d string) string { return srv.URL + "/.well-known/restmail?domain=" + d }
	w.sts = mtasts.NewResolver()
	w.sts.LookupTXT = func(context.Context, string) ([]string, error) { return nil, mtasts.ErrNoPolicy }

	now := time.Now()
	item := models.OutboundQueue{Sender: "alice@example.com", Recipient: "bob@" + domain, Domain: domain,
		RawMessage: "From: alice@example.com\r\nTo: bob@" + domain + "\r\nSubject: 291\r\n\r\nhi\r\n",
		Status:     "delivering", NextAttempt: now, ExpiresAt: now.Add(time.Hour), MaxRetries: 10}
	if set != nil {
		set(&item)
	}
	if err := gdb.Create(&item).Error; err != nil {
		t.Fatalf("queue row: %v", err)
	}
	t.Cleanup(func() { gdb.Delete(&models.OutboundQueue{}, item.ID) })
	return w, gdb, item
}

func cacheRESTMAIL(t *testing.T, gdb *gorm.DB, domain string, supported bool) {
	t.Helper()
	now := time.Now()
	if err := gdb.Create(&models.RESTMAILCapability{Domain: domain, MXHost: "mx." + domain, Supported: supported,
		LastProbed: now, ExpiresAt: now.Add(time.Hour)}).Error; err != nil {
		t.Fatalf("cache capability: %v", err)
	}
}

// noSMTP fails the test if the message went by SMTP, and checks it failed the
// way want says and is pinned to RESTMAIL.
func noSMTP(t *testing.T, gdb *gorm.DB, item models.OutboundQueue, mx *noFallbackMX, err error, permanent bool) {
	t.Helper()
	if n := mx.mails.Load(); n != 0 {
		t.Fatalf("sent %d MAIL command(s): a failed RESTMAIL delivery fell back to SMTP (#291)", n)
	}
	if err == nil {
		t.Fatal("delivery reported success, want a failure")
	}
	if got := ClassifyFailure(err).Permanent(); got != permanent {
		t.Fatalf("error %v classified permanent=%v (%s), want %v", err, got, ClassifyFailure(err), permanent)
	}
	if row := readRow(t, gdb, item.ID); row.Transport != models.TransportRESTMAIL {
		t.Fatalf("row transport %q, want pinned to %q", row.Transport, models.TransportRESTMAIL)
	}
}

func cachedSupported(t *testing.T, gdb *gorm.DB, domain string) {
	t.Helper()
	var cap models.RESTMAILCapability
	if err := gdb.Where("domain = ?", domain).First(&cap).Error; err != nil || !cap.Supported {
		t.Fatalf("capability cache %+v (%v): a delivery error must not clear or downgrade it", cap, err)
	}
}

// A cached REST Mail peer whose delivery fails is deferred (transient) or
// bounced (permanent), never sent by SMTP, and the cache is left as it was.
func TestNoSMTPFallback_CachedRESTMAILFails(t *testing.T) {
	for _, tc := range []struct {
		name      string
		post      int
		permanent bool
	}{{"transient 503", http.StatusServiceUnavailable, false}, {"permanent 422", http.StatusUnprocessableEntity, true}} {
		t.Run(tc.name, func(t *testing.T) {
			domain := fmt.Sprintf("nofallback-cached-%d.test", tc.post)
			mx := newNoFallbackMX(t, false)
			peer := &restPeer{}
			peer.discovery.Store(http.StatusOK)
			peer.post.Store(int32(tc.post))
			w, gdb, item := noFallbackWorker(t, domain, mx, peer, nil)
			cacheRESTMAIL(t, gdb, domain, true)

			err := w.deliver(context.Background(), item)
			noSMTP(t, gdb, item, mx, err, tc.permanent)
			if peer.posts.Load() != 1 {
				t.Fatalf("%d RESTMAIL POSTs, want 1", peer.posts.Load())
			}
			cachedSupported(t, gdb, domain)
		})
	}
}

// A peer the EHLO probe identifies as REST Mail is upgraded, and a failure
// after that is a failure: transient defers, a verified "not hosted" from
// discovery bounces, and neither goes by SMTP.
func TestNoSMTPFallback_ProbeUpgradedFails(t *testing.T) {
	for _, tc := range []struct {
		name            string
		discovery, post int
		permanent       bool
	}{
		{"transient 503", http.StatusOK, http.StatusServiceUnavailable, false},
		{"discovery unavailable", http.StatusBadGateway, http.StatusCreated, false},
		{"not hosted", http.StatusNotFound, http.StatusCreated, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			domain := fmt.Sprintf("nofallback-probe-%d-%d.test", tc.discovery, tc.post)
			mx := newNoFallbackMX(t, true)
			peer := &restPeer{}
			peer.discovery.Store(int32(tc.discovery))
			peer.post.Store(int32(tc.post))
			w, gdb, item := noFallbackWorker(t, domain, mx, peer, nil)

			err := w.deliver(context.Background(), item)
			noSMTP(t, gdb, item, mx, err, tc.permanent)
			cachedSupported(t, gdb, domain)
		})
	}
}

// A row already pinned to RESTMAIL goes by RESTMAIL even when the capability
// cache is gone, says "not supported", or the EHLO no longer advertises
// RESTMAIL: any of those is what a downgrade attack would show.
func TestNoSMTPFallback_PinnedRowNeverDowngrades(t *testing.T) {
	pinned := func(item *models.OutboundQueue) { item.Transport = models.TransportRESTMAIL }
	for _, tc := range []struct {
		name  string
		cache func(*testing.T, *gorm.DB, string)
	}{
		{"cache gone", func(*testing.T, *gorm.DB, string) {}},
		{"cache says unsupported", func(t *testing.T, gdb *gorm.DB, d string) { cacheRESTMAIL(t, gdb, d, false) }},
	} {
		t.Run(tc.name+", peer down", func(t *testing.T) {
			domain := "nofallback-pinned-down-" + strings.ReplaceAll(tc.name, " ", "-") + ".test"
			mx := newNoFallbackMX(t, false)
			peer := &restPeer{}
			peer.discovery.Store(http.StatusServiceUnavailable)
			w, gdb, item := noFallbackWorker(t, domain, mx, peer, pinned)
			tc.cache(t, gdb, domain)

			err := w.deliver(context.Background(), item)
			noSMTP(t, gdb, item, mx, err, false)
		})
		t.Run(tc.name+", peer up", func(t *testing.T) {
			domain := "nofallback-pinned-up-" + strings.ReplaceAll(tc.name, " ", "-") + ".test"
			mx := newNoFallbackMX(t, false)
			peer := &restPeer{}
			peer.discovery.Store(http.StatusOK)
			peer.post.Store(http.StatusCreated)
			w, gdb, item := noFallbackWorker(t, domain, mx, peer, pinned)
			tc.cache(t, gdb, domain)

			if err := w.deliver(context.Background(), item); err != nil {
				t.Fatalf("pinned delivery: %v", err)
			}
			if mx.mails.Load() != 0 || peer.posts.Load() != 1 {
				t.Fatalf("MAIL commands %d, RESTMAIL POSTs %d: want the pinned message by RESTMAIL only", mx.mails.Load(), peer.posts.Load())
			}
		})
	}
}

// A domain never identified as REST Mail goes by SMTP as before, and the row
// records it.
func TestNoSMTPFallback_NeverIdentifiedUsesSMTP(t *testing.T) {
	const domain = "nofallback-plain.test"
	mx := newNoFallbackMX(t, false)
	peer := &restPeer{}
	peer.discovery.Store(http.StatusOK)
	peer.post.Store(http.StatusCreated)
	w, gdb, item := noFallbackWorker(t, domain, mx, peer, nil)

	if err := w.deliver(context.Background(), item); err != nil {
		t.Fatalf("SMTP delivery: %v", err)
	}
	if mx.mails.Load() != 1 || peer.posts.Load() != 0 {
		t.Fatalf("MAIL commands %d, RESTMAIL POSTs %d: want SMTP only", mx.mails.Load(), peer.posts.Load())
	}
	if row := readRow(t, gdb, item.ID); row.Transport != models.TransportSMTP {
		t.Fatalf("row transport %q, want %q", row.Transport, models.TransportSMTP)
	}
}

// An ordinary first message between two REST Mail installs (#291): the
// receiver's default inbound pipeline greylists it, and its deliver handler
// answers 503 "deferred". The row stays queued on the greylisting schedule, its
// first retry no sooner than the greylist's five-minute delay, and the retry
// goes by RESTMAIL and is accepted. No attempt goes by SMTP, whether the peer
// was identified by the EHLO probe or by the capability cache, and whatever
// the EHLO says on the retry.
func TestNoSMTPFallback_GreylistDeferredThenAccepted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cached bool
	}{{"identified by probe", false}, {"identified by cache", true}} {
		t.Run(tc.name, func(t *testing.T) {
			domain := "nofallback-greylist-" + strings.ReplaceAll(tc.name, " ", "-") + ".test"
			mx := newNoFallbackMX(t, !tc.cached)
			peer := &restPeer{}
			peer.discovery.Store(http.StatusOK)
			peer.post.Store(http.StatusAccepted)
			peer.deferFirst.Store(1)
			w, gdb, item := noFallbackWorker(t, domain, mx, peer, nil)
			if tc.cached {
				cacheRESTMAIL(t, gdb, domain, true)
			}

			now := time.Now()
			err := w.deliver(context.Background(), item)
			noSMTP(t, gdb, item, mx, err, false)
			if got := ClassifyFailure(err); got != models.FailureGreylisted {
				t.Fatalf("deferred answer classified %s, want %s: %v", got, models.FailureGreylisted, err)
			}
			item.Attempts = 1
			w.recordFailure(context.Background(), item, err, now)
			row := readRow(t, gdb, item.ID)
			if row.Status != "deferred" || row.FailureCategory != models.FailureGreylisted {
				t.Fatalf("row status %q, category %q: want deferred, greylisted", row.Status, row.FailureCategory)
			}
			if wait := row.NextAttempt.Sub(now); wait < 5*time.Minute {
				t.Fatalf("first retry after %s: before the receiver's five-minute greylist delay ends", wait)
			}

			// The retry: the EHLO no longer advertising RESTMAIL changes nothing.
			mx.restmail.Store(false)
			if err := w.deliver(context.Background(), row); err != nil {
				t.Fatalf("retry: %v", err)
			}
			if mx.mails.Load() != 0 || peer.posts.Load() != 2 {
				t.Fatalf("MAIL commands %d, RESTMAIL POSTs %d: want both attempts by RESTMAIL only", mx.mails.Load(), peer.posts.Load())
			}
			cachedSupported(t, gdb, domain)
		})
	}
}

// A REST Mail server's "deferred" refusal is retried on the greylisting
// schedule; any other 503 stays an ordinary transient failure.
func TestRESTMAILRefusalDeferredIsGreylisting(t *testing.T) {
	for _, tc := range []struct {
		body string
		want models.FailureCategory
	}{
		{`{"error":{"code":"deferred","message":"Message deferred, retry later"}}`, models.FailureGreylisted},
		{`{"error":{"code":"internal_error","message":"Internal server error"}}`, models.FailureOtherTransient},
		{`not json`, models.FailureOtherTransient},
	} {
		if got := ClassifyFailure(restmailRefusal(http.StatusServiceUnavailable, []byte(tc.body))); got != tc.want {
			t.Errorf("503 %s classified %s, want %s", tc.body, got, tc.want)
		}
	}
	if got := ClassifyFailure(restmailRefusal(http.StatusForbidden, []byte(`{"error":{"code":"deferred"}}`))); !got.Permanent() {
		t.Errorf("403 with code deferred classified %s, want permanent", got)
	}
}
