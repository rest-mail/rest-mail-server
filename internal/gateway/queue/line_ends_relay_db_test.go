package queue

import (
	"bufio"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	netmail "net/mail"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/rest-mail/go-dkim/pkg/dkim"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
)

// wirePeer is a scripted SMTP server keeping the exact octets a DATA carried, dot-stuffing and all,
// and every command it saw, so that one read as a command after a false end of data shows.
type wirePeer struct {
	addr string

	mu       sync.Mutex
	commands []string
	data     []string
}

func newWirePeer(t *testing.T, eightBit bool) *wirePeer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	peer := &wirePeer{addr: listener.Addr().String()}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go peer.serve(conn, eightBit)
		}
	}()
	return peer
}

func (p *wirePeer) serve(conn net.Conn, eightBit bool) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	_, _ = io.WriteString(conn, "220 peer ESMTP\r\n")
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.ToUpper(strings.TrimSpace(line))
		p.mu.Lock()
		p.commands = append(p.commands, strings.TrimSpace(line))
		p.mu.Unlock()
		switch {
		case strings.HasPrefix(command, "EHLO"):
			if eightBit {
				_, _ = io.WriteString(conn, "250-peer\r\n250 8BITMIME\r\n")
			} else {
				_, _ = io.WriteString(conn, "250 peer\r\n")
			}
		case command == "DATA":
			_, _ = io.WriteString(conn, "354 go on\r\n")
			var data strings.Builder
			for !strings.HasSuffix(data.String(), "\r\n.\r\n") && data.String() != ".\r\n" {
				chunk, err := reader.ReadString('\n')
				data.WriteString(chunk)
				if err != nil {
					return
				}
			}
			p.mu.Lock()
			p.data = append(p.data, data.String())
			p.mu.Unlock()
			_, _ = io.WriteString(conn, "250 taken\r\n")
		case command == "QUIT":
			_, _ = io.WriteString(conn, "221 bye\r\n")
			return
		default:
			_, _ = io.WriteString(conn, "250 ok\r\n")
		}
	}
}

func (p *wirePeer) received() (commands, data []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string{}, p.commands...), append([]string{}, p.data...)
}

// unstuffed is what a receiver reads from the octets of a DATA that end at the only end of data.
func unstuffed(t *testing.T, wire string) string {
	t.Helper()
	body, ok := strings.CutSuffix(wire, ".\r\n")
	if !ok {
		t.Fatalf("no end of data: %q", wire)
	}
	lines := strings.SplitAfter(body, "\r\n")
	for i, line := range lines {
		lines[i] = strings.TrimPrefix(line, ".")
	}
	return strings.Join(lines, "")
}

func bodyOf(t *testing.T, message string) string {
	t.Helper()
	parsed, err := netmail.ReadMessage(strings.NewReader(message))
	if err != nil {
		t.Fatalf("not one message: %v\n%q", err, message)
	}
	body, err := io.ReadAll(parsed.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func relayedOnce(t *testing.T, peer *wirePeer) string {
	t.Helper()
	commands, data := peer.received()
	if len(data) != 1 {
		t.Fatalf("%d transactions, commands %q", len(data), commands)
	}
	mails := 0
	for _, command := range commands {
		if strings.HasPrefix(strings.ToUpper(command), "MAIL ") {
			mails++
		}
	}
	if mails != 1 {
		t.Fatalf("%d MAIL commands: %q", mails, commands)
	}
	return data[0]
}

// Every way a message can spell an end of data that some receiver would take, with a second
// message behind it. RFC 5321 section 2.3.8 and the 2023 SMTP smuggling reports are what this is for.
func TestLineEndsCannotSmuggleAnEndOfData(t *testing.T) {
	const smuggled = "MAIL FROM:<evil@sender.test>\r\nRCPT TO:<victim@elsewhere.test>\r\nDATA\r\nSubject: spoofed\r\n\r\nspoofed\r\n.\r\n"
	db := eightBitDB(t)
	domain, sender, _ := signedSender(t, db)
	for name, spelling := range map[string]string{
		"LF . LF":      "\n.\n",
		"CR . CR":      "\r.\r",
		"CR . LF":      "\r.\n",
		"LF . CR":      "\n.\r",
		"CRLF . LF":    "\r\n.\n",
		"LF . CRLF":    "\n.\r\n",
		"CRLF . CR":    "\r\n.\r",
		"CR . CRLF":    "\r.\r\n",
		"CR CR LF dot": "\r\r\n.\r\r\n",
		"CRLF . CRLF":  "\r\n.\r\n",
		"LF dots":      "\n..\n",
		"at a block":   strings.Repeat("x", dataBlockSize-1) + "\n.\n",
	} {
		t.Run(name, func(t *testing.T) {
			head := "From: " + sender + "\r\nTo: bob@elsewhere.test\r\nSubject: smuggle\r\nDate: Wed, 04 Nov 2026 12:00:00 +0000\r\nMessage-ID: <" + strings.ReplaceAll(name, " ", "-") + "@remote.test>\r\n\r\n"
			message := head + "before" + spelling + smuggled + "after\r\n"

			peer := newWirePeer(t, true)
			w := eightBitWorker(t, db, "mx.elsewhere.test", peer.addr)
			row := enqueue(t, db, sender, "bob@elsewhere.test", message)
			w.processOne(0)
			if after := readRow(t, db, row.ID); after.Status != "delivered" {
				t.Fatalf("status %s: %s", after.Status, after.LastError)
			}
			wire := relayedOnce(t, peer)
			// Everything before the dot of the one real end of data.
			before := strings.TrimSuffix(wire, ".\r\n")
			for _, bad := range []string{"\n.\n", "\r.\r", "\r.\n", "\n.\r"} {
				if strings.Contains(before, bad) {
					t.Fatalf("%q on the wire: %q", bad, wire)
				}
			}
			if strings.ReplaceAll(wire, "\r\n", "") != strings.NewReplacer("\r", "", "\n", "").Replace(wire) {
				t.Fatalf("bare line end on the wire: %q", wire)
			}
			message1 := unstuffed(t, wire)
			text := bodyOf(t, message1)
			want := "\r\nMAIL FROM:<evil@sender.test>\r\nRCPT TO:<victim@elsewhere.test>\r\nDATA\r\nSubject: spoofed\r\n\r\nspoofed\r\n.\r\nafter\r\n"
			if !strings.HasPrefix(text, "before") || !strings.HasSuffix(text, want) {
				t.Fatalf("text not intact: %q", text)
			}
			dkimVerifies(t, db, domain, message1)
			storedExactly(t, db, row.ID, message)
			db.Delete(&models.OutboundQueue{}, row.ID)

			// The real receiver, which reads ends of data for itself, sees one message.
			mx := newRealMX(t, true, nil)
			w = eightBitWorker(t, db, "mx.elsewhere.test", mx.addr)
			row = enqueue(t, db, sender, "bob@elsewhere.test", message)
			w.processOne(0)
			got := mx.received()
			if len(got) != 1 || bodyOf(t, got[0].data) != text {
				t.Fatalf("real receiver: %+v", got)
			}
			db.Delete(&models.OutboundQueue{}, row.ID)
		})
	}
}

func TestLineEndsNormalisedBeforeSigning(t *testing.T) {
	db := eightBitDB(t)
	domain, sender, _ := signedSender(t, db)
	head := "From: " + sender + "\r\nTo: bob@elsewhere.test\r\nSubject: endings\r\nDate: Wed, 04 Nov 2026 12:00:00 +0000\r\nMessage-ID: <%s@remote.test>\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: 8bit\r\n\r\n"
	for name, c := range map[string]struct{ body, want string }{
		"bare LF":  {"one\ntwo\xe9\r\n", "one\r\ntwo\xe9\r\n"},
		"bare CR":  {"one\rtwo\xe9\r\n", "one\r\ntwo\xe9\r\n"},
		"CR CR LF": {"one\r\r\ntwo\xe9\r\n", "one\r\ntwo\xe9\r\n"},
	} {
		t.Run(name, func(t *testing.T) {
			message := fmt.Sprintf(head, strings.ReplaceAll(name, " ", "-")) + c.body
			mx := newRealMX(t, true, nil)
			w := eightBitWorker(t, db, "mx.elsewhere.test", mx.addr)
			row := enqueue(t, db, sender, "bob@elsewhere.test", message)
			w.processOne(0)
			if after := readRow(t, db, row.ID); after.Status != "delivered" {
				t.Fatalf("status %s: %s", after.Status, after.LastError)
			}
			got := mx.received()
			if len(got) != 1 || bodyOf(t, got[0].data) != c.want {
				t.Fatalf("arrived %+v, want body %q", got, c.want)
			}
			dkimVerifies(t, db, domain, got[0].data)
			storedExactly(t, db, row.ID, message)
			db.Delete(&models.OutboundQueue{}, row.ID)
		})
	}
	t.Run("all LF still goes as CRLF", func(t *testing.T) {
		message := strings.ReplaceAll(fmt.Sprintf(head, "lf-only")+"one\ntwo\xe9\n", "\r\n", "\n")
		mx := newRealMX(t, true, nil)
		w := eightBitWorker(t, db, "mx.elsewhere.test", mx.addr)
		row := enqueue(t, db, sender, "bob@elsewhere.test", message)
		w.processOne(0)
		if after := readRow(t, db, row.ID); after.Status != "delivered" {
			t.Fatalf("status %s: %s", after.Status, after.LastError)
		}
		got := mx.received()
		if len(got) != 1 || !strings.HasSuffix(got[0].data, strings.ReplaceAll(message, "\n", "\r\n")) {
			t.Fatalf("arrived %+v", got)
		}
		dkimVerifies(t, db, domain, got[0].data)
		storedExactly(t, db, row.ID, message)
		db.Delete(&models.OutboundQueue{}, row.ID)
	})
	t.Run("through a relay too", func(t *testing.T) {
		cert, roots := relayTestCertificate(t, false)
		peer, finish := relayTestPeer(t, cert, relayPeerOptions{})
		seedRelay(t, db, domain, models.DomainRelay{Transport: models.OutboundTransportRelay, Host: peer.host, Port: peer.port, TLSMode: models.RelayTLSStartTLS})
		w := eightBitWorker(t, db)
		w.relayRoots = roots
		message := fmt.Sprintf(head, "via-relay") + "one\ntwo\xe9\r\n"
		row := enqueue(t, db, sender, "bob@elsewhere.test", message)
		w.processOne(0)
		db.Exec("DELETE FROM domain_relays")
		if after := readRow(t, db, row.ID); after.Status != "delivered" {
			t.Fatalf("status %s: %s", after.Status, after.LastError)
		}
		result := finish()
		if !strings.Contains(result.data, "one\r\ntwo\xe9\r\n") {
			t.Fatalf("relay got %q", result.data)
		}
		storedExactly(t, db, row.ID, message)
		db.Delete(&models.OutboundQueue{}, row.ID)
	})
}

// every octet in every order of line end, which only base64 can bring through unchanged.
func binaryOctets() string {
	var octets strings.Builder
	for i := range 2048 {
		octets.WriteByte(byte(i))
	}
	octets.WriteString("\n.\n\r.\r\r\r\n\n\n")
	return octets.String()
}

func TestBinaryPartArrivesExactly(t *testing.T) {
	db := eightBitDB(t)
	domain, sender, mailboxID := signedSender(t, db)
	attachment := binaryOctets()
	message := "From: " + sender + "\r\nTo: bob@elsewhere.test\r\nSubject: attached\r\nDate: Wed, 04 Nov 2026 12:00:00 +0000\r\nMessage-ID: <binary@remote.test>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"b1\"\r\n\r\n" +
		"--b1\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nsee\nattached\r\n--b1\r\nContent-Type: application/octet-stream; name=\"a.bin\"\r\nContent-Disposition: attachment; filename=\"a.bin\"\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" +
		attachment + "\r\n--b1--\r\n"

	mx := newRealMX(t, true, nil)
	w := eightBitWorker(t, db, "mx.elsewhere.test", mx.addr)
	row := enqueue(t, db, sender, "bob@elsewhere.test", message)
	w.processOne(0)
	if after := readRow(t, db, row.ID); after.Status != "delivered" {
		t.Fatalf("status %s: %s", after.Status, after.LastError)
	}
	got := mx.received()
	if len(got) != 1 {
		t.Fatalf("%d arrived", len(got))
	}
	dkimVerifies(t, db, domain, got[0].data)
	storedExactly(t, db, row.ID, message)
	db.Delete(&models.OutboundQueue{}, row.ID)

	parsed, err := netmail.ReadMessage(strings.NewReader(got[0].data))
	if err != nil {
		t.Fatal(err)
	}
	_, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(parsed.Body, params["boundary"])
	text, err := reader.NextRawPart()
	if err != nil {
		t.Fatal(err)
	}
	if content, _ := io.ReadAll(text); string(content) != "see\r\nattached" {
		t.Fatalf("text part = %q", content)
	}
	binary, err := reader.NextRawPart()
	if err != nil {
		t.Fatal(err)
	}
	if encoding := binary.Header.Get("Content-Transfer-Encoding"); encoding != "base64" {
		t.Fatalf("encoding = %q", encoding)
	}
	if binary.FileName() != "a.bin" {
		t.Fatalf("name = %q", binary.FileName())
	}
	content, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, newlineStripper{binary}))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != attachment {
		t.Fatalf("attachment changed:\n got %q\nwant %q", content, attachment)
	}

	t.Run("a peer without 8BITMIME takes it once it is all ASCII", func(t *testing.T) {
		peer := newWirePeer(t, false)
		w := eightBitWorker(t, db, "mx.elsewhere.test", peer.addr)
		row := enqueue(t, db, sender, "bob@elsewhere.test", message)
		if err := db.Model(&models.OutboundQueue{}).Where("id = ?", row.ID).Update("body_type", "").Error; err != nil {
			t.Fatal(err)
		}
		w.processOne(0)
		if after := readRow(t, db, row.ID); after.Status != "delivered" {
			t.Fatalf("status %s: %s", after.Status, after.LastError)
		}
		wire := relayedOnce(t, peer)
		for i := range len(wire) {
			if wire[i] >= 0x80 {
				t.Fatalf("8-bit octet to a peer without 8BITMIME at %d", i)
			}
		}
		dkimVerifies(t, db, domain, unstuffed(t, wire))
		storedExactly(t, db, row.ID, message)
		db.Delete(&models.OutboundQueue{}, row.ID)
	})
	t.Run("declared 8BITMIME to a peer without it still bounces", func(t *testing.T) {
		peer := newWirePeer(t, false)
		w := eightBitWorker(t, db, "mx.elsewhere.test", peer.addr)
		row := enqueue(t, db, sender, "bob@elsewhere.test", message)
		w.processOne(0)
		if after := readRow(t, db, row.ID); after.Status != "bounced" || !strings.Contains(after.LastError, "5.6.3") {
			t.Fatalf("status %s: %s", after.Status, after.LastError)
		}
		if _, data := peer.received(); len(data) != 0 {
			t.Fatalf("%d sent", len(data))
		}
		storedExactly(t, db, row.ID, message)
		db.Delete(&models.OutboundQueue{}, row.ID)
	})
	t.Run("a part base64 would break still bounces", func(t *testing.T) {
		signed := "From: " + sender + "\r\nTo: bob@elsewhere.test\r\nSubject: signed\r\nDate: Wed, 04 Nov 2026 12:00:00 +0000\r\nMessage-ID: <signed@remote.test>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/signed; protocol=\"application/pkcs7-signature\"; boundary=\"b1\"\r\n\r\n" +
			"--b1\r\nContent-Type: application/octet-stream\r\n\r\none\ntwo\xe9\r\n--b1\r\nContent-Type: application/pkcs7-signature\r\nContent-Transfer-Encoding: base64\r\n\r\nAAAA\r\n--b1--\r\n"
		mx := newRealMX(t, true, nil)
		w := eightBitWorker(t, db, "mx.elsewhere.test", mx.addr)
		row := enqueue(t, db, sender, "bob@elsewhere.test", signed)
		w.processOne(0)
		if after := readRow(t, db, row.ID); after.Status != "bounced" || !strings.Contains(after.LastError, "5.6.3") {
			t.Fatalf("status %s: %s", after.Status, after.LastError)
		}
		if got := mx.received(); len(got) != 0 {
			t.Fatalf("%d sent", len(got))
		}
		if dsn := dsnInInbox(t, db, mailboxID); !strings.Contains(string(dsn.Source()), "Status: 5.6.3") {
			t.Fatalf("DSN = %q", dsn.Source())
		}
		storedExactly(t, db, row.ID, signed)
		db.Delete(&models.OutboundQueue{}, row.ID)
	})
}

type newlineStripper struct{ r io.Reader }

func (s newlineStripper) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	kept := 0
	for _, c := range p[:n] {
		if c != '\r' && c != '\n' {
			p[kept] = c
			kept++
		}
	}
	return kept, err
}

// A relay of a message another domain signed goes on with that signature kept and not re-signed,
// as Prepare does. What the rewrite does to the signature depends on what the signer hashed.
// TestForeignSignatureThroughTheRewrite pins each result, and #1921 holds the table.
func TestForeignSignatureThroughTheRewrite(t *testing.T) {
	db := eightBitDB(t)
	_, sender, _ := signedSender(t, db)
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(private.Public())
	if err != nil {
		t.Fatal(err)
	}
	record, err := dkim.RecordValue(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})))
	if err != nil {
		t.Fatal(err)
	}
	resolver := func(_ context.Context, name string) ([]string, error) {
		if strings.TrimSuffix(name, ".") == "sel._domainkey.other.test" {
			return []string{record}, nil
		}
		return nil, nil
	}
	sign := func(t *testing.T, raw, header, body string) string {
		t.Helper()
		value, err := dkim.Sign([]byte(raw), dkim.SignOptions{Domain: "other.test", Selector: "sel", PrivateKey: crypto.Signer(private),
			HeaderCanon: header, BodyCanon: body, Headers: []string{"from", "to", "subject"}})
		if err != nil {
			t.Fatal(err)
		}
		return "DKIM-Signature: " + value + "\r\n" + raw
	}
	verify := func(data string) string {
		results := dkim.Verify(context.Background(), []byte(data), resolver)
		if len(results) != 1 {
			return fmt.Sprintf("%d results", len(results))
		}
		return results[0].Result
	}
	head := "From: someone@other.test\r\nTo: bob@elsewhere.test\r\nSubject: signed elsewhere\r\n"
	text := head + "\r\n"
	binary := head + "MIME-Version: 1.0\r\nContent-Type: image/png\r\n\r\n"
	for _, c := range []struct {
		name, message string
		// what the verifier answers over the wire, whatever the canonicalisation: the go-dkim signer
		// reads a bare LF or CR as a line end already.
		want string
	}{
		{"bare LF", text + "one\ntwo\r\n", "pass"},
		{"all LF", strings.ReplaceAll(text+"one\ntwo\n", "\r\n", "\n"), "pass"},
		{"bare CR", text + "one\rtwo\r\n", "pass"},
		{"CR CR LF", text + "one\r\r\ntwo\r\n", "fail"},
		{"binary to base64", binary + "\x01\n\x02\r\n", "fail"},
	} {
		for _, canon := range [][2]string{{"simple", "simple"}, {"relaxed", "relaxed"}, {"relaxed", "simple"}, {"simple", "relaxed"}} {
			t.Run(c.name+" "+canon[0]+"/"+canon[1], func(t *testing.T) {
				message := sign(t, c.message, canon[0], canon[1])
				if got := verify(message); got != "pass" {
					t.Fatalf("over the source: %s", got)
				}
				mx := newRealMX(t, true, nil)
				w := eightBitWorker(t, db, "mx.elsewhere.test", mx.addr)
				row := enqueue(t, db, sender, "bob@elsewhere.test", message)
				w.processOne(0)
				if after := readRow(t, db, row.ID); after.Status != "delivered" {
					t.Fatalf("status %s: %s", after.Status, after.LastError)
				}
				arrived := mx.received()
				if len(arrived) != 1 {
					t.Fatalf("%d arrived", len(arrived))
				}
				got := verify(arrived[0].data)
				t.Logf("%s c=%s/%s: over the wire %s", c.name, canon[0], canon[1], got)
				if !strings.HasPrefix(got, c.want) {
					t.Fatalf("over the wire: %s, want %s", got, c.want)
				}
				storedExactly(t, db, row.ID, message)
				db.Delete(&models.OutboundQueue{}, row.ID)
			})
		}
	}
	// A signer that hashes the octets it was given, bare LFs and all: nothing a verifier computes
	// over CRLF can match its body hash, on either canonicalisation (RFC 6376 section 3.4 defines
	// both over CRLF lines). This compares hashes, as no signer here works that way.
	t.Run("a signer hashing the bare octets", func(t *testing.T) {
		source, wire := "one\ntwo\r\n", "one\r\ntwo\r\n"
		for _, canon := range []string{"simple", "relaxed"} {
			literal := sha256.Sum256([]byte(source))
			arrived := sha256.Sum256([]byte(dkim.CanonicalizeBody(wire, canon)))
			t.Logf("%s: hash over the source %x, over the wire %x", canon, literal[:6], arrived[:6])
			if literal == arrived {
				t.Fatalf("%s: the hashes match", canon)
			}
		}
	})
}

// A 999-octet line ended by a bare LF just before a delimiter: the delimiter's CRLF would make it
// 1001 octets on the wire, over RFC 5321 section 4.5.3.1.6's limit.
func TestLineEndBeforeADelimiterKeepsTheLimitOnTheWire(t *testing.T) {
	db := eightBitDB(t)
	domain, sender, mailboxID := signedSender(t, db)
	long := strings.Repeat("x", 999)
	head := "From: " + sender + "\r\nTo: bob@elsewhere.test\r\nSubject: long\r\nDate: Wed, 04 Nov 2026 12:00:00 +0000\r\nMessage-ID: <long@remote.test>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"b1\"\r\n\r\n--b1\r\n"
	message := func(partHead, partBody string) string {
		return head + partHead + "\r\n" + partBody + "\n--b1--\r\n"
	}
	sends := func(t *testing.T, raw string) (*wirePeer, models.OutboundQueue) {
		t.Helper()
		peer := newWirePeer(t, true)
		w := eightBitWorker(t, db, "mx.elsewhere.test", peer.addr)
		row := enqueue(t, db, sender, "bob@elsewhere.test", raw)
		t.Cleanup(func() { db.Delete(&models.OutboundQueue{}, row.ID) })
		w.processOne(0)
		return peer, readRow(t, db, row.ID)
	}

	t.Run("text goes as base64", func(t *testing.T) {
		raw := message("Content-Type: text/plain; charset=utf-8\r\n", "first\n"+long)
		peer, row := sends(t, raw)
		if row.Status != "delivered" {
			t.Fatalf("status %s: %s", row.Status, row.LastError)
		}
		wire := relayedOnce(t, peer)
		for _, line := range strings.Split(wire, "\r\n") {
			if len(line)+2 > 1000 {
				t.Fatalf("a line of %d octets on the wire", len(line)+2)
			}
		}
		text := unstuffed(t, wire)
		dkimVerifies(t, db, domain, text)
		storedExactly(t, db, row.ID, raw)
		_, rest, found := strings.Cut(text, "Content-Transfer-Encoding: base64\r\n\r\n")
		if !found {
			t.Fatalf("no base64: %q", text)
		}
		encoded, _, _ := strings.Cut(rest, "--b1--")
		decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(encoded, "\r\n", ""))
		if err != nil {
			t.Fatal(err)
		}
		if string(decoded) != "first\r\n"+long {
			t.Fatalf("decoded %q", decoded)
		}
	})

	t.Run("under multipart/signed it bounces with nothing sent", func(t *testing.T) {
		raw := strings.Replace(message("Content-Type: text/plain\r\n", long), "multipart/mixed", "multipart/signed", 1)
		peer, row := sends(t, raw)
		if row.Status != "bounced" || !strings.Contains(row.LastError, "5.6.3") {
			t.Fatalf("status %s: %s", row.Status, row.LastError)
		}
		if _, data := peer.received(); len(data) != 0 {
			t.Fatalf("%d sent", len(data))
		}
		if dsn := dsnInInbox(t, db, mailboxID); !strings.Contains(string(dsn.Source()), "Status: 5.6.3") {
			t.Fatalf("DSN = %q", dsn.Source())
		}
		storedExactly(t, db, row.ID, raw)
	})
}

// Normalising on the relay path adds one copy of the message, and the 8x bound of the handlers'
// tests holds for all the worker does around it, so long as a message is held in its row (#1437).
func TestNormalisingARelayHoldsOneMoreCopy(t *testing.T) {
	const size = 4 << 20
	body := strings.Repeat("a line of text\n", size/15)
	message := "From: a@b.test\r\nTo: bob@elsewhere.test\r\nSubject: big\r\n\r\n" + body
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	normal, err := forData(message, "mx.elsewhere.test")
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if len(normal) != len(message)+size/15 {
		t.Fatalf("%d octets from %d", len(normal), len(message))
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("%d octets allocated %d", len(message), allocated)
	if allocated > 2*uint64(len(message)) {
		t.Fatalf("%d octets allocated %d", len(message), allocated)
	}
	clean := normal
	runtime.ReadMemStats(&before)
	same, err := forData(clean, "mx.elsewhere.test")
	runtime.ReadMemStats(&after)
	if err != nil || same != clean {
		t.Fatal("a clean message changed")
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 64<<10 {
		t.Fatalf("a clean message allocated %d", allocated)
	}
}
