package handlers

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/rest-mail/rest-mail-server/internal/api/endpoint"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	rmail "github.com/rest-mail/rest-mail-server/internal/mail"
	"github.com/rest-mail/rest-mail-server/internal/pipeline"
)

// UnsubscribeOptions are the ways a received message's List-Unsubscribe field offers to stop
// its mail (#1575), checked because the sender wrote them. one_click is the HTTPS URI the
// server POSTs to when the person asks (RFC 8058), present only when List-Unsubscribe-Post
// says List-Unsubscribe=One-Click and a DKIM signature this server verified covers both
// fields (RFC 8058 §4); links are other HTTPS pages for the person to open in a
// browser; mailto are addresses the server can write to from the account.
type UnsubscribeOptions struct {
	OneClick string                    `json:"one_click,omitempty" description:"HTTPS URI for RFC 8058 one-click unsubscribe; the server POSTs to it only on unsubscribeOneClick" example:"https://news.example.com/unsubscribe?t=8f2c"`
	Links    []string                  `json:"links" description:"Other HTTPS unsubscribe pages, for the person to open; the server never fetches them"`
	Mailto   []UnsubscribeMailtoOption `json:"mailto" description:"mailto: targets (RFC 2369, RFC 6068), in the order the field lists them"`
}

// UnsubscribeMailtoOption is one mailto: target: the address and the subject and body its
// URI names.
type UnsubscribeMailtoOption struct {
	To      string `json:"to" example:"leave@news.example.com"`
	Subject string `json:"subject" example:"unsubscribe"`
	Body    string `json:"body"`
}

// UnsubscribedOneClick is the answer to a one-click unsubscribe: the target accepted the POST
// with response_status.
type UnsubscribedOneClick struct {
	Method         string `json:"method" enum:"one_click" example:"one_click"`
	Target         string `json:"target" example:"https://news.example.com/unsubscribe?t=8f2c"`
	ResponseStatus int    `json:"response_status" example:"200"`
}

// unsubscribeMailtoRequest sends the unsubscribe message of mailto target index (0 when
// omitted) of message {id}, from the given address or, when from is omitted, from the
// message's own account.
type unsubscribeMailtoRequest struct {
	ID   uint32                `path:"id" minimum:"1" example:"1"`
	Body unsubscribeMailtoBody `body:""`
}

type unsubscribeMailtoBody struct {
	From  string `json:"from,omitempty" description:"The address to send from; the message's account when omitted"`
	Index int    `json:"index,omitempty" minimum:"0" description:"Which mailto target, counted from 0 in the order getUnsubscribeOptions lists them"`
}

// unsubscribeTargets reads the targets of a stored message from its decoded headers or, when
// the row has none, from the header section of its source, as messageClass does. Only the
// header section of the source is read: it holds this server's Authentication-Results and
// the DKIM-Signature fields, and the body is never needed.
//
// RFC 8058 §4: "The DKIM signature MUST cover the List-Unsubscribe and
// List-Unsubscribe-Post header fields." So the HTTPS target is one-click only when a DKIM
// signature this server verified as pass signed both fields (pipeline.StoredDKIMCovers);
// otherwise it is an ordinary link the person opens.
// unsigned reports that the field claimed one-click but no such signature covers it.
func (h *MessageHandler) unsubscribeTargets(ctx context.Context, m models.Message) (targets rmail.UnsubscribeTargets, unsigned bool, err error) {
	var source models.Message
	if err := h.db.WithContext(ctx).Select("id", "raw_message", "raw_octets").First(&source, m.ID).Error; err != nil {
		return rmail.UnsubscribeTargets{}, false, fmt.Errorf("load the message source: %w", err)
	}
	header := sourceHeaderSection(source.SourceReader())
	var headers map[string][]string
	if err := json.Unmarshal(m.Headers, &headers); err != nil || len(headers) == 0 {
		headers = rmail.SourceHeaders(strings.NewReader(header))
	}
	targets = rmail.ParseUnsubscribe(headers)
	if targets.OneClick != "" && !pipeline.StoredDKIMCovers(header, "List-Unsubscribe", "List-Unsubscribe-Post") {
		return targets.WithoutOneClick(), true, nil
	}
	return targets, false, nil
}

// maxUnsubscribeHeaderSection bounds the header section unsubscribeTargets reads.
const maxUnsubscribeHeaderSection = 1 << 20

// sourceHeaderSection returns the header section of source: every line up to the blank line
// that ends it, at most maxUnsubscribeHeaderSection octets.
func sourceHeaderSection(source io.Reader) string {
	reader := bufio.NewReader(io.LimitReader(source, maxUnsubscribeHeaderSection))
	var out strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if line == "\n" || line == "\r\n" {
			return out.String()
		}
		out.WriteString(line)
		if err != nil {
			return out.String()
		}
	}
}

// unsubscribeMessageFields are the columns the unsubscribe routes read first.
var unsubscribeMessageFields = []string{"id", "mailbox_id", "headers"}

// getUnsubscribeOptions answers the targets a message offers. It sends nothing.
func (h *MessageHandler) getUnsubscribeOptions(r *http.Request, in messagePath) (UnsubscribeOptions, error) {
	msg, err := h.ownedMessage(r, in.ID, models.CapMailRead, unsubscribeMessageFields...)
	if err != nil {
		return UnsubscribeOptions{}, err
	}
	targets, _, err := h.unsubscribeTargets(r.Context(), msg)
	if err != nil {
		return UnsubscribeOptions{}, err
	}
	out := UnsubscribeOptions{OneClick: targets.OneClick, Links: []string{}, Mailto: []UnsubscribeMailtoOption{}}
	out.Links = append(out.Links, targets.Links...)
	for _, m := range targets.Mailto {
		out.Mailto = append(out.Mailto, UnsubscribeMailtoOption(m))
	}
	return out, nil
}

var (
	errNoOneClick       = endpoint.Refuse(http.StatusNotFound, "no_one_click_unsubscribe", "The message offers no RFC 8058 one-click unsubscribe")
	errNoMailtoTarget   = endpoint.Refuse(http.StatusNotFound, "no_mailto_unsubscribe", "The message offers no mailto: unsubscribe target at that index")
	errUnsubscribeBlock = errors.New("unsubscribe target refused")
	// RFC 8058 §4: one-click needs a DKIM signature covering List-Unsubscribe and
	// List-Unsubscribe-Post. Without one the target is a link for the person to open.
	errOneClickUnsigned = endpoint.Refuse(http.StatusUnprocessableEntity, "unsubscribe_target_refused",
		"No DKIM signature this server verified covers List-Unsubscribe and List-Unsubscribe-Post, so the server will not post to the target (RFC 8058 §4); open it as a link instead")
)

// unsubscribeOneClick POSTs List-Unsubscribe=One-Click to the message's one-click target,
// once, because the person asked (RFC 8058 §3.1). The target must be HTTPS and resolve only
// to public addresses, on every redirect hop; no cookie or credential is sent.
func (h *MessageHandler) unsubscribeOneClick(r *http.Request, in messagePath) (UnsubscribedOneClick, error) {
	msg, err := h.ownedMessage(r, in.ID, models.CapMailWrite, unsubscribeMessageFields...)
	if err != nil {
		return UnsubscribedOneClick{}, err
	}
	targets, unsigned, err := h.unsubscribeTargets(r.Context(), msg)
	if err != nil {
		return UnsubscribedOneClick{}, err
	}
	if unsigned {
		return UnsubscribedOneClick{}, errOneClickUnsigned
	}
	target := targets.OneClick
	if target == "" {
		return UnsubscribedOneClick{}, errNoOneClick
	}
	poster := h.oneClick
	if poster == nil {
		poster = defaultOneClickPoster()
	}
	status, err := poster.post(r.Context(), target)
	switch {
	case errors.Is(err, errUnsubscribeBlock):
		return UnsubscribedOneClick{}, endpoint.Refuse(http.StatusUnprocessableEntity, "unsubscribe_target_refused",
			"The unsubscribe target is not a public HTTPS address, so the server will not post to it")
	case err != nil:
		return UnsubscribedOneClick{}, endpoint.Refuse(http.StatusBadGateway, "unsubscribe_failed",
			"The unsubscribe target could not be reached")
	case status < 200 || status > 299:
		return UnsubscribedOneClick{}, endpoint.Refuse(http.StatusBadGateway, "unsubscribe_failed",
			fmt.Sprintf("The unsubscribe target answered %d", status))
	}
	return UnsubscribedOneClick{Method: "one_click", Target: target, ResponseStatus: status}, nil
}

// unsubscribeMailto sends the message a mailto: target asks for through sendMessage, so the
// sender authorization, rate limits, recipient checks and outbound policy of every send apply.
// It takes no Idempotency-Key, so it sends past sendMessage's idempotency wrapper.
func (h *MessageHandler) unsubscribeMailto(r *http.Request, in unsubscribeMailtoRequest) (endpoint.Status[any], error) {
	var zero endpoint.Status[any]
	msg, err := h.ownedMessage(r, in.ID, models.CapMailRead, unsubscribeMessageFields...)
	if err != nil {
		return zero, err
	}
	targets, _, err := h.unsubscribeTargets(r.Context(), msg)
	if err != nil {
		return zero, err
	}
	mailto := targets.Mailto
	if in.Body.Index < 0 || in.Body.Index >= len(mailto) {
		return zero, errNoMailtoTarget
	}
	target := mailto[in.Body.Index]
	from := in.Body.From
	if from == "" {
		var mailbox models.Mailbox
		if err := h.db.WithContext(r.Context()).Select("id", "address").First(&mailbox, msg.MailboxID).Error; err != nil {
			return zero, fmt.Errorf("load the message's account: %w", err)
		}
		from = mailbox.Address
	}
	subject := target.Subject
	if subject == "" {
		subject = "unsubscribe"
	}
	body := target.Body
	if body == "" {
		body = "unsubscribe"
	}
	return h.sendMessageWithParts(r, sendMessageRequest{From: from, To: []string{target.To}, Subject: subject, BodyText: body}, nil)
}

// oneClickPoster sends the RFC 8058 POST. blocked decides which addresses it refuses, and
// lookup resolves a host name; tests replace both and the TLS roots.
type oneClickPoster struct {
	blocked   func(net.IP) bool
	lookup    func(ctx context.Context, host string) ([]net.IPAddr, error)
	tlsConfig *tls.Config
	timeout   time.Duration
}

func defaultOneClickPoster() *oneClickPoster {
	return &oneClickPoster{blocked: blockedUnsubscribeIP, lookup: net.DefaultResolver.LookupIPAddr, timeout: 15 * time.Second}
}

// SetOneClickTarget points the one-click POST at a target a test serves itself: every host
// resolves to ip, ip is allowed although it is not public, and roots are the only trusted
// certificates. Every other address stays refused, on every redirect hop and on the address
// dialed. The server never calls it, so its POSTs keep the guarded defaults.
func (h *MessageHandler) SetOneClickTarget(ip net.IP, roots *x509.CertPool) {
	h.oneClick = &oneClickPoster{
		blocked: func(candidate net.IP) bool { return !candidate.Equal(ip) },
		lookup: func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: ip}}, nil
		},
		tlsConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		timeout:   defaultOneClickPoster().timeout,
	}
}

// blockedUnsubscribeIP reports whether the one-click POST must not reach ip. The target is
// written by whoever sent the mail, so, as outbound delivery does (gateway/queue's
// blockedOutboundIP), every non-public class is refused, and the shared and reserved ranges a
// cloud or carrier network uses internally besides.
func blockedUnsubscribeIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsPrivate() || ip.IsUnspecified() {
		return true
	}
	for _, block := range nonPublicUnsubscribeNets {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

var nonPublicUnsubscribeNets = func() []*net.IPNet {
	var nets []*net.IPNet
	for _, cidr := range []string{
		"0.0.0.0/8",       // this network
		"100.64.0.0/10",   // carrier-grade NAT (RFC 6598)
		"192.0.0.0/24",    // IETF protocol assignments
		"198.18.0.0/15",   // benchmarking
		"240.0.0.0/4",     // reserved, and the broadcast address
		"64:ff9b::/96",    // NAT64, which reaches an IPv4 address the checks above did not see
		"64:ff9b:1::/48",  // local-use NAT64
		"2002::/16",       // 6to4, the same
		"fec0::/10",       // deprecated site-local
		"100::/64",        // discard
		"2001:db8::/32",   // documentation
		"192.0.2.0/24",    // documentation
		"198.51.100.0/24", // documentation
		"203.0.113.0/24",  // documentation
	} {
		_, block, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(err)
		}
		nets = append(nets, block)
	}
	return nets
}()

// checkHost refuses a host that is, or resolves to, any refused address. Every address is
// checked, so a name with one public and one private address is refused.
func (p *oneClickPoster) checkHost(ctx context.Context, host string) error {
	if ip := net.ParseIP(host); ip != nil {
		if p.blocked(ip) {
			return fmt.Errorf("%w: %s is not a public address", errUnsubscribeBlock, ip)
		}
		return nil
	}
	addrs, err := p.lookup(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("resolve %s: no address", host)
	}
	for _, addr := range addrs {
		if p.blocked(addr.IP) {
			return fmt.Errorf("%w: %s resolves to %s", errUnsubscribeBlock, host, addr.IP)
		}
	}
	return nil
}

// dialControl checks the address actually dialed, after the dialer's own resolution, so a
// name that resolved to a public address in checkHost and to a private one now (DNS
// rebinding) is still refused.
func (p *oneClickPoster) dialControl(_ string, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: bad dial address %q", errUnsubscribeBlock, address)
	}
	ip := net.ParseIP(host)
	if ip == nil || p.blocked(ip) {
		return fmt.Errorf("%w: refusing to connect to %s", errUnsubscribeBlock, host)
	}
	return nil
}

// checkTarget refuses a URL the POST may not go to: anything but HTTPS with a host and no
// user information, or a host that is not public.
func (p *oneClickPoster) checkTarget(ctx context.Context, target *url.URL) error {
	if !rmail.UnsubscribeHTTPSValid(target) {
		return fmt.Errorf("%w: %s is not an https URL without credentials", errUnsubscribeBlock, target.Redacted())
	}
	return p.checkHost(ctx, target.Hostname())
}

// maxUnsubscribeRedirects is how many redirects the POST follows, each one checked.
const maxUnsubscribeRedirects = 3

// post sends the one-click POST to target once and answers the final status.
func (p *oneClickPoster) post(ctx context.Context, target string) (int, error) {
	u, err := url.Parse(target)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", errUnsubscribeBlock, err)
	}
	if err := p.checkTarget(ctx, u); err != nil {
		return 0, err
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: p.dialControl}
	client := &http.Client{
		Timeout: p.timeout,
		// No Jar: no cookie is sent or kept (RFC 8058 §3.1).
		Transport: &http.Transport{
			// No proxy: a proxy would dial the target itself, past the address checks.
			Proxy:               nil,
			DialContext:         dialer.DialContext,
			TLSClientConfig:     p.tlsConfig,
			TLSHandshakeTimeout: 10 * time.Second,
			DisableKeepAlives:   true,
			ForceAttemptHTTP2:   true,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxUnsubscribeRedirects {
				return fmt.Errorf("unsubscribe: stopped after %d redirects", maxUnsubscribeRedirects)
			}
			return p.checkTarget(req.Context(), req.URL)
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(rmail.UnsubscribeOneClick))
	if err != nil {
		return 0, fmt.Errorf("%w: %v", errUnsubscribeBlock, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "REST Mail one-click unsubscribe")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, nil
}
