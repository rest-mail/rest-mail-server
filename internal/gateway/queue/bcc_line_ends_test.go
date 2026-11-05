package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/rest-mail/go-mtasts/pkg/mtasts"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"gorm.io/gorm"
)

// bccPeerShapes are the line ends a queue row can hold, because submission keeps a client's bytes
// exactly (#1970). peer is what a RESTMAIL peer must read: the row without its Bcc fields, every
// other byte as it was.
var bccPeerShapes = []struct{ name, raw, peer string }{
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
}

// A row's Bcc fields do not reach a peer by the worker's real RESTMAIL route, from the EHLO probe
// through discovery to the POST, and the row keeps the source it was stored with.
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
						RawMessage string `json:"raw_message"`
					}
					if err := json.Unmarshal(body, &payload); err != nil {
						t.Errorf("the POST is not JSON: %v", err)
					}
					posted = append(posted, payload.RawMessage)
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

			if err := w.deliver(item); err != nil {
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

// The same rows over SMTP DATA: nothing a Bcc field held reaches the peer, the other fields and the
// body do, and the row keeps its source.
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
			if err := w.deliverSMTP(addr, settings.host, item, nil, nil); err != nil {
				t.Fatalf("deliverSMTP: %v", err)
			}
			data := finish().data
			if strings.Contains(data, "secret") || strings.Contains(data, "more@") {
				t.Errorf("a Bcc recipient reached the peer: %q", data)
			}
			for _, kept := range []string{"From: a@x.test", "body"} {
				if strings.Contains(tc.peer, kept) && !strings.Contains(data, kept) {
					t.Errorf("DATA lost %q: %q", kept, data)
				}
			}
			if strings.Contains(tc.peer, "quoted in the text") != strings.Contains(data, "quoted in the text") {
				t.Errorf("the line in the body changed: %q", data)
			}
			if row := readRow(t, gdb, item.ID); row.RawMessage != tc.raw {
				t.Errorf("the stored source changed: %q, want %q", row.RawMessage, tc.raw)
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
			if err := w.deliverRESTMAILHTTPS(srv.URL, item); err != nil {
				t.Fatalf("deliverRESTMAILHTTPS: %v", err)
			}
			if got != raw {
				t.Fatalf("raw_message = %q, want it as stored: %q", got, raw)
			}
		})
	}
}
