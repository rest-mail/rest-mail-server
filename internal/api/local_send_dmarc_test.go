package api

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dkim "github.com/rest-mail/go-dkim/pkg/dkim"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/gateway/connlimiter"
	"github.com/rest-mail/rest-mail-server/internal/gateway/queue"
	smtpgw "github.com/rest-mail/rest-mail-server/internal/gateway/smtp"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"gorm.io/gorm"
)

// These tests run a local send over the whole route a real install has: the
// API's send handlers, the outbound queue, the real queue worker signing and
// dialing the domain's MX, the real SMTP gateway's inbound port, and the API's
// deliver route with the domain's inbound pipeline. Only the network is
// simulated: the DNS is served on the loopback, and the worker's dial of port 25
// is pointed at the inbound port's real address. Everything that depends on a
// real MX, hairpin NAT, a certificate an MX would present, the RESTMAIL hop and
// the SSRF guard is for the E2E topology (tests/e2e) to prove. These tests hold the
// worker on the SMTP hop: see smtpHopOnly.

const (
	localSendDomain = "example.test"
	// localSendMX is the MX localSendDomain publishes.
	localSendMX = "mx.example.test"
	// spfOtherServer lists a server that is not the one the test's worker dials
	// from, so SPF fails and only a real DKIM signature can pass DMARC.
	spfOtherServer = "v=spf1 ip4:192.0.2.25 -all"
	// spfThisServer lists the loopback address the worker dials from.
	spfThisServer = "v=spf1 ip4:127.0.0.1 -all"
	// strictReject is the policy the owner's mail was refused under.
	strictReject = "v=DMARC1; p=reject; aspf=s; adkim=s"
)

// localRoute is the credential fixture's server with a second mailbox on its
// domain, a mail route around it, and an inbound pipeline with the sender checks
// and the recipient's own steps.
type localRoute struct {
	*credentialFixture
	recipientID uint
	scans       *atomic.Int32
}

// newLocalRoute publishes spf and strict DMARC reject, and the domain's DKIM key
// when signed is true.
func newLocalRoute(t *testing.T, spf string, signed bool, extraSteps ...pipeline.FilterConfig) *localRoute {
	t.Helper()
	f := newCredentialFixture(t)
	var sender models.Mailbox
	if err := f.db.First(&sender, f.mailboxID).Error; err != nil {
		t.Fatal(err)
	}
	txt := map[string][]string{
		localSendDomain:             {spf},
		"_dmarc." + localSendDomain: {strictReject},
	}
	if signed {
		txt["rm1._domainkey."+localSendDomain] = []string{giveDomainDKIMKey(t, f.db, localSendDomain)}
	}
	serveZone(t, dnsZone{txt: txt, mx: map[string]string{localSendDomain: localSendMX}, a: map[string]string{localSendMX: "127.0.0.1"}})

	route := &localRoute{credentialFixture: f, recipientID: f.seedMailbox(sender.DomainID, "recipient"), scans: &atomic.Int32{}}
	scanner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/scan" {
			route.scans.Add(1)
		}
		_, _ = w.Write([]byte(`{"status":"OK"}`))
	}))
	t.Cleanup(scanner.Close)
	steps := []pipeline.FilterConfig{
		{Name: "spf_check", Type: pipeline.FilterTypeAction, Enabled: true, Unskippable: true},
		{Name: "dkim_verify", Type: pipeline.FilterTypeTransform, Enabled: true, Unskippable: true},
		{Name: "arc_verify", Type: pipeline.FilterTypeTransform, Enabled: true, Unskippable: true},
		{Name: "dmarc_check", Type: pipeline.FilterTypeAction, Enabled: true, Unskippable: true},
	}
	steps = append(steps, extraSteps...)
	steps = append(steps,
		pipeline.FilterConfig{Name: "clamav", Type: pipeline.FilterTypeTransform, Enabled: true, Config: json.RawMessage(fmt.Sprintf(`{"url":%q}`, scanner.URL))},
		pipeline.FilterConfig{Name: "sieve", Type: pipeline.FilterTypeTransform, Enabled: true},
	)
	encoded, err := json.Marshal(steps)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&models.Pipeline{DomainID: sender.DomainID, Direction: "inbound", Active: true, Filters: encoded}).Error; err != nil {
		t.Fatal(err)
	}
	return route
}

// giveDomainDKIMKey gives the domain a key, stored as plaintext as it is with no
// MASTER_KEY, and returns the DNS record that publishes it.
func giveDomainDKIMKey(t *testing.T, db *gorm.DB, domain string) string {
	t.Helper()
	private, public, err := dkim.GenerateKey(2048)
	if err != nil {
		t.Fatalf("generate a DKIM key: %v", err)
	}
	record, err := dkim.RecordValue(public)
	if err != nil {
		t.Fatalf("DKIM record: %v", err)
	}
	if err := db.Model(&models.Domain{}).Where("name = ?", domain).
		Updates(models.Domain{DKIMSelector: "rm1", DKIMPrivateKey: private}).Error; err != nil {
		t.Fatalf("set the domain's DKIM key: %v", err)
	}
	return record
}

// startInboundPort puts the SMTP gateway's real inbound port in front of router
// and returns its address.
func startInboundPort(t *testing.T, db *gorm.DB, public, internal http.Handler) string {
	t.Helper()
	certSrv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(certSrv.Close)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("open the inbound port: %v", err)
	}
	gateway := smtpgw.NewServer("mx.example.test", gatewayAPI(t, public, internal, deliveryCN),
		&tls.Config{Certificates: certSrv.TLS.Certificates, MinVersion: tls.VersionTLS12},
		smtpgw.NewStore(db, ""), connlimiter.New(connlimiter.Config{MaxPerIP: 100, MaxGlobal: 100}))
	if err := gateway.Serve(smtpgw.SMTPListeners{Inbound: listener}); err != nil {
		t.Fatalf("start the SMTP gateway: %v", err)
	}
	t.Cleanup(gateway.Shutdown)
	return listener.Addr().String()
}

// smtpHopOnly records every hosted domain's MX as not taking RESTMAIL. The inbound
// port advertises RESTMAIL, and a message whose peer does is pinned to it with no SMTP
// fallback (#291), but these tests have no HTTPS endpoint at the MX. The worker's
// cache is what it reads first, so it goes straight to SMTP, the route under test.
func smtpHopOnly(t *testing.T, db *gorm.DB) {
	t.Helper()
	var domains []models.Domain
	if err := db.Find(&domains).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, domain := range domains {
		row := models.RESTMAILCapability{Domain: domain.Name, MXHost: localSendMX, Supported: false, LastProbed: now, ExpiresAt: now.Add(time.Hour)}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// startMailRoute runs the inbound port and the queue worker a real install runs
// around the router. From here on, whatever is queued is delivered.
func startMailRoute(t *testing.T, db *gorm.DB, public, internal http.Handler) string {
	t.Helper()
	inbound := startInboundPort(t, db, public, internal)
	smtpHopOnly(t, db)
	worker := queue.NewWorker(db, "mx.example.test", 1, 10*time.Millisecond)
	// What the E2E topology sets as well: a test's peers are on a private network
	// and have certificates no public root issued.
	worker.SetAllowPrivateDestinations(true)
	worker.SetTLSInsecure(true)
	worker.SetMTASTSEnforce(false)
	worker.SetSMTPAddress(func(string) string { return inbound })
	worker.Start()
	t.Cleanup(worker.Shutdown)
	return inbound
}

// queueRow waits for the row to recipient to settle on one of statuses.
func queueRow(t *testing.T, db *gorm.DB, recipient string, statuses ...string) models.OutboundQueue {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var row models.OutboundQueue
	for {
		err := db.Where("recipient = ?", recipient).Order("id DESC").First(&row).Error
		if err == nil {
			for _, status := range statuses {
				if row.Status == status {
					return row
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue row to %s: %+v, err %v; want status in %v", recipient, row, err, statuses)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// deliveredPastGreylist waits for the message to recipient to be delivered,
// letting each greylist delay and retry schedule pass at once: the default inbound
// pipeline has a greylist, and it holds a first message from this server back.
func deliveredPastGreylist(t *testing.T, db *gorm.DB, recipient string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := db.Exec("UPDATE greylist_entries SET retry_after = now() - interval '1 hour'").Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec("UPDATE outbound_queue SET next_attempt = now() WHERE status = 'deferred' AND recipient = ?", recipient).Error; err != nil {
			t.Fatal(err)
		}
		var row models.OutboundQueue
		err := db.Where("recipient = ?", recipient).Order("id DESC").First(&row).Error
		if err == nil && row.Status == "delivered" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue row to %s: %+v, err %v; want delivered", recipient, row, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// send sends a message with the route's name: send, draft, forward or calendar.
func (f *localRoute) send(t *testing.T, route, authorization string, recipients ...string) *httptest.ResponseRecorder {
	t.Helper()
	return localSendRequest(t, f.credentialFixture, route, authorization, recipients)
}

// localSendRequest is one signed-in send to recipients over route. A calendar
// reply goes to the first recipient, the invitation's organizer.
func localSendRequest(t *testing.T, f *credentialFixture, route, authorization string, recipients []string) *httptest.ResponseRecorder {
	t.Helper()
	addresses, _ := json.Marshal(recipients)
	switch route {
	case "draft":
		draft := models.Message{MailboxID: f.mailboxID, Sender: f.address, RecipientsTo: addresses, RecipientsCc: models.JSONB(`[]`), Subject: "local policy", BodyText: "hello", Folder: "Drafts", IsDraft: true}
		if err := f.db.Create(&draft).Error; err != nil {
			t.Fatal(err)
		}
		return f.do(http.MethodPost, fmt.Sprintf("/api/v1/messages/draft/%d/send", draft.ID), authorization, "")
	case "forward":
		if err := f.db.FirstOrCreate(&models.QuotaUsage{}, models.QuotaUsage{MailboxID: f.mailboxID}).Error; err != nil {
			t.Fatal(err)
		}
		source := models.Message{MailboxID: f.mailboxID, Sender: "someone@elsewhere.test", RecipientsTo: models.JSONB(`["` + f.address + `"]`), RecipientsCc: models.JSONB(`[]`), Subject: "local policy", BodyText: "hello", Folder: "Archive", ReceivedAt: time.Now()}
		if err := f.db.Create(&source).Error; err != nil {
			t.Fatal(err)
		}
		return f.do(http.MethodPost, fmt.Sprintf("/api/v1/messages/%d/forward", source.ID), authorization, fmt.Sprintf(`{"from":%q,"to":%s}`, f.address, addresses))
	case "calendar":
		events, err := json.Marshal([]pipeline.CalendarEvent{{Method: "REQUEST", UID: "local-route", Summary: "local policy", Organizer: pipeline.CalendarAddress{Address: recipients[0]}}})
		if err != nil {
			t.Fatal(err)
		}
		invite := models.Message{MailboxID: f.mailboxID, Sender: recipients[0], RecipientsTo: models.JSONB(`["` + f.address + `"]`), RecipientsCc: models.JSONB(`[]`), Subject: "local policy", Folder: "Archive", CalendarEventsRaw: events, ReceivedAt: time.Now()}
		if err := f.db.Create(&invite).Error; err != nil {
			t.Fatal(err)
		}
		return f.do(http.MethodPost, fmt.Sprintf("/api/v1/messages/%d/calendar-reply", invite.ID), authorization, fmt.Sprintf(`{"response":"ACCEPTED","from":%q}`, f.address))
	}
	return f.do(http.MethodPost, "/api/v1/messages/send", authorization, fmt.Sprintf(`{"from":%q,"to":%s,"subject":"local policy","body_text":"hello"}`, f.address, addresses))
}

func wantSent(t *testing.T, route string, response *httptest.ResponseRecorder) {
	t.Helper()
	want := http.StatusCreated
	if route == "calendar" {
		want = http.StatusOK
	}
	if response.Code != want {
		t.Fatalf("%s: %d %s; want %d", route, response.Code, response.Body, want)
	}
}

// answerShape is an answer's status and the names of its data's fields, with its
// own folder: what must not depend on where the recipient is.
func answerShape(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var answer struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
		t.Fatalf("answer %s: %v", response.Body, err)
	}
	fields := make([]string, 0, len(answer.Data))
	for _, field := range []string{"id", "mailbox_id", "folder", "message_id", "thread_id", "sender", "recipients_to", "recipients_cc", "subject", "raw_size", "sealed_hash"} {
		if _, ok := answer.Data[field]; ok {
			fields = append(fields, field)
		}
	}
	return fmt.Sprintf("%d %v in %v", response.Code, fields, answer.Data["folder"])
}

// TestLocalSendQueuesForNormalDelivery: nothing reaches the recipient from the
// send handler, and the answer is the same whether the recipient is on this
// server or not, and on both send routes.
func TestLocalSendQueuesForNormalDelivery(t *testing.T) {
	answers := map[string]string{}
	for _, route := range []string{"send", "draft", "forward", "calendar"} {
		t.Run(route, func(t *testing.T) {
			f := newLocalRoute(t, spfOtherServer, true)
			script := `require ["fileinto"]; fileinto "Local";`
			if err := f.db.Create(&models.SieveScript{MailboxID: f.recipientID, Script: script, ScriptHash: fmt.Sprintf("%x", sha256.Sum256([]byte(script))), Active: true}).Error; err != nil {
				t.Fatal(err)
			}
			response := f.send(t, route, f.session, "recipient@example.test")
			wantSent(t, route, response)
			if route == "send" || route == "draft" {
				answers[route] = answerShape(t, response)
			}
			var queued []models.OutboundQueue
			if err := f.db.Where("sender = ?", f.address).Find(&queued).Error; err != nil {
				t.Fatal(err)
			}
			if len(queued) != 1 || queued[0].Recipient != "recipient@example.test" || queued[0].Domain != localSendDomain || queued[0].Status != "pending" {
				t.Errorf("local send must enter the normal outbound queue: %+v", queued)
			}
			if f.scans.Load() != 0 {
				t.Errorf("inbound scanner ran before outbound delivery: %d scans", f.scans.Load())
			}
			var copies int64
			f.db.Model(&models.Message{}).Where("mailbox_id = ?", f.recipientID).Count(&copies)
			if copies != 0 {
				t.Errorf("recipient holds %d copies before the worker delivered any", copies)
			}

			// A recipient elsewhere gets the same answer, and the same queue row.
			if route != "send" && route != "draft" {
				return
			}
			external := f.send(t, route, f.session, "someone@elsewhere.test")
			wantSent(t, route, external)
			if got, want := answerShape(t, external), answers[route]; got != want {
				t.Errorf("%s answers a local recipient with %s and an external one with %s", route, want, got)
			}
		})
	}
	if answers["send"] != answers["draft"] {
		t.Errorf("sendMessage answers %s, draft send %s", answers["send"], answers["draft"])
	}
}

// TestLocalSendPassesDMARCOnItsSignature is the owner's case: two users on one
// domain with p=reject, adkim=s and an SPF record that does not list this server.
// The message arrives through the inbound route, and DMARC passes on the real
// DKIM signature, with every sender check on.
func TestLocalSendPassesDMARCOnItsSignature(t *testing.T) {
	for _, route := range []string{"send", "draft", "forward", "calendar"} {
		t.Run(route, func(t *testing.T) {
			f := newLocalRoute(t, spfOtherServer, true)
			script := `require ["fileinto"]; fileinto "Local";`
			if err := f.db.Create(&models.SieveScript{MailboxID: f.recipientID, Script: script, ScriptHash: fmt.Sprintf("%x", sha256.Sum256([]byte(script))), Active: true}).Error; err != nil {
				t.Fatal(err)
			}
			wantSent(t, route, f.send(t, route, f.session, "recipient@example.test"))
			startMailRoute(t, f.db, f.router, f.gatewayRouter())

			row := queueRow(t, f.db, "recipient@example.test", "delivered", "bounced")
			if row.Status != "delivered" {
				t.Fatalf("queue row ended %s: %s", row.Status, row.LastError)
			}
			var copy models.Message
			if err := f.db.Where("mailbox_id = ?", f.recipientID).First(&copy).Error; err != nil {
				t.Fatalf("no copy for the recipient: %v", err)
			}
			if copy.Folder != "Local" {
				t.Errorf("the recipient's Sieve rule did not run: copy in %q", copy.Folder)
			}
			if f.scans.Load() != 1 {
				t.Errorf("the recipient's virus scan ran %d times, want 1", f.scans.Load())
			}
			raw := copy.RawMessage
			for _, want := range []string{"DKIM-Signature:", "spf=fail", "dkim=pass", "dmarc=pass"} {
				if !strings.Contains(raw, want) {
					t.Errorf("stored copy has no %q; its verdicts: %s", want, authenticationResults(raw))
				}
			}
			var dsns int64
			f.db.Model(&models.Message{}).Where("mailbox_id = ? AND is_dsn = ?", f.mailboxID, true).Count(&dsns)
			if dsns != 0 {
				t.Errorf("sender got %d bounces for delivered mail", dsns)
			}
		})
	}
}

// authenticationResults is the verdict header of a stored message, for failures.
func authenticationResults(raw string) string {
	for _, line := range strings.Split(raw, "\r\n") {
		if strings.HasPrefix(line, "Authentication-Results:") {
			return line
		}
	}
	return "(none)"
}

// TestLocalSendPermanentRefusalBounces: a send with several recipients is decided
// per recipient, after the answer. One refused for good fails its own queue row
// and bounces to the sender, and the other is still delivered.
func TestLocalSendPermanentRefusalBounces(t *testing.T) {
	for _, route := range []string{"send", "draft"} {
		t.Run(route, func(t *testing.T) {
			f := newLocalRoute(t, spfOtherServer, true)
			wantSent(t, route, f.send(t, route, f.session, "recipient@example.test", "nobody@example.test"))
			startMailRoute(t, f.db, f.router, f.gatewayRouter())

			if row := queueRow(t, f.db, "recipient@example.test", "delivered", "bounced"); row.Status != "delivered" {
				t.Errorf("the recipient that exists: %s: %s", row.Status, row.LastError)
			}
			refused := queueRow(t, f.db, "nobody@example.test", "bounced", "delivered")
			if refused.Status != "bounced" || refused.LastErrorCode != 550 {
				t.Errorf("the recipient that does not exist: %s %d: %s", refused.Status, refused.LastErrorCode, refused.LastError)
			}
			assertBounced(t, f, "nobody@example.test")
		})
	}
}

// assertBounced waits for the sender's one DSN, which names the recipient.
func assertBounced(t *testing.T, f *localRoute, recipient string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var dsns []models.Message
	for {
		if err := f.db.Where("mailbox_id = ? AND is_dsn = ? AND folder = ?", f.mailboxID, true, "INBOX").Find(&dsns).Error; err != nil {
			t.Fatal(err)
		}
		if len(dsns) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(dsns) != 1 || !strings.Contains(dsns[0].BodyText, recipient) {
		t.Errorf("the sender's bounces: %d, want one naming %s: %+v", len(dsns), recipient, dsns)
	}
}

// TestLocalSendWithoutADKIMKey: a domain with no key sends unsigned, as it does
// to anyone. What the inbound checks make of it decides: DMARC passes on SPF when
// the domain's record lists this server, and otherwise the message is refused like
// any other, and bounces.
func TestLocalSendWithoutADKIMKey(t *testing.T) {
	for _, route := range []string{"send", "draft"} {
		t.Run(route+"/SPF lists this server", func(t *testing.T) {
			f := newLocalRoute(t, spfThisServer, false)
			wantSent(t, route, f.send(t, route, f.session, "recipient@example.test"))
			startMailRoute(t, f.db, f.router, f.gatewayRouter())
			if row := queueRow(t, f.db, "recipient@example.test", "delivered", "bounced"); row.Status != "delivered" {
				t.Fatalf("queue row ended %s: %s", row.Status, row.LastError)
			}
			var copy models.Message
			if err := f.db.Where("mailbox_id = ?", f.recipientID).First(&copy).Error; err != nil {
				t.Fatal(err)
			}
			if strings.Contains(copy.RawMessage, "DKIM-Signature:") {
				t.Error("a domain with no key signed")
			}
			for _, want := range []string{"spf=pass", "dmarc=pass"} {
				if !strings.Contains(copy.RawMessage, want) {
					t.Errorf("stored copy has no %q; its verdicts: %s", want, authenticationResults(copy.RawMessage))
				}
			}
		})
		t.Run(route+"/SPF does not list this server", func(t *testing.T) {
			f := newLocalRoute(t, spfOtherServer, false)
			wantSent(t, route, f.send(t, route, f.session, "recipient@example.test"))
			startMailRoute(t, f.db, f.router, f.gatewayRouter())
			row := queueRow(t, f.db, "recipient@example.test", "bounced", "delivered")
			if row.Status != "bounced" || row.LastErrorCode != 550 {
				t.Fatalf("queue row: %s %d: %s; want bounced with 550", row.Status, row.LastErrorCode, row.LastError)
			}
			assertBounced(t, f, "recipient@example.test")
			var copies int64
			f.db.Model(&models.Message{}).Where("mailbox_id = ?", f.recipientID).Count(&copies)
			if copies != 0 {
				t.Errorf("a refused message left %d copies with the recipient", copies)
			}
		})
	}
}

// TestLocalSendThatCannotBeSigned: a configured key that cannot sign refuses the
// send with a retryable answer, on both routes, as it does for any recipient: no
// Sent copy, no queue row, and a draft is kept to retry. Nothing leaves unsigned.
func TestLocalSendThatCannotBeSigned(t *testing.T) {
	for _, route := range []string{"send", "draft"} {
		t.Run(route, func(t *testing.T) {
			f := newLocalRoute(t, spfOtherServer, true)
			if err := f.db.Model(&models.Domain{}).Where("name = ?", localSendDomain).Update("dkim_private_key", "not a key").Error; err != nil {
				t.Fatal(err)
			}
			response := f.send(t, route, f.session, "recipient@example.test")
			if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"deferred"`) {
				t.Fatalf("%d %s; want 503 deferred", response.Code, response.Body)
			}
			var queued, sent, drafts int64
			f.db.Model(&models.OutboundQueue{}).Count(&queued)
			f.db.Model(&models.Message{}).Where("mailbox_id = ? AND folder = ?", f.mailboxID, "Sent").Count(&sent)
			f.db.Model(&models.Message{}).Where("mailbox_id = ? AND is_draft = ?", f.mailboxID, true).Count(&drafts)
			if queued != 0 || sent != 0 {
				t.Errorf("%d queued and %d sent, want neither", queued, sent)
			}
			if route == "draft" && drafts != 1 {
				t.Errorf("%d drafts, want the draft kept", drafts)
			}
		})
	}
}

// TestLocalSendTemporaryRefusalRetries: a temporary refusal keeps the message on
// the queue with no bounce, and a later attempt delivers it.
func TestLocalSendTemporaryRefusalRetries(t *testing.T) {
	greylist := pipeline.FilterConfig{Name: "greylist", Type: pipeline.FilterTypeAction, Enabled: true, Config: json.RawMessage(`{"delay_minutes": 5, "ttl_days": 36}`)}
	for _, route := range []string{"send", "draft"} {
		t.Run(route, func(t *testing.T) {
			f := newLocalRoute(t, spfOtherServer, true, greylist)
			wantSent(t, route, f.send(t, route, f.session, "recipient@example.test"))
			startMailRoute(t, f.db, f.router, f.gatewayRouter())

			row := queueRow(t, f.db, "recipient@example.test", "deferred", "delivered", "bounced")
			if row.Status != "deferred" || row.LastErrorCode != 450 || !row.NextAttempt.After(time.Now()) {
				t.Fatalf("queue row after a 450: %s %d, next attempt %s: %s", row.Status, row.LastErrorCode, row.NextAttempt, row.LastError)
			}
			var copies, dsns int64
			f.db.Model(&models.Message{}).Where("mailbox_id = ?", f.recipientID).Count(&copies)
			f.db.Model(&models.Message{}).Where("mailbox_id = ? AND is_dsn = ?", f.mailboxID, true).Count(&dsns)
			if copies != 0 || dsns != 0 {
				t.Errorf("after a 450: %d copies, %d bounces, want neither", copies, dsns)
			}

			// The delay has passed, and the retry falls due.
			if err := f.db.Exec("UPDATE greylist_entries SET retry_after = now() - interval '1 hour'").Error; err != nil {
				t.Fatal(err)
			}
			if err := f.db.Model(&row).Update("next_attempt", time.Now().Add(-time.Second)).Error; err != nil {
				t.Fatal(err)
			}
			if row := queueRow(t, f.db, "recipient@example.test", "delivered", "bounced"); row.Status != "delivered" {
				t.Fatalf("the retry: %s: %s", row.Status, row.LastError)
			}
		})
	}
}

// TestLocalSendDMARCUntrusted: the full route adds no way around the checks. A
// send needs a session, and mail from outside for the same domain is refused both
// on the deliver route and on the real inbound port, whatever it claims.
func TestLocalSendDMARCUntrusted(t *testing.T) {
	f := newLocalRoute(t, spfOtherServer, true)
	for _, route := range []string{"send", "draft"} {
		response := f.send(t, route, "", "recipient@example.test")
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("unsigned %s: %d %s", route, response.Code, response.Body)
		}
	}
	raw := "From: chris@example.test\r\nTo: recipient@example.test\r\nSubject: forged\r\nAuthentication-Results: restmail; spf=pass; dkim=pass; dmarc=pass\r\nX-Authenticated-Sender: true\r\nDKIM-Signature: v=1; a=rsa-sha256; d=example.test; s=rm1; h=from; bh=AAAA; b=AAAA\r\n\r\nforged"
	for _, clientIP := range []string{"", "192.0.2.44"} {
		body := fmt.Sprintf(`{"address":"recipient@example.test","sender":%q,"recipients_to":["recipient@example.test"],"subject":"forged","raw_message":%q,"client_ip":%q}`, f.address, base64.StdEncoding.EncodeToString([]byte(raw)), clientIP)
		response := doRequest(asGateway(deliveryCN, f.gatewayRouter()), http.MethodPost, "/api/v1/messages/deliver", "", body)
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "550 DMARC policy reject for example.test") {
			t.Errorf("forged inbound with client IP %q: %d %s; want DMARC refusal", clientIP, response.Code, response.Body)
		}
	}

	inbound := startInboundPort(t, f.db, f.router, f.gatewayRouter())
	c, err := smtp.Dial(inbound)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Hello("forger.test"); err != nil {
		t.Fatal(err)
	}
	if err := c.StartTLS(&tls.Config{InsecureSkipVerify: true}); err != nil { //nolint:gosec // the test's own certificate
		t.Fatal(err)
	}
	if err := send(c, f.address, "recipient@example.test", raw); smtpCode(err) != 550 {
		t.Errorf("forged over the inbound port: %v; want 550", err)
	}

	var count int64
	if err := f.db.Model(&models.Message{}).Where("mailbox_id = ?", f.recipientID).Count(&count).Error; err != nil || count != 0 || f.scans.Load() != 0 {
		t.Fatalf("untrusted sends delivered %d copies, scanned %d: %v", count, f.scans.Load(), err)
	}
	var queued int64
	f.db.Model(&models.OutboundQueue{}).Count(&queued)
	if queued != 0 {
		t.Errorf("mail from outside was queued %d times: inbound mail must never become outbound", queued)
	}
}
