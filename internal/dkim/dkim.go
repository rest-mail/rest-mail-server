// Package dkim generates DKIM signing keys and renders their DNS TXT records.
//
// The product can store a domain's DKIM private key (via PUT /api/v1/admin/dkim)
// and sign with it in the pipeline, but it has no way to *generate* a keypair or
// render the public key as the DNS record receivers verify against. This package
// fills that gap so an instance can be given a working DKIM setup: generate a
// keypair, install the private key via the admin API, and publish the record.
package dkim

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"strings"
)

// DefaultSelector is the selector the API's DNS health check looks up
// (default._domainkey.<domain>). Keep in sync with internal/api/handlers.
const DefaultSelector = "default"

// GenerateKey creates an RSA DKIM keypair and returns both halves PEM-encoded.
// The private PEM is what you install via the admin API; the public PEM feeds
// RecordValue.
func GenerateKey(bits int) (privatePEM, publicPEM string, err error) {
	key, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		return "", "", err
	}
	privatePEM = string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", "", err
	}
	publicPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
	return privatePEM, publicPEM, nil
}

// RecordName returns the DKIM record name <selector>._domainkey.<domain>.
func RecordName(selector, domain string) string {
	if selector == "" {
		selector = DefaultSelector
	}
	return selector + "._domainkey." + domain
}

// RecordValue renders the DKIM DNS TXT value (v=DKIM1; k=rsa; p=<base64 DER>)
// from a PEM-encoded RSA public key (as produced by GenerateKey).
func RecordValue(publicKeyPEM string) (string, error) {
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		return "", fmt.Errorf("invalid public key PEM")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parse public key: %w", err)
	}
	if _, ok := pub.(*rsa.PublicKey); !ok {
		return "", fmt.Errorf("public key is not RSA")
	}
	// block.Bytes is the DER SubjectPublicKeyInfo — exactly what p= carries.
	return "v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(block.Bytes), nil
}

// RecordFragment renders a dnsmasq txt-record line for a DKIM record, splitting
// the value into <=255-char strings (the DNS TXT per-string limit) so that
// 2048-bit records — whose p= value exceeds 255 chars — stay valid:
//
//	txt-record=default._domainkey.d,"chunk1","chunk2"
func RecordFragment(name, value string) string {
	const maxLen = 255
	var chunks []string
	for i := 0; i < len(value); i += maxLen {
		end := i + maxLen
		if end > len(value) {
			end = len(value)
		}
		chunks = append(chunks, `"`+value[i:end]+`"`)
	}
	if len(chunks) == 0 {
		chunks = []string{`""`}
	}
	return "txt-record=" + name + "," + strings.Join(chunks, ",")
}
