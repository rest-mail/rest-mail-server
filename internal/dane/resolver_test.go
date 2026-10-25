package dane

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// reply describes how the fake validating resolver answers.
type reply struct {
	rcode     dnsmessage.RCode
	ad        bool
	truncated bool // over UDP only; TCP always carries the whole answer
	tlsa      [][]byte
	wrongID   bool
}

// fakeResolver answers DNS queries on 127.0.0.1 over UDP and TCP from replyFor, and
// records the last query so tests can check what was asked.
type fakeResolver struct {
	addr     string
	replyFor func(q dnsmessage.Question) reply
	lastDO   atomic.Bool
	lastAD   atomic.Bool
	tcpUsed  atomic.Bool
}

func startFake(t *testing.T, replyFor func(q dnsmessage.Question) reply) *fakeResolver {
	t.Helper()
	// UDP and TCP share one port, as a resolver's do. The kernel picks the UDP port; the
	// same TCP port can be taken by another process, so try a fresh pair until both bind.
	var udp net.PacketConn
	var tcp net.Listener
	for attempt := 0; ; attempt++ {
		u, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		l, err := net.Listen("tcp", u.LocalAddr().String())
		if err == nil {
			udp, tcp = u, l
			break
		}
		_ = u.Close()
		if attempt == 20 {
			t.Fatalf("no port free for both UDP and TCP: %v", err)
		}
	}
	f := &fakeResolver{addr: udp.LocalAddr().String(), replyFor: replyFor}
	t.Cleanup(func() { _ = udp.Close(); _ = tcp.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			n, from, err := udp.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = udp.WriteTo(f.answer(t, buf[:n], false), from)
		}
	}()
	go func() {
		for {
			conn, err := tcp.Accept()
			if err != nil {
				return
			}
			var length [2]byte
			if _, err := readFull(conn, length[:]); err == nil {
				query := make([]byte, binary.BigEndian.Uint16(length[:]))
				if _, err := readFull(conn, query); err == nil {
					f.tcpUsed.Store(true)
					out := f.answer(t, query, true)
					framed := append(binary.BigEndian.AppendUint16(nil, uint16(len(out))), out...)
					_, _ = conn.Write(framed)
				}
			}
			_ = conn.Close()
		}
	}()
	return f
}

func (f *fakeResolver) answer(t *testing.T, query []byte, overTCP bool) []byte {
	var p dnsmessage.Parser
	h, err := p.Start(query)
	if err != nil {
		t.Error(err)
		return nil
	}
	q, err := p.Question()
	if err != nil {
		t.Error(err)
		return nil
	}
	_ = p.SkipAllQuestions()
	_ = p.SkipAllAnswers()
	_ = p.SkipAllAuthorities()
	f.lastAD.Store(h.AuthenticData)
	for {
		rh, err := p.AdditionalHeader()
		if err != nil {
			break
		}
		if rh.Type == dnsmessage.TypeOPT {
			f.lastDO.Store(rh.TTL&(1<<15) != 0)
		}
		_ = p.SkipAdditional()
	}
	r := f.replyFor(q)
	id := h.ID
	if r.wrongID {
		id++
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, Response: true, RCode: r.rcode,
		AuthenticData: r.ad, Truncated: r.truncated && !overTCP, RecursionAvailable: true})
	_ = b.StartQuestions()
	_ = b.Question(q)
	_ = b.StartAnswers()
	if !(r.truncated && !overTCP) {
		for _, data := range r.tlsa {
			_ = b.UnknownResource(dnsmessage.ResourceHeader{Name: q.Name, Type: q.Type, Class: dnsmessage.ClassINET, TTL: 300},
				dnsmessage.UnknownResource{Type: q.Type, Data: data})
		}
	}
	out, err := b.Finish()
	if err != nil {
		t.Error(err)
	}
	return out
}

func eeRecord(fill byte) []byte {
	return append([]byte{3, 1, 1}, bytes.Repeat([]byte{fill}, 32)...)
}

func newTestResolver(t *testing.T, f *fakeResolver) *Resolver {
	t.Helper()
	r, err := NewResolver(f.addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSecureTLSA(t *testing.T) {
	f := startFake(t, func(q dnsmessage.Question) reply {
		if q.Name.String() != "_25._tcp.mx.example.test." || q.Type != TypeTLSA {
			t.Errorf("asked %v %v", q.Name, q.Type)
		}
		return reply{ad: true, tlsa: [][]byte{eeRecord(0xaa), {2, 0, 0, 0x30, 0x82}}}
	})
	records, sec, err := newTestResolver(t, f).TLSA(context.Background(), "mx.example.test.", 25)
	if err != nil || sec != Secure {
		t.Fatalf("security %v, err %v", sec, err)
	}
	if len(records) != 2 || records[0].Usage != 3 || records[0].Selector != 1 || records[0].MatchingType != 1 ||
		!bytes.Equal(records[0].Data, bytes.Repeat([]byte{0xaa}, 32)) || records[1].Usage != 2 || records[1].MatchingType != 0 {
		t.Fatalf("records %+v", records)
	}
	if !f.lastDO.Load() || !f.lastAD.Load() {
		t.Fatalf("the query must set DO (EDNS0) and AD: DO %v, AD %v", f.lastDO.Load(), f.lastAD.Load())
	}
}

func TestAnswerWithoutADIsInsecure(t *testing.T) {
	f := startFake(t, func(dnsmessage.Question) reply { return reply{tlsa: [][]byte{eeRecord(1)}} })
	_, sec, err := newTestResolver(t, f).TLSA(context.Background(), "mx.example.test", 25)
	if err != nil || sec != Insecure {
		t.Fatalf("security %v, err %v", sec, err)
	}
}

// A validating resolver reports a bogus answer as SERVFAIL. It must never read as
// insecure, which would let an attacker strip DANE by breaking signatures (RFC 7672 §2.1.1).
func TestServfailIsBogus(t *testing.T) {
	f := startFake(t, func(dnsmessage.Question) reply { return reply{rcode: dnsmessage.RCodeServerFailure} })
	records, sec, err := newTestResolver(t, f).TLSA(context.Background(), "mx.example.test", 25)
	if err != nil || sec != Bogus || records != nil {
		t.Fatalf("records %v, security %v, err %v", records, sec, err)
	}
}

// A validated denial says the host publishes no TLSA records: secure, and empty.
func TestValidatedDenialIsSecureAndEmpty(t *testing.T) {
	f := startFake(t, func(dnsmessage.Question) reply { return reply{rcode: dnsmessage.RCodeNameError, ad: true} })
	records, sec, err := newTestResolver(t, f).TLSA(context.Background(), "mx.example.test", 25)
	if err != nil || sec != Secure || len(records) != 0 {
		t.Fatalf("records %v, security %v, err %v", records, sec, err)
	}
}

func TestTruncatedAnswerRetriesOverTCP(t *testing.T) {
	f := startFake(t, func(dnsmessage.Question) reply {
		return reply{ad: true, truncated: true, tlsa: [][]byte{eeRecord(2), eeRecord(3)}}
	})
	records, sec, err := newTestResolver(t, f).TLSA(context.Background(), "mx.example.test", 25)
	if err != nil || sec != Secure || len(records) != 2 || !f.tcpUsed.Load() {
		t.Fatalf("records %d, security %v, err %v, tcp %v", len(records), sec, err, f.tcpUsed.Load())
	}
}

func TestUnusableRecordsAreDropped(t *testing.T) {
	f := startFake(t, func(dnsmessage.Question) reply {
		return reply{ad: true, tlsa: [][]byte{{3, 1}, {3, 1, 1, 0xaa}, append([]byte{3, 1, 2}, bytes.Repeat([]byte{1}, 32)...), eeRecord(4)}}
	})
	records, _, err := newTestResolver(t, f).TLSA(context.Background(), "mx.example.test", 25)
	if err != nil || len(records) != 1 || records[0].Data[0] != 4 {
		t.Fatalf("records %+v, err %v", records, err)
	}
}

func TestReplyToAnotherQueryIsRefused(t *testing.T) {
	f := startFake(t, func(dnsmessage.Question) reply { return reply{ad: true, wrongID: true, tlsa: [][]byte{eeRecord(5)}} })
	_, _, err := newTestResolver(t, f).TLSA(context.Background(), "mx.example.test", 25)
	if err == nil {
		t.Fatal("a reply with another ID was accepted")
	}
}

func TestMXSecure(t *testing.T) {
	f := startFake(t, func(q dnsmessage.Question) reply {
		if q.Type != dnsmessage.TypeMX || q.Name.String() != "example.test." {
			t.Errorf("asked %v %v", q.Name, q.Type)
		}
		return reply{ad: true}
	})
	sec, err := newTestResolver(t, f).MXSecure(context.Background(), "example.test")
	if err != nil || sec != Secure {
		t.Fatalf("security %v, err %v", sec, err)
	}
}

// The AD bit proves nothing across a network, so only a loopback resolver is accepted.
func TestResolverMustBeLoopback(t *testing.T) {
	for _, addr := range []string{"192.0.2.53:53", "8.8.8.8:53", "[2001:db8::53]:53"} {
		if _, err := NewResolver(addr, 0); !errors.Is(err, ErrResolver) {
			t.Errorf("%s: %v", addr, err)
		}
	}
	for _, addr := range []string{"127.0.0.1:53", "[::1]:5353", "127.0.0.53:53"} {
		if _, err := NewResolver(addr, 0); err != nil {
			t.Errorf("%s: %v", addr, err)
		}
	}
	if _, err := NewResolver("localhost:53", 0); err == nil {
		t.Error("a host name was accepted; the address must be literal")
	}
}

func TestUnreachableResolverIsAnError(t *testing.T) {
	r, err := NewResolver("127.0.0.1:1", 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, sec, err := r.TLSA(context.Background(), "mx.example.test", 25); err == nil || sec == Secure {
		t.Fatalf("security %v, err %v", sec, err)
	}
}
