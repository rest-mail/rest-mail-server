package dkim

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"strings"
	"testing"
)

// buildRaw assembles a raw message with a QUOTED display-name From — the exact
// shape that broke production signing (the signer had reconstructed it unquoted).
func buildRaw(body string) string {
	return strings.Join([]string{
		`From: "Verify Outbound" <verify-out@example.test>`,
		"To: alice@rcpt.test",
		"Subject: sign round trip",
		"Date: Thu, 23 Jul 2026 02:53:08 +0000",
		"Message-ID: <sign-rt-1@example.test>",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		body,
	}, "\r\n")
}

func TestSign_VerifyRoundTrip(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM := publicPEM(t, priv)
	resolver := testKeyResolver(t, pubPEM, "default", "example.test")

	for _, canon := range []struct{ h, b string }{
		{"relaxed", "relaxed"}, {"simple", "simple"}, {"relaxed", "simple"}, {"simple", "relaxed"},
	} {
		t.Run(canon.h+"/"+canon.b, func(t *testing.T) {
			raw := buildRaw("Testing outbound DKIM signature validity.\r\n")
			val, err := Sign([]byte(raw), SignOptions{
				Domain: "example.test", Selector: "default", PrivateKey: priv,
				HeaderCanon: canon.h, BodyCanon: canon.b, Time: 1784775188,
			})
			if err != nil {
				t.Fatal(err)
			}
			signed := "DKIM-Signature: " + val + "\r\n" + raw

			results := Verify(context.Background(), []byte(signed), resolver)
			if len(results) != 1 {
				t.Fatalf("want 1 result, got %d", len(results))
			}
			if results[0].Result != ResultPass {
				t.Errorf("want pass, got %s (%s)", results[0].Result, results[0].Reason)
			}
		})
	}
}

func TestSign_TamperFails(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 1024)
	resolver := testKeyResolver(t, publicPEM(t, priv), "default", "example.test")
	raw := buildRaw("Original.\r\n")
	val, err := Sign([]byte(raw), SignOptions{Domain: "example.test", Selector: "default", PrivateKey: priv})
	if err != nil {
		t.Fatal(err)
	}
	signed := "DKIM-Signature: " + val + "\r\n" + raw

	// Tamper the (signed) Subject header.
	tampered := strings.Replace(signed, "Subject: sign round trip", "Subject: evil", 1)
	results := Verify(context.Background(), []byte(tampered), resolver)
	if len(results) != 1 || results[0].Result != ResultFail {
		t.Fatalf("want fail after header tamper, got %+v", results)
	}
}

func TestSign_OnlySignsPresentHeaders(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 1024)
	// Request signing Cc (absent) — it must be omitted from h=, and still verify.
	raw := buildRaw("body\r\n")
	val, err := Sign([]byte(raw), SignOptions{
		Domain: "example.test", Selector: "default", PrivateKey: priv,
		Headers: []string{"from", "cc", "subject"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(val, "cc:") || strings.Contains(val, ":cc") {
		t.Errorf("absent Cc should not appear in h=: %s", val)
	}
	signed := "DKIM-Signature: " + val + "\r\n" + raw
	results := Verify(context.Background(), []byte(signed), testKeyResolver(t, publicPEM(t, priv), "default", "example.test"))
	if len(results) != 1 || results[0].Result != ResultPass {
		t.Fatalf("want pass, got %+v", results)
	}
}
