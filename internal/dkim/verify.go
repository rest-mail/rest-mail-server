package dkim

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
)

// Verify performs RFC 6376 DKIM verification against a raw RFC 5322 message.
//
// Verification MUST run over the exact bytes that were signed — the header and
// body as transmitted — so this operates on the raw message, never on a parsed
// or reconstructed representation (reconstructing headers/body from structured
// fields would not reproduce the signer's canonicalization for anything but the
// simplest messages).
//
// It returns one VerifyResult per DKIM-Signature header found, in header order.
// An empty slice means the message carried no DKIM-Signature.
func Verify(ctx context.Context, rawMessage []byte, resolver TXTResolver) []VerifyResult {
	if resolver == nil {
		resolver = net.DefaultResolver.LookupTXT
	}
	headers, body := splitMessage(rawMessage)

	var results []VerifyResult
	for _, h := range headers {
		if !strings.EqualFold(h.name, "DKIM-Signature") {
			continue
		}
		results = append(results, verifyOne(ctx, h, headers, body, resolver))
	}
	return results
}

// TXTResolver looks up DNS TXT records for a name. It matches the signature of
// net.Resolver.LookupTXT so the default resolver can be used directly, and a
// stub can be injected in tests.
type TXTResolver func(ctx context.Context, name string) ([]string, error)

// Verification result strings, mirroring RFC 8601 dkim= values.
const (
	ResultPass      = "pass"
	ResultFail      = "fail"
	ResultNeutral   = "neutral"
	ResultNone      = "none"
	ResultTempError = "temperror"
	ResultPermError = "permerror"
)

// VerifyResult is the outcome of verifying a single DKIM-Signature.
type VerifyResult struct {
	Domain   string // d= signing domain
	Selector string // s= selector
	Result   string // one of the Result* constants
	Reason   string // human-readable detail
}

// header is one parsed header field: its name, and its raw value (everything
// after the colon, folding CRLFs preserved, trailing CRLF stripped).
type header struct {
	name  string
	value string
	// raw is the full field as it appeared (name, colon, value, folds), with no
	// trailing CRLF — used by simple header canonicalization.
	raw string
}

func verifyOne(ctx context.Context, sig header, allHeaders []header, body string, resolver TXTResolver) VerifyResult {
	tags := parseTagList(sig.value)

	res := VerifyResult{Domain: tags["d"], Selector: tags["s"]}
	permfail := func(reason string) VerifyResult { res.Result = ResultPermError; res.Reason = reason; return res }

	if tags["v"] != "" && tags["v"] != "1" {
		return permfail("unsupported DKIM version " + tags["v"])
	}
	for _, req := range []string{"a", "b", "bh", "d", "s", "h"} {
		if tags[req] == "" {
			return permfail("missing required tag " + req)
		}
	}

	// Algorithm → hash.
	var hashType crypto.Hash
	switch strings.ToLower(tags["a"]) {
	case "rsa-sha256":
		hashType = crypto.SHA256
	case "rsa-sha1":
		hashType = crypto.SHA1
	default:
		return permfail("unsupported algorithm " + tags["a"])
	}

	// Canonicalization: c=header/body, default simple/simple.
	headerCanon, bodyCanon := "simple", "simple"
	if c := tags["c"]; c != "" {
		parts := strings.SplitN(c, "/", 2)
		headerCanon = parts[0]
		if len(parts) == 2 && parts[1] != "" {
			bodyCanon = parts[1]
		} else {
			bodyCanon = "simple" // "c=relaxed" means relaxed header, simple body
		}
	}
	if headerCanon != "simple" && headerCanon != "relaxed" {
		return permfail("unsupported header canonicalization " + headerCanon)
	}
	if bodyCanon != "simple" && bodyCanon != "relaxed" {
		return permfail("unsupported body canonicalization " + bodyCanon)
	}

	// ── Body hash ────────────────────────────────────────────────────
	canonBody := canonicalizeBody(body, bodyCanon)
	if l := tags["l"]; l != "" {
		n, err := parseUint(l)
		if err != nil {
			return permfail("invalid l= tag")
		}
		if n < len(canonBody) {
			canonBody = canonBody[:n]
		}
	}
	computedBH := hashBytes(hashType, []byte(canonBody))
	expectedBH, err := base64.StdEncoding.DecodeString(stripWSP(tags["bh"]))
	if err != nil {
		return permfail("invalid bh= base64")
	}
	if !bytesEqual(computedBH, expectedBH) {
		res.Result = ResultFail
		res.Reason = "body hash mismatch"
		return res
	}

	// ── Header hash / signature ──────────────────────────────────────
	signedData := buildSignedHeaders(tags["h"], allHeaders, sig, headerCanon)

	sigBytes, err := base64.StdEncoding.DecodeString(stripWSP(tags["b"]))
	if err != nil {
		return permfail("invalid b= base64")
	}

	// ── Public key via DNS ───────────────────────────────────────────
	pub, kres := fetchKey(ctx, tags["s"], tags["d"], resolver)
	if kres != "" {
		res.Result = kres
		res.Reason = "key lookup: " + res.Reason
		if kres == ResultTempError {
			res.Reason = "temporary DNS failure for " + RecordName(tags["s"], tags["d"])
		} else {
			res.Reason = "no valid key at " + RecordName(tags["s"], tags["d"])
		}
		return res
	}

	hashed := hashBytes(hashType, []byte(signedData))
	if err := rsa.VerifyPKCS1v15(pub, hashType, hashed, sigBytes); err != nil {
		res.Result = ResultFail
		res.Reason = "signature verification failed"
		return res
	}

	res.Result = ResultPass
	res.Reason = fmt.Sprintf("signature ok (d=%s s=%s)", tags["d"], tags["s"])
	return res
}

// fetchKey resolves and parses the signer's RSA public key. On success it
// returns (key, ""); otherwise (nil, temperror|permerror).
func fetchKey(ctx context.Context, selector, domain string, resolver TXTResolver) (*rsa.PublicKey, string) {
	name := RecordName(selector, domain)
	records, err := resolver(ctx, name)
	if err != nil {
		var dnsErr *net.DNSError
		if ok := asDNSError(err, &dnsErr); ok && dnsErr.IsNotFound {
			return nil, ResultPermError
		}
		return nil, ResultTempError
	}
	for _, rec := range records {
		kt := parseTagList(rec)
		if kt["p"] == "" {
			continue // revoked or malformed
		}
		if k := kt["k"]; k != "" && !strings.EqualFold(k, "rsa") {
			continue
		}
		der, derr := base64.StdEncoding.DecodeString(stripWSP(kt["p"]))
		if derr != nil {
			continue
		}
		pub, perr := x509.ParsePKIXPublicKey(der)
		if perr != nil {
			continue
		}
		if rsaKey, ok := pub.(*rsa.PublicKey); ok {
			return rsaKey, ""
		}
	}
	return nil, ResultPermError
}

// buildSignedHeaders assembles the canonicalized header block that the b= tag
// signs: each header named in h= (matched bottom-up), followed by the
// DKIM-Signature header itself with its b= value emptied and NO trailing CRLF.
func buildSignedHeaders(hTag string, allHeaders []header, sig header, canon string) string {
	// Track, per lowercased name, how many instances we've already consumed so
	// repeated names match from the bottom of the header block upward (RFC 6376
	// §5.4.2).
	consumed := map[string]int{}
	var b strings.Builder
	for _, name := range strings.Split(hTag, ":") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		lname := strings.ToLower(name)
		h := nthFromBottom(allHeaders, lname, consumed[lname])
		consumed[lname]++
		if h == nil {
			continue // absent header contributes nothing
		}
		b.WriteString(canonicalizeHeader(*h, canon))
		b.WriteString("\r\n")
	}
	// The DKIM-Signature being verified, b= emptied, no trailing CRLF.
	stripped := sig
	stripped.value = removeBValue(sig.value)
	stripped.raw = removeBValue(sig.raw)
	b.WriteString(canonicalizeHeader(stripped, canon))
	return b.String()
}

// nthFromBottom returns the nth (0-based) instance of the named header counting
// from the bottom of the header block, or nil if there are fewer than n+1.
func nthFromBottom(headers []header, lname string, n int) *header {
	count := 0
	for i := len(headers) - 1; i >= 0; i-- {
		if strings.ToLower(headers[i].name) == lname {
			if count == n {
				return &headers[i]
			}
			count++
		}
	}
	return nil
}
