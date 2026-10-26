package queue

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/smtp"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rest-mail/go-mtasts/pkg/mtasts"
	"github.com/restmail/restmail/internal/dane"
	"github.com/restmail/restmail/internal/db/models"
	"github.com/restmail/restmail/internal/db/repositories"
	rmail "github.com/restmail/restmail/internal/mail"
	"github.com/restmail/restmail/internal/metrics"
	"github.com/restmail/restmail/internal/outbound"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SMTPError represents a structured SMTP error with response code.
type SMTPError struct {
	Code     int    // 3-digit SMTP code (e.g. 550)
	Enhanced string // Enhanced status code (e.g. "5.1.1")
	Message  string // Human-readable message
	// RemoteMTA is the next-hop host whose reply produced this error, when the
	// failure came from a specific peer (a MAIL/RCPT/DATA rejection). It is
	// plumbed into the DSN's Remote-MTA field (RFC 3464 §2.3.5). Empty for a
	// failure with no identified peer (e.g. a locally synthesized error).
	RemoteMTA string
}

func (e *SMTPError) Error() string {
	if e.Enhanced != "" {
		return fmt.Sprintf("SMTP %d %s: %s", e.Code, e.Enhanced, e.Message)
	}
	return fmt.Sprintf("SMTP %d: %s", e.Code, e.Message)
}

// smtpErrorFrom recovers a *SMTPError from anywhere in err's wrap chain. The
// delivery path returns the structured SMTP error wrapped (fmt.Errorf("… %w",
// lastErr)), so callers MUST use errors.As rather than a concrete type assertion
// — an assertion only matches the outermost error and would silently miss a
// wrapped permanent (5xx) failure, retrying it for the whole budget instead of
// bouncing. A reply net/smtp hands back as it is (a refusal at the greeting, to
// EHLO or to DATA) counts too. Returns nil when the failure carried no SMTP
// status (e.g. a connect/DNS/TLS error).
func smtpErrorFrom(err error) *SMTPError {
	var se *SMTPError
	if errors.As(err, &se) {
		return se
	}
	var reply *textproto.Error
	if errors.As(err, &reply) {
		return parseSMTPError(reply)
	}
	return nil
}

// dsnStatusCode maps an SMTP failure to the RFC 3463 enhanced status reported in
// the DSN. An explicit enhanced code from the peer is used verbatim; otherwise it
// is derived from the reply's major digit so a 4xx (e.g. a retry budget exhausted
// on repeated "451 try again later") is reported as a persistent-transient 4.x.y,
// not a fabricated permanent 5.0.0. A code-less failure (no SMTP reply — a
// connect/DNS/TLS error) falls back to a generic 5.0.0.
func dsnStatusCode(smtpErr *SMTPError) string {
	if smtpErr != nil {
		if smtpErr.Enhanced != "" {
			return smtpErr.Enhanced
		}
		if smtpErr.Code >= 400 && smtpErr.Code < 600 {
			return fmt.Sprintf("%d.0.0", smtpErr.Code/100)
		}
	}
	return "5.0.0"
}

// parseSMTPError reads the reply in an error net/smtp returned: its code, the
// RFC 3463 enhanced code its text starts with, if any, and the rest of the
// text. The reply's fields are read, not its Error() string, whose format is
// Go's to change (Go 1.27 quotes the text, which would hide the enhanced
// code). An error that carries no reply (the connection failed mid-command)
// becomes a code-less SMTPError with its text.
func parseSMTPError(err error) *SMTPError {
	var reply *textproto.Error
	if !errors.As(err, &reply) {
		return &SMTPError{Code: 0, Message: err.Error()}
	}
	text := strings.TrimSpace(reply.Msg)
	enhanced := ""
	if first, rest, _ := strings.Cut(text, " "); isEnhancedStatus(first) {
		enhanced, text = first, rest
	}
	return &SMTPError{Code: reply.Code, Enhanced: enhanced, Message: text}
}

// isEnhancedStatus reports whether s looks like an RFC 3463 status code:
// class.subject.detail, the class 2, 4 or 5.
func isEnhancedStatus(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 3 || len(parts[0]) != 1 || !strings.ContainsAny(parts[0], "245") {
		return false
	}
	for _, p := range parts[1:] {
		if p == "" || len(p) > 3 || strings.Trim(p, "0123456789") != "" {
			return false
		}
	}
	return true
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

// maxRESTMAILErrorBody caps how much of a non-2xx RESTMAIL response body is read
// into the delivery error (#169). The endpoint is attacker-advertised (an MX's
// RESTMAIL EHLO keyword), so an unbounded read of the error body would let a
// hostile endpoint exhaust worker memory; 64 KiB is far more than any legitimate
// error payload needs.
const maxRESTMAILErrorBody = 64 << 10

// messageRequires8BitTransport reports whether an outbound message can only be
// relayed over a channel that accepts 8-bit content — i.e. it must NOT be sent to
// a next hop that does not advertise 8BITMIME (RFC 6152). This is true when the
// submitting client declared BODY=8BITMIME or BODY=BINARYMIME, or when the raw
// bytes actually contain a non-ASCII octet (>= 0x80) regardless of what was
// declared (a client may under-declare). A 7-bit-clean message declared 8BITMIME
// still relays fine to a 7-bit peer, so the byte scan avoids refusing it need-
// lessly.
func messageRequires8BitTransport(bodyType, raw string) bool {
	switch strings.ToUpper(strings.TrimSpace(bodyType)) {
	case "8BITMIME", "BINARYMIME":
		return true
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] >= 0x80 {
			return true
		}
	}
	return false
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
	dane         daneLookup // nil: DANE off (#725)
	shutdown     chan struct{}
	wg           sync.WaitGroup

	// masterKey decrypts the domain's at-rest DKIM private key when the worker
	// signs an outbound message that arrived unsigned (SMTP submission, #171).
	// Empty only when encryption is disabled (dev), where keys are plaintext.
	masterKey string
	// relayRoots replaces system trust for relay certificates; tests only.
	relayRoots *x509.CertPool
	// resolver answers the MX and address lookups delivery makes; nil is the
	// system resolver. Tests only.
	resolver mxResolver
	// restmailRoots holds system roots plus any configured operator/testbed CA
	// for both RESTMAIL discovery and delivery. Nil uses system roots.
	restmailRoots *x509.CertPool
	// smtpRoots are the roots an MX's certificate is verified against; nil is the system's.
	// Tests set it to trust their own peer.
	smtpRoots *x509.CertPool
	// txtResolver answers the TXT lookups TLS-RPT reporting makes, and
	// tlsReportClient sends its HTTPS reports; nil is the system resolver and
	// a guarded client. Tests only.
	txtResolver     func(ctx context.Context, name string) ([]string, error)
	tlsReportClient *http.Client

	// Bounce/DSN anti-mailbomb controls (OSI-25). bounceMax caps how many DSNs
	// may be delivered into a single recipient mailbox within bounceWindow;
	// bounceMax <= 0 disables the cap. See generateBounce.
	bounceMax    int
	bounceWindow time.Duration

	// suppressionTTL is how long an address stays suppressed after it hard
	// bounces (#141). See suppression.go.
	suppressionTTL time.Duration

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

	// SSRF guard (#167). Outbound delivery dials whatever a recipient domain's
	// MX resolves to, and POSTs to whatever RESTMAIL endpoint an MX advertises —
	// both attacker-influenced. allowPrivateDest, when true, is an explicit
	// dev/testbed opt-in that disables the guard so delivery between containers
	// on a private bridge network works; it defaults false (deny), so a
	// production worker refuses to dial loopback/link-local/private/metadata
	// addresses. See ssrf.go.
	allowPrivateDest bool
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
		db:             db,
		hostname:       hostname,
		numWorkers:     numWorkers,
		pollInterval:   pollInterval,
		tlsInsecure:    false,
		stsEnforce:     true,
		shutdown:       make(chan struct{}),
		bounceMax:      defaultBounceMaxPerRecipient,
		bounceWindow:   defaultBounceWindow,
		sendDeadline:   defaultSendDeadline,
		suppressionTTL: DefaultSuppressionTTL,
		reclaim:        staleDeliveringTimeout,
	}
	// MTA-STS resolver: real DNS lookups, HTTPS policy fetch whose certificate
	// verification tracks the worker's tlsInsecure flag at call time.
	w.sts = mtasts.NewResolver()
	w.sts.FetchPolicy = func(ctx context.Context, url string) ([]byte, error) {
		return mtasts.HTTPFetch(ctx, url, w.tlsInsecure)
	}
	w.sts.RefreshFailed = logSTSRefreshFailed
	return w
}

// logSTSRefreshFailed reports a failed MTA-STS policy refresh. Delivery goes
// on under the cached policy until it expires, so this is the warning RFC
// 8461 §3.3 and §10.2 ask for: a domain's policy host or TXT record has broken
// (or is being blocked), and mail to it stops once max_age runs out.
func logSTSRefreshFailed(domain string, cached *mtasts.Policy, err error) {
	slog.Warn("mta-sts: policy refresh failed, using the cached policy",
		"domain", domain, "mode", cached.Mode, "error", err)
}

// SetTLSInsecure sets whether to skip TLS certificate verification for SMTP
// delivery. RESTMAIL discovery and delivery always verify certificates (#291).
func (w *Worker) SetTLSInsecure(insecure bool) {
	w.tlsInsecure = insecure
}

// SetRESTMAILRootCAs sets the trusted roots for RESTMAIL discovery and delivery.
// Pass system roots augmented with the operator CA; nil uses system roots.
func (w *Worker) SetRESTMAILRootCAs(roots *x509.CertPool) {
	w.restmailRoots = roots
}

// SetAllowPrivateDestinations opts the worker in to dialing non-public
// destinations (loopback/link-local/private/metadata) during outbound delivery
// (#167). It exists only for a dev/testbed that delivers between containers on a
// private bridge network. It defaults false so production denies SSRF-class
// destinations; enabling it disables the outbound SSRF guard entirely.
func (w *Worker) SetAllowPrivateDestinations(allow bool) {
	w.allowPrivateDest = allow
}

// SetMasterKey provides the key used to decrypt at-rest DKIM private keys so the
// worker can DKIM-sign outbound messages that arrived unsigned (#171). It is
// the same MASTER_KEY the API encrypts them with; empty only when encryption is
// disabled (dev).
func (w *Worker) SetMasterKey(key string) {
	w.masterKey = key
}

// prepareOutbound applies the outbound transforms to a queue row's raw message
// immediately before it is written to the wire: the Bcc header is stripped and,
// when the message arrived unsigned, it is DKIM-signed with the sender domain's
// key. This is the single choke point every outbound message passes through, so
// SMTP-submitted mail (which enqueues raw MUA bytes) gets the same treatment the
// API path applies at submission (#171); the transforms are idempotent, so
// already-prepared API mail passes through unchanged. A message this server
// forwards is then ARC-sealed as the forwarding domain, last, over the bytes that
// leave (RFC 8617 §5.1, #731). It returns an error only when a configured DKIM key
// cannot sign, so the caller fails closed (defer/retry) rather than relaying the
// message unsigned.
func (w *Worker) prepareOutbound(item models.OutboundQueue) (string, error) {
	raw, err := outbound.Prepare(w.db, w.masterKey, sendsAs(item), item.RawMessage)
	if err != nil || item.ARCResults == "" {
		return raw, err
	}
	return outbound.SealARC(w.db, w.masterKey, sendsAs(item), raw, item.ARCResults, item.ARCChain)
}

// sendsAs is the hosted domain a queued message is sent as: the one the row
// names, for a reply with a null envelope sender or a forward whose sender is
// someone else's, else the envelope sender's.
func sendsAs(item models.OutboundQueue) string {
	if item.SendingDomain != "" {
		return item.SendingDomain
	}
	return rmail.DomainFromAddress(item.Sender)
}

// newGuardedDialer builds a net.Dialer whose Control hook refuses to connect to
// non-public addresses (#167) unless the worker was opted in to private
// destinations. The check runs on the concrete resolved IP about to be dialed,
// so it also defeats DNS rebinding. Used for every outbound SMTP dial and the
// RESTMAIL HTTPS hop.
func (w *Worker) newGuardedDialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{
		Timeout: timeout,
		Control: outboundDialControl(w.allowPrivateDest),
	}
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
	// The daily TLS-RPT reports on the sessions the workers record (#726).
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.tlsReportLoop()
	}()
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
		"attempt", item.Attempts, // the claim above counted it already
	)

	w.process(item, now)
}

// process sends a claimed message and records how that went.
func (w *Worker) process(item models.OutboundQueue, now time.Time) {
	// A recipient that hard bounced from this sending domain isn't tried
	// again until its suppression expires (#141).
	if s, ok := w.suppression(item, now); ok {
		w.bounceSuppressed(item, s)
		return
	}

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

	w.recordFailure(item, deliveryErr, time.Now())
}

// recordFailure decides what a failed attempt means for the message: a
// permanent failure bounces it at once, a transient one is retried on its
// category's schedule until that runs out, and then it bounces. Either bounce
// sends the sender a DSN.
func (w *Worker) recordFailure(item models.OutboundQueue, deliveryErr error, now time.Time) {
	// deliver() wraps the underlying failure (fmt.Errorf("all MX hosts failed:
	// %w", …)), so the SMTP reply, when there was one, is recovered with
	// errors.As (smtpErrorFrom), never a type assertion that would miss it.
	smtpErr := smtpErrorFrom(deliveryErr)
	var errorCode int
	if smtpErr != nil {
		errorCode = smtpErr.Code
	}
	category := ClassifyFailure(deliveryErr)
	item.FailureCategory = category

	slog.Warn("queue: delivery failed",
		"id", item.ID,
		"recipient", item.Recipient,
		"attempt", item.Attempts,
		"error", deliveryErr,
		"smtp_code", errorCode,
		"failure_category", category,
	)

	failed := map[string]interface{}{
		"last_error":       deliveryErr.Error(),
		"last_error_code":  errorCode,
		"failure_category": category,
	}

	if !category.Permanent() {
		if next, ok := retryAt(item, category, now); ok {
			failed["status"] = "deferred"
			failed["next_attempt"] = next
			w.db.Model(&item).Updates(failed)
			return
		}
	}

	failed["status"] = "bounced"
	w.db.Model(&item).Updates(failed)
	if category.Suppresses() {
		w.suppress(item, category, deliveryErr.Error(), now)
	}
	if category.Permanent() {
		slog.Warn("queue: permanent failure, bouncing", "id", item.ID, "smtp_code", errorCode, "failure_category", category)
	} else {
		slog.Warn("queue: message bounced, retries ran out", "id", item.ID, "recipient", item.Recipient, "failure_category", category)
	}
	// Report the SMTP status when we have one, so a retry budget exhausted on
	// repeated 4xx bounces as a persistent-transient 4.x.y, not a fabricated
	// 5.0.0. Only a failure with no SMTP reply (a connect/DNS error) gets a
	// code-less DSN.
	bounceErr := smtpErr
	if bounceErr == nil {
		bounceErr = &SMTPError{Code: 0, Message: deliveryErr.Error()}
	}
	w.generateBounce(item, bounceErr)
}

// mxResolver is what delivery asks the DNS; *net.Resolver answers it.
type mxResolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// errNoMailHost is RFC 5321 §5.1's error for a domain mail cannot be routed to:
// one the DNS says does not exist, or one with no MX whose implicit MX, the
// domain itself, has no address. It bounces at once as a bad destination
// system (RFC 3463 X.1.2), not after days of retrying a typo.
func errNoMailHost() error {
	return &SMTPError{Code: 550, Enhanced: "5.1.2", Message: "recipient domain does not exist or has no mail host"}
}

// mxHosts is where mail for dnsDomain goes (RFC 5321 §5.1): its MX records, or,
// with none, the domain itself as an implicit MX when it has an address. A
// lookup that fails for now (SERVFAIL, a timeout) is an error to retry.
//
// Go reports "no such host" both for NXDOMAIN and for a name with no MX, and
// some resolvers (Docker's embedded DNS answering for container names) serve
// addresses but no MX at all, so the address lookup is what tells them apart:
// a name with neither an MX nor an address has nowhere to deliver to, and
// whether it exists does not change that.
func (w *Worker) mxHosts(ctx context.Context, dnsDomain string) ([]*net.MX, error) {
	var r mxResolver = net.DefaultResolver
	if w.resolver != nil {
		r = w.resolver
	}
	var dnsErr *net.DNSError
	mx, err := r.LookupMX(ctx, dnsDomain)
	if err == nil && len(mx) > 0 {
		return mx, nil
	}
	if err != nil && (!errors.As(err, &dnsErr) || !dnsErr.IsNotFound) {
		return nil, fmt.Errorf("MX lookup failed for %s: %w", dnsDomain, err)
	}
	if _, err := r.LookupHost(ctx, dnsDomain); err != nil {
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return nil, errNoMailHost()
		}
		return nil, fmt.Errorf("address lookup failed for %s: %w", dnsDomain, err)
	}
	return []*net.MX{{Host: dnsDomain, Pref: 0}}, nil
}

// deliver attempts to send a message via SMTP to the destination MX.
// It first checks if the primary MX supports the RESTMAIL protocol for
// direct HTTPS delivery, falling back to standard SMTP if not.
func (w *Worker) deliver(item models.OutboundQueue) error {
	// Defense in depth against SMTP command / header injection (#166): never write
	// a control-character-bearing address into a MAIL FROM / RCPT TO command or the
	// RESTMAIL HTTPS payload. net/smtp does not sanitize its arguments, so a
	// recipient carrying CR/LF would inject an extra SMTP command. The API boundary
	// already rejects such addresses, so reaching here means a poisoned queue row;
	// fail it permanently rather than attempt delivery.
	if rmail.ContainsControlChar(item.Sender) || rmail.ContainsControlChar(item.Recipient) {
		return &SMTPError{Code: 550, Enhanced: "5.1.3", Message: "address contains control characters"}
	}
	if _, _, err := smtpEnvelope(item); err != nil {
		return err
	}

	// A sending domain configured to relay sends everything through its relay
	// and nothing direct (#143). Not knowing which retries the message.
	relay, err := w.relayRoute(item)
	if err != nil {
		return err
	}
	if relay != nil {
		return w.deliverViaRelay(*relay, item)
	}

	// Keep the queue's Unicode domain for storage/cache identity, but use its
	// IDNA ASCII form for DNS, implicit MX and MTA-STS discovery (#153).
	dnsDomain, err := rmail.DomainASCII(item.Domain)
	if err != nil {
		return &SMTPError{Code: 550, Enhanced: "5.1.2", Message: "invalid recipient domain"}
	}
	mxCtx, mxCancel := context.WithTimeout(context.Background(), 30*time.Second)
	mxRecords, err := w.mxHosts(mxCtx, dnsDomain)
	mxCancel()
	if err != nil {
		return err
	}
	// A null MX (RFC 7505) says the domain accepts no mail. Its host is "." —
	// left alone, that would be dialed as ":25", this machine.
	if len(mxRecords) == 1 && mxRecords[0].Host == "." {
		return &SMTPError{Code: 556, Enhanced: "5.1.10", Message: "recipient domain accepts no mail (null MX)"}
	}

	firstHost := strings.TrimSuffix(mxRecords[0].Host, ".")

	// DANE (RFC 7672) applies when the MX RRset is DNSSEC-secure; then each MX host's
	// TLSA records decide its TLS below. A domain DANE covers goes by SMTP alone: the
	// RESTMAIL upgrade's HTTPS is checked against the Web PKI, not the TLSA records.
	daneSecure, err := w.daneMXSecure(dnsDomain)
	if err != nil {
		return err
	}

	// Discover the recipient domain's MTA-STS policy (RFC 8461) BEFORE choosing a
	// delivery path, so an enforce-mode policy gates EVERY outbound path — the
	// plaintext EHLO-advertised RESTMAIL upgrade as well as the SMTP fallback
	// (#168). Discovery is fail-open: a missing/unreachable policy leaves stsPolicy
	// nil and delivery proceeds with ordinary opportunistic TLS.
	stsPolicy, stsErr := w.recipientPolicy(item, dnsDomain)

	// Whether an enforce policy is actively enforced by this worker. When
	// enforcement is disabled, an enforce policy behaves like "testing".
	enforcing := stsPolicy != nil && stsPolicy.Mode == mtasts.ModeEnforce && w.stsEnforce

	// RESTMAIL upgrade path, gated by the MTA-STS policy resolved above (#168). The
	// capability cache entry is bound to the MX host that advertised it (OSI-20): a
	// hit requires the stored row to be for THIS domain's current primary MX host
	// and still within its TTL. The cache only remembers that the MX speaks REST;
	// the endpoint is asked of the MX host's verified discovery endpoint every
	// time, never taken from a stored or advertised address (#291). Under an
	// enforce-mode policy restmailUpgradePermitted must approve the MX host the
	// discovery request goes to; otherwise we skip the upgrade and use the
	// policy-guarded SMTP fallback.
	// A message that requires TLS goes by SMTP alone, where REQUIRETLS is checked hop by
	// hop (RFC 8689 §4.2.1); RESTMAIL's HTTPS cannot carry that promise on.
	var cap models.RESTMAILCapability
	found := !item.RequireTLS && !daneSecure && w.db.Where("domain = ?", item.Domain).First(&cap).Error == nil
	cacheHit := found && capabilityApplies(cap, item.Domain, firstHost, time.Now())

	if cacheHit {
		if cap.Supported {
			discoveryURL := restmailDiscoveryURL(firstHost, dnsDomain)
			if restmailUpgradePermitted(discoveryURL, stsPolicy, enforcing) {
				slog.Info("queue: using cached RESTMAIL capability", "domain", item.Domain, "mx", firstHost)
				err := w.deliverRESTMAILDiscovered(discoveryURL, item)
				if err == nil {
					return nil
				}
				if errors.Is(err, errRESTMAILNotHosted) {
					w.cacheCapability(item.Domain, firstHost, false)
				} else {
					slog.Warn("queue: cached RESTMAIL delivery failed, invalidating cache",
						"domain", item.Domain, "error", err)
					w.db.Where("domain = ?", item.Domain).Delete(&models.RESTMAILCapability{})
				}
				// Fall through to SMTP
			} else {
				// Enforce-mode policy does not name this MX host: refuse the
				// upgrade and deliver over the policy-guarded SMTP path.
				slog.Warn("queue: MTA-STS enforce — RESTMAIL discovery host not permitted by policy, using SMTP",
					"domain", item.Domain, "url", discoveryURL)
			}
		}
		// cap.Supported == false: skip RESTMAIL, go straight to SMTP
	} else if !item.RequireTLS && !daneSecure {
		// No cache or expired — do EHLO probe (gated by the policy inside tryRESTMAIL).
		upgraded, err := w.tryRESTMAIL(firstHost, dnsDomain, item, stsPolicy, enforcing)
		if upgraded && err == nil {
			return nil // RESTMAIL delivery succeeded
		}
		if upgraded && err != nil {
			slog.Warn("queue: RESTMAIL delivery failed, falling back to SMTP",
				"host", firstHost, "error", err)
		}
	}

	// Fall back to SMTP delivery
	var lastErr, transientErr error
	for _, mx := range mxRecords {
		host := strings.TrimSuffix(mx.Host, ".")

		// A host with usable TLSA records is DANE's, and its records, not MTA-STS,
		// decide its TLS: MTA-STS must not override a DANE verdict (RFC 8461 §2).
		var records []dane.TLSA
		if daneSecure {
			if records, lastErr = w.daneRecords(item.Domain, host, !item.TLSReport); lastErr != nil {
				transientErr = lastErr
				continue
			}
		}

		// MTA-STS MX validation: under enforce, only connect to MX hosts named
		// by the policy (RFC 8461 section 4.1).
		if records == nil && stsPolicy != nil && len(stsPolicy.MX) > 0 && !stsPolicy.MatchesMX(host) {
			if enforcing {
				slog.Warn("queue: MTA-STS enforce — MX host not named by policy, skipping",
					"host", host, "domain", item.Domain)
				lastErr = &mtasts.EnforceError{Domain: item.Domain, MXHost: host, Reason: "MX host not named by policy"}
				continue
			}
			slog.Warn("queue: MTA-STS — MX host not named by policy, delivering anyway",
				"host", host, "domain", item.Domain, "mode", stsPolicy.Mode)
		}

		lastErr = w.deliverToHost(host, item, stsPolicy, stsErr, records)
		if lastErr == nil {
			return nil
		}
		var refused *SMTPError
		if !errors.As(lastErr, &refused) || refused.Code < 500 {
			transientErr = lastErr
		}
		slog.Debug("queue: MX attempt failed", "host", host, "error", lastErr)
	}

	// A message that requires TLS bounces when no MX can take it so (RFC 8689 §4.2.1), but an
	// MX that could not be reached at all might: it is tried again, as any message is.
	if item.RequireTLS && transientErr != nil {
		lastErr = transientErr
	}
	return fmt.Errorf("all MX hosts failed: %w", lastErr)
}

// recipientPolicy is the MTA-STS policy a delivery to dnsDomain applies, nil
// when there is none, with the error saying why. A message that carries
// "TLS-Required: No" applies none, and its policy is not even looked up: its
// sender asks that the recipient's policy be ignored, so it goes with
// opportunistic TLS, STARTTLS whenever the MX offers it, as mail to a domain
// without a policy does (RFC 8689 §4.2.2, §8.4).
func (w *Worker) recipientPolicy(item models.OutboundQueue, dnsDomain string) (*mtasts.Policy, error) {
	if item.TLSReport {
		// A TLS-RPT report goes despite any TLS failure, MTA-STS included:
		// it may be what tells the domain its TLS is broken (RFC 8460 §3).
		return nil, mtasts.ErrNoPolicy
	}
	// "If the REQUIRETLS MAIL FROM parameter is specified, the TLS-Required header field MUST
	// be ignored" (RFC 8689 §4.1).
	if !item.RequireTLS && outbound.TLSOptional(item.RawMessage) {
		slog.Info("queue: TLS-Required: No, the recipient's MTA-STS policy is not applied", "domain", item.Domain)
		return nil, errTLSOptional
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	policy, err := w.sts.Resolve(ctx, dnsDomain)
	if err != nil {
		slog.Debug("queue: MTA-STS not available, proceeding normally",
			"domain", item.Domain, "error", err)
		return nil, err
	}
	slog.Info("queue: MTA-STS policy found",
		"domain", item.Domain, "mode", policy.Mode, "mx_count", len(policy.MX), "enforce", w.stsEnforce)
	return policy, nil
}

// errTLSOptional is why a message that carries "TLS-Required: No" applies no
// MTA-STS policy.
var errTLSOptional = errors.New("queue: TLS-Required: No, the recipient's policy is not applied")

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

// restmailUpgradePermitted reports whether a RESTMAIL endpoint may be used for a
// delivery under the recipient's MTA-STS policy (#168).
//
// The RESTMAIL capability keyword is learned over cleartext port 25, so it is
// attacker-influenceable. The endpoint is now asked of the MX host's verified
// discovery endpoint (#291) rather than taken from the keyword; this check is
// applied to that discovery URL, so under enforce only an MX host named by the
// policy is asked. When no enforce-mode policy is in
// effect (enforcing == false) the upgrade is permitted, preserving opportunistic
// behavior. Under an enforce-mode policy the endpoint is untrusted input and is
// permitted only when it is served over HTTPS AND its host is named by the
// (MTA-STS-authenticated) policy MX set. That stops an on-path attacker from
// injecting a rogue RESTMAIL endpoint — even a cleartext http:// one — and
// intercepting enforce-mode mail in cleartext, exactly what MTA-STS prevents.
//
// The endpoint is normalized the same way deliverRESTMAILHTTPS normalizes it: a
// scheme-less value is treated as https, so a bare "host/path" is judged on its
// host, and a hostile "http://…" is judged (and refused) on its explicit scheme.
func restmailUpgradePermitted(endpointURL string, stsPolicy *mtasts.Policy, enforcing bool) bool {
	if !enforcing {
		return true
	}
	// Enforcing implies a policy was resolved; be defensive if it is somehow nil.
	if stsPolicy == nil {
		return false
	}
	raw := endpointURL
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if parsed.Scheme != "https" {
		return false
	}
	host := parsed.Hostname()
	if host == "" {
		return false
	}
	// The endpoint host must be named by the policy MX set (RFC 8461 §4.1
	// matching, wildcards included), the same trust anchor the SMTP path uses.
	return stsPolicy.MatchesMX(host)
}

// cacheCapability stores a RESTMAIL capability probe result in the database,
// bound to the MX host that advertised it (OSI-20). Only one row is kept per
// domain; probing a new primary MX replaces the previous host's entry. No
// endpoint is stored: it is discovered from the MX host on every delivery (#291).
func (w *Worker) cacheCapability(domain, mxHost string, supported bool) {
	now := time.Now()
	ttl := 15 * time.Minute
	if supported {
		ttl = 1 * time.Hour
	}

	cap := models.RESTMAILCapability{
		Domain:      domain,
		MXHost:      mxHost,
		Supported:   supported,
		EndpointURL: "",
		LastProbed:  now,
		ExpiresAt:   now.Add(ttl),
	}

	w.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "domain"}},
		DoUpdates: clause.AssignmentColumns([]string{"mx_host", "supported", "endpoint_url", "last_probed", "expires_at"}),
	}).Create(&cap)
}

// deliverRESTMAILHTTPS sends the message via HTTPS POST to a known RESTMAIL endpoint.
//
// The endpoint comes from verified MX discovery. Its certificate must also be
// verified against its own host, in every environment (#291). The SMTP-only
// tlsInsecure flag never applies to either RESTMAIL HTTPS connection.
func (w *Worker) deliverRESTMAILHTTPS(endpointURL string, item models.OutboundQueue) error {
	// Strip Bcc and DKIM-sign before transmission (#171). Fail closed on a key
	// that cannot be loaded so the send retries rather than going out unsigned.
	prepared, err := w.prepareOutbound(item)
	if err != nil {
		return fmt.Errorf("prepare outbound message: %w", err)
	}
	item.RawMessage = prepared

	payload := map[string]interface{}{
		"from":        item.Sender,
		"to":          []string{item.Recipient},
		"raw_message": item.RawMessage,
	}
	payloadBytes, _ := json.Marshal(payload)

	messagesURL := endpointURL + "/messages"
	if !strings.Contains(messagesURL, "://") {
		messagesURL = "https://" + messagesURL
	}

	// Require HTTPS (#167). Even a discovered endpoint is remote input; the
	// raw message must never be POSTed in cleartext or over a non-HTTP(S) scheme.
	parsed, err := url.Parse(messagesURL)
	if err != nil {
		return fmt.Errorf("RESTMAIL endpoint %q invalid: %w", endpointURL, err)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("RESTMAIL endpoint %q refused: only https is permitted, got scheme %q", endpointURL, parsed.Scheme)
	}

	// Guarded dialer refuses non-public resolved addresses (#167) unless opted in
	// — the discovered RESTMAIL host could point at loopback,
	// link-local metadata, or internal relays. The check runs on the resolved IP,
	// so it also defeats DNS rebinding.
	dialer := w.newGuardedDialer(10 * time.Second)
	httpClient := &http.Client{
		// Size-aware upload budget (OSI-7): the payload carries the full raw
		// message, so a fixed 30 s cannot transfer a large body. Bounded — a
		// finite function of the message size — so this hop stays slowloris-safe.
		Timeout: w.sendDeadline(int64(len(item.RawMessage))),
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			TLSClientConfig:     &tls.Config{RootCAs: w.restmailRoots, MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout: 10 * time.Second,
		},
		// Do not follow redirects (#167): a 30x could otherwise downgrade
		// HTTPS→HTTP or re-POST the body to a different, unchecked host.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
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

	// Cap the error body read: an unbounded read would let a hostile endpoint
	// exhaust worker memory with a huge response (#169).
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxRESTMAILErrorBody))
	return fmt.Errorf("RESTMAIL delivery got %d: %s", resp.StatusCode, string(body))
}

// deliverRESTMAILDiscovered asks discoveryURL — the MX host's verified
// discovery endpoint (#291) — where to deliver, then POSTs the message there.
// errRESTMAILNotHosted means the MX verifiably does not take REST mail for the
// domain.
func (w *Worker) deliverRESTMAILDiscovered(discoveryURL string, item models.OutboundQueue) error {
	endpointURL, err := w.discoverRESTMAILEndpoint(discoveryURL)
	if err != nil {
		return err
	}
	slog.Info("queue: RESTMAIL endpoint discovered", "discovery", discoveryURL, "endpoint", endpointURL)
	return w.deliverRESTMAILHTTPS(endpointURL, item)
}

// tryRESTMAIL probes a host for the RESTMAIL EHLO extension. If found — and the
// recipient's MTA-STS policy permits the upgrade (#168) — it asks the host's
// discovery endpoint for the domain's REST endpoint and delivers the message
// via HTTPS POST instead of SMTP. dnsDomain is the recipient domain's IDNA
// ASCII form.
// Returns (true, nil) on successful RESTMAIL delivery,
// (true, err) if RESTMAIL was detected but discovery or delivery failed,
// (false, nil) if the host does not support RESTMAIL, its discovery endpoint
// verifiably does not host the domain, or an enforce-mode policy does not name
// the host (caller falls back to the SMTP path).
func (w *Worker) tryRESTMAIL(host, dnsDomain string, item models.OutboundQueue, stsPolicy *mtasts.Policy, enforcing bool) (upgraded bool, err error) {
	return w.tryRESTMAILAt(host+":25", restmailDiscoveryURL(host, dnsDomain), host, item, stsPolicy, enforcing)
}

// tryRESTMAILAt is tryRESTMAIL with the probe address and discovery URL given
// separately from the MX host name, so a test can use listeners on other ports.
func (w *Worker) tryRESTMAILAt(addr, discoveryURL, host string, item models.OutboundQueue, stsPolicy *mtasts.Policy, enforcing bool) (upgraded bool, err error) {
	probed, ok := w.probeRESTMAIL(addr, host)
	if !probed {
		return false, nil // Can't connect / probe failed, let SMTP path handle it
	}

	if !ok {
		w.cacheCapability(item.Domain, host, false)
		return false, nil // No RESTMAIL support
	}

	// The keyword is only a hint: the endpoint comes from the MX host's verified
	// discovery endpoint, not from the address the MX advertised in cleartext
	// (#291).
	slog.Info("queue: RESTMAIL capability detected", "host", host)
	w.cacheCapability(item.Domain, host, true)

	// Under an enforce-mode MTA-STS policy only ask an MX host named by the
	// policy (#168); otherwise report "not upgraded" so the caller delivers over
	// the policy-guarded SMTP path.
	if !restmailUpgradePermitted(discoveryURL, stsPolicy, enforcing) {
		slog.Warn("queue: MTA-STS enforce — RESTMAIL discovery host not permitted by policy, using SMTP",
			"host", host, "url", discoveryURL)
		return false, nil
	}

	err = w.deliverRESTMAILDiscovered(discoveryURL, item)
	if errors.Is(err, errRESTMAILNotHosted) {
		w.cacheCapability(item.Domain, host, false)
		return false, nil
	}
	if err != nil {
		return true, err
	}
	return true, nil
}

// probeRESTMAIL dials addr and runs the RESTMAIL capability probe against it:
// read the SMTP greeting, EHLO, check for the RESTMAIL extension, then QUIT.
// serverName is passed to smtp.NewClient (the TLS server name, informational
// until STARTTLS). It reports (probed, supported):
//   - probed is true only when the greeting+EHLO handshake completed and the
//     remote gave a definitive RESTMAIL answer. When probed is false the socket
//     or handshake failed and the caller falls through to plain SMTP without
//     caching a capability result.
//   - supported carries the RESTMAIL extension result when probed. Any address
//     the keyword carries is discarded (#291): see discoverRESTMAILEndpoint.
//
// The ENTIRE probe conversation is bounded by a single absolute deadline set on
// the socket immediately after dial, before smtp.NewClient reads the greeting.
// net/smtp installs no deadlines of its own, so without this a remote that
// accepts the TCP connection and then stays silent would park this worker
// goroutine forever: the queue row is reclaimed and re-probed on a timer, leaking
// a goroutine each cycle, so a single silent MX can drain the worker pool and
// wedge the outbound queue until restart (#169). The probe transfers no message
// body, so it uses the base per-attempt budget (sendDeadline(0)), which stays far
// below the reclaim interval.
func (w *Worker) probeRESTMAIL(addr, serverName string) (probed, supported bool) {
	dialer := w.newGuardedDialer(10 * time.Second)
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return false, false
	}

	// Bound the whole probe with one absolute deadline, set before smtp.NewClient
	// reads the greeting, so a silent remote cannot wedge this goroutine (#169).
	_ = conn.SetDeadline(time.Now().Add(w.sendDeadline(0)))

	client, err := smtp.NewClient(conn, serverName)
	if err != nil {
		conn.Close()
		return false, false
	}

	if err := client.Hello(w.hostname); err != nil {
		_ = client.Close()
		return false, false
	}

	ok, _ := client.Extension("RESTMAIL")
	_ = client.Quit()
	_ = client.Close()
	return true, ok
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

// recentDSNCount returns how many locally generated DSNs have been delivered into
// mailboxID within the worker's bounce window. It counts only rows this worker
// marked is_dsn = true, NOT every message whose sender equals
// mailer-daemon@<hostname>: an external sender can forge that envelope-from, so
// counting inbound rows would let an attacker inflate a target mailbox's counter
// and suppress its legitimate bounces (the DSN rate-limit-spoof gap). is_dsn is
// never set from the wire, so it cannot be spoofed.
// queueRemoteDSN queues the notice of a failed delivery for a sender outside REST Mail. It
// goes with a null reverse path, as every DSN does (RFC 5321 §6.1), From: mailer-daemon at
// this host, and is sent as this host. Like a local DSN it is limited per recipient, so mail
// that forged a sender cannot turn into a stream of notices to them (backscatter).
func (w *Worker) queueRemoteDSN(item models.OutboundQueue, smtpErr *SMTPError) {
	recipient := strings.TrimSpace(item.Sender)
	domain := rmail.DomainFromAddress(recipient)
	if domain == "" || rmail.ValidateAddress(recipient) != nil {
		slog.Warn("queue: the sender of a failed message is not an address, no DSN", "id", item.ID)
		return
	}
	now := time.Now()
	if w.bounceMax > 0 {
		var recent int64
		w.db.Model(&models.OutboundQueue{}).
			Where("is_dsn = ? AND recipient = ? AND created_at > ?", true, recipient, now.Add(-w.bounceWindow)).
			Count(&recent)
		if !withinDSNRateLimit(int(recent), w.bounceMax) {
			slog.Warn("queue: DSN rate limit exceeded for a sender outside, suppressing DSN",
				"sender", recipient, "recent", recent, "max", w.bounceMax, "window", w.bounceWindow)
			return
		}
	}
	lastAttempt := now
	if item.LastAttempt != nil {
		lastAttempt = *item.LastAttempt
	}
	dsn := buildDSNMessage(w.hostname, item, smtpErr, now, lastAttempt)
	notice := models.OutboundQueue{
		Sender:        "",
		SendingDomain: w.hostname,
		Recipient:     recipient,
		Domain:        domain,
		RawMessage:    dsn.raw,
		Status:        "pending",
		IsDSN:         true,
	}
	if err := w.db.Create(&notice).Error; err != nil {
		slog.Error("queue: failed to queue a DSN for a sender outside", "sender", recipient, "error", err)
		return
	}
	slog.Info("queue: RFC 3464 DSN queued for a sender outside", "sender", recipient, "failed_recipient", item.Recipient)
}

func (w *Worker) recentDSNCount(mailboxID uint) int64 {
	var recent int64
	w.db.Table("messages").
		Where("mailbox_id = ? AND is_dsn = ? AND created_at > ?",
			mailboxID, true, time.Now().Add(-w.bounceWindow)).
		Count(&recent)
	return recent
}

// generateBounce creates an RFC 3464 DSN (Delivery Status Notification)
// and delivers it to the original sender's mailbox if the sender is local.
func (w *Worker) generateBounce(item models.OutboundQueue, smtpErr *SMTPError) {
	// Mail sent with a null reverse path, such as a vacation reply, is never
	// bounced (RFC 5321 §6.1): there is nobody to tell, and a notice about a
	// notice could loop.
	if item.Sender == "" {
		slog.Debug("queue: null sender, no DSN", "id", item.ID)
		return
	}
	// Check if the sender has a local mailbox, resolving its hosted identity as
	// submission did when it attributed the message.
	var senderMailbox models.Mailbox
	if err := repositories.FindActiveMailboxByAddress(w.db, item.Sender, &senderMailbox); err != nil {
		// A sender outside REST Mail, such as the author of mail a Sieve redirect
		// forwarded: accepting the message took on telling them it failed (RFC 5321
		// §6.1), so the notice goes by mail (#715).
		w.queueRemoteDSN(item, smtpErr)
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
		recent := w.recentDSNCount(senderMailbox.ID)
		if !withinDSNRateLimit(int(recent), w.bounceMax) {
			slog.Warn("queue: DSN rate limit exceeded for recipient, suppressing DSN",
				"sender", item.Sender, "recent", recent, "max", w.bounceMax, "window", w.bounceWindow)
			return
		}
	}

	now := time.Now()
	lastAttempt := now
	if item.LastAttempt != nil {
		lastAttempt = *item.LastAttempt
	}
	dsn := buildDSNMessage(w.hostname, item, smtpErr, now, lastAttempt)

	// Insert bounce message into sender's INBOX. is_dsn marks this row as a
	// locally generated DSN so the per-recipient rate-limit counter counts only
	// our own bounces, never inbound mail that merely claims a mailer-daemon@
	// envelope-from (which an external sender could use to inflate the counter and
	// suppress a target user's legitimate bounces).
	bounceMsg := map[string]interface{}{
		"mailbox_id":    senderMailbox.ID,
		"folder":        "INBOX",
		"sender":        "mailer-daemon@" + w.hostname,
		"sender_name":   "Mail Delivery System",
		"recipients_to": fmt.Sprintf(`[%q]`, rmail.SanitizeHeaderValue(item.Sender)),
		"recipients_cc": "[]",
		"subject":       dsn.subject,
		"body_text":     dsn.humanPart,
		"raw_message":   dsn.raw,
		"is_read":       false,
		"is_dsn":        true,
		"size_bytes":    len(dsn.raw),
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

// builtDSN is the fully rendered bounce message plus the two fields the caller
// stores alongside the raw form (the Subject header and the human-readable text).
type builtDSN struct {
	raw       string
	subject   string
	humanPart string
}

// buildDSNMessage renders an RFC 3464 multipart/report DSN for a failed outbound
// item. It is pure (no DB, no clock of its own — now and lastAttempt are passed
// in) so the header set and escaping are unit-testable.
//
// Every value that originates from a remote peer (smtpErr.Message, the failing
// MX host) or from the queue row's envelope (item.Recipient / item.Sender, which
// may carry control bytes on a poisoned row) is run through
// mail.SanitizeHeaderValue before interpolation, so a CR/LF in a multiline SMTP
// reply — or in a crafted recipient — cannot inject forged headers or fake
// message/delivery-status fields into this trusted mailer-daemon message.
//
// The message carries Auto-Submitted: auto-generated (RFC 3834 §5) so a
// well-behaved auto-responder does not reply to it (backscatter), and the
// per-recipient status block carries Last-Attempt-Date (RFC 3464 §2.3.7) and,
// when the failure named a specific next hop, Remote-MTA (RFC 3464 §2.3.5).
// An internationalized recipient, remote reply or returned header section
// switches the affected parts to the RFC 6533 global types (#153).
func buildDSNMessage(hostname string, item models.OutboundQueue, smtpErr *SMTPError, now, lastAttempt time.Time) builtDSN {
	boundary := fmt.Sprintf("=_restmail_dsn_%d", now.UnixNano())
	msgID := fmt.Sprintf("<dsn-%d-%d@%s>", item.ID, now.UnixNano(), hostname)

	recipient := rmail.SanitizeHeaderValue(item.Recipient)
	sender := rmail.SanitizeHeaderValue(item.Sender)

	statusCode := dsnStatusCode(smtpErr)
	diagnosticCode := "smtp; delivery failed"
	remoteMTA := ""
	if smtpErr != nil {
		if smtpErr.Code > 0 {
			diagnosticCode = fmt.Sprintf("smtp; %d %s", smtpErr.Code, rmail.SanitizeHeaderValue(smtpErr.Message))
		} else {
			diagnosticCode = fmt.Sprintf("smtp; %s", rmail.SanitizeHeaderValue(smtpErr.Message))
		}
		remoteMTA = rmail.SanitizeHeaderValue(smtpErr.RemoteMTA)
	}

	// Extract original headers from RawMessage for Part 3.
	originalHeaders := item.RawMessage
	if idx := strings.Index(originalHeaders, "\r\n\r\n"); idx >= 0 {
		originalHeaders = originalHeaders[:idx]
	} else if idx := strings.Index(originalHeaders, "\n\n"); idx >= 0 {
		originalHeaders = originalHeaders[:idx]
	}

	// Part 1: Human-readable, with the reason in plain words when the failure
	// was classified.
	reason := ""
	if d := item.FailureCategory.Description(); d != "" {
		reason = d + "\r\n\r\n"
	}
	// A message to a suppressed recipient is bounced without an attempt.
	attempted := ""
	if item.Attempts > 0 {
		attempted = fmt.Sprintf("The delivery has been attempted %d time(s).\r\n\r\n", item.Attempts)
	}
	humanPart := fmt.Sprintf(
		"This is the mail delivery system at %s.\r\n\r\n"+
			"Your message could not be delivered to the following recipient:\r\n\r\n"+
			"    %s\r\n\r\n"+
			"%s"+
			"%s"+
			"Error: %s\r\n",
		hostname, recipient, reason, attempted, diagnosticCode,
	)

	// Part 2: Machine-readable DSN (RFC 3464). Remote-MTA is emitted only when the
	// failure named a specific next hop.
	var dsnBuf strings.Builder
	fmt.Fprintf(&dsnBuf, "Reporting-MTA: dns; %s\r\n", hostname)
	fmt.Fprintf(&dsnBuf, "Arrival-Date: %s\r\n\r\n", item.CreatedAt.Format(time.RFC1123Z))
	fmt.Fprintf(&dsnBuf, "Final-Recipient: %s\r\n", dsnRecipientField(recipient))
	dsnBuf.WriteString("Action: failed\r\n")
	fmt.Fprintf(&dsnBuf, "Status: %s\r\n", statusCode)
	if remoteMTA != "" {
		fmt.Fprintf(&dsnBuf, "Remote-MTA: dns; %s\r\n", remoteMTA)
	}
	fmt.Fprintf(&dsnBuf, "Diagnostic-Code: %s\r\n", diagnosticCode)
	fmt.Fprintf(&dsnBuf, "Last-Attempt-Date: %s\r\n", lastAttempt.Format(time.RFC1123Z))
	dsnPart := dsnBuf.String()

	// RFC 6533: a status block or returned header section carrying UTF-8 uses
	// the global media types, which permit it; the RFC 3464 types are ASCII-only.
	reportType := "delivery-status"
	if containsNonASCII(dsnPart) {
		reportType = "global-delivery-status"
	}
	headersType := "text/rfc822-headers"
	if containsNonASCII(originalHeaders) {
		headersType = "message/global-headers"
	}

	bounceSubject := fmt.Sprintf("Undelivered Mail Returned to Sender <%s>", recipient)

	var b strings.Builder
	b.WriteString("From: mailer-daemon@" + hostname + "\r\n")
	b.WriteString("To: " + sender + "\r\n")
	b.WriteString("Subject: " + bounceSubject + "\r\n")
	b.WriteString("Date: " + now.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Message-ID: " + msgID + "\r\n")
	// RFC 3834 §5: a DSN is an automatically generated response and MUST carry
	// Auto-Submitted so downstream auto-responders do not reply to it (backscatter).
	b.WriteString("Auto-Submitted: auto-generated\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: multipart/report; report-type=" + reportType + "; boundary=\"" + boundary + "\"\r\n")
	b.WriteString("\r\n")
	// Part 1
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(humanPart + "\r\n")
	// Part 2
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: message/" + reportType + "\r\n\r\n")
	b.WriteString(dsnPart + "\r\n")
	// Part 3
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: " + headersType + "\r\n\r\n")
	b.WriteString(originalHeaders + "\r\n")
	b.WriteString("--" + boundary + "--\r\n")

	return builtDSN{raw: b.String(), subject: bounceSubject, humanPart: humanPart}
}

// dsnRecipientField renders a Final-Recipient value. An ASCII address keeps the
// rfc822 type; an internationalized one uses the RFC 6533 utf-8 type, whose
// unitext form carries UTF-8 as is and writes space, "+", "=", "\\" and any
// other non-printing ASCII as \x{HEX}.
func dsnRecipientField(addr string) string {
	if !containsNonASCII(addr) {
		return "rfc822; " + addr
	}
	var b strings.Builder
	for _, r := range addr {
		if r > 0x7f || (r > 0x20 && r < 0x7f && r != '+' && r != '=' && r != '\\') {
			b.WriteRune(r)
			continue
		}
		fmt.Fprintf(&b, "\\x{%X}", r)
	}
	return "utf-8; " + b.String()
}

// deliverToHost attempts SMTP delivery to a specific host.
//
// TLS is applied per the recipient's MTA-STS policy (RFC 8461):
//   - enforce: STARTTLS MUST succeed with a certificate that is valid for the
//     MX host (which the caller has already confirmed is named by the policy).
//     Certificate verification is forced on regardless of the worker's
//     tlsInsecure flag, and any failure returns a deferrable *EnforceError so
//     the queue retries rather than delivering in the clear.
//   - testing / none / no policy: opportunistic TLS (RFC 7435): encrypt when the
//     MX offers STARTTLS, whatever its certificate, and record whether it
//     verified; a handshake that fails is retried on a new connection without
//     TLS. A would-fail under "testing" is logged but delivery proceeds.
func (w *Worker) deliverToHost(host string, item models.OutboundQueue, stsPolicy *mtasts.Policy, stsErr error, records []dane.TLSA) error {
	return w.deliverSMTPWith(net.JoinHostPort(host, "25"), host, item, stsPolicy, stsErr, records)
}

// deliverSMTP keeps the socket address separate from the MX identity used for
// EHLO/TLS policy, as probeRESTMAIL does. Production uses the MX's port 25.
// stsErr is why MTA-STS discovery found no policy (nil when stsPolicy is
// set), recorded with the session for the domain's TLS-RPT report.
func (w *Worker) deliverSMTP(addr, host string, item models.OutboundQueue, stsPolicy *mtasts.Policy, stsErr error) error {
	return w.deliverSMTPWith(addr, host, item, stsPolicy, stsErr, nil)
}

// deliverSMTPWith is deliverSMTP for a host DANE covers when records is not nil: TLS
// is then required and authenticated by those TLSA records alone (RFC 7672 §2.2),
// and the domain's MTA-STS policy does not apply to the host.
func (w *Worker) deliverSMTPWith(addr, host string, item models.OutboundQueue, stsPolicy *mtasts.Policy, stsErr error, records []dane.TLSA) error {
	if records != nil {
		stsPolicy, stsErr = nil, nil
	}
	// Strip Bcc and DKIM-sign before transmission (#171). Fail closed on a key
	// that cannot be loaded so the send retries rather than going out unsigned.
	prepared, err := w.prepareOutbound(item)
	if err != nil {
		return fmt.Errorf("prepare outbound message: %w", err)
	}
	item.RawMessage = prepared
	sender, recipient, err := smtpEnvelope(item)
	if err != nil {
		return err
	}

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

	// REQUIRETLS (RFC 8689 §4.2.1): a message whose sender required TLS goes only to an MX the
	// domain's MTA-STS policy names (REST Mail validates no DNSSEC), over TLS with a
	// certificate verified for that MX, which offers REQUIRETLS once TLS is up. A bounce,
	// with a null reverse path, is not held to it (§5).
	requireTLS := item.RequireTLS && item.Sender != ""
	// A DANE host authenticates TLS from DNSSEC, which REQUIRETLS accepts (RFC 8689 §4.2.1).
	if requireTLS && records == nil {
		if stsPolicy == nil || len(stsPolicy.MX) == 0 || !stsPolicy.MatchesMX(host) {
			return &SMTPError{Code: 550, Enhanced: "5.7.10", RemoteMTA: host,
				Message: "REQUIRETLS: " + host + " is not an MX the recipient domain's MTA-STS policy names, and REST Mail does not validate DNSSEC"}
		}
		enforce = true
	}

	link, err := w.openSMTP(ctx, addr, host, attemptDeadline, true, enforce, records)
	if err != nil {
		return err
	}
	if records != nil && !item.TLSReport {
		w.recordDANESession(item.Domain, host, records, link, time.Now())
	}
	if records != nil && (link.tlsErr != nil || link.security != transportAuthenticated) {
		_ = link.client.Close()
		return daneRefusal(host, link)
	}
	if requireTLS {
		if link.tlsErr != nil || link.security != transportAuthenticated {
			_ = link.client.Quit()
			return &SMTPError{Code: 550, Enhanced: "5.7.10", RemoteMTA: host,
				Message: "REQUIRETLS: no TLS session with a certificate verified for " + host}
		}
		if ok, _ := link.client.Extension("REQUIRETLS"); !ok {
			_ = link.client.Quit()
			return &SMTPError{Code: 550, Enhanced: "5.7.30", RemoteMTA: host,
				Message: "REQUIRETLS support required: " + host + " does not offer REQUIRETLS"}
		}
	}
	// What this session's TLS came to, for the recipient's TLS-RPT report
	// (RFC 8460): a report itself is not counted (§3).
	// A DANE session was recorded above, with the tlsa policy type.
	if !item.TLSReport && records == nil {
		w.recordTLSSession(item.Domain, host, stsPolicy, stsErr, link, time.Now())
	}
	if link.tlsErr != nil && !enforce {
		// The failed handshake has left that connection unusable: net/smtp has
		// already put TLS in its place, and every later command would fail with
		// it. An opportunistic client starts again on a new connection without
		// TLS (RFC 7435 §3, as Postfix does) rather than give up on the MX.
		if testingMode {
			slog.Warn("queue: MTA-STS testing — STARTTLS failed, delivering without TLS",
				"host", host, "error", link.tlsErr)
		} else {
			slog.Info("queue: STARTTLS failed, delivering without TLS", "host", host, "error", link.tlsErr)
		}
		_ = link.client.Close()
		if link, err = w.openSMTP(ctx, addr, host, attemptDeadline, false, false, nil); err != nil {
			return err
		}
	}
	client := link.client
	defer func() { _ = client.Close() }()
	starttls := link.security != transportCleartext
	if testingMode {
		switch link.security {
		case transportCleartext:
			slog.Warn("queue: MTA-STS testing — no TLS with the host, delivering anyway", "host", host)
		case transportEncrypted:
			slog.Warn("queue: MTA-STS testing — the host's certificate did not verify, delivering anyway", "host", host)
		}
	}

	// MTA-STS enforcement decision point. Under enforce this refuses cleartext
	// or an unverified certificate by returning a deferrable *EnforceError
	// (under enforce, openSMTP only upgrades with a certificate verified for host).
	// go-mtasts v0.2.0 removed the caller-supplied Mode override: Evaluate now
	// gates on the discovered Policy.Mode (RFC 8461 §5). Our operator-facing
	// downgrade — deliver anyway when MTA-STS enforcement is globally disabled —
	// is expressed via AllowInsecureDowngrade instead. It is set exactly when the
	// old code downgraded enforce→testing (i.e. !w.stsEnforce), preserving prior
	// behaviour; with stsEnforce on it stays false so an enforce policy fails
	// closed, the secure default.
	if decision := mtasts.Evaluate(mtasts.EvalInput{
		Policy:                 stsPolicy,
		Domain:                 item.Domain,
		MXHost:                 host,
		STARTTLS:               starttls,
		CertValid:              link.security == transportAuthenticated,
		AllowInsecureDowngrade: !w.stsEnforce,
	}); decision != nil {
		slog.Warn("queue: MTA-STS enforce — refusing delivery", "host", host, "domain", item.Domain, "error", decision)
		return decision
	}

	// 8BITMIME relay guarantee (RFC 6152 §3). net/smtp appends BODY=8BITMIME to
	// MAIL FROM only when the peer advertised 8BITMIME; when it did NOT, and this
	// message needs 8-bit transport (it was submitted with BODY=8BITMIME/BINARYMIME,
	// or its bytes actually contain octets >= 0x80), sending it verbatim would put
	// undeclared, unconverted 8-bit content on a 7-bit-only channel. We do not
	// implement on-the-fly 7-bit downconversion, so refuse this hop with a
	// permanent 5.6.3 (conversion required but not supported). The caller tries the
	// next MX; only if no MX offers 8BITMIME does the message bounce — never
	// silently corrupted. Peers that advertise 8BITMIME (nearly all modern MTAs)
	// are unaffected.
	if ok, _ := client.Extension("8BITMIME"); !ok && messageRequires8BitTransport(item.BodyType, item.RawMessage) {
		slog.Warn("queue: next hop lacks 8BITMIME for 8-bit message, refusing (no 7-bit downconversion)",
			"host", host, "domain", item.Domain, "body_type", item.BodyType)
		return &SMTPError{
			Code:      550,
			Enhanced:  "5.6.3",
			Message:   "next hop does not support 8BITMIME and 7-bit conversion is not implemented",
			RemoteMTA: host,
		}
	}

	if err := checkSMTPUTF8(client, sender, recipient, item.RawMessage, host); err != nil {
		return err
	}

	// Set sender, with REQUIRETLS when the message requires it (RFC 8689 §4.2.1).
	mail := client.Mail
	if requireTLS {
		mail = func(from string) error { return mailRequireTLS(client, from) }
	}
	if err := mail(sender); err != nil {
		se := parseSMTPError(err)
		se.RemoteMTA = host
		return se
	}

	// Set recipient
	if err := client.Rcpt(recipient); err != nil {
		se := parseSMTPError(err)
		se.RemoteMTA = host
		return se
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
		se := parseSMTPError(err)
		se.RemoteMTA = host
		return se
	}

	// Quit
	_ = client.Quit()
	// What protected it, never more than it was (RFC 7435 §3): a certificate
	// that did not verify is encryption, not authentication.
	metrics.OutboundTransport.WithLabelValues(link.security).Inc()
	slog.Info("queue: delivered over SMTP", "host", host, "domain", item.Domain, "transport", link.security)
	return nil
}

// The transport security of an SMTP delivery (RFC 7435): TLS with a certificate
// verified for the MX host, TLS whose certificate did not verify, or none.
const (
	transportAuthenticated = "authenticated"
	transportEncrypted     = "encrypted"
	transportCleartext     = "cleartext"
)

// smtpLink is an open SMTP conversation with an MX, past EHLO and, when it was
// asked for and the MX offers it, STARTTLS.
type smtpLink struct {
	client   *smtp.Client
	security string // transportAuthenticated, transportEncrypted or transportCleartext
	// tlsErr is the STARTTLS failure, when the upgrade was tried and failed; the
	// connection is then unusable, and the caller decides what to do.
	tlsErr error
	// certErr is why the certificate did not verify on a connection that is
	// encrypted but not authenticated, for TLS-RPT (RFC 8460 §4.3).
	certErr error
	// localIP and remoteIP are this end's and the MX's address.
	localIP, remoteIP string
}

// openSMTP dials addr, which the guarded dialer refuses when it resolves to a
// non-public address (#167) unless the worker was opted in to private
// destinations, and says EHLO. With tryTLS it upgrades when the MX offers
// STARTTLS. With requireVerified (an MTA-STS enforce policy) the certificate
// must verify for host or the upgrade fails; without it the certificate is
// checked and its verdict recorded, not required (RFC 7435 §3).
//
// One absolute deadline bounds the whole conversation, the greeting, EHLO, the
// handshake, MAIL/RCPT, the message and the final reply, set on the socket
// because net/smtp sets none; it persists across the STARTTLS upgrade.
func (w *Worker) openSMTP(ctx context.Context, addr, host string, deadline time.Time, tryTLS, requireVerified bool, records []dane.TLSA) (*smtpLink, error) {
	dialer := w.newGuardedDialer(10 * time.Second)
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", addr, err)
	}
	_ = conn.SetDeadline(deadline)
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("SMTP client for %s: %w", addr, err)
	}
	if err := client.Hello(w.hostname); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("EHLO to %s: %w", host, err)
	}
	link := &smtpLink{client: client, security: transportCleartext, localIP: addrIP(conn.LocalAddr()), remoteIP: addrIP(conn.RemoteAddr())}
	if ok, _ := client.Extension("STARTTLS"); !tryTLS || !ok {
		return link, nil
	}
	tlsConfig := &tls.Config{ServerName: host, RootCAs: w.smtpRoots}
	var verifyErr error
	if records != nil {
		// DANE: the TLSA records, not the system roots or the name, authenticate the
		// chain (RFC 7672 §3), and a chain no record matches fails the handshake.
		tlsConfig.InsecureSkipVerify = true //nolint:gosec // dane.Verify checks the chain against the TLSA records
		tlsConfig.VerifyConnection = func(cs tls.ConnectionState) error {
			return dane.Verify(records, host, cs.PeerCertificates, time.Now())
		}
	} else if !requireVerified {
		tlsConfig.InsecureSkipVerify = true //nolint:gosec // verified below, recorded rather than required
		tlsConfig.VerifyConnection = func(cs tls.ConnectionState) error {
			verifyErr = verifyPeerCertificate(cs, host, w.smtpRoots)
			return nil
		}
	}
	if err := client.StartTLS(tlsConfig); err != nil {
		link.tlsErr = err
		return link, nil
	}
	link.security = transportAuthenticated
	if verifyErr != nil {
		link.security, link.certErr = transportEncrypted, verifyErr
	}
	return link, nil
}

// verifyPeerCertificate checks a server's chain against the system roots and
// host, as crypto/tls would have.
func verifyPeerCertificate(cs tls.ConnectionState, host string, roots *x509.CertPool) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("no certificate")
	}
	opts := x509.VerifyOptions{DNSName: host, Roots: roots, Intermediates: x509.NewCertPool()}
	for _, c := range cs.PeerCertificates[1:] {
		opts.Intermediates.AddCert(c)
	}
	_, err := cs.PeerCertificates[0].Verify(opts)
	return err
}

// mailRequireTLS issues MAIL FROM with the REQUIRETLS parameter (RFC 8689 §4.2.1), which
// net/smtp's Mail cannot add, alongside the BODY=8BITMIME and SMTPUTF8 parameters Mail adds
// when the server offers them.
func mailRequireTLS(c *smtp.Client, from string) error {
	if strings.ContainsAny(from, "\r\n") {
		return errors.New("smtp: a line must not contain CR or LF")
	}
	cmd := "MAIL FROM:<" + from + ">"
	if ok, _ := c.Extension("8BITMIME"); ok {
		cmd += " BODY=8BITMIME"
	}
	if ok, _ := c.Extension("SMTPUTF8"); ok {
		cmd += " SMTPUTF8"
	}
	id, err := c.Text.Cmd("%s REQUIRETLS", cmd)
	if err != nil {
		return err
	}
	c.Text.StartResponse(id)
	defer c.Text.EndResponse(id)
	_, _, err = c.Text.ReadResponse(250)
	return err
}
