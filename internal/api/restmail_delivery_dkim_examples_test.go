package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/rest-mail/rest-mail-server/internal/auth"
	"github.com/rest-mail/rest-mail-server/internal/config"
	"github.com/rest-mail/rest-mail-server/internal/examplepairs"
)

// The keys that verify the signed messages in example-matrix.json. Each private half
// signed those messages once and was thrown away, so a new pair signs with a new key.
const (
	restmailDKIMContractKey = "mRteUEFJW/VDjgH/nm8N8FqKqSu8HGRdjiBGTXEebuY="
	restmailDKIMOtherKey    = "2/8aLf7uyxdQ3ZhBjBKn9W5GxKcgerCE+VINlPyZfZo="
	restmailDKIMRSAKey      = "MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAuJwMopHSlVcfjXELcu1wjUlYHxWR0jlSx2C2uQFzETfALmr/YvDG+XRplXGq/TdxBGvXlD6r8weMM+oh1u/XYuLWEYYbfv3jo8IO1GLr2aFtKIrc44wmdw8oeeaVchC6FOQEje+ob4ThuXqbGc1LrQZRn2hl8Ns+mLksLchZdEsSaC0QlWJaDWlhOGSd189ap0NYiZjwFHr99RgSAEwwNLGylxhAP0PBt0yUC1tNFmFLElwYn6UGuPqrozzmjM3qczHnbQ9gpZY2I060lxEOvdYpRDoFGyxt5I261LIILkPY6/+wDXSkPOOWludsfHYSazebaY9BEyWiEr/KNxbzDQIDAQAB"
)

// restmailDKIMSigners are the domains the DNS fixture holds keys for. Any other name,
// including one a resolver's search list makes up, has no such domain.
var restmailDKIMSigners = map[string]bool{
	"restmail.test": true, "mail.restmail.test": true, "other.test": true, "notrestmail.test": true, "external.test": true,
}

// restmailDKIMAnswer is the fixture's answer for one selector, which names the case.
func restmailDKIMAnswer(selector string) ([]string, dnsmessage.RCode) {
	switch selector {
	case "contract":
		return []string{"v=DKIM1; k=ed25519; p=" + restmailDKIMContractKey}, dnsmessage.RCodeSuccess
	case "wrongkey":
		return []string{"v=DKIM1; k=ed25519; p=" + restmailDKIMOtherKey}, dnsmessage.RCodeSuccess
	case "rsa2048":
		// A 2048-bit key does not fit one TXT string, so a record holds several.
		record := "v=DKIM1; k=rsa; p=" + restmailDKIMRSAKey
		var chunks []string
		for len(record) > 200 {
			chunks, record = append(chunks, record[:200]), record[200:]
		}
		return append(chunks, record), dnsmessage.RCodeSuccess
	case "revoked":
		return []string{"v=DKIM1; k=ed25519; p="}, dnsmessage.RCodeSuccess
	case "broken":
		return nil, dnsmessage.RCodeServerFailure
	}
	return nil, dnsmessage.RCodeNameError
}

// restmailDKIMDNS serves the keys over real UDP to the real resolver, which go-dkim
// takes from net.DefaultResolver when delivery verifies a signature. It returns how
// many key lookups were asked. Setting the resolver is why these tests are not parallel.
func restmailDKIMDNS(t *testing.T) func() int {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	lookups := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 4096)
		for {
			n, peer, err := conn.ReadFrom(buffer)
			if err != nil {
				return
			}
			var question dnsmessage.Message
			if err := question.Unpack(buffer[:n]); err != nil {
				t.Error(err)
				return
			}
			answer := dnsmessage.Message{
				Header:    dnsmessage.Header{ID: question.ID, Response: true, RecursionAvailable: true, RCode: dnsmessage.RCodeNameError},
				Questions: question.Questions,
			}
			for _, q := range question.Questions {
				selector, domain, found := strings.Cut(strings.TrimSuffix(q.Name.String(), "."), "._domainkey.")
				if q.Type != dnsmessage.TypeTXT || !found || !restmailDKIMSigners[domain] {
					continue
				}
				mu.Lock()
				lookups++
				mu.Unlock()
				var records []string
				records, answer.RCode = restmailDKIMAnswer(selector)
				if records != nil {
					answer.Answers = append(answer.Answers, dnsmessage.Resource{
						Header: dnsmessage.ResourceHeader{Name: q.Name, Type: q.Type, Class: dnsmessage.ClassINET, TTL: 60},
						Body:   &dnsmessage.TXTResource{TXT: records},
					})
				}
			}
			packed, err := answer.Pack()
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := conn.WriteTo(packed, peer); err != nil {
				return
			}
		}
	}()
	previous := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "udp", conn.LocalAddr().String())
	}}
	t.Cleanup(func() {
		net.DefaultResolver = previous
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
		<-done
	})
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return lookups
	}
}

// restmailDKIMConnection answers the delivery's real SQL: restmail.test and its
// subdomain are hosted, every other sender domain is absent, and the recipient lookup
// fails. Passing the gate therefore ends in a sanitized 503 with nothing stored, and
// the recipient lookup tells it apart from a 503 the gate answered itself.
type restmailDKIMConnection struct {
	capabilityExampleConnection
	mu             sync.Mutex
	senderReads    int
	recipientReads int
	unexpected     []string
}

func (c *restmailDKIMConnection) Connect(context.Context) (driver.Conn, error) { return c, nil }

func (c *restmailDKIMConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.Contains(query, `FROM "capabilities"`) {
		return c.capabilityExampleConnection.QueryContext(ctx, query, args)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.Contains(query, `FROM "domains"`) && len(args) > 0 {
		c.senderReads++
		rows := &authReadSnapshotRows{columns: []string{"id", "name", "active"}}
		if domain, _ := args[0].Value.(string); domain == "restmail.test" || domain == "sub.restmail.test" {
			rows.rows = [][]driver.Value{{int64(42), domain, true}}
		}
		return rows, nil
	}
	if strings.Contains(query, `FROM "mailboxes"`) && len(args) > 0 && args[0].Value == "eve@restmail.test" {
		c.recipientReads++
		return nil, errors.New("private recipient lookup dependency detail")
	}
	c.unexpected = append(c.unexpected, query)
	return nil, errors.New("unexpected SQL in RESTMAIL DKIM example")
}

type restmailDKIMExample struct {
	name         string
	strict       bool
	trustedPeers string
	// noKeyLookup says the delivery is settled with no key: the gate does not need the
	// signature, or the signature has already failed on its own body hash or time.
	noKeyLookup bool
}

var restmailDKIMExamples = []restmailDKIMExample{
	{name: "dkim_local_aligned_deferred"},
	{name: "dkim_local_rsa_aligned_deferred"},
	{name: "dkim_local_subdomain_signer_deferred"},
	{name: "dkim_local_parent_signer_deferred"},
	{name: "dkim_local_structured_from_aligned_deferred"},
	{name: "dkim_local_second_signature_aligned_deferred"},
	{name: "dkim_local_unaligned_refused"},
	{name: "dkim_local_lookalike_signer_refused"},
	{name: "dkim_local_tampered_body_refused", noKeyLookup: true},
	{name: "dkim_local_tampered_subject_refused"},
	{name: "dkim_local_wrong_key_refused"},
	{name: "dkim_local_revoked_key_refused"},
	{name: "dkim_local_missing_key_refused"},
	{name: "dkim_local_expired_refused", noKeyLookup: true},
	{name: "dkim_local_unaligned_lookup_failed_refused"},
	{name: "dkim_local_key_lookup_failed_deferred"},
	{name: "dkim_local_lookup_failed_beside_unaligned_deferred"},
	{name: "dkim_strict_external_aligned_deferred", strict: true},
	{name: "dkim_strict_external_unaligned_refused", strict: true},
	{name: "dkim_strict_external_key_lookup_failed_deferred", strict: true},
	{name: "dkim_default_external_tampered_unchecked_deferred", noKeyLookup: true},
	{name: "dkim_trusted_peer_tampered_unchecked_deferred", strict: true, trustedPeers: "127.0.0.1/32", noKeyLookup: true},
}

// testRestmailDKIMExamples sends each signed delivery through the production router,
// which verifies the real signature against a key fetched over real DNS. Every pair
// states where the delivery stopped: at the gate, or at the recipient lookup behind it.
func testRestmailDKIMExamples(t *testing.T, binary string) {
	t.Helper()
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, example := range restmailDKIMExamples {
		t.Run(example.name, func(t *testing.T) {
			pair, ok := pairs["restmailDeliver"][example.name]
			if !ok {
				t.Fatal("the named DKIM pair is missing")
			}
			examplepairs.Proven(t, "restmailDeliver", example.name)
			t.Setenv("RESTMAIL_DELIVER_STRICT", "false")
			if example.strict {
				t.Setenv("RESTMAIL_DELIVER_STRICT", "true")
			}
			t.Setenv("RESTMAIL_DELIVER_TRUSTED_CIDRS", example.trustedPeers)
			keyLookups := restmailDKIMDNS(t)
			connection := &restmailDKIMConnection{}
			pool := sql.OpenDB(connection)
			pool.SetMaxOpenConns(1)
			t.Cleanup(func() {
				if err := pool.Close(); err != nil {
					t.Error(err)
				}
			})
			routers := NewRouters(openAccountReadDB(t, pool), auth.NewJWTService(routerTestSecret, time.Minute, time.Hour), &config.Config{Environment: "test"}, nil)
			t.Cleanup(routers.Close)

			if binary == "" {
				response := httptest.NewRecorder()
				routers.Public.ServeHTTP(response, requirementRequest(requirementRoute{http.MethodPost, "/restmail/messages"}, pair))
				assertRequirementResponse(t, pair, response)
			} else {
				checkNamedPairWithVertrag(t, binary, routers.Public, namedPairExchange{
					method: http.MethodPost, path: "/restmail/messages", uri: "/restmail/messages", name: example.name,
					status: pair.Response.Status, body: string(pair.Request.Body),
				})
			}

			if got := keyLookups(); example.noKeyLookup != (got == 0) {
				t.Errorf("key lookups = %d, want none = %v", got, example.noKeyLookup)
			}
			wantRecipientReads := 0
			if pair.Requires["fault"] == "database" {
				wantRecipientReads = 1
			}
			connection.mu.Lock()
			defer connection.mu.Unlock()
			if connection.senderReads != 1 || connection.recipientReads != wantRecipientReads || len(connection.unexpected) != 0 {
				t.Errorf("sender reads = %d, recipient reads = %d, unexpected = %v; want 1, %d, none",
					connection.senderReads, connection.recipientReads, connection.unexpected, wantRecipientReads)
			}
		})
	}
}

func TestRestmailDKIMAuthenticationExamples(t *testing.T) {
	testRestmailDKIMExamples(t, "")
}

func TestRestmailDKIMAuthenticationPairsFitTheDescription(t *testing.T) {
	generator := describedRouters(t).pairGenerator(t, "signed RESTMAIL authentication")
	for _, warning := range generator.Report() {
		if warning.OperationID == "restmailDeliver" {
			t.Error(warning)
		}
	}
}

func TestRestmailDKIMAuthenticationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY to replay RESTMAIL DKIM examples")
	}
	testRestmailDKIMExamples(t, binary)
}
