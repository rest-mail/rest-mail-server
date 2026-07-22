package dkim

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net"
	"strings"
	"testing"
	"time"
)

// ── Canonicalization anchored to RFC 6376 §3.4.5 worked example ──────────
//
// These assertions pin the canonicalizers to the outputs the RFC itself
// publishes, independently of any crypto — so a body/header canon bug can't
// hide behind a self-consistent sign/verify round-trip.

func TestRelaxedHeaderCanon_RFCExample(t *testing.T) {
	// "A: X" and the folded "B : Y<TAB>CRLF<TAB>Z  " from §3.4.5.
	a := header{name: "A", value: " X", raw: "A: X"}
	if got := canonicalizeHeader(a, "relaxed"); got != "a:X" {
		t.Errorf("relaxed A: got %q want %q", got, "a:X")
	}
	b := header{name: "B", value: " Y\t\r\n\tZ  ", raw: "B : Y\t\r\n\tZ  "}
	if got := canonicalizeHeader(b, "relaxed"); got != "b:Y Z" {
		t.Errorf("relaxed B: got %q want %q", got, "b:Y Z")
	}
}

func TestSimpleHeaderCanon_Unchanged(t *testing.T) {
	b := header{name: "B", value: " Y\t\r\n\tZ  ", raw: "B : Y\t\r\n\tZ  "}
	if got := canonicalizeHeader(b, "simple"); got != "B : Y\t\r\n\tZ  " {
		t.Errorf("simple header should be byte-identical, got %q", got)
	}
}

func TestBodyCanon_RFCExample(t *testing.T) {
	body := " C \r\nD \t E\r\n\r\n\r\n"
	if got := canonicalizeBody(body, "relaxed"); got != " C\r\nD E\r\n" {
		t.Errorf("relaxed body: got %q want %q", got, " C\r\nD E\r\n")
	}
	if got := canonicalizeBody(body, "simple"); got != " C \r\nD \t E\r\n" {
		t.Errorf("simple body: got %q want %q", got, " C \r\nD \t E\r\n")
	}
}

func TestBodyCanon_Empty(t *testing.T) {
	if got := canonicalizeBody("", "simple"); got != "\r\n" {
		t.Errorf("simple empty body should be CRLF, got %q", got)
	}
	if got := canonicalizeBody("", "relaxed"); got != "" {
		t.Errorf("relaxed empty body should be empty, got %q", got)
	}
}

// ── removeBValue ─────────────────────────────────────────────────────────

func TestRemoveBValue(t *testing.T) {
	in := "v=1; a=rsa-sha256; bh=ABC; h=from:to; b=SIGDATA=="
	want := "v=1; a=rsa-sha256; bh=ABC; h=from:to; b="
	if got := removeBValue(in); got != want {
		t.Errorf("removeBValue: got %q want %q", got, want)
	}
	// b= not last, and bh= must be untouched.
	in2 := "v=1; b=SIG; bh=HASH; s=sel"
	want2 := "v=1; b=; bh=HASH; s=sel"
	if got := removeBValue(in2); got != want2 {
		t.Errorf("removeBValue mid: got %q want %q", got, want2)
	}
}

// ── Sign/verify round-trip ──────────────────────────────────────────────

// signTestMessage is an independent reference signer used only by tests: it
// assembles a DKIM-Signature over the given header fields + body and returns a
// full raw message with the signature prepended.
func signTestMessage(t *testing.T, priv *rsa.PrivateKey, d, s, hcanon, bcanon string, fields []string, body string) []byte {
	t.Helper()

	bodyHash := sha256.Sum256([]byte(canonicalizeBody(body, bcanon)))
	bh := base64.StdEncoding.EncodeToString(bodyHash[:])

	// h= = field names in order.
	var names []string
	hdrs := make([]header, 0, len(fields))
	for _, f := range fields {
		eq := strings.IndexByte(f, ':')
		hdrs = append(hdrs, header{name: strings.TrimSpace(f[:eq]), value: f[eq+1:], raw: f})
		names = append(names, strings.ToLower(strings.TrimSpace(f[:eq])))
	}
	hlist := strings.Join(names, ":")

	sigVal := " v=1; a=rsa-sha256; c=" + hcanon + "/" + bcanon +
		"; d=" + d + "; s=" + s + "; h=" + hlist + "; bh=" + bh + "; b="

	// Build the data to sign: each named header, then the sig header (b= empty).
	var sb strings.Builder
	for _, h := range hdrs {
		sb.WriteString(canonicalizeHeader(h, hcanon))
		sb.WriteString("\r\n")
	}
	sigHdr := header{name: "DKIM-Signature", value: sigVal, raw: "DKIM-Signature:" + sigVal}
	sb.WriteString(canonicalizeHeader(sigHdr, hcanon))

	hashed := sha256.Sum256([]byte(sb.String()))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, hashed[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	b64 := base64.StdEncoding.EncodeToString(sig)

	var msg strings.Builder
	msg.WriteString("DKIM-Signature:" + sigVal + b64 + "\r\n")
	for _, f := range fields {
		msg.WriteString(f + "\r\n")
	}
	msg.WriteString("\r\n")
	msg.WriteString(body)
	return []byte(msg.String())
}

func testKeyResolver(t *testing.T, pubPEM, selector, domain string) TXTResolver {
	t.Helper()
	txt, err := RecordValue(pubPEM)
	if err != nil {
		t.Fatalf("RecordValue: %v", err)
	}
	want := RecordName(selector, domain)
	return func(_ context.Context, name string) ([]string, error) {
		if name == want {
			return []string{txt}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
}

func fields() []string {
	return []string{
		"From: Alice <alice@example.test>",
		"To: Bob <bob@rcpt.test>",
		"Subject: DKIM round trip",
		"Date: " + time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC).Format(time.RFC1123Z),
		"Message-ID: <rt-1@example.test>",
	}
}

func TestVerify_RoundTrip(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM := publicPEM(t, priv)

	for _, canon := range []struct{ h, b string }{
		{"relaxed", "relaxed"},
		{"simple", "simple"},
		{"relaxed", "simple"},
		{"simple", "relaxed"},
	} {
		t.Run(canon.h+"/"+canon.b, func(t *testing.T) {
			raw := signTestMessage(t, priv, "example.test", "sel", canon.h, canon.b, fields(), "Hello DKIM world.\r\nSecond line.\r\n")
			resolver := testKeyResolver(t, pubPEM, "sel", "example.test")

			results := Verify(context.Background(), raw, resolver)
			if len(results) != 1 {
				t.Fatalf("want 1 result, got %d", len(results))
			}
			if results[0].Result != ResultPass {
				t.Errorf("want pass, got %s (%s)", results[0].Result, results[0].Reason)
			}
			if results[0].Domain != "example.test" || results[0].Selector != "sel" {
				t.Errorf("d/s mismatch: %+v", results[0])
			}
		})
	}
}

func TestVerify_BodyTamperFails(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 1024)
	pubPEM := publicPEM(t, priv)
	raw := signTestMessage(t, priv, "example.test", "sel", "relaxed", "relaxed", fields(), "Original body.\r\n")
	// Flip the body after signing.
	tampered := strings.Replace(string(raw), "Original body.", "Tampered body!", 1)

	results := Verify(context.Background(), []byte(tampered), testKeyResolver(t, pubPEM, "sel", "example.test"))
	if len(results) != 1 || results[0].Result != ResultFail {
		t.Fatalf("want fail on body tamper, got %+v", results)
	}
	if results[0].Reason != "body hash mismatch" {
		t.Errorf("want body hash mismatch, got %q", results[0].Reason)
	}
}

func TestVerify_HeaderTamperFails(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 1024)
	pubPEM := publicPEM(t, priv)
	raw := signTestMessage(t, priv, "example.test", "sel", "relaxed", "relaxed", fields(), "Body.\r\n")
	// Change a signed header (Subject) after signing → body hash still ok, sig fails.
	tampered := strings.Replace(string(raw), "Subject: DKIM round trip", "Subject: Evil replacement", 1)

	results := Verify(context.Background(), []byte(tampered), testKeyResolver(t, pubPEM, "sel", "example.test"))
	if len(results) != 1 || results[0].Result != ResultFail {
		t.Fatalf("want fail on header tamper, got %+v", results)
	}
	if results[0].Reason != "signature verification failed" {
		t.Errorf("want signature verification failed, got %q", results[0].Reason)
	}
}

func TestVerify_NoSignature(t *testing.T) {
	raw := "From: a@example.test\r\nSubject: plain\r\n\r\nno dkim here\r\n"
	results := Verify(context.Background(), []byte(raw), func(_ context.Context, _ string) ([]string, error) {
		t.Fatal("resolver should not be called with no signature")
		return nil, nil
	})
	if len(results) != 0 {
		t.Errorf("want no results, got %+v", results)
	}
}

func TestVerify_NoKeyPermError(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 1024)
	raw := signTestMessage(t, priv, "example.test", "sel", "relaxed", "relaxed", fields(), "Body.\r\n")
	// Resolver reports NXDOMAIN for everything.
	nx := func(_ context.Context, name string) ([]string, error) {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	results := Verify(context.Background(), raw, nx)
	if len(results) != 1 || results[0].Result != ResultPermError {
		t.Fatalf("want permerror on missing key, got %+v", results)
	}
}

func TestVerify_DNSTempError(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 1024)
	raw := signTestMessage(t, priv, "example.test", "sel", "relaxed", "relaxed", fields(), "Body.\r\n")
	temp := func(_ context.Context, name string) ([]string, error) {
		return nil, &net.DNSError{Err: "server misbehaving", Name: name, IsTemporary: true}
	}
	results := Verify(context.Background(), raw, temp)
	if len(results) != 1 || results[0].Result != ResultTempError {
		t.Fatalf("want temperror on DNS failure, got %+v", results)
	}
}

func TestVerify_RevokedKeyPermError(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 1024)
	raw := signTestMessage(t, priv, "example.test", "sel", "relaxed", "relaxed", fields(), "Body.\r\n")
	revoked := func(_ context.Context, _ string) ([]string, error) {
		return []string{"v=DKIM1; k=rsa; p="}, nil // empty p = revoked
	}
	results := Verify(context.Background(), raw, revoked)
	if len(results) != 1 || results[0].Result != ResultPermError {
		t.Fatalf("want permerror on revoked key, got %+v", results)
	}
}

func TestVerify_WrongKeyFails(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 1024)
	other, _ := rsa.GenerateKey(rand.Reader, 1024)
	raw := signTestMessage(t, priv, "example.test", "sel", "relaxed", "relaxed", fields(), "Body.\r\n")
	// Publish the WRONG public key.
	results := Verify(context.Background(), raw, testKeyResolver(t, publicPEM(t, other), "sel", "example.test"))
	if len(results) != 1 || results[0].Result != ResultFail {
		t.Fatalf("want fail with wrong key, got %+v", results)
	}
}

// publicPEM renders a private key's public half exactly as GenerateKey does
// (PKIX SubjectPublicKeyInfo), which is what RecordValue consumes.
func publicPEM(t *testing.T, priv *rsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}
