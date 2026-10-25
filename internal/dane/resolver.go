// Package dane looks up the DNS records DANE for SMTP (RFC 7672) depends on, and says
// whether each answer is DNSSEC-secure.
//
// Go's resolver does not validate DNSSEC, so this package asks a validating resolver and
// reads the AD (authenticated data) bit of its answer, as RFC 7672 §2.1 allows. The AD
// bit is only as trustworthy as the path to that resolver, so the resolver must be on a
// loopback address: a resolver on the same host, such as unbound or the cluster's own
// validating resolver reached over a local socket. Nothing here is used unless a
// resolver is configured.
package dane

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Security is what an answer's DNSSEC status lets a sender conclude (RFC 7672 §2.1).
type Security int

const (
	// Insecure: the answer is not DNSSEC-validated, so DANE does not apply.
	Insecure Security = iota
	// Secure: the resolver validated the answer (AD set), including a validated denial.
	Secure
	// Bogus: the resolver could not validate an answer it should have (SERVFAIL). A
	// sender must not treat it as insecure: it defers (§2.1.1).
	Bogus
)

func (s Security) String() string {
	switch s {
	case Secure:
		return "secure"
	case Bogus:
		return "bogus"
	}
	return "insecure"
}

// TypeTLSA is the TLSA record type (RFC 6698 §7.1); dnsmessage has no constant for it.
const TypeTLSA dnsmessage.Type = 52

// TLSA is one TLSA record (RFC 6698 §2.1).
type TLSA struct {
	Usage        uint8 // 2 DANE-TA, 3 DANE-EE (RFC 7672 §3.1)
	Selector     uint8 // 0 full certificate, 1 SubjectPublicKeyInfo
	MatchingType uint8 // 0 exact, 1 SHA-256, 2 SHA-512
	Data         []byte
}

// ErrResolver is returned when the configured resolver is not a loopback address.
var ErrResolver = errors.New("dane: the validating resolver must be on a loopback address")

// Resolver asks one validating resolver.
type Resolver struct {
	addr    netip.AddrPort
	timeout time.Duration
}

// NewResolver returns a resolver that asks addr ("127.0.0.1:53", "[::1]:5353"). The
// address must be loopback: the AD bit proves nothing across a network (RFC 6698 §4.1).
func NewResolver(addr string, timeout time.Duration) (*Resolver, error) {
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		return nil, fmt.Errorf("dane: resolver address %q: %w", addr, err)
	}
	if !ap.Addr().IsLoopback() {
		return nil, ErrResolver
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Resolver{addr: ap, timeout: timeout}, nil
}

// TLSA looks up _<port>._tcp.<host> TLSA records (RFC 7672 §2.2.3). Records with
// fields this package cannot read are dropped, as unusable records are (§2.2.3).
func (r *Resolver) TLSA(ctx context.Context, host string, port int) ([]TLSA, Security, error) {
	name := fmt.Sprintf("_%d._tcp.%s", port, strings.TrimSuffix(host, "."))
	answers, sec, err := r.lookup(ctx, name, TypeTLSA)
	if err != nil || sec == Bogus {
		return nil, sec, err
	}
	var out []TLSA
	for _, rr := range answers {
		if t, ok := parseTLSA(rr); ok {
			out = append(out, t)
		}
	}
	return out, sec, nil
}

// MXSecure reports the DNSSEC status of a domain's MX RRset: DANE applies to an MX host
// only when the MX lookup itself was secure (RFC 7672 §2.2.1).
func (r *Resolver) MXSecure(ctx context.Context, domain string) (Security, error) {
	_, sec, err := r.lookup(ctx, strings.TrimSuffix(domain, "."), dnsmessage.TypeMX)
	return sec, err
}

// lookup returns the answer records' raw data for name and qtype, and their status.
func (r *Resolver) lookup(ctx context.Context, name string, qtype dnsmessage.Type) ([][]byte, Security, error) {
	qname, err := dnsmessage.NewName(name + ".")
	if err != nil {
		return nil, Insecure, fmt.Errorf("dane: name %q: %w", name, err)
	}
	id := uint16(rand.UintN(1 << 16))
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, RecursionDesired: true, AuthenticData: true})
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		return nil, Insecure, err
	}
	if err := b.Question(dnsmessage.Question{Name: qname, Type: qtype, Class: dnsmessage.ClassINET}); err != nil {
		return nil, Insecure, err
	}
	if err := b.StartAdditionals(); err != nil {
		return nil, Insecure, err
	}
	var opt dnsmessage.ResourceHeader
	// DO asks for DNSSEC processing (RFC 3225); 1232 is the payload size DNS flag day 2020 settled on.
	if err := opt.SetEDNS0(1232, dnsmessage.RCodeSuccess, true); err != nil {
		return nil, Insecure, err
	}
	if err := b.OPTResource(opt, dnsmessage.OPTResource{}); err != nil {
		return nil, Insecure, err
	}
	query, err := b.Finish()
	if err != nil {
		return nil, Insecure, err
	}

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	reply, err := r.exchange(ctx, "udp", query)
	if err == nil {
		var h dnsmessage.Header
		var p dnsmessage.Parser
		if h, err = p.Start(reply); err == nil && h.Truncated {
			reply, err = r.exchange(ctx, "tcp", query)
		}
	}
	if err != nil {
		return nil, Insecure, fmt.Errorf("dane: asking %s: %w", r.addr, err)
	}
	return parseReply(reply, id, qname, qtype)
}

func (r *Resolver) exchange(ctx context.Context, network string, query []byte) ([]byte, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, network, r.addr.String())
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if network == "tcp" {
		framed := make([]byte, 2+len(query))
		binary.BigEndian.PutUint16(framed, uint16(len(query)))
		copy(framed[2:], query)
		if _, err := conn.Write(framed); err != nil {
			return nil, err
		}
		var length [2]byte
		if _, err := readFull(conn, length[:]); err != nil {
			return nil, err
		}
		reply := make([]byte, binary.BigEndian.Uint16(length[:]))
		_, err := readFull(conn, reply)
		return reply, err
	}
	if _, err := conn.Write(query); err != nil {
		return nil, err
	}
	reply := make([]byte, 65535)
	n, err := conn.Read(reply)
	return reply[:n], err
}

func readFull(conn net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// parseReply checks the reply answers this query and classifies it. SERVFAIL is how a
// validating resolver reports a bogus answer, so it is Bogus, never Insecure; other
// error codes are errors. A validated denial (NXDOMAIN or no records, AD set) is Secure
// with no records: the domain has proven it publishes none.
func parseReply(reply []byte, id uint16, qname dnsmessage.Name, qtype dnsmessage.Type) ([][]byte, Security, error) {
	var p dnsmessage.Parser
	h, err := p.Start(reply)
	if err != nil {
		return nil, Insecure, fmt.Errorf("dane: unreadable reply: %w", err)
	}
	if h.ID != id || !h.Response {
		return nil, Insecure, errors.New("dane: the reply does not answer the query")
	}
	q, err := p.Question()
	if err != nil || q.Name != qname || q.Type != qtype {
		return nil, Insecure, errors.New("dane: the reply answers another question")
	}
	if err := p.SkipAllQuestions(); err != nil {
		return nil, Insecure, err
	}
	switch h.RCode {
	case dnsmessage.RCodeServerFailure:
		return nil, Bogus, nil
	case dnsmessage.RCodeSuccess, dnsmessage.RCodeNameError:
	default:
		return nil, Insecure, fmt.Errorf("dane: the resolver answered %v", h.RCode)
	}
	sec := Insecure
	if h.AuthenticData {
		sec = Secure
	}
	var answers [][]byte
	for {
		rh, err := p.AnswerHeader()
		if errors.Is(err, dnsmessage.ErrSectionDone) {
			break
		}
		if err != nil {
			return nil, Insecure, err
		}
		if rh.Type != qtype || rh.Class != dnsmessage.ClassINET {
			if err := p.SkipAnswer(); err != nil {
				return nil, Insecure, err
			}
			continue
		}
		res, err := p.UnknownResource()
		if err != nil {
			return nil, Insecure, err
		}
		answers = append(answers, res.Data)
	}
	return answers, sec, nil
}

// parseTLSA reads TLSA rdata: usage, selector, matching type, then the association data.
func parseTLSA(rdata []byte) (TLSA, bool) {
	if len(rdata) < 4 {
		return TLSA{}, false
	}
	t := TLSA{Usage: rdata[0], Selector: rdata[1], MatchingType: rdata[2], Data: append([]byte(nil), rdata[3:]...)}
	switch t.MatchingType {
	case 1:
		return t, len(t.Data) == 32
	case 2:
		return t, len(t.Data) == 64
	}
	return t, true
}
