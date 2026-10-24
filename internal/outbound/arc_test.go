package outbound

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"strings"
	"testing"

	arc "github.com/rest-mail/go-arc"
	dkim "github.com/rest-mail/go-dkim"
)

// arcKeys serves each sealer's public key at <selector>._domainkey.<domain>, as DNS would.
func arcKeys(t *testing.T, keys map[string]*rsa.PrivateKey) dkim.TXTResolver {
	t.Helper()
	records := map[string]string{}
	for name, key := range keys {
		der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		records[name] = "v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(der)
	}
	return func(_ context.Context, name string) ([]string, error) {
		if r, ok := records[strings.TrimSuffix(name, ".")]; ok {
			return []string{r}, nil
		}
		return nil, nil
	}
}

func rsaKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

const forwardedMessage = "Delivered-To: fwd@elsewhere.test\r\n" +
	"From: alice@remote.test\r\n" +
	"To: dana@forwarder.test\r\n" +
	"Subject: Lunch?\r\n" +
	"Date: Sat, 24 Oct 2026 05:00:00 +0000\r\n" +
	"Message-ID: <m1@remote.test>\r\n" +
	"\r\n" +
	"Are you free on Monday?\r\n"

// A forward that arrived with no ARC chain leaves with set i=1, cv=none, recording the results
// this server found, and the chain validates; the next forwarder adds i=2 with cv=pass, and a
// chain that arrived failed is relayed without a set (RFC 8617 §5.1, #731).
func TestSealARC(t *testing.T) {
	first, second := rsaKey(t), rsaKey(t)
	resolver := arcKeys(t, map[string]*rsa.PrivateKey{
		"sel._domainkey.forwarder.test": first,
		"sel._domainkey.second.test":    second,
	})
	results := "restmail; spf=pass smtp.mailfrom=remote.test; dkim=pass header.d=remote.test; dmarc=pass header.from=remote.test"

	sealed, err := sealARC(forwardedMessage, arc.SealOptions{Domain: "forwarder.test", Selector: "sel", PrivateKey: first, AuthResults: results, ChainValidation: "none", Resolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ARC-Authentication-Results: i=1; " + results + "\r\n", "d=forwarder.test", "cv=none"} {
		if !strings.Contains(sealed, want) {
			t.Errorf("the sealed forward lacks %q:\n%s", want, sealed)
		}
	}
	if !strings.HasSuffix(sealed, forwardedMessage) {
		t.Errorf("the seal changed the message it sealed:\n%s", sealed)
	}
	if cv, reason := arc.Verify(context.Background(), []byte(sealed), resolver); cv != "pass" {
		t.Fatalf("the chain does not validate: %s (%s)", cv, reason)
	}

	// The next forwarder found the chain valid when the message arrived.
	again, err := sealARC(sealed, arc.SealOptions{Domain: "second.test", Selector: "sel", PrivateKey: second, AuthResults: "restmail; arc=pass", ChainValidation: "pass", Resolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(again, "ARC-Seal: i=2;") || !strings.Contains(again, "cv=pass") {
		t.Errorf("the second forward is not i=2, cv=pass:\n%s", again)
	}
	if cv, reason := arc.Verify(context.Background(), []byte(again), resolver); cv != "pass" {
		t.Fatalf("the two-set chain does not validate: %s (%s)", cv, reason)
	}

	// A verdict of none on a message that carries a chain is not trusted: the seal verifies
	// the chain itself, and finds it valid.
	checked, err := sealARC(sealed, arc.SealOptions{Domain: "second.test", Selector: "sel", PrivateKey: second, AuthResults: "restmail; arc=none", ChainValidation: "none", Resolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(checked, "ARC-Seal: i=2;") || !strings.Contains(checked, "cv=pass") {
		t.Errorf("a chain status that did not fit was used:\n%s", checked)
	}

	// A chain whose latest seal says cv=fail is not continued.
	failed, err := sealARC(sealed, arc.SealOptions{Domain: "second.test", Selector: "sel", PrivateKey: second, AuthResults: "restmail; arc=fail", ChainValidation: "fail", Resolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	relayed, err := sealARC(failed, arc.SealOptions{Domain: "forwarder.test", Selector: "sel", PrivateKey: first, AuthResults: results, Resolver: resolver})
	if err != nil || relayed != failed {
		t.Errorf("a failed chain was sealed again (err %v):\n%s", err, relayed)
	}
}

// With no domain key to seal with, the forward goes as it is.
func TestSealARC_NoKey(t *testing.T) {
	got, err := SealARC(nil, "", "forwarder.test", forwardedMessage, "restmail; none", "none")
	if err != nil || got != forwardedMessage {
		t.Errorf("SealARC without a key: %v\n%s", err, got)
	}
}
