package outbound

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	dkim "github.com/rest-mail/go-dkim"
)

// MinDKIMRSABits is the smallest RSA key a domain may sign with. go-dkim signs
// with 1024 bits, the floor RFC 8301 §3.2 sets, but the same section says
// signers SHOULD use 2048, and go-dkim's GenerateKey makes nothing smaller.
const MinDKIMRSABits = 2048

// DKIMKeyError says why a DKIM key or selector is refused. Field names the
// request field at fault, "selector" or "private_key", and Reason says what is
// wrong with it.
type DKIMKeyError struct {
	Field  string
	Reason string
}

func (e *DKIMKeyError) Error() string { return e.Field + " " + e.Reason }

// ParseDKIMKey parses a PEM-encoded DKIM signing key: an RSA key, as go-dkim's
// ParsePrivateKey reads it (PKCS#1 or PKCS#8), or an Ed25519 key in PKCS#8
// form, which go-dkim signs with but has no parser for. The signer and the
// check at save time both read keys here, so they accept the same ones.
func ParseDKIMKey(keyPEM string) (crypto.Signer, error) {
	if key, err := dkim.ParsePrivateKey(keyPEM); err == nil {
		return key, nil
	}
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return nil, errors.New("is not a PEM-encoded private key")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if ed, ok := key.(ed25519.PrivateKey); ok {
			return ed, nil
		}
	}
	return nil, errors.New("is not an RSA or Ed25519 private key")
}

// CheckDKIMKey reports whether domain can sign its mail with keyPEM under
// selector, so a key is refused when it is stored rather than when mail is
// sent, where SignDKIM fails closed and defers the domain's mail (#391). The
// selector must be a DNS name, the key an RSA key of at least MinDKIMRSABits
// or an Ed25519 key, and go-dkim must sign a test message with them, a
// signature that verifies against the key's own DNS record. The test
// signature is what keeps this check from drifting from what signing accepts.
//
// A refusal is a *DKIMKeyError.
func CheckDKIMKey(domain, selector, keyPEM string) error {
	if !validSelector(selector) {
		return &DKIMKeyError{Field: "selector", Reason: fmt.Sprintf(
			"%q is not a DNS label: use up to 63 letters, digits and hyphens, not starting or ending with a hyphen, with dots between labels", selector)}
	}
	key, err := ParseDKIMKey(keyPEM)
	if err != nil {
		return &DKIMKeyError{Field: "private_key", Reason: err.Error()}
	}
	if rk, ok := key.(*rsa.PrivateKey); ok && rk.N.BitLen() < MinDKIMRSABits {
		return &DKIMKeyError{Field: "private_key", Reason: fmt.Sprintf(
			"is a %d-bit RSA key: a DKIM key must have at least %d bits", rk.N.BitLen(), MinDKIMRSABits)}
	}
	if err := testSignature(domain, selector, key); err != nil {
		return &DKIMKeyError{Field: "private_key", Reason: err.Error()}
	}
	return nil
}

// testSignature signs a message from domain the way SignDKIM does, and checks
// the signature against the record the key's public half publishes.
func testSignature(domain, selector string, key crypto.Signer) error {
	msg := "From: postmaster@" + domain + "\r\n" +
		"To: postmaster@" + domain + "\r\n" +
		"Subject: DKIM key check\r\n" +
		"Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n" +
		"Message-ID: <dkim-key-check@" + domain + ">\r\n" +
		"\r\n" +
		"This message is never sent.\r\n"
	val, err := dkim.Sign([]byte(msg), dkim.SignOptions{
		Domain:     domain,
		Selector:   selector,
		PrivateKey: key,
		Time:       time.Now().Unix(),
	})
	if err != nil {
		return fmt.Errorf("does not sign for %s: %w", domain, err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil {
		return fmt.Errorf("has a public half that cannot be encoded: %w", err)
	}
	record, err := dkim.RecordValue(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})))
	if err != nil {
		return fmt.Errorf("has no DKIM record: %w", err)
	}
	name := dkim.RecordName(selector, domain)
	resolver := func(_ context.Context, n string) ([]string, error) {
		if strings.EqualFold(n, name) {
			return []string{record}, nil
		}
		return nil, fmt.Errorf("no TXT record for %s", n)
	}
	results := dkim.Verify(context.Background(), []byte("DKIM-Signature: "+val+"\r\n"+msg), resolver)
	if len(results) != 1 {
		return fmt.Errorf("signs, but the message carries %d signatures, not one", len(results))
	}
	if results[0].Result != dkim.ResultPass {
		return fmt.Errorf("signs, but the signature does not verify against the key's own DNS record: %s", results[0].Reason)
	}
	return nil
}

// validSelector reports whether s is a selector RFC 6376 §3.1 allows: DNS
// labels of letters, digits and hyphens, none starting or ending with a
// hyphen, joined by dots. It is at most 63 characters, the size of
// domains.dkim_selector. go-dkim's own check is looser (it lets an underscore
// through); the test signature covers whatever it refuses.
func validSelector(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
