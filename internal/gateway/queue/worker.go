package queue

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/smtp"
	"strings"
	"sync"
	"time"

	"github.com/rest-mail/go-mtasts"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/metrics"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SMTPError represents a structured SMTP error with response code.
type SMTPError struct {
	Code     int    // 3-digit SMTP code (e.g. 550)
	Enhanced string // Enhanced status code (e.g. "5.1.1")
	Message  string // Human-readable message
}

func (e *SMTPError) Error() string {
	if e.Enhanced != "" {
		return fmt.Sprintf("SMTP %d %s: %s", e.Code, e.Enhanced, e.Message)
	}
	return fmt.Sprintf("SMTP %d: %s", e.Code, e.Message)
}

func (e *SMTPError) IsPermanent() bool {
	return e.Code >= 500 && e.Code < 600
}

// parseSMTPError extracts SMTP status code from a net/smtp error string.
func parseSMTPError(err error) *SMTPError {
	msg := err.Error()
	// Try to parse "NNN X.X.X rest" or "NNN rest"
	if len(msg) >= 3 && msg[0] >= '1' && msg[0] <= '5' {
		code := 0
		_, _ = fmt.Sscanf(msg[:3], "%d", &code)
		if code >= 100 && code <= 599 {
			rest := strings.TrimSpace(msg[3:])
			enhanced := ""
			if len(rest) > 0 && rest[0] >= '1' && rest[0] <= '5' {
				parts := strings.SplitN(rest, " ", 2)
				if len(parts[0]) >= 5 && strings.Count(parts[0], ".") == 2 {
					enhanced = parts[0]
					if len(parts) > 1 {
						rest = parts[1]
					} else {
						rest = ""
					}
				}
			}
			return &SMTPError{Code: code, Enhanced: enhanced, Message: rest}
		}
	}
	return &SMTPError{Code: 0, Message: msg}
}

// staleDeliveringTimeout is the DEFAULT interval a row may sit in "delivering"
// before the worker treats it as orphaned by a crashed worker and reclaims it.
// The effective interval is Worker.reclaim, which SetDeliveryDeadline can raise
// so it always exceeds the worst-case single-send budget (OSI-7) — otherwise a
// legitimately slow max-size send would be reclaimed mid-flight and duplicated.
const staleDeliveringTimeout = 15 * time.Minute

// defaultSendDeadline is the per-attempt send budget used when the caller has not
// configured a size-aware one via SetDeliveryDeadline. It matches the historical
// fixed 30 s deadline, so behavior is unchanged until wired to the config.
func defaultSendDeadline(int64) time.Duration { return 30 * time.Second }

// computeBackoff returns the retry delay for the Nth delivery attempt:
// exponential (1m, 2m, 4m, 8m, ...) capped at 4 hours.
//
// The exponent MUST be guarded. The obvious `2^attempts * time.Minute`
// overflows int64 once attempts is large and wraps to a NEGATIVE duration
// that slips under a naive `> 4h` cap, scheduling the retry in the past so it
// fires immediately forever. 2^8 minutes (~4.3h) already exceeds the cap, so
// anything at or beyond attempt 8 is clamped.
func computeBackoff(attempts int) time.Duration {
	const maxBackoff = 4 * time.Hour
	if attempts < 0 {
		attempts = 0
	}
	if attempts >= 8 {
		return maxBackoff
	}
	if b := time.Duration(1<<uint(attempts)) * time.Minute; b < maxBackoff {
		return b
	}
	return maxBackoff
}

// Worker processes outbound mail queue entries.
type Worker struct {
	db           *gorm.DB
	hostname     string
	numWorkers   int
	pollInterval time.Duration
	tlsInsecure  bool
	stsEnforce   bool
	sts          *mtasts.Resolver
	shutdown     chan struct{}
	wg           sync.WaitGroup

	// Bounce/DSN anti-mailbomb controls (OSI-25). bounceMax caps how many DSNs
	// may be delivered into a single recipient mailbox within bounceWindow;
	// bounceMax <= 0 disables the cap. See generateBounce.
	bounceMax    int
	bounceWindow time.Duration

	// Size-aware delivery timing (OSI-7). sendDeadline maps a message's byte size
	// to the maximum time a single outbound send attempt (SMTP to a peer MX, or
	// the RESTMAIL HTTPS hop) may take, so a large-but-permitted message is not
	// cut off by a fixed short deadline. reclaim is how long a row may sit
	// "delivering" before being reclaimed as orphaned; it is kept strictly larger
	// than sendDeadline(max size) so an in-flight slow large send completes before
	// it can be reclaimed and duplicated. Defaults preserve the historical fixed
	// 30 s send / 15 min reclaim; SetDeliveryDeadline wires the config-derived,
	// size-aware values.
	sendDeadline func(sizeBytes int64) time.Duration
	reclaim      time.Duration
}

const (
	// defaultBounceMaxPerRecipient bounds DSNs delivered into one mailbox per
	// window when the caller has not configured a limit (OSI-25). It is generous
	// enough never to drop a legitimate low-volume bounce stream while still
	// capping a spoofed-sender mail-bombing amplification.
	defaultBounceMaxPerRecipient = 20
	// defaultBounceWindow is the sliding window over which bounceMax is counted.
	defaultBounceWindow = time.Hour
)

// NewWorker creates a new queue worker.
func NewWorker(db *gorm.DB, hostname string, numWorkers int, pollInterval time.Duration) *Worker {
	w := &Worker{
		db:           db,
		hostname:     hostname,
		numWorkers:   numWorkers,
		pollInterval: pollInterval,
		tlsInsecure:  false,
		stsEnforce:   true,
		shutdown:     make(chan struct{}),
		bounceMax:    defaultBounceMaxPerRecipient,
		bounceWindow: defaultBounceWindow,
		sendDeadline: defaultSendDeadline,
		reclaim:      staleDeliveringTimeout,
	}
	// MTA-STS resolver: real DNS lookups, HTTPS policy fetch whose certificate
	// verification tracks the worker's tlsInsecure flag at call time.
	w.sts = mtasts.NewResolver()
	w.sts.FetchPolicy = func(ctx context.Context, url string) ([]byte, error) {
		return mtasts.HTTPFetch(ctx, url, w.tlsInsecure)
	}
	return w
}

// SetTLSInsecure sets whether to skip TLS certificate verification for outbound delivery.
func (w *Worker) SetTLSInsecure(insecure bool) {
	w.tlsInsecure = insecure
}

// SetMTASTSEnforce controls whether "enforce"-mode MTA-STS policies are honored
// on outbound delivery. When false (e.g. a dev/test deployment that cannot
// verify real certificates) an enforce policy is downgraded to "testing":
// TLS is still attempted and would-fail conditions are logged, but delivery is
// not blocked.
func (w *Worker) SetMTASTSEnforce(enforce bool) {
	w.stsEnforce = enforce
}

// SetBounceRateLimit configures the per-recipient DSN cap (OSI-25): at most max
// bounce messages are delivered into any one mailbox within window. A max <= 0
// disables the cap; a non-positive window falls back to the default window.
func (w *Worker) SetBounceRateLimit(max int, window time.Duration) {
	w.bounceMax = max
	if window <= 0 {
		window = defaultBounceWindow
	}
	w.bounceWindow = window
}

// SetDeliveryDeadline configures the size-aware per-attempt send budget and the
// stale-delivering reclaim interval (OSI-7). deadlineFn maps a message's byte
// size to the maximum time one outbound send attempt may take; reclaim is how
// long a row may sit "delivering" before the worker reclaims it as orphaned.
//
// The caller MUST supply a reclaim that strictly exceeds deadlineFn(max message
// size): if reclaim were shorter, a legitimately slow max-size send still in
// flight would be reclaimed by another worker and delivered a second time. A nil
// deadlineFn or a non-positive reclaim is ignored (the safe default is kept), so
// a misconfiguration can never shorten reclaim below a send budget.
func (w *Worker) SetDeliveryDeadline(deadlineFn func(sizeBytes int64) time.Duration, reclaim time.Duration) {
	if deadlineFn != nil {
		w.sendDeadline = deadlineFn
	}
	if reclaim > 0 {
		w.reclaim = reclaim
	}
}

// Start begins processing the outbound queue.
func (w *Worker) Start() {
	slog.Info("queue: starting workers", "count", w.numWorkers, "poll_interval", w.pollInterval)

	for i := 0; i < w.numWorkers; i++ {
		w.wg.Add(1)
		go func(workerID int) {
			defer w.wg.Done()
			w.processLoop(workerID)
		}(i)
	}
}

// Shutdown stops the queue workers gracefully.
func (w *Worker) Shutdown() {
	close(w.shutdown)
	w.wg.Wait()
	slog.Info("queue: workers stopped")
}

func (w *Worker) processLoop(workerID int) {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.shutdown:
			return
		case <-ticker.C:
			w.processOne(workerID)
		}
	}
}

func (w *Worker) processOne(workerID int) {
	// Claim a pending item using raw SQL with FOR UPDATE SKIP LOCKED
	var item models.OutboundQueue
	now := time.Now()
	// Rows that have been "delivering" longer than w.reclaim were almost certainly
	// orphaned by a worker that crashed mid-send: a live send is bounded by the
	// size-aware send deadline (always < w.reclaim), and holds the row's
	// transaction lock. Reclaim them so mail isn't stranded forever (the claim
	// below only re-selects pending/deferred). w.reclaim exceeds the worst-case
	// max-size send budget so a slow-but-legitimate large send is never reclaimed
	// mid-flight and duplicated (OSI-7).
	staleDelivering := now.Add(-w.reclaim)
	err := w.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Raw(
			`SELECT * FROM outbound_queue
			 WHERE (
			     (status IN ('pending', 'deferred') AND next_attempt <= ?)
			     OR (status = 'delivering' AND last_attempt < ?)
			 ) AND expires_at > ?
			 ORDER BY next_attempt ASC
			 LIMIT 1
			 FOR UPDATE SKIP LOCKED`, now, staleDelivering, now,
		).Scan(&item)

		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}

		// Mark as delivering
		return tx.Model(&item).Updates(map[string]interface{}{
			"status":       "delivering",
			"last_attempt": now,
			"attempts":     item.Attempts + 1,
		}).Error
	})

	if err != nil {
		// No items to process (or DB error)
		return
	}

	slog.Info("queue: processing",
		"worker", workerID,
		"id", item.ID,
		"sender", item.Sender,
		"recipient", item.Recipient,
		"domain", item.Domain,
		"attempt", item.Attempts+1,
	)

	// Attempt delivery
	deliveryErr := w.deliver(item)

	if deliveryErr == nil {
		// Success
		w.db.Model(&item).Updates(map[string]interface{}{
			"status": "delivered",
		})
		// Always-on outbound aggregate metrics (atomic, lock-free). This runs in
		// the gateway process; the counter surfaces on that process's /metrics.
		metrics.MessagesSent.Inc()
		metrics.PipelineTerminal.WithLabelValues("outbound", "delivered").Inc()
		slog.Info("queue: delivered", "id", item.ID, "recipient", item.Recipient)
		return
	}

	// Extract SMTP error code if available
	var smtpErr *SMTPError
	var errorCode int
	if se, ok := deliveryErr.(*SMTPError); ok {
		smtpErr = se
		errorCode = se.Code
	}

	slog.Warn("queue: delivery failed",
		"id", item.ID,
		"recipient", item.Recipient,
		"attempt", item.Attempts+1,
		"error", deliveryErr,
		"smtp_code", errorCode,
	)

	// Permanent failure (5xx) — bounce immediately, don't retry
	if smtpErr != nil && smtpErr.IsPermanent() {
		w.db.Model(&item).Updates(map[string]interface{}{
			"status":          "bounced",
			"last_error":      deliveryErr.Error(),
			"last_error_code": errorCode,
		})
		slog.Warn("queue: permanent failure, bouncing", "id", item.ID, "smtp_code", errorCode)
		w.generateBounce(item, smtpErr)
		return
	}

	// Check if we should retry or bounce (max retries exhausted)
	if item.Attempts+1 >= item.MaxRetries {
		w.db.Model(&item).Updates(map[string]interface{}{
			"status":          "bounced",
			"last_error":      deliveryErr.Error(),
			"last_error_code": errorCode,
		})
		slog.Warn("queue: message bounced (max retries)", "id", item.ID, "recipient", item.Recipient)
		w.generateBounce(item, &SMTPError{Code: 0, Message: deliveryErr.Error()})
		return
	}

	backoff := computeBackoff(item.Attempts)

	w.db.Model(&item).Updates(map[string]interface{}{
		"status":          "deferred",
		"next_attempt":    time.Now().Add(backoff),
		"last_error":      deliveryErr.Error(),
		"last_error_code": errorCode,
	})
}

// deliver attempts to send a message via SMTP to the destination MX.
// It first checks if the primary MX supports the RESTMAIL protocol for
// direct HTTPS delivery, falling back to standard SMTP if not.
func (w *Worker) deliver(item models.OutboundQueue) error {
	// Look up MX records
	mxRecords, err := net.LookupMX(item.Domain)
	if err != nil {
		// RFC 5321 implicit MX: a domain with no MX records is still
		// deliverable via its A/AAAA record. Go's LookupMX reports "no such
		// host" both for NXDOMAIN and for a host that simply has no MX (and
		// some resolvers — e.g. Docker's embedded DNS answering for container
		// hostnames — serve A records but no MX at all), so fall back to the
		// domain itself and let the dial resolve it. A truly unresolvable host
		// then fails at connect time and defers as usual. Other lookup errors
		// (e.g. resolver unreachable) still defer here.
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			mxRecords = []*net.MX{{Host: item.Domain, Pref: 0}}
		} else {
			return fmt.Errorf("MX lookup failed for %s: %w", item.Domain, err)
		}
	}

	if len(mxRecords) == 0 {
		// Fall back to A record
		mxRecords = []*net.MX{{Host: item.Domain, Pref: 0}}
	}

	firstHost := strings.TrimSuffix(mxRecords[0].Host, ".")

	// Check capability cache before EHLO probe. The cached entry is bound to the
	// MX host that advertised it (OSI-20): a hit requires the stored row to be
	// for THIS domain's current primary MX host and still within its TTL. If the
	// primary MX has changed since the entry was learned, capabilityApplies
	// returns false and we re-probe rather than trusting a capability advertised
	// by a different (possibly rogue) host.
	var cap models.RESTMAILCapability
	found := w.db.Where("domain = ?", item.Domain).First(&cap).Error == nil
	cacheHit := found && capabilityApplies(cap, item.Domain, firstHost, time.Now())

	if cacheHit {
		if cap.Supported {
			slog.Info("queue: using cached RESTMAIL capability", "domain", item.Domain, "url", cap.EndpointURL)
			err := w.deliverRESTMAILHTTPS(cap.EndpointURL, item)
			if err == nil {
				return nil
			}
			slog.Warn("queue: cached RESTMAIL delivery failed, invalidating cache",
				"domain", item.Domain, "error", err)
			w.db.Where("domain = ?", item.Domain).Delete(&models.RESTMAILCapability{})
			// Fall through to SMTP
		}
		// cap.Supported == false: skip RESTMAIL, go straight to SMTP
	} else {
		// No cache or expired — do EHLO probe
		upgraded, err := w.tryRESTMAIL(firstHost, item)
		if upgraded && err == nil {
			return nil // RESTMAIL delivery succeeded
		}
		if upgraded && err != nil {
			slog.Warn("queue: RESTMAIL delivery failed, falling back to SMTP",
				"host", firstHost, "error", err)
		}
	}

	// Discover the recipient domain's MTA-STS policy (RFC 8461). Discovery is
	// fail-open: a missing/unreachable policy leaves stsPolicy nil and delivery
	// proceeds with ordinary opportunistic TLS.
	var stsPolicy *mtasts.Policy
	stsCtx, stsCancel := context.WithTimeout(context.Background(), 15*time.Second)
	policy, err := w.sts.Resolve(stsCtx, item.Domain)
	stsCancel()
	if err != nil {
		slog.Debug("queue: MTA-STS not available, proceeding normally",
			"domain", item.Domain, "error", err)
	} else {
		stsPolicy = policy
		slog.Info("queue: MTA-STS policy found",
			"domain", item.Domain, "mode", policy.Mode, "mx_count", len(policy.MX), "enforce", w.stsEnforce)
	}

	// Whether an enforce policy is actively enforced by this worker. When
	// enforcement is disabled, an enforce policy behaves like "testing".
	enforcing := stsPolicy != nil && stsPolicy.Mode == mtasts.ModeEnforce && w.stsEnforce

	// Fall back to SMTP delivery
	var lastErr error
	for _, mx := range mxRecords {
		host := strings.TrimSuffix(mx.Host, ".")

		// MTA-STS MX validation: under enforce, only connect to MX hosts named
		// by the policy (RFC 8461 section 4.1).
		if stsPolicy != nil && len(stsPolicy.MX) > 0 && !stsPolicy.MatchesMX(host) {
			if enforcing {
				slog.Warn("queue: MTA-STS enforce — MX host not named by policy, skipping",
					"host", host, "domain", item.Domain)
				lastErr = &mtasts.EnforceError{Domain: item.Domain, MXHost: host, Reason: "MX host not named by policy"}
				continue
			}
			slog.Warn("queue: MTA-STS — MX host not named by policy, delivering anyway",
				"host", host, "domain", item.Domain, "mode", stsPolicy.Mode)
		}

		lastErr = w.deliverToHost(host, item, stsPolicy)
		if lastErr == nil {
			return nil
		}
		slog.Debug("queue: MX attempt failed", "host", host, "error", lastErr)
	}

	return fmt.Errorf("all MX hosts failed: %w", lastErr)
}

// capabilityApplies reports whether a cached RESTMAIL capability row may be
// reused for a delivery to domain whose current primary MX is mxHost, as of now
// (OSI-20). A cached capability is bound to the exact MX host that advertised it:
// reuse requires the same domain, the same primary MX host, and an unexpired TTL.
// If the domain's primary MX has rotated — or a different/rogue host has started
// answering on a shared relay — the entry no longer applies and the caller must
// re-probe rather than trust a capability learned from another host.
func capabilityApplies(c models.RESTMAILCapability, domain, mxHost string, now time.Time) bool {
	return c.Domain == domain && c.MXHost == mxHost && c.ExpiresAt.After(now)
}

// cacheCapability stores a RESTMAIL capability probe result in the database,
// bound to the MX host that advertised it (OSI-20). Only one row is kept per
// domain; probing a new primary MX replaces the previous host's entry.
func (w *Worker) cacheCapability(domain, mxHost string, supported bool, endpointURL string) {
	now := time.Now()
	ttl := 15 * time.Minute
	if supported {
		ttl = 1 * time.Hour
	}

	cap := models.RESTMAILCapability{
		Domain:      domain,
		MXHost:      mxHost,
		Supported:   supported,
		EndpointURL: endpointURL,
		LastProbed:  now,
		ExpiresAt:   now.Add(ttl),
	}

	w.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "domain"}},
		DoUpdates: clause.AssignmentColumns([]string{"mx_host", "supported", "endpoint_url", "last_probed", "expires_at"}),
	}).Create(&cap)
}

// deliverRESTMAILHTTPS sends the message via HTTPS POST to a known RESTMAIL endpoint.
func (w *Worker) deliverRESTMAILHTTPS(endpointURL string, item models.OutboundQueue) error {
	payload := map[string]interface{}{
		"from":        item.Sender,
		"to":          []string{item.Recipient},
		"raw_message": item.RawMessage,
	}
	payloadBytes, _ := json.Marshal(payload)

	messagesURL := endpointURL + "/messages"
	if !strings.HasPrefix(messagesURL, "http") {
		messagesURL = "https://" + messagesURL
	}

	httpClient := &http.Client{
		// Size-aware upload budget (OSI-7): the payload carries the full raw
		// message, so a fixed 30 s cannot transfer a large body. Bounded — a
		// finite function of the message size — so this hop stays slowloris-safe.
		Timeout: w.sendDeadline(int64(len(item.RawMessage))),
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: w.tlsInsecure},
		},
	}
	resp, err := httpClient.Post(messagesURL, "application/json", bytes.NewReader(payloadBytes))
	if err != nil {
		return fmt.Errorf("RESTMAIL POST to %s: %w", messagesURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		slog.Info("queue: RESTMAIL delivery succeeded", "url", messagesURL, "recipient", item.Recipient)
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("RESTMAIL delivery got %d: %s", resp.StatusCode, string(body))
}

// tryRESTMAIL probes a host for the RESTMAIL EHLO extension. If found,
// it delivers the message via HTTPS POST instead of SMTP.
// Returns (true, nil) on successful RESTMAIL delivery,
// (true, err) if RESTMAIL was detected but delivery failed,
// (false, nil) if the host does not support RESTMAIL.
func (w *Worker) tryRESTMAIL(host string, item models.OutboundQueue) (upgraded bool, err error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.Dial("tcp", host+":25")
	if err != nil {
		return false, nil // Can't connect, let SMTP path handle it
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return false, nil
	}

	if err := client.Hello(w.hostname); err != nil {
		_ = client.Close()
		return false, nil
	}

	ok, restmailURL := client.Extension("RESTMAIL")
	_ = client.Quit()
	_ = client.Close()

	if !ok || restmailURL == "" {
		w.cacheCapability(item.Domain, host, false, "")
		return false, nil // No RESTMAIL support
	}

	slog.Info("queue: RESTMAIL capability detected", "host", host, "url", restmailURL)
	w.cacheCapability(item.Domain, host, true, restmailURL)

	err = w.deliverRESTMAILHTTPS(restmailURL, item)
	if err != nil {
		return true, err
	}
	return true, nil
}

// bounceSenderAuthorized reports whether a DSN for a failed outbound item may be
// delivered into the sender's local mailbox (OSI-25). A bounce is only trustworthy
// if the queue row's Sender genuinely belongs to the account that submitted the
// message.
//
// When the queue row is linked to a stored sent message (hasMessage — the
// authenticated webmail/API send path sets OutboundQueue.MessageID), that
// message MUST belong to the same mailbox the bounce would land in
// (messageMailboxID == senderMailboxID). A mismatch is positive evidence that
// the row's Sender was forged relative to the real submission, so the DSN is
// suppressed — this is the anti mail-bombing check.
//
// When there is no linked message (hasMessage false: a message row that no
// longer exists, or a system-generated report / SMTP submission that carries no
// message id) there is nothing to contradict the sender, so the bounce proceeds
// — still subject to the per-recipient rate limit. This deliberately fails toward
// delivering legitimate DSNs (only a proven forgery is dropped), honoring the
// rule that bounces are how legitimate mail flow signals failure.
func bounceSenderAuthorized(hasMessage bool, messageMailboxID, senderMailboxID uint) bool {
	if !hasMessage {
		return true
	}
	return messageMailboxID == senderMailboxID
}

// withinDSNRateLimit reports whether delivering one more DSN into a recipient
// mailbox stays within the configured per-window cap (OSI-25). recentCount is how
// many DSNs have already been delivered into that mailbox within the window. A
// maxPerWindow <= 0 disables the limit.
func withinDSNRateLimit(recentCount, maxPerWindow int) bool {
	if maxPerWindow <= 0 {
		return true
	}
	return recentCount < maxPerWindow
}

// generateBounce creates an RFC 3464 DSN (Delivery Status Notification)
// and delivers it to the original sender's mailbox if the sender is local.
func (w *Worker) generateBounce(item models.OutboundQueue, smtpErr *SMTPError) {
	// Check if the sender has a local mailbox
	var senderMailbox struct {
		ID     uint
		Active bool
	}
	result := w.db.Raw("SELECT id, active FROM mailboxes WHERE address = ? AND active = true LIMIT 1", item.Sender).Scan(&senderMailbox)
	if result.Error != nil || result.RowsAffected == 0 {
		slog.Debug("queue: bounce sender not local, discarding DSN", "sender", item.Sender)
		return
	}

	// Sender authentication (OSI-25): when the queue row is linked to a sent
	// message, that message must belong to the mailbox we would deliver the DSN
	// into. A mismatch means the row's Sender was spoofed relative to the actual
	// submission, so we refuse to bounce into that mailbox (anti mail-bombing).
	if item.MessageID != nil {
		var msgMailbox struct{ MailboxID uint }
		mres := w.db.Raw("SELECT mailbox_id FROM messages WHERE id = ? LIMIT 1", *item.MessageID).Scan(&msgMailbox)
		msgFound := mres.Error == nil && mres.RowsAffected > 0
		if !bounceSenderAuthorized(msgFound, msgMailbox.MailboxID, senderMailbox.ID) {
			slog.Warn("queue: bounce sender not authorized for submitted message, suppressing DSN",
				"sender", item.Sender, "message_id", *item.MessageID)
			return
		}
	}

	// Per-recipient DSN rate limit (OSI-25): cap how many bounces may land in one
	// mailbox within the window, bounding a spoofed-sender mail-bombing regardless
	// of the authentication check above.
	if w.bounceMax > 0 {
		var recent int64
		w.db.Table("messages").
			Where("mailbox_id = ? AND sender = ? AND created_at > ?",
				senderMailbox.ID, "mailer-daemon@"+w.hostname, time.Now().Add(-w.bounceWindow)).
			Count(&recent)
		if !withinDSNRateLimit(int(recent), w.bounceMax) {
			slog.Warn("queue: DSN rate limit exceeded for recipient, suppressing DSN",
				"sender", item.Sender, "recent", recent, "max", w.bounceMax, "window", w.bounceWindow)
			return
		}
	}

	now := time.Now()
	boundary := fmt.Sprintf("=_restmail_dsn_%d", now.UnixNano())
	msgID := fmt.Sprintf("<dsn-%d-%d@%s>", item.ID, now.UnixNano(), w.hostname)

	statusCode := "5.0.0"
	diagnosticCode := "smtp; delivery failed"
	if smtpErr != nil {
		if smtpErr.Enhanced != "" {
			statusCode = smtpErr.Enhanced
		} else if smtpErr.Code >= 500 {
			statusCode = fmt.Sprintf("%d.0.0", smtpErr.Code/100)
		}
		if smtpErr.Code > 0 {
			diagnosticCode = fmt.Sprintf("smtp; %d %s", smtpErr.Code, smtpErr.Message)
		} else {
			diagnosticCode = fmt.Sprintf("smtp; %s", smtpErr.Message)
		}
	}

	// Extract original headers from RawMessage for Part 3
	originalHeaders := item.RawMessage
	if idx := strings.Index(originalHeaders, "\r\n\r\n"); idx >= 0 {
		originalHeaders = originalHeaders[:idx]
	} else if idx := strings.Index(originalHeaders, "\n\n"); idx >= 0 {
		originalHeaders = originalHeaders[:idx]
	}

	// Part 1: Human-readable
	humanPart := fmt.Sprintf(
		"This is the mail delivery system at %s.\r\n\r\n"+
			"Your message could not be delivered to the following recipient:\r\n\r\n"+
			"    %s\r\n\r\n"+
			"The delivery has been attempted %d time(s).\r\n\r\n"+
			"Error: %s\r\n",
		w.hostname, item.Recipient, item.Attempts, diagnosticCode,
	)

	// Part 2: Machine-readable DSN (RFC 3464)
	dsnPart := fmt.Sprintf(
		"Reporting-MTA: dns; %s\r\n"+
			"Arrival-Date: %s\r\n\r\n"+
			"Final-Recipient: rfc822; %s\r\n"+
			"Action: failed\r\n"+
			"Status: %s\r\n"+
			"Diagnostic-Code: %s\r\n",
		w.hostname,
		item.CreatedAt.Format(time.RFC1123Z),
		item.Recipient,
		statusCode,
		diagnosticCode,
	)

	// Build full multipart/report message
	bounceSubject := fmt.Sprintf("Undelivered Mail Returned to Sender <%s>", item.Recipient)

	var b strings.Builder
	b.WriteString("From: mailer-daemon@" + w.hostname + "\r\n")
	b.WriteString("To: " + item.Sender + "\r\n")
	b.WriteString("Subject: " + bounceSubject + "\r\n")
	b.WriteString("Date: " + now.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Message-ID: " + msgID + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: multipart/report; report-type=delivery-status; boundary=\"" + boundary + "\"\r\n")
	b.WriteString("\r\n")
	// Part 1
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(humanPart + "\r\n")
	// Part 2
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: message/delivery-status\r\n\r\n")
	b.WriteString(dsnPart + "\r\n")
	// Part 3
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/rfc822-headers\r\n\r\n")
	b.WriteString(originalHeaders + "\r\n")
	b.WriteString("--" + boundary + "--\r\n")

	rawBounce := b.String()

	// Insert bounce message into sender's INBOX
	bounceMsg := map[string]interface{}{
		"mailbox_id":    senderMailbox.ID,
		"folder":        "INBOX",
		"sender":        "mailer-daemon@" + w.hostname,
		"sender_name":   "Mail Delivery System",
		"recipients_to": fmt.Sprintf(`["%s"]`, item.Sender),
		"recipients_cc": "[]",
		"subject":       bounceSubject,
		"body_text":     humanPart,
		"raw_message":   rawBounce,
		"is_read":       false,
		"size_bytes":    len(rawBounce),
		"received_at":   now,
		"created_at":    now,
		"updated_at":    now,
	}

	if err := w.db.Table("messages").Create(bounceMsg).Error; err != nil {
		slog.Error("queue: failed to insert bounce DSN", "sender", item.Sender, "error", err)
		return
	}

	slog.Info("queue: RFC 3464 bounce DSN delivered", "sender", item.Sender, "failed_recipient", item.Recipient)
}

// deliverToHost attempts SMTP delivery to a specific host.
//
// TLS is applied per the recipient's MTA-STS policy (RFC 8461):
//   - enforce: STARTTLS MUST succeed with a certificate that is valid for the
//     MX host (which the caller has already confirmed is named by the policy).
//     Certificate verification is forced on regardless of the worker's
//     tlsInsecure flag, and any failure returns a deferrable *EnforceError so
//     the queue retries rather than delivering in the clear.
//   - testing / none / no policy: opportunistic TLS as before; a would-fail
//     under "testing" is logged but delivery proceeds.
func (w *Worker) deliverToHost(host string, item models.OutboundQueue, stsPolicy *mtasts.Policy) error {
	// Size-aware whole-attempt budget (OSI-7): the fixed 30 s could not transfer
	// a large-but-permitted message's DATA phase. The deadline scales with the
	// message size and stays strictly below w.reclaim, so a slow large send
	// completes before the row is reclaimed (no duplicate delivery), while
	// remaining a finite bound (no slowloris). One absolute deadline bounds the
	// whole attempt (dial + the entire SMTP conversation), applied to the socket
	// below because net/smtp sets no deadlines of its own.
	attemptDeadline := time.Now().Add(w.sendDeadline(int64(len(item.RawMessage))))
	ctx, cancel := context.WithDeadline(context.Background(), attemptDeadline)
	defer cancel()

	addr := host + ":25"

	// Effective MTA-STS mode. When enforcement is disabled an enforce policy is
	// downgraded to testing (attempt TLS, log would-fail, still deliver).
	mode := ""
	if stsPolicy != nil {
		mode = stsPolicy.Mode
		if mode == mtasts.ModeEnforce && !w.stsEnforce {
			mode = mtasts.ModeTesting
		}
	}
	enforce := mode == mtasts.ModeEnforce
	testingMode := mode == mtasts.ModeTesting

	// Dial with timeout
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}

	// Bound the ENTIRE conversation — greeting, EHLO, STARTTLS handshake,
	// MAIL/RCPT, the DATA body write and the final reply — with the single
	// size-aware absolute deadline. net/smtp sets no deadlines, so without this a
	// remote that hangs mid-DATA would block the worker indefinitely. The deadline
	// persists across the StartTLS upgrade (the tls.Conn wraps this same socket).
	_ = conn.SetDeadline(attemptDeadline)

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("SMTP client for %s: %w", addr, err)
	}
	defer func() { _ = client.Close() }()

	// Say hello
	if err := client.Hello(w.hostname); err != nil {
		return fmt.Errorf("EHLO to %s: %w", host, err)
	}

	// Try STARTTLS if available. Under enforce, force certificate verification
	// on so that a successful handshake proves the cert is valid for host.
	starttls := false
	if ok, _ := client.Extension("STARTTLS"); ok {
		tlsConfig := &tls.Config{
			ServerName:         host,
			InsecureSkipVerify: w.tlsInsecure && !enforce, //nolint:gosec // enforce always verifies
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			if testingMode {
				slog.Warn("queue: MTA-STS testing — STARTTLS failed, delivering anyway",
					"host", host, "error", err)
			} else if !enforce {
				slog.Debug("queue: STARTTLS failed, continuing without TLS", "host", host, "error", err)
			}
			// Under enforce, starttls stays false and Evaluate defers below.
		} else {
			starttls = true
		}
	} else if testingMode {
		slog.Warn("queue: MTA-STS testing — host does not support STARTTLS, delivering anyway", "host", host)
	}

	// MTA-STS enforcement decision point. Under enforce this refuses cleartext
	// or an unverified certificate by returning a deferrable *EnforceError
	// (starttls succeeding under enforce implies the cert verified for host).
	if decision := mtasts.Evaluate(mtasts.EvalInput{
		Policy:    stsPolicy,
		Mode:      mode,
		Domain:    item.Domain,
		MXHost:    host,
		STARTTLS:  starttls,
		CertValid: starttls,
	}); decision != nil {
		slog.Warn("queue: MTA-STS enforce — refusing delivery", "host", host, "domain", item.Domain, "error", decision)
		return decision
	}

	// Set sender
	if err := client.Mail(item.Sender); err != nil {
		return parseSMTPError(err)
	}

	// Set recipient
	if err := client.Rcpt(item.Recipient); err != nil {
		return parseSMTPError(err)
	}

	// Send data
	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA to %s: %w", host, err)
	}
	_, err = fmt.Fprint(wc, item.RawMessage)
	if err != nil {
		wc.Close()
		return fmt.Errorf("write message to %s: %w", host, err)
	}
	if err := wc.Close(); err != nil {
		return parseSMTPError(err)
	}

	// Quit
	_ = client.Quit()
	return nil
}
