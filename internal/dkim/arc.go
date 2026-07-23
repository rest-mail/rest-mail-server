package dkim

import (
	"context"
	"crypto"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

// arcSet is one ARC header set at a given instance: ARC-Authentication-Results,
// ARC-Message-Signature, ARC-Seal.
type arcSet struct {
	aar *header
	ams *header
	as  *header
}

// VerifyARC cryptographically verifies the ARC chain (RFC 8617 §5.2) in a raw
// message: the most recent ARC-Message-Signature must verify over the message
// (a DKIM-style signature), and every ARC-Seal must verify over the ARC header
// chain up to its instance. It returns a chain-validation status — "pass",
// "fail", or "none" — plus a human-readable reason.
//
// Header/body canonicalization is shared with Verify (canon.go / verifyOne), so
// ARC verification is consistent with DKIM verification and with any RFC 8617
// verifier operating on the same bytes.
func VerifyARC(ctx context.Context, rawMessage []byte, resolver TXTResolver) (string, string) {
	if resolver == nil {
		resolver = net.DefaultResolver.LookupTXT
	}
	headers, body := splitMessage(rawMessage)

	sets := map[int]*arcSet{}
	get := func(i int) *arcSet {
		if sets[i] == nil {
			sets[i] = &arcSet{}
		}
		return sets[i]
	}
	for idx := range headers {
		h := &headers[idx]
		i := arcInstance(h.value)
		if i < 1 {
			continue
		}
		switch strings.ToLower(h.name) {
		case "arc-authentication-results":
			get(i).aar = h
		case "arc-message-signature":
			get(i).ams = h
		case "arc-seal":
			get(i).as = h
		}
	}
	if len(sets) == 0 {
		return "none", "no ARC sets present"
	}

	instances := make([]int, 0, len(sets))
	for i := range sets {
		instances = append(instances, i)
	}
	sort.Ints(instances)
	n := len(instances)

	// Chain must be contiguous 1..N with every set complete.
	for pos, i := range instances {
		if i != pos+1 {
			return "fail", fmt.Sprintf("non-contiguous chain (expected i=%d, found i=%d)", pos+1, i)
		}
		if s := sets[i]; s.aar == nil || s.ams == nil || s.as == nil {
			return "fail", fmt.Sprintf("i=%d incomplete ARC set", i)
		}
	}

	// 1. The most recent ARC-Message-Signature must verify over the message.
	amsRes := verifyOne(ctx, *sets[n].ams, headers, body, resolver)
	if amsRes.Result != ResultPass {
		return "fail", fmt.Sprintf("ARC-Message-Signature (i=%d) %s: %s", n, amsRes.Result, amsRes.Reason)
	}

	// 2. Every ARC-Seal must verify over the ARC header chain up to its instance.
	ordered := make([]*arcSet, 0, n)
	for _, i := range instances {
		ordered = append(ordered, sets[i])
	}
	for pos, i := range instances {
		if res, reason := verifyARCSeal(ctx, ordered[:pos+1], resolver); res != ResultPass {
			return "fail", fmt.Sprintf("ARC-Seal (i=%d) %s: %s", i, res, reason)
		}
	}

	return "pass", fmt.Sprintf("ARC chain cryptographically verified (%d set(s))", n)
}

// verifyARCSeal verifies the ARC-Seal of the LAST set in chain (ascending
// instance order) over the relaxed-canonicalized ARC header chain: for each set,
// ARC-Authentication-Results, ARC-Message-Signature, ARC-Seal — with the final
// seal's b= emptied and no trailing CRLF (RFC 8617 §5.1.1).
func verifyARCSeal(ctx context.Context, chain []*arcSet, resolver TXTResolver) (string, string) {
	seal := chain[len(chain)-1].as
	tags := parseTagList(seal.value)
	if !strings.EqualFold(tags["a"], "rsa-sha256") {
		return ResultPermError, "unsupported ARC-Seal algorithm " + tags["a"]
	}
	base := arcSealBase(chain)

	sigBytes, err := base64.StdEncoding.DecodeString(stripWSP(tags["b"]))
	if err != nil {
		return ResultPermError, "invalid ARC-Seal b= base64"
	}
	pub, kres := fetchKey(ctx, tags["s"], tags["d"], resolver)
	if kres != "" {
		return kres, "ARC-Seal key lookup for " + RecordName(tags["s"], tags["d"])
	}
	hashed := hashBytes(crypto.SHA256, []byte(base))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, hashed, sigBytes); err != nil {
		return ResultFail, "ARC-Seal signature verification failed"
	}
	return ResultPass, "ok"
}

// arcSealBase builds the signing input for the ARC-Seal of the last set in the
// chain: relaxed-canonicalized AAR, AMS, AS for each set in ascending order,
// each followed by CRLF except the final seal (b= emptied, no trailing CRLF).
func arcSealBase(chain []*arcSet) string {
	var b strings.Builder
	last := len(chain) - 1
	for idx, s := range chain {
		b.WriteString(canonicalizeHeader(*s.aar, "relaxed"))
		b.WriteString("\r\n")
		b.WriteString(canonicalizeHeader(*s.ams, "relaxed"))
		b.WriteString("\r\n")
		if idx == last {
			stripped := *s.as
			stripped.value = removeBValue(s.as.value)
			stripped.raw = removeBValue(s.as.raw)
			b.WriteString(canonicalizeHeader(stripped, "relaxed")) // no trailing CRLF
		} else {
			b.WriteString(canonicalizeHeader(*s.as, "relaxed"))
			b.WriteString("\r\n")
		}
	}
	return b.String()
}

// arcInstance extracts the i= instance number from an ARC header value.
func arcInstance(value string) int {
	if v := parseTagList(value)["i"]; v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return 0
}
