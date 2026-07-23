package dkim

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// --- test-side ARC sealer (constructs valid ARC sets to verify against) ---

func arcBaseMessage() string {
	return strings.Join([]string{
		`From: "Alice" <alice@example.test>`,
		"To: bob@rcpt.test",
		"Subject: arc round trip",
		"Date: Thu, 23 Jul 2026 03:01:40 +0000",
		"Message-ID: <arc-1@example.test>",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"ARC body.\r\n",
	}, "\r\n")
}

// signAMS builds an ARC-Message-Signature value (DKIM-style) over msgHeaders+body.
func signAMS(t *testing.T, priv *rsa.PrivateKey, d, s string, i int, msgHeaders []header, body string) string {
	t.Helper()
	bodyHash := hashBytes(crypto.SHA256, []byte(canonicalizeBody(body, "relaxed")))
	bh := base64.StdEncoding.EncodeToString(bodyHash)
	hTag := "from:to:subject:date:message-id"
	valueNoB := fmt.Sprintf("i=%d; a=rsa-sha256; c=relaxed/relaxed; d=%s; s=%s; h=%s; bh=%s; b=", i, d, s, hTag, bh)
	sigHeader := header{name: "ARC-Message-Signature", value: " " + valueNoB, raw: "ARC-Message-Signature: " + valueNoB}
	signed := buildSignedHeaders(hTag, msgHeaders, sigHeader, "relaxed")
	hashed := hashBytes(crypto.SHA256, []byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, hashed)
	if err != nil {
		t.Fatal(err)
	}
	return valueNoB + base64.StdEncoding.EncodeToString(sig)
}

// signAS builds an ARC-Seal value over the chain (ascending), sealing the last set.
func signAS(t *testing.T, priv *rsa.PrivateKey, d, s string, i int, cv string, chain []*arcSet) string {
	t.Helper()
	valueNoB := fmt.Sprintf("i=%d; a=rsa-sha256; d=%s; s=%s; t=1784776000; cv=%s; b=", i, d, s, cv)
	seal := &header{name: "ARC-Seal", value: " " + valueNoB, raw: "ARC-Seal: " + valueNoB}
	chain[len(chain)-1].as = seal
	base := arcSealBase(chain)
	hashed := hashBytes(crypto.SHA256, []byte(base))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, hashed)
	if err != nil {
		t.Fatal(err)
	}
	return valueNoB + base64.StdEncoding.EncodeToString(sig)
}

func mkHeader(name, value string) *header {
	return &header{name: name, value: " " + value, raw: name + ": " + value}
}

// sealChain builds an N-instance ARC-sealed message over base and returns the raw.
func sealChain(t *testing.T, priv *rsa.PrivateKey, d, s string, n int) string {
	t.Helper()
	base := arcBaseMessage()
	msgHeaders, body := splitMessage([]byte(base))

	var prepend strings.Builder
	chain := []*arcSet{}
	for i := 1; i <= n; i++ {
		cv := "pass"
		if i == 1 {
			cv = "none"
		}
		aar := mkHeader("ARC-Authentication-Results", fmt.Sprintf("i=%d; example.test; spf=pass", i))
		ams := mkHeader("ARC-Message-Signature", signAMS(t, priv, d, s, i, msgHeaders, body))
		set := &arcSet{aar: aar, ams: ams}
		chain = append(chain, set)
		set.as = mkHeader("ARC-Seal", signAS(t, priv, d, s, i, cv, chain))

		// ARC sets are prepended in reverse (highest instance on top).
		block := set.as.raw + "\r\n" + set.ams.raw + "\r\n" + set.aar.raw + "\r\n"
		prepend.WriteString(block)
	}
	return prepend.String() + base
}

// --- tests ---

func TestVerifyARC_SingleSetRoundTrip(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 1024)
	resolver := testKeyResolver(t, publicPEM(t, priv), "arc", "example.test")
	raw := sealChain(t, priv, "example.test", "arc", 1)

	cv, reason := VerifyARC(context.Background(), []byte(raw), resolver)
	if cv != "pass" {
		t.Fatalf("want pass, got %s (%s)", cv, reason)
	}
}

func TestVerifyARC_TwoSetChain(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 1024)
	resolver := testKeyResolver(t, publicPEM(t, priv), "arc", "example.test")
	raw := sealChain(t, priv, "example.test", "arc", 2)

	cv, reason := VerifyARC(context.Background(), []byte(raw), resolver)
	if cv != "pass" {
		t.Fatalf("want pass for 2-set chain, got %s (%s)", cv, reason)
	}
}

func TestVerifyARC_TamperedBodyFailsAMS(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 1024)
	resolver := testKeyResolver(t, publicPEM(t, priv), "arc", "example.test")
	raw := sealChain(t, priv, "example.test", "arc", 1)
	tampered := strings.Replace(raw, "ARC body.", "TAMPERED.", 1)

	cv, reason := VerifyARC(context.Background(), []byte(tampered), resolver)
	if cv != "fail" {
		t.Fatalf("want fail on body tamper, got %s (%s)", cv, reason)
	}
	if !strings.Contains(reason, "ARC-Message-Signature") {
		t.Errorf("expected AMS failure, got: %s", reason)
	}
}

func TestVerifyARC_TamperedSealFails(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 1024)
	resolver := testKeyResolver(t, publicPEM(t, priv), "arc", "example.test")
	raw := sealChain(t, priv, "example.test", "arc", 1)
	// Tamper the signed ARC-Authentication-Results (covered by the seal, not AMS).
	tampered := strings.Replace(raw, "spf=pass", "spf=fail", 1)

	cv, reason := VerifyARC(context.Background(), []byte(tampered), resolver)
	if cv != "fail" {
		t.Fatalf("want fail on AAR tamper, got %s (%s)", cv, reason)
	}
	if !strings.Contains(reason, "ARC-Seal") {
		t.Errorf("expected ARC-Seal failure, got: %s", reason)
	}
}

func TestVerifyARC_NoChain(t *testing.T) {
	cv, _ := VerifyARC(context.Background(), []byte("From: a@b.test\r\nSubject: x\r\n\r\nbody\r\n"),
		func(context.Context, string) ([]string, error) { return nil, nil })
	if cv != "none" {
		t.Errorf("want none, got %s", cv)
	}
}

func TestVerifyARC_WrongKeyFails(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 1024)
	other, _ := rsa.GenerateKey(rand.Reader, 1024)
	raw := sealChain(t, priv, "example.test", "arc", 1)
	cv, _ := VerifyARC(context.Background(), []byte(raw), testKeyResolver(t, publicPEM(t, other), "arc", "example.test"))
	if cv != "fail" {
		t.Errorf("want fail with wrong key, got %s", cv)
	}
}
