package auditanchor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/restmail/restmail/internal/audit"
	"github.com/restmail/restmail/internal/db/models"
	rmail "github.com/restmail/restmail/internal/mail"
	"gorm.io/gorm"
)

const httpsAction = "audit.anchor.https"

// The HTTPS target keeps its own lock and schedule, so a slow or failing
// endpoint never holds up or skips an email anchor.
const httpsPublicationLock int64 = 0x6175647468747470 // "audthttp"

const httpsAttempts = 3
const httpsAttemptTimeout = 10 * time.Second

// httpsTarget POSTs the anchor text to an endpoint on another host. It does
// not follow redirects: the operator configured one URL, and only that host
// is told the anchor.
type httpsTarget struct {
	url, token string
	client     *http.Client
	backoff    time.Duration
}

type httpsDelivery struct {
	result   string // accepted, refused or unreachable
	status   int
	attempts int
	err      error
}

// post tries up to httpsAttempts times, doubling the wait between attempts.
// Network errors, 408, 429 and 5xx are retried; any other answer is final.
func (t *httpsTarget) post(ctx context.Context, body string) httpsDelivery {
	var d httpsDelivery
	wait := t.backoff
	for d.attempts < httpsAttempts {
		if d.attempts > 0 {
			select {
			case <-ctx.Done():
				return d
			case <-time.After(wait):
			}
			wait *= 2
		}
		d.attempts++
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, strings.NewReader(body))
		if err != nil {
			d.result, d.err = "unreachable", err
			return d
		}
		req.Header.Set("Content-Type", "text/plain; charset=utf-8")
		if t.token != "" {
			req.Header.Set("Authorization", "Bearer "+t.token)
		}
		resp, err := t.client.Do(req)
		if err != nil {
			d.result, d.status, d.err = "unreachable", 0, err
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
		d.status = resp.StatusCode
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			d.result, d.err = "accepted", nil
			return d
		}
		d.result, d.err = "refused", fmt.Errorf("endpoint answered %s", resp.Status)
		if resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
			return d
		}
	}
	return d
}

// HTTPSWorker publishes the same anchors as the email Worker, on the same
// schedule, to an operator-configured HTTPS endpoint. Each attempt, accepted
// or not, is recorded as an audit.anchor.https entry.
type HTTPSWorker struct {
	db     *gorm.DB
	server string
	target *httpsTarget
	cancel context.CancelFunc
	done   sync.WaitGroup
}

// NewHTTPSWorker refuses anything but an https:// URL with a host. The token,
// when set, is sent as a bearer token and never written to the audit log.
func NewHTTPSWorker(db *gorm.DB, server, endpoint, token string) (*HTTPSWorker, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return nil, errors.New("audit anchor endpoint must be an https:// URL with a host and no credentials")
	}
	// Clone the default transport so an operator CA loaded at startup applies.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	client := &http.Client{
		Transport:     transport,
		Timeout:       httpsAttemptTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &HTTPSWorker{db: db, server: server, target: &httpsTarget{url: endpoint, token: token, client: client, backoff: 2 * time.Second}}, nil
}

// Start begins the background loop. Call once, then Shutdown before closing DB.
func (w *HTTPSWorker) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.done.Add(1)
	go func() {
		defer w.done.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			// Room for every attempt and its backoff, plus the database work.
			checkCtx, stop := context.WithTimeout(ctx, 50*time.Second)
			if _, err := w.Check(checkCtx, time.Now()); err != nil && ctx.Err() == nil {
				slog.Error("audit anchor https: publication failed", "error", err)
			}
			stop()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// Shutdown cancels an in-flight check and waits for its transaction to finish.
func (w *HTTPSWorker) Shutdown() {
	if w.cancel != nil {
		w.cancel()
		w.done.Wait()
	}
}

// Check posts a due anchor and records the attempt, returning true only when
// the endpoint accepted it and the audit entry committed. A refused or
// unreachable endpoint is recorded as a failed entry and returned as an error.
// Any attempt, failed or not, starts the hourly limit, so an endpoint that
// stays down is retried and recorded at most once an hour. The publication
// lock is held across the POST so API replicas never post the same anchor.
func (w *HTTPSWorker) Check(ctx context.Context, now time.Time) (bool, error) {
	if rmail.ContainsControlChar(w.server) || len(w.server) == 0 || len(w.server) > 255 {
		return false, fmt.Errorf("invalid audit anchor server name")
	}
	var delivery *httpsDelivery
	err := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked bool
		if err := tx.Raw("SELECT pg_try_advisory_xact_lock(?)", httpsPublicationLock).Scan(&locked).Error; err != nil {
			return err
		}
		if !locked {
			return nil
		}
		var attempt models.AuditEntry
		if err := tx.Where("action = ?", httpsAction).Order("seq DESC").Limit(1).Find(&attempt).Error; err != nil {
			return err
		}
		if attempt.Seq > 0 && now.Sub(attempt.OccurredAt) < minimumInterval {
			return nil
		}
		var previous models.AuditEntry
		if err := tx.Where("action = ? AND outcome = ?", httpsAction, models.AuditOutcomeSuccess).Order("seq DESC").Limit(1).Find(&previous).Error; err != nil {
			return err
		}
		last, err := previousHead(previous)
		if err != nil {
			return err
		}
		var head audit.Anchor
		if err := tx.Model(&models.AuditEntry{}).Select("seq, hash").Order("seq DESC").Limit(1).Scan(&head).Error; err != nil {
			return err
		}
		if head.Seq == 0 {
			return nil
		}
		if !validHash(head.Hash) {
			return fmt.Errorf("invalid audit chain head")
		}
		changes, err := adminChanges(tx, last.Seq, head.Seq)
		if err != nil {
			return err
		}
		if !due(now, previous, changes) {
			return nil
		}
		now = now.UTC().Truncate(time.Microsecond)
		d := w.target.post(ctx, anchorBody(w.server, now, head, last))
		entry := models.AuditEntry{
			OccurredAt: now, ActorKind: models.AuditActorSystem, Action: httpsAction, Outcome: models.AuditOutcomeSuccess,
			Target: models.AuditTarget{"seq": strconv.FormatInt(head.Seq, 10), "hash": head.Hash, "previous_seq": strconv.FormatInt(last.Seq, 10), "previous_hash": last.Hash,
				"endpoint": hostOf(w.target.url), "delivery": d.result, "attempts": strconv.Itoa(d.attempts)},
		}
		if d.status != 0 {
			entry.Target["status"] = strconv.Itoa(d.status)
		}
		if d.result != "accepted" {
			entry.Outcome = models.AuditOutcomeFailed
		}
		if err := audit.Append(tx, &entry); err != nil {
			return err
		}
		delivery = &d
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("record audit anchor https: %w", err)
	}
	if delivery == nil {
		return false, nil
	}
	if delivery.result != "accepted" {
		return false, fmt.Errorf("audit anchor https %s after %d attempts: %v", delivery.result, delivery.attempts, delivery.err)
	}
	slog.Info("audit anchor https accepted", "endpoint", hostOf(w.target.url))
	return true, nil
}

func hostOf(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	return u.Host
}
