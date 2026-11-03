package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/rest-mail/rest-mail-server"
	"github.com/rest-mail/rest-mail-server/internal/api/middleware"
	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/db/models"
	"github.com/rest-mail/rest-mail-server/internal/grants"
	"golang.org/x/net/dns/dnsmessage"
)

const domainDNSCheckPath = "/api/v1/admin/domains/{id}/dns"

// domainDNSPublicKey is the DKIM record of domainDNSPrivateKey, which is the RFC 8032 §7.1 test key.
const domainDNSPublicKey = "v=DKIM1; k=ed25519; p=11qYAYKxCrfVS/7TyWQHOg7hcvPapiMlrwIaaPcHURo="

const (
	domainDNSMailHost    = "mail.example.net"
	domainDNSReverseName = "25.113.0.203.in-addr.arpa"
	domainDNSOldHost     = "old.provider.example"
	domainDNSDiffering   = "customer.example"
)

func domainDNSPrivateKey(t *testing.T) string {
	t.Helper()
	seed, err := hex.DecodeString("9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(ed25519.NewKeyFromSeed(seed))
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

type domainDNSCheckContextKey struct{}

type domainDNSCheckConnection struct {
	domainMutationConnection
	signingKey string
}

func (c *domainDNSCheckConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c *domainDNSCheckConnection) Driver() driver.Driver { return c }

func (c *domainDNSCheckConnection) Open(string) (driver.Conn, error) { return c, nil }

func (c *domainDNSCheckConnection) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	// No fixture domain has an active MTA-STS policy or a rotation under way.
	if strings.Contains(q, `FROM "mtasts_policies"`) || strings.Contains(q, `FROM "dkim_rotation_keys"`) {
		c.record(q, args)
		return &authReadSnapshotRows{columns: []string{"id"}}, nil
	}
	if strings.Contains(q, `FROM "domains"`) {
		c.record(q, args)
		if len(args) == 0 {
			return nil, errors.New("DNS lookup lost domain ID")
		}
		id := args[0].Value.(int64)
		name := "example.com"
		if id == 4 {
			name = "not a domain"
		}
		if id == 5 {
			name = "Bücher.Test"
		}
		if id == 6 {
			name = domainDNSDiffering
		}
		if strings.Contains(q, `SELECT "name"`) {
			if len(args) != 1 || !strings.Contains(q, "id =") {
				return nil, errors.New("scope query lost exact ID")
			}
			if id == 8 {
				return nil, errors.New("private domain scope detail")
			}
			r := &authReadSnapshotRows{columns: []string{"name"}}
			if id != 2147483647 {
				r.rows = [][]driver.Value{{name}}
			}
			return r, nil
		}
		c.record("DNS_CONTEXT", []driver.NamedValue{{Value: ctx.Value(domainDNSCheckContextKey{}) == true}})
		if len(args) != 2 || args[1].Value != int64(1) || !strings.Contains(q, `"domains"."id" =`) {
			return nil, errors.New("DNS resource query lost exact ID/bound")
		}
		if id == 3 {
			return nil, errors.New("private DNS domain lookup detail")
		}
		r := &authReadSnapshotRows{columns: []string{"id", "name", "dkim_selector", "dkim_private_key", "dmarc_policy"}}
		if id != 2147483647 {
			selector, key, policy := "rm2026", c.signingKey, models.DMARCPolicyNone
			if id == 2 {
				selector = ""
				key = ""
			}
			if id == 6 {
				policy = models.DMARCPolicyQuarantine
			}
			r.rows = [][]driver.Value{{id, name, selector, key, policy}}
		}
		return r, nil
	}
	return c.accountCredentialListConnection.QueryContext(ctx, q, args)
}

func (c *domainDNSCheckConnection) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(q, `"domains"`) {
		c.record("DNS_WRITE", args)
	}
	return nil, errors.New("DNS check must not mutate domains")
}

type domainDNSCalls struct {
	mu        sync.Mutex
	questions []string
}

func (c *domainDNSCalls) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.questions...)
}

// Use the real resolver and UDP transport. Every dial is redirected to this
// bounded local fixture; no external DNS service or mutable public record is used.
func newDomainDNSCheckScenarios(t *testing.T) (map[string]bulkQueueExampleScenario, *domainDNSCalls) {
	t.Helper()
	calls := &domainDNSCalls{}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var question dnsmessage.Message
			if err := question.Unpack(buf[:n]); err != nil {
				t.Error(err)
				return
			}
			response := dnsmessage.Message{Header: dnsmessage.Header{ID: question.ID, Response: true, RecursionDesired: true, RecursionAvailable: true}, Questions: question.Questions}
			for _, q := range question.Questions {
				name := strings.TrimSuffix(q.Name.String(), ".")
				calls.mu.Lock()
				calls.questions = append(calls.questions, q.Type.String()+" "+name)
				calls.mu.Unlock()
				header := dnsmessage.ResourceHeader{Name: q.Name, Type: q.Type, Class: dnsmessage.ClassINET, TTL: 60}
				if strings.HasPrefix(name, "_mta-sts.") {
					response.RCode = dnsmessage.RCodeNameError
					continue
				}
				body, e := domainDNSAnswer(q.Type, name)
				if e != nil {
					t.Error(e)
					return
				}
				response.Answers = append(response.Answers, dnsmessage.Resource{Header: header, Body: body})
			}
			raw, err := response.Pack()
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := pc.WriteTo(raw, addr); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = pc.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("DNS fixture failed to stop")
		}
	})
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		_, deadline := ctx.Deadline()
		if ctx.Value(domainDNSCheckContextKey{}) != true || !deadline {
			return nil, errors.New("resolver lost HTTP context")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "udp", pc.LocalAddr().String())
	}}
	return newDomainDNSCheckRouters(t, resolver), calls
}

// domainDNSAnswer publishes what a domain must, on domainDNSMailHost, except that
// domainDNSDiffering's MX still names its old provider.
func domainDNSAnswer(typ dnsmessage.Type, name string) (dnsmessage.ResourceBody, error) {
	domainOf := func(prefix string) string { return strings.TrimPrefix(name, prefix) }
	target := func(host string) (dnsmessage.Name, error) { return dnsmessage.NewName(host + ".") }
	switch typ {
	case dnsmessage.TypeMX:
		host := domainDNSMailHost
		if name == domainDNSDiffering {
			host = domainDNSOldHost
		}
		mx, err := target(host)
		if err != nil {
			return nil, err
		}
		return &dnsmessage.MXResource{Pref: 10, MX: mx}, nil
	case dnsmessage.TypeTXT:
		value := "v=spf1 mx -all"
		switch {
		case strings.HasPrefix(name, "_dmarc."):
			value = "v=DMARC1; p=none; rua=mailto:postmaster@" + domainOf("_dmarc.")
		case strings.Contains(name, "._domainkey."):
			value = domainDNSPublicKey
		case strings.HasPrefix(name, "_smtp._tls."):
			value = "v=TLSRPTv1; rua=https://" + domainDNSMailHost + "/.well-known/smtp-tlsrpt"
		}
		return &dnsmessage.TXTResource{TXT: []string{value}}, nil
	case dnsmessage.TypeSRV:
		for service, port := range map[string]uint16{"_submissions": 465, "_imaps": 993, "_pop3s": 995} {
			if strings.HasPrefix(name, service+"._tcp.") {
				host, err := target(domainDNSMailHost)
				if err != nil {
					return nil, err
				}
				return &dnsmessage.SRVResource{Weight: 1, Port: port, Target: host}, nil
			}
		}
		root, err := dnsmessage.NewName(".")
		if err != nil {
			return nil, err
		}
		return &dnsmessage.SRVResource{Target: root}, nil
	case dnsmessage.TypeA:
		return &dnsmessage.AResource{A: [4]byte{203, 0, 113, 25}}, nil
	case dnsmessage.TypePTR:
		host, err := target(domainDNSMailHost)
		if err != nil {
			return nil, err
		}
		return &dnsmessage.PTRResource{PTR: host}, nil
	}
	return nil, fmt.Errorf("unexpected DNS type %v", typ)
}

func newDomainDNSCheckRouters(t *testing.T, resolver *net.Resolver) map[string]bulkQueueExampleScenario {
	t.Helper()
	unavailable, _, _, jwt := authReadBatchRouters(t)
	token := mailboxToken(t, jwt)
	previousSessionCheck := sessionCheck
	sessionCheck = cachedSessionCheck
	defer func() { sessionCheck = previousSessionCheck }()
	keys := map[string]accountListKey{}
	tokens := map[string]string{}
	for name, p := range map[string]auth.ScopedPermission{
		"key_read":         {Capability: middleware.CapDomainsRead, Scope: "*"},
		"key_write":        {Capability: middleware.CapDomainsWrite, Scope: "*"},
		"key_domain":       {Capability: middleware.CapDomainsRead, Scope: "domain:example.com"},
		"key_mailbox":      {Capability: middleware.CapDomainsRead, Scope: "mailbox:owner@example.com"},
		"key_wrong_domain": {Capability: middleware.CapDomainsRead, Scope: "domain:other.test"},
	} {
		value, public, hash, err := auth.NewAPIKey()
		if err != nil {
			t.Fatal(err)
		}
		scope, err := json.Marshal(models.CredentialScope{Permissions: []auth.ScopedPermission{p}})
		if err != nil {
			t.Fatal(err)
		}
		keys[public] = accountListKey{accountReadKey{hash: hash, scope: scope}, models.CredentialKindAPIKey}
		tokens[name] = value
	}
	previous := heldGrants
	defer func() { heldGrants = previous }()
	signingKey := domainDNSPrivateKey(t)
	cfg := config.Config{
		Environment: "test", APIMaxBodyBytes: 512, GatewayHostname: domainDNSMailHost,
		SMTPPortSubmissionTLS: 465, IMAPTLSPort: 993, POP3TLSPort: 995,
	}
	makeRouter := func(entries []grants.Entry, fail, limited bool) bulkQueueExampleScenario {
		heldGrants = func(*grants.Cache) middleware.HeldGrants {
			return func(uint) (grants.Held, error) {
				if fail {
					return grants.Held{}, errors.New("private domain grant detail")
				}
				return grants.HeldOf(entries...), nil
			}
		}
		store := &bulkQueueExampleStore{}
		c := &domainDNSCheckConnection{signingKey: signingKey, domainMutationConnection: domainMutationConnection{accountCredentialListConnection: accountCredentialListConnection{mode: "normal", keys: keys}, store: store}}
		pool := sql.OpenDB(c)
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = pool.Close() })
		db := openAccountReadDB(t, pool)
		db.NowFunc = func() time.Time { return pipelineMutationTime }

		r := func() *Routers {
			previousResolver := net.DefaultResolver
			net.DefaultResolver = resolver
			defer func() { net.DefaultResolver = previousResolver }()
			routerConfig := cfg
			return NewRouters(db, jwt, &routerConfig, nil)
		}()
		t.Cleanup(r.Close)
		return bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx, cancel := context.WithTimeout(context.WithValue(req.Context(), domainDNSCheckContextKey{}, true), time.Second)
			defer cancel()
			req = req.WithContext(ctx)
			r.Public.ServeHTTP(w, req)
		}), token}, store}
	}
	wild := []grants.Entry{{Capability: grants.Wildcard, Scope: grants.All}}
	normal := makeRouter(wild, false, false)
	out := map[string]bulkQueueExampleScenario{}
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"domainDNSCheck"} {
		for name := range pairs[op] {
			out[name] = normal
		}
	}
	out["read_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsRead, Scope: grants.All}}, false, false)
	out["write_only"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsWrite, Scope: grants.All}}, false, false)
	out["domain_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsRead, Scope: grants.Domain("example.com")}}, false, false)
	out["mailbox_scope"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsRead, Scope: grants.Mailbox("owner@example.com")}}, false, false)
	out["permission_store_unavailable"] = makeRouter(nil, true, false)
	out["unauthenticated"] = bulkQueueExampleScenario{accountReadScenario{unavailable, ""}, nil}
	out["session_unavailable"] = bulkQueueExampleScenario{accountReadScenario{unavailable, token}, nil}
	for name, key := range tokens {
		s := normal
		s.token = key
		out[name] = s
	}
	missing := out["write_only"]
	missing.token = tokens["key_read"]
	out["key_holder_missing"] = missing
	for _, name := range []string{"cookie_session"} {
		name := name
		out[name] = bulkQueueExampleScenario{accountReadScenario{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.Clone(r.Context())
			r.Header.Del("Authorization")
			r.AddCookie(&http.Cookie{Name: auth.AccessCookieName, Value: token})
			r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "domain-csrf"})
			if name == "cookie_session" {
				r.Header.Set(auth.CSRFHeaderName, "domain-csrf")
			}
			if name == "csrf_mismatch" {
				r.Header.Set(auth.CSRFHeaderName, "wrong")
			}
			normal.handler.ServeHTTP(w, r)
		}), ""}, normal.store}
	}
	cfg.GatewayHostname = "localhost"
	out["mail_host_unconfigured"] = makeRouter(wild, false, false)
	cfg.GatewayHostname = domainDNSMailHost
	out["wrong_domain"] = makeRouter([]grants.Entry{{Capability: middleware.CapDomainsRead, Scope: grants.Domain("other.test")}}, false, false)

	out["scope_lookup_unavailable"] = out["domain_scope"]
	out["rate_limited"] = bulkQueueExampleScenario{accountReadScenario{apiKeyRefusalLimited(t), ""}, nil}
	return out
}

func TestDomainDNSCheckExamples(t *testing.T) {
	f, calls := newDomainDNSCheckScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs["domainDNSCheck"]) < 20 {
		t.Fatal("DNS examples incomplete")
	}
	for name, pair := range pairs["domainDNSCheck"] {
		t.Run(name, func(t *testing.T) {
			examplepairs.Proven(t, "domainDNSCheck", name)
			s, ok := f[name]
			if !ok {
				t.Fatalf("missing DNS scenario%s", name)
			}
			before := len(calls.snapshot())
			dbBefore := 0
			if s.store != nil {
				dbBefore = len(s.store.snapshot())
			}
			var body []byte
			if name == "request_too_large" {
				body = bytes.Repeat([]byte("x"), 513)
			}
			rr := mailboxSessionEndingRequest(t, s, http.MethodGet, messageCollectionReadURI(domainDNSCheckPath, pair), body)
			if rr.Code != pair.Response.Status {
				t.Fatalf("got%d %s want%d", rr.Code, rr.Body, pair.Response.Status)
			}
			assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			questions := calls.snapshot()[before:]
			want := []string{}
			if rr.Code == 200 && name != "invalid_stored_name" {
				domain := "example.com"
				if name == "unicode_domain" {
					domain = "xn--bcher-kva.test"
				}
				if name == "published_differs" {
					domain = domainDNSDiffering
				}
				want = []string{"TypeMX " + domain, "TypeTXT " + domain, "TypeTXT _dmarc." + domain, "TypeTXT _mta-sts." + domain, "TypeTXT _smtp._tls." + domain}
				for _, service := range []string{"_submissions", "_imaps", "_pop3s", "_submission", "_imap", "_pop3"} {
					want = append(want, "TypeSRV "+service+"._tcp."+domain)
				}
				if name != "no_signing_key" {
					want = append(want, "TypeTXT rm2026._domainkey."+domain)
				}
				if name != "mail_host_unconfigured" {
					want = append(want, "TypeA "+domainDNSMailHost, "TypePTR "+domainDNSReverseName)
				}
			}
			sort.Strings(want)
			sort.Strings(questions)
			a, _ := json.Marshal(questions)
			b, _ := json.Marshal(want)
			if len(questions) != len(want) || (len(want) > 0 && !bytes.Equal(a, b)) {
				t.Fatalf("DNS questions%s want%s", a, b)
			}
			if s.store != nil {
				for _, c := range s.store.snapshot()[dbBefore:] {
					if c.query == "DNS_WRITE" {
						t.Fatal("DNS check mutated domain")
					}
					if c.query == "DNS_CONTEXT" && c.args[0].Value != true {
						t.Fatal("domain lookup lost HTTP context")
					}
				}
			}
		})
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if w.OperationID == "domainDNSCheck" {
				t.Errorf("DNS warning:%v", w)
			}
		}
	}
}

func TestDomainDNSCheckExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := newDomainDNSCheckScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["domainDNSCheck"] {
		t.Run(name, func(t *testing.T) {
			checkQueueMutationPairWithVertrag(t, binary, f[name], http.MethodGet, domainDNSCheckPath, name, pair, 513)
		})
	}
}
