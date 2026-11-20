package queue

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	netmail "net/mail"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/rest-mail/go-mtasts/pkg/mtasts"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/outbound"
)

// wireLineEnds is what a receiver takes as a line end once SMTP has carried a message: a run of CRs
// before an LF, a lone LF or a lone CR. It is written out here rather than taken from ForData, so the
// expected bytes don't come from the code under test.
var wireLineEnds = regexp.MustCompile("\r*\n|\r")

// hiddenFieldMix is one way of spelling a message's line ends: head ends the head's lines and body the
// body's. The issue's reproduced mixes are CRLF, LF, CR CR LF and LF dot LF (#2107 AC05, #1970, #1973).
//
// bodyIsHeadToSomeReader says that a line after the blank line still reads as the head to one of the
// readers rmail.HeadFields follows, worked out by hand: an LF head has no CRLF blank for a CRLF reader,
// and net/textproto drops only one CR of a CR CR LF blank, so to it that line isn't blank. readAsFields
// are the look-alike lines in the body that such a reader meets at the start of a line, so to it they
// are hidden fields, and a peer that reads that way would read them so: they go too. Every other
// look-alike line is the body to every reader and stays.
type hiddenFieldMix struct {
	name, head, body       string
	bodyIsHeadToSomeReader bool
	readAsFields           []string
}

var (
	bccLookAlike       = "Bcc: quoted in the text"
	resentBccLookAlike = "Resent-Bcc: also quoted"
	hiddenFieldMixes   = []hiddenFieldMix{
		{"all CRLF", "\r\n", "\r\n", false, nil},
		{"all LF", "\n", "\n", false, nil},
		// net/textproto reads "\r" lines, never a blank one, so the whole message is its head.
		{"CR CR LF", "\r\r\n", "\r\r\n", true, []string{bccLookAlike, resentBccLookAlike}},
		{"LF dot LF in a CRLF message", "\r\n", "\n", false, nil},
		// A CRLF reader's first line runs to the end of the first body line, so the second starts one.
		{"LF head, CRLF body", "\n", "\r\n", true, []string{resentBccLookAlike}},
		// net/textproto again: no blank line, and every LF in the body starts a line.
		{"CR CR LF head, LF body", "\r\r\n", "\n", true, []string{bccLookAlike, resentBccLookAlike}},
		{"bare CR", "\r", "\r", false, nil},
	}
)

// cutOurSignature splits a message the worker signed into its signature field and the message it
// signed, which starts at its From field.
func cutOurSignature(t *testing.T, signed string) (signature, message string) {
	t.Helper()
	i := strings.Index(signed, "\r\nFrom: ")
	if i < 0 || !strings.HasPrefix(signed, "DKIM-Signature: ") {
		t.Fatalf("not signed by us: %q", signed)
	}
	return signed[:i], signed[i+2:]
}

// hiddenFieldMessage is a message whose head hides recipients in a folded, mixed-case Bcc and
// Resent-Bcc, and whose body has lines that only look like them, a lone dot and a dot-led line. want is
// the same message without the two hidden fields and the look-alikes the mix makes fields, every other
// byte where it was.
func hiddenFieldMessage(sender string, mix hiddenFieldMix) (raw, want string) {
	e, b := mix.head, mix.body
	head := "From: " + sender + e + "To: bob@elsewhere.test" + e
	hidden := "bCC: secret@c.test," + e + " \tmore@c.test" + e + "RESENT-bcc :" + e + " secret2@c.test" + e
	rest := "Subject: s" + e + "Date: Wed, 04 Nov 2026 12:00:00 +0000" + e + "Message-ID: <hidden@remote.test>" + e + e
	body := []string{bccLookAlike, resentBccLookAlike, ".", "..dots", "end"}
	raw, want = head+hidden+rest, head+rest
	for _, line := range body {
		raw += line + b
		if !slices.Contains(mix.readAsFields, line) {
			want += line + b
		}
	}
	return raw, want
}

// No hidden recipient leaves by RESTMAIL or by SMTP DATA, for any of the reproduced line-end mixes.
// RESTMAIL carries the row's exact bytes less the two fields. DATA carries those bytes with every line
// end a CRLF, dot-stuffed, ending at the one real end of data, and with our signature verifying over
// what arrived. Lines in the body that look like the fields stay, and the queue row keeps its source.
func TestNoHiddenRecipientLeavesOnEitherRoute(t *testing.T) {
	db := eightBitDB(t)
	domain, sender, _ := signedSender(t, db)
	var posted string
	var postedTo []string
	srv := captureRESTMAILServer(t, &posted, &postedTo)
	for i, mix := range hiddenFieldMixes {
		t.Run(mix.name, func(t *testing.T) {
			raw, want := hiddenFieldMessage(sender, mix)
			recipient := fmt.Sprintf("bob%d@elsewhere.test", i)
			row := enqueue(t, db, sender, recipient, raw)
			t.Cleanup(func() { db.Delete(&models.OutboundQueue{}, row.ID) })
			w := eightBitWorker(t, db)
			w.restmailRoots = srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs

			if err := w.deliverRESTMAILHTTPS(context.Background(), srv.URL, row); err != nil {
				t.Fatalf("RESTMAIL: %v", err)
			}
			if _, rest := cutOurSignature(t, posted); rest != want {
				t.Fatalf("RESTMAIL raw_message = %q\nwant our signature, then %q", posted, want)
			}
			if len(postedTo) != 1 || postedTo[0] != recipient {
				t.Errorf("RESTMAIL recipients = %v, want %s", postedTo, recipient)
			}

			peer := newWirePeer(t, true)
			if err := w.deliverSMTP(context.Background(), peer.addr, "mx.elsewhere.test", row, nil, nil); err != nil {
				t.Fatalf("DATA: %v", err)
			}
			wire := relayedOnce(t, peer)
			for _, stuffed := range []string{"\r\n..\r\n", "\r\n...dots\r\n"} {
				if !strings.Contains(wire, stuffed) {
					t.Errorf("DATA isn't dot-stuffed, no %q in %q", stuffed, wire)
				}
			}
			if strings.Count(wire, "\r\n.\r\n") != 1 || !strings.HasSuffix(wire, "\r\n.\r\n") {
				t.Errorf("DATA has more than its one end of data: %q", wire)
			}
			arrived := unstuffed(t, wire)
			crlf := wireLineEnds.ReplaceAllString(want, "\r\n")
			if _, rest := cutOurSignature(t, arrived); rest != crlf {
				t.Fatalf("DATA carried %q\nwant our signature, then %q", arrived, crlf)
			}
			dkimVerifies(t, db, domain, arrived)
			storedExactly(t, db, row.ID, raw)
		})
	}
}

// One reading of where the head ends decides all three things the worker reads from a head: which
// fields hide recipients (StripBcc), whether the message is already signed (Prepare's
// HasDKIMSignature) and whether its sender waives the recipient's TLS policy (TLSOptional). Each slot
// holds one field in turn: in the head; after an LF blank but before a CRLF one, where a CRLF reader
// still takes it for the head; and after the mix's own blank line. The three answer the same in every
// slot of every mix, on the worker's real routes.
//
// DATA signs what it sends, and what it sends has every line end a CRLF, so there the signature is
// looked for in those bytes: a field that only some reader took for the head is in the body once the
// line ends are made one kind. The hidden field was taken out of the row's own bytes before that, so
// it is gone either way.
func TestOneHeadReaderDecidesStripSignAndTLSWaiver(t *testing.T) {
	db := eightBitDB(t)
	domain, sender, _ := signedSender(t, db)
	var posted string
	var postedTo []string
	srv := captureRESTMAILServer(t, &posted, &postedTo)
	const foreign = "DKIM-Signature: v=1; a=rsa-sha256; d=other.test; s=sel; h=from; bh=YQ==; b=YQ=="
	type slot struct {
		name string
		// inHead says whether the row's bytes hold the field in the head, and sentInHead whether the
		// bytes DATA sends do.
		inHead, sentInHead func(mix hiddenFieldMix) bool
		place              func(mix hiddenFieldMix, field string) string
	}
	always := func(hiddenFieldMix) bool { return true }
	never := func(hiddenFieldMix) bool { return false }
	slots := []slot{
		{"in the head", always, always, func(m hiddenFieldMix, f string) string {
			return "From: " + sender + m.head + f + m.head + "Subject: s" + m.head + m.head + "body" + m.body
		}},
		{"between an LF blank and a CRLF blank", always, never, func(m hiddenFieldMix, f string) string {
			return "From: " + sender + "\n\nbody\r\n" + f + "\r\n\r\nmore" + m.body
		}},
		{"after the blank line", func(m hiddenFieldMix) bool { return m.bodyIsHeadToSomeReader }, never, func(m hiddenFieldMix, f string) string {
			return "From: " + sender + m.head + "Subject: s" + m.head + m.head + "body" + m.body + f + m.body + "more" + m.body
		}},
	}
	for i, mix := range hiddenFieldMixes {
		for j, s := range slots {
			t.Run(mix.name+"/"+s.name, func(t *testing.T) {
				w := eightBitWorker(t, db)
				w.restmailRoots = srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
				item := func(raw string) models.OutboundQueue {
					return models.OutboundQueue{Sender: sender, Recipient: fmt.Sprintf("r%d-%d@elsewhere.test", i, j), Domain: "elsewhere.test", RawMessage: raw}
				}

				inHead, sentInHead := s.inHead(mix), s.sentInHead(mix)
				withBcc := s.place(mix, "Bcc: secret@c.test")
				stripped := outbound.StripBcc(withBcc)
				if removed := !strings.Contains(stripped, "secret"); removed != inHead {
					t.Fatalf("Bcc removed = %v, want %v: %q", removed, inHead, stripped)
				}

				// TLS-Required: No is read where the worker reads it, before any policy lookup.
				_, err := w.recipientPolicy(context.Background(), item(s.place(mix, "TLS-Required: No")), "elsewhere.test")
				if waived := errors.Is(err, errTLSOptional); waived != inHead {
					t.Fatalf("TLS policy waived = %v (%v), want %v", waived, err, inHead)
				}
				if !inHead && !errors.Is(err, mtasts.ErrNoPolicy) {
					t.Fatalf("policy lookup = %v, want the lookup to run", err)
				}

				// A signature already there is seen, so the worker adds none of its own, on both routes.
				signed := s.place(mix, foreign)
				if err := w.deliverRESTMAILHTTPS(context.Background(), srv.URL, item(signed)); err != nil {
					t.Fatalf("RESTMAIL: %v", err)
				}
				if ours := strings.Contains(posted, "d="+domain); ours == inHead {
					t.Fatalf("RESTMAIL added our signature = %v, want %v: %q", ours, !inHead, posted)
				}
				peer := newWirePeer(t, true)
				if err := w.deliverSMTP(context.Background(), peer.addr, "mx.elsewhere.test", item(signed), nil, nil); err != nil {
					t.Fatalf("DATA: %v", err)
				}
				arrived := unstuffed(t, relayedOnce(t, peer))
				if ours := strings.Contains(arrived, "d="+domain); ours == sentInHead {
					t.Fatalf("DATA added our signature = %v, want %v: %q", ours, !sentInHead, arrived)
				}
				if !sentInHead {
					dkimVerifies(t, db, domain, arrived)
				}
			})
		}
	}
}

// stuffedForData is what DATA must carry for message, worked out without the code under test: every
// line end a CRLF (wireLineEnds), a dot doubled at the start of each line (RFC 5321 section 4.5.2), a
// CRLF ending the last line, and then the one end of data.
func stuffedForData(message string) string {
	lines := strings.Split(strings.TrimSuffix(wireLineEnds.ReplaceAllString(message, "\r\n"), "\r\n"), "\r\n")
	for i, line := range lines {
		if strings.HasPrefix(line, ".") {
			lines[i] = "." + line
		}
	}
	return strings.Join(lines, "\r\n") + "\r\n.\r\n"
}

// The octets DATA carries are exact, for every reproduced mix of line ends, on a worker's real claim:
// a legal Latin-1 text with lines that are a lone dot or start with one is sent with every line end a
// CRLF and every leading dot doubled, in front of the one end of data, under our signature, which
// verifies. RESTMAIL carries the row's own octets, 8-bit and line ends as they are. A binary part, which
// no line end may touch, arrives as the same octets. The queue row keeps the source throughout (#2107
// AC04, #1921, #1970).
func TestDataOctetsAreExactOnEveryLineEndMix(t *testing.T) {
	db := eightBitDB(t)
	domain, sender, _ := signedSender(t, db)
	var posted string
	var postedTo []string
	srv := captureRESTMAILServer(t, &posted, &postedTo)
	for i, mix := range hiddenFieldMixes {
		e, b := mix.head, mix.body
		head := fmt.Sprintf("From: %s%sTo: bob@elsewhere.test%sSubject: octets%sDate: Wed, 04 Nov 2026 12:00:00 +0000%sMessage-ID: <octets-%d@remote.test>%sMIME-Version: 1.0%s", sender, e, e, e, e, i, e, e)
		t.Run(mix.name+"/Latin-1 text", func(t *testing.T) {
			message := head + "Content-Type: text/plain; charset=iso-8859-1" + e + "Content-Transfer-Encoding: 8bit" + e + e +
				"caf\xe9" + b + "." + b + "..two" + b + ".\xe9" + b + "end" + b
			peer := newWirePeer(t, true)
			w := eightBitWorker(t, db, "mx.elsewhere.test", peer.addr)
			row := enqueue(t, db, sender, fmt.Sprintf("octets%d@elsewhere.test", i), message)
			w.processOne(0)
			if after := readRow(t, db, row.ID); after.Status != "delivered" {
				t.Fatalf("status %s: %s", after.Status, after.LastError)
			}
			wire := relayedOnce(t, peer)
			want := stuffedForData(message)
			if !strings.HasSuffix(wire, want) {
				t.Fatalf("DATA carried %q\nwant our signature, then %q", wire, want)
			}
			signature := strings.TrimSuffix(wire, want)
			if !strings.HasPrefix(signature, "DKIM-Signature: ") || !strings.HasSuffix(signature, "\r\n") ||
				strings.Contains(signature, "\r\n.") || strings.Contains(strings.ReplaceAll(signature, "\r\n ", ""), "\r\n\t") {
				t.Fatalf("DATA has more than our signature before the message: %q", signature)
			}
			dkimVerifies(t, db, domain, unstuffed(t, wire))
			storedExactly(t, db, row.ID, message)

			w.restmailRoots = srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
			if err := w.deliverRESTMAILHTTPS(context.Background(), srv.URL, row); err != nil {
				t.Fatalf("RESTMAIL: %v", err)
			}
			if _, rest := cutOurSignature(t, posted); rest != message {
				t.Fatalf("RESTMAIL raw_message = %q\nwant our signature, then %q", posted, message)
			}
			db.Delete(&models.OutboundQueue{}, row.ID)
		})
		t.Run(mix.name+"/binary part", func(t *testing.T) {
			attachment := binaryOctets()
			message := head + "Content-Type: multipart/mixed; boundary=\"b1\"" + e + e +
				"--b1" + e + "Content-Type: text/plain; charset=iso-8859-1" + e + e + "caf\xe9" + b + "." + b + "--b1" + e +
				"Content-Type: application/octet-stream" + e + "Content-Transfer-Encoding: 8bit" + e + e + attachment + e + "--b1--" + e
			peer := newWirePeer(t, true)
			w := eightBitWorker(t, db, "mx.elsewhere.test", peer.addr)
			row := enqueue(t, db, sender, fmt.Sprintf("binary%d@elsewhere.test", i), message)
			w.processOne(0)
			if after := readRow(t, db, row.ID); after.Status != "delivered" {
				t.Fatalf("status %s: %s", after.Status, after.LastError)
			}
			wire := relayedOnce(t, peer)
			if strings.Count(wire, "\r\n.\r\n") != 1 || !strings.HasSuffix(wire, "\r\n.\r\n") {
				t.Fatalf("DATA has more than its one end of data: %q", wire)
			}
			if wireLineEnds.ReplaceAllString(wire, "\r\n") != wire {
				t.Fatalf("a bare line end on the wire: %q", wire)
			}
			if !strings.Contains(wire, "\r\ncaf\xe9\r\n..\r\n--b1\r\n") {
				t.Fatalf("the text part isn't CRLF and stuffed: %q", wire)
			}
			arrived := unstuffed(t, wire)
			dkimVerifies(t, db, domain, arrived)
			if got := binaryPartOf(t, arrived); got != attachment {
				t.Fatalf("the binary part changed:\n got %q\nwant %q", got, attachment)
			}
			storedExactly(t, db, row.ID, message)
			db.Delete(&models.OutboundQueue{}, row.ID)
		})
	}
}

// binaryPartOf decodes the second part of a multipart message as it arrived, which must have gone as
// base64, the only way its octets can pass through DATA.
func binaryPartOf(t *testing.T, message string) string {
	t.Helper()
	parsed, err := netmail.ReadMessage(strings.NewReader(message))
	if err != nil {
		t.Fatal(err)
	}
	_, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(parsed.Body, params["boundary"])
	if _, err := reader.NextRawPart(); err != nil {
		t.Fatal(err)
	}
	part, err := reader.NextRawPart()
	if err != nil {
		t.Fatal(err)
	}
	if encoding := part.Header.Get("Content-Transfer-Encoding"); encoding != "base64" {
		t.Fatalf("the binary part went as %q", encoding)
	}
	content, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, newlineStripper{part}))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
