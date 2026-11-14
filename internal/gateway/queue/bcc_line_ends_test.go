package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/rest-mail/go-mtasts/pkg/mtasts"
	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/api/handlers"
	"github.com/rest-mail/rest-mail-server/internal/bootstrap"
	rmdb "github.com/rest-mail/rest-mail-server/internal/db"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
	"github.com/rest-mail/rest-mail-server/internal/pipeline/filters"
	"gorm.io/gorm"
)

type bccPeerShape struct{ name, raw, peer string }

// bccPeerShapes are the line ends a queue row can hold, because submission keeps a client's bytes
// exactly (#1970). peer is what a RESTMAIL peer must read: the row without its Bcc and Resent-Bcc
// fields (#1973), every other byte as it was.
var bccPeerShapes = append(withResentBcc([]bccPeerShape{
	{
		name: "all CRLF",
		raw:  "From: a@x.test\r\nBcc: secret@c.test\r\nSubject: s\r\n\r\nbody\r\n",
		peer: "From: a@x.test\r\nSubject: s\r\n\r\nbody\r\n",
	},
	{
		name: "all LF",
		raw:  "From: a@x.test\nBcc: secret@c.test\nSubject: s\n\nbody\n",
		peer: "From: a@x.test\nSubject: s\n\nbody\n",
	},
	{
		name: "LF head, with a CRLF blank line later in the body",
		raw:  "From: a@x.test\nBcc: secret@c.test\nSubject: s\n\nbody\n\r\n\r\nmore\r\n",
		peer: "From: a@x.test\nSubject: s\n\nbody\n\r\n\r\nmore\r\n",
	},
	{
		name: "one LF in a CRLF head",
		raw:  "From: a@x.test\r\nBcc: secret@c.test\nSubject: s\r\n\r\nbody\r\n",
		peer: "From: a@x.test\r\nSubject: s\r\n\r\nbody\r\n",
	},
	{
		name: "one LF before the field",
		raw:  "From: a@x.test\nBcc: secret@c.test\r\nSubject: s\r\n\r\nbody\r\n",
		peer: "From: a@x.test\nSubject: s\r\n\r\nbody\r\n",
	},
	{
		name: "bare CR in the head",
		raw:  "From: a@x.test\rBcc: secret@c.test\r\nSubject: s\r\n\r\nbody\r\n",
		peer: "From: a@x.test\rSubject: s\r\n\r\nbody\r\n",
	},
	{
		name: "folded with mixed line ends, in another case, twice",
		raw:  "BCC: secret@c.test,\n more@c.test\r\nFrom: a@x.test\r\nbcc : \r\n\tsecret2@c.test\rSubject: s\r\n\r\nbody\r\n",
		peer: "From: a@x.test\r\nSubject: s\r\n\r\nbody\r\n",
	},
	{
		name: "a Bcc line in the body stays",
		raw:  "From: a@x.test\nBcc: secret@c.test\n\nBcc: quoted in the text\n\r\n\r\nmore\r\n",
		peer: "From: a@x.test\n\nBcc: quoted in the text\n\r\n\r\nmore\r\n",
	},
	{
		name: "a space before the first field, which a forward's stamp must not fold",
		raw:  " Bcc: secret@c.test\r\nFrom: a@x.test\r\nSubject: s\r\n\r\nbody\r\n",
		peer: "From: a@x.test\r\nSubject: s\r\n\r\nbody\r\n",
	},
}), bccPeerShape{
	name: "a resent message: only the hidden field goes",
	raw:  "Resent-From: b@x.test\r\nResent-To: c@x.test\r\nResent-Bcc: secret@c.test,\r\n more@c.test\r\nFrom: a@x.test\r\nSubject: s\r\n\r\nbody\r\n",
	peer: "Resent-From: b@x.test\r\nResent-To: c@x.test\r\nFrom: a@x.test\r\nSubject: s\r\n\r\nbody\r\n",
}, bccPeerShape{
	name: "a Bcc that only comes into view when the Resent-Bcc goes",
	raw:  "From: a@x.test\rResent-Bcc: secret@c.test\n\nBcc: secret2@c.test\n\nbody\n",
	peer: "From: a@x.test\r\n\nbody\n",
})

// withResentBcc adds the same row about Resent-Bcc, in the same case, after each row about Bcc.
func withResentBcc(shapes []bccPeerShape) []bccPeerShape {
	resent := strings.NewReplacer("Bcc", "Resent-Bcc", "BCC", "RESENT-BCC", "bcc", "resent-bcc")
	var both []bccPeerShape
	for _, shape := range shapes {
		both = append(both, shape, bccPeerShape{"Resent-Bcc, " + shape.name, resent.Replace(shape.raw), resent.Replace(shape.peer)})
	}
	return both
}

// checkBccFreeData is what a peer's DATA must show for one of the shapes.
func checkBccFreeData(t *testing.T, tc bccPeerShape, data string) {
	t.Helper()
	if strings.Contains(data, "secret") || strings.Contains(data, "more@") {
		t.Errorf("a hidden recipient reached the peer: %q", data)
	}
	for _, kept := range []string{"From: a@x.test", "body"} {
		if strings.Contains(tc.peer, kept) && !strings.Contains(data, kept) {
			t.Errorf("DATA lost %q: %q", kept, data)
		}
	}
	if strings.Contains(tc.peer, "quoted in the text") != strings.Contains(data, "quoted in the text") {
		t.Errorf("the line in the body changed: %q", data)
	}
}

// A row's Bcc and Resent-Bcc fields do not reach a peer by the worker's real RESTMAIL route, from the
// EHLO probe through discovery to the POST. The recipient it is delivered to doesn't change, and the
// row keeps the source it was stored with.
func TestBccDoesNotReachARESTMAILPeer(t *testing.T) {
	for i, tc := range bccPeerShapes {
		t.Run(tc.name, func(t *testing.T) {
			domain := fmt.Sprintf("bcc-route-%d.test", i)
			var posted []string
			var base string
			srv, w := discoveryServer(t, func(rw http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/.well-known/restmail":
					fmt.Fprintf(rw, `{"endpoint":%q}`, base+"/restmail")
				case "/restmail/messages":
					body, _ := io.ReadAll(r.Body)
					var payload struct {
						RawMessage []byte `json:"raw_message"`
					}
					if err := json.Unmarshal(body, &payload); err != nil {
						t.Errorf("the POST is not JSON: %v", err)
					}
					posted = append(posted, string(payload.RawMessage))
				default:
					rw.WriteHeader(http.StatusNotFound)
				}
			})
			base = srv.URL
			gdb := bccRouteWorker(t, w, domain, srv.URL)
			item := models.OutboundQueue{Sender: "a@x.test", Recipient: "bob@" + domain, Domain: domain,
				RawMessage: tc.raw, Status: "delivering", MaxRetries: 10}
			if err := gdb.Create(&item).Error; err != nil {
				t.Fatalf("queue row: %v", err)
			}
			t.Cleanup(func() { gdb.Delete(&models.OutboundQueue{}, item.ID) })

			if err := w.deliver(context.Background(), item); err != nil {
				t.Fatalf("deliver: %v", err)
			}
			if len(posted) != 1 {
				t.Fatalf("the peer took %d POSTs, want 1", len(posted))
			}
			// The whole message is compared, so a change to how raw_message is carried can't turn
			// this into a search of bytes that say nothing.
			if posted[0] != tc.peer {
				t.Errorf("raw_message = %q\n         want %q", posted[0], tc.peer)
			}
			if row := readRow(t, gdb, item.ID); row.RawMessage != tc.raw {
				t.Errorf("the stored source changed: %q, want %q", row.RawMessage, tc.raw)
			}
		})
	}
}

// The same rows over SMTP DATA: nothing a hidden field held reaches the peer, the other fields and
// the body do, the envelope still names the recipient, and the row keeps its source.
func TestBccDoesNotReachAnSMTPPeer(t *testing.T) {
	for i, tc := range bccPeerShapes {
		t.Run(tc.name, func(t *testing.T) {
			domain := fmt.Sprintf("bcc-data-%d.test", i)
			gdb := restmailTestDB(t, domain)
			w := &Worker{db: gdb, hostname: "sender.test", allowPrivateDest: true, sendDeadline: defaultSendDeadline}
			item := models.OutboundQueue{Sender: "a@x.test", Recipient: "bob@" + domain, Domain: domain,
				RawMessage: tc.raw, Status: "delivering", MaxRetries: 10}
			if err := gdb.Create(&item).Error; err != nil {
				t.Fatalf("queue row: %v", err)
			}
			t.Cleanup(func() { gdb.Delete(&models.OutboundQueue{}, item.ID) })
			cert, _ := relayTestCertificate(t, false)
			settings, finish := relayTestPeer(t, cert, relayPeerOptions{})
			addr := net.JoinHostPort(settings.host, strconv.Itoa(settings.port))
			if err := w.deliverSMTP(context.Background(), addr, settings.host, item, nil, nil); err != nil {
				t.Fatalf("deliverSMTP: %v", err)
			}
			result := finish()
			checkBccFreeData(t, tc, result.data)
			if rcpt := "RCPT TO:<bob@" + domain + ">"; !strings.Contains(strings.Join(result.commands, "\n"), rcpt) {
				t.Errorf("the envelope lost %s: %q", rcpt, result.commands)
			}
			if row := readRow(t, gdb, item.ID); row.RawMessage != tc.raw {
				t.Errorf("the stored source changed: %q, want %q", row.RawMessage, tc.raw)
			}
		})
	}
}

// A message that arrives from another server and is forwarded by a Sieve redirect or an alias
// (buildRedirectForwards) leaves without its hidden fields, on both routes. Each row is made by the
// real inbound POST and sent by the worker. The row and the kept copy still hold the source exactly.
func TestForwardedMessageLeavesWithoutHiddenFields(t *testing.T) {
	// The whole schema, on its own connection: migrating inside a test's transaction blocks.
	if err := rmdb.AutoMigrate(openQueueTestDB(t)); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"sieve redirect", "alias"} {
		t.Run(kind, func(t *testing.T) {
			w, tx, forwarder := srsForwardWorker(t)
			inbound, target, box := forwardFixture(t, tx, forwarder, kind)

			var posted string
			var postedTo []string
			srv := captureRESTMAILServer(t, &posted, &postedTo)
			w.restmailRoots = srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs

			for i, tc := range bccPeerShapes {
				t.Run(tc.name, func(t *testing.T) {
					payload, _ := json.Marshal(map[string]any{"from": "author@original.test", "to": []string{inbound}, "raw_message": []byte(tc.raw)})
					req := httptest.NewRequest(http.MethodPost, "/restmail/messages", strings.NewReader(string(payload)))
					req.RemoteAddr = "127.0.0.1:5000"
					rr := httptest.NewRecorder()
					box.router.ServeHTTP(rr, req)
					if rr.Code != http.StatusCreated {
						t.Fatalf("inbound POST = %d %s", rr.Code, rr.Body)
					}
					var rows []models.OutboundQueue
					if err := tx.Where("sending_domain = ? AND recipient = ?", forwarder, target).Find(&rows).Error; err != nil {
						t.Fatal(err)
					}
					if len(rows) != 1 {
						t.Fatalf("queue rows = %d, want 1", len(rows))
					}
					row := rows[0]
					t.Cleanup(func() { tx.Delete(&models.OutboundQueue{}, row.ID) })
					if !strings.HasSuffix(row.RawMessage, strings.TrimLeft(tc.raw, " ")) {
						t.Fatalf("the row isn't the message it forwards: %q", row.RawMessage)
					}
					if kind == "alias" && row.ARCResults == "" {
						t.Fatal("the forward has no ARC results, so it isn't on the sealing branch")
					}

					if err := w.deliverRESTMAILHTTPS(context.Background(), srv.URL, row); err != nil {
						t.Fatalf("RESTMAIL: %v", err)
					}
					if !strings.HasSuffix(posted, tc.peer) {
						t.Errorf("RESTMAIL raw_message = %q\nwant it to end with %q", posted, tc.peer)
					}
					if strings.Contains(posted, "secret") || strings.Contains(posted, "more@") {
						t.Errorf("a hidden recipient reached the RESTMAIL peer: %q", posted)
					}
					if len(postedTo) != 1 || postedTo[0] != target {
						t.Errorf("RESTMAIL recipients = %v, want %s", postedTo, target)
					}

					cert, _ := relayTestCertificate(t, false)
					settings, finish := relayTestPeer(t, cert, relayPeerOptions{})
					addr := net.JoinHostPort(settings.host, strconv.Itoa(settings.port))
					if err := w.deliverSMTP(context.Background(), addr, settings.host, row, nil, nil); err != nil {
						t.Fatalf("SMTP: %v", err)
					}
					result := finish()
					checkBccFreeData(t, tc, result.data)
					if rcpt := "RCPT TO:<" + target + ">"; !strings.Contains(strings.Join(result.commands, "\n"), rcpt) {
						t.Errorf("the envelope lost %s: %q", rcpt, result.commands)
					}
					if stored := readRow(t, tx, row.ID); stored.RawMessage != row.RawMessage {
						t.Errorf("sending changed the row: %q, want %q", stored.RawMessage, row.RawMessage)
					}
					if err := tx.Delete(&models.OutboundQueue{}, row.ID).Error; err != nil {
						t.Fatal(err)
					}
					if box.id != 0 {
						var kept []models.Message
						if err := tx.Where("mailbox_id = ?", box.id).Order("id DESC").Limit(1).Find(&kept).Error; err != nil {
							t.Fatal(err)
						}
						if len(kept) != 1 || !strings.HasSuffix(string(kept[0].Source()), tc.raw) {
							t.Errorf("the kept copy of shape %d isn't the source", i)
						}
					}
				})
			}
		})
	}
}

// forwardBox is the inbound API of a forwarding domain, and the mailbox that keeps a copy, if any.
type forwardBox struct {
	router http.Handler
	id     uint
}

// forwardFixture gives domain a mailbox with a Sieve "redirect :copy", or an alias, and answers the
// address to send to, the target it forwards to, and the inbound API.
func forwardFixture(t *testing.T, tx *gorm.DB, domain, kind string) (inbound, target string, box forwardBox) {
	t.Helper()
	if err := tx.Model(&models.Domain{}).Where("name = ?", domain).Update("active", true).Error; err != nil {
		t.Fatal(err)
	}
	var dom models.Domain
	if err := tx.Where("name = ?", domain).First(&dom).Error; err != nil {
		t.Fatal(err)
	}
	var steps []pipeline.FilterConfig
	if kind == "alias" {
		inbound, target = "alias@"+domain, "fwd@elsewhere.test"
		if err := tx.Create(&models.Alias{DomainID: dom.ID, SourceAddress: inbound, DestinationAddress: target, Active: true}).Error; err != nil {
			t.Fatal(err)
		}
		steps = []pipeline.FilterConfig{{Name: "recipient_check", Type: pipeline.FilterTypeAction, Enabled: true}}
	} else {
		// A script may redirect only inside the domain unless the operator allows more (OSI-13).
		inbound, target = "dana@"+domain, "fwd@"+domain
		mb := models.Mailbox{DomainID: dom.ID, LocalPart: "dana", Address: inbound, Active: true, QuotaBytes: 1 << 30}
		if err := tx.Create(&mb).Error; err != nil {
			t.Fatal(err)
		}
		if err := bootstrap.EnsureRBAC(tx); err != nil {
			t.Fatal(err)
		}
		if err := grants.EnsureDefaults(tx, &mb); err != nil {
			t.Fatal(err)
		}
		script, _ := json.Marshal(map[string]string{"script": `require ["copy"]; redirect :copy "` + target + `";`})
		steps = []pipeline.FilterConfig{{Name: "sieve", Type: pipeline.FilterTypeTransform, Enabled: true, Config: script}}
		box.id = mb.ID
	}
	stored, _ := json.Marshal(steps)
	if err := tx.Create(&models.Pipeline{DomainID: dom.ID, Direction: "inbound", Active: true, Filters: stored}).Error; err != nil {
		t.Fatal(err)
	}
	registry := pipeline.NewRegistry()
	registry.Register("sieve", filters.NewSieve)
	registry.Register("recipient_check", filters.NewRecipientCheck(tx))
	registry.Register("dmarc_reports_received", filters.NewDMARCReportsReceived)
	registry.Register("vacation", func([]byte) (pipeline.Filter, error) { return aliasForwardPass{}, nil })
	engine := pipeline.NewEngine(registry, nil)
	router := chi.NewRouter()
	routes := endpoint.Routes{Router: router, Registry: endpoint.NewRegistry()}
	middleware := endpoint.Middleware{Wrap: func(next http.Handler) http.Handler { return next }}
	messages := handlers.NewMessageHandler(tx, nil, engine, relayRouteMasterKey, nil)
	rest := handlers.NewRestmailHandler(tx, engine, nil, handlers.RestmailTarpitConfig{}, handlers.RestmailDeliverAuthConfig{TrustedCIDRs: []string{"127.0.0.0/8"}})
	rest.SetLocalDelivery(messages)
	rest.Register(routes, middleware)
	box.router = router
	return inbound, target, box
}

// A DSN returns the failed message's head, and DSN part 3 sits in the notice's body where the
// worker's strip can't reach it, so it is left out when the notice is built. The notice goes to a
// sender outside, and this is what that peer reads.
func TestHiddenFieldsDoNotComeBackInADSN(t *testing.T) {
	w, tx, _ := srsForwardWorker(t)
	if err := tx.AutoMigrate(&models.Mailbox{}, &models.OutboundQueue{}); err != nil {
		t.Fatal(err)
	}
	for i, tc := range bccPeerShapes {
		t.Run(tc.name, func(t *testing.T) {
			author := fmt.Sprintf("author-%d@original.test", i)
			w.generateBounce(context.Background(), models.OutboundQueue{ID: uint(i + 1), Sender: author, Recipient: "gone@far.test", Domain: "far.test", RawMessage: tc.raw},
				&SMTPError{Code: 550, Enhanced: "5.1.1", Message: "no such user"})
			var notice models.OutboundQueue
			if err := tx.Where("recipient = ?", author).First(&notice).Error; err != nil {
				t.Fatalf("no DSN: %v", err)
			}
			cert, _ := relayTestCertificate(t, false)
			settings, finish := relayTestPeer(t, cert, relayPeerOptions{})
			addr := net.JoinHostPort(settings.host, strconv.Itoa(settings.port))
			if err := w.deliverSMTP(context.Background(), addr, settings.host, notice, nil, nil); err != nil {
				t.Fatalf("deliverSMTP: %v", err)
			}
			data := finish().data
			if strings.Contains(data, "secret") || strings.Contains(data, "more@") {
				t.Errorf("a hidden recipient came back in the DSN: %q", data)
			}
			if !strings.Contains(data, "From: a@x.test") || !strings.Contains(data, "gone@far.test") {
				t.Errorf("the DSN lost the head or the failed recipient: %q", data)
			}
		})
	}
}

// bccRouteWorker makes w deliver to domain by RESTMAIL, through an MX that advertises it and the
// discovery endpoint at srvURL, and answers the database the row belongs in.
func bccRouteWorker(t *testing.T, w *Worker, domain, srvURL string) *gorm.DB {
	t.Helper()
	gdb := restmailTestDB(t, domain)
	mx := newNoFallbackMX(t, true)
	w.db = gdb
	w.resolver = fixedMXResolver{host: "mx." + domain}
	w.smtpAddr = func(string) string { return mx.addr }
	w.restmailDiscoveryURL = func(_, d string) string { return srvURL + "/.well-known/restmail?domain=" + d }
	w.sts = mtasts.NewResolver()
	w.sts.LookupTXT = func(context.Context, string) ([]string, error) { return nil, mtasts.ErrNoPolicy }
	return gdb
}

// A message signed before it came here keeps the one signature on the real RESTMAIL POST, wherever
// its line ends hid it from the old split, so the worker doesn't sign it a second time.
func TestSignatureHiddenByLineEndsIsNotSignedAgain(t *testing.T) {
	const masterKey = "bcc-line-ends-master-key"
	db := openQueueTestDB(t)
	tx := db.Begin()
	t.Cleanup(func() { tx.Rollback() })
	domain := seedSignedDomain(t, tx, masterKey)

	var got string
	var gotTo []string
	srv := captureRESTMAILServer(t, &got, &gotTo)
	w := &Worker{db: tx, masterKey: masterKey, sendDeadline: defaultSendDeadline, restmailRoots: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs, allowPrivateDest: true}

	const signature = "DKIM-Signature: v=1; a=rsa-sha256; d=x.test; s=sel; b=abc"
	for name, raw := range map[string]string{
		"LF head, CRLF blank later": "From: a@" + domain + "\n" + signature + "\nSubject: s\n\nbody\n\r\n\r\nmore\r\n",
		"one LF in the head":        "From: a@" + domain + "\n" + signature + "\r\nSubject: s\r\n\r\nbody\r\n",
		"bare CR in the head":       "From: a@" + domain + "\r" + signature + "\r\nSubject: s\r\n\r\nbody\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			item := models.OutboundQueue{Sender: "a@" + domain, Recipient: "bob@example.net", RawMessage: raw}
			if err := w.deliverRESTMAILHTTPS(context.Background(), srv.URL, item); err != nil {
				t.Fatalf("deliverRESTMAILHTTPS: %v", err)
			}
			if got != raw {
				t.Fatalf("raw_message = %q, want it as stored: %q", got, raw)
			}
		})
	}
}
