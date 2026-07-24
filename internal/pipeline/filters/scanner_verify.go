package filters

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
)

// ScannerSignatureHeader is the HTTP response header an external content scanner
// (rspamd / ClamAV / equivalent) sets to the hex-encoded HMAC-SHA256 of the raw
// verdict body, keyed by the shared SCANNER_HMAC_SECRET. It authenticates a
// verdict end-to-end even though it travels over plain HTTP (OSI-15): a
// man-in-the-middle that rewrites a "reject"/"infected" verdict into a "clean"
// one, or an attacker who stands up a rogue scanner, cannot forge the signature
// without the secret.
const ScannerSignatureHeader = "X-Scanner-Signature"

// verifyScannerSignature authenticates a scanner verdict body against the
// signature the scanner returned in its response headers.
//
//   - secret == "": verdict-signature verification is DISABLED (no shared secret
//     configured) and it returns nil. This is the default posture because no
//     external scanner is wired into the default pipeline; the fail-closed
//     fallback in adapterFilter still governs an unreachable or errored scanner.
//   - secret != "": a missing or non-matching signature is an error. The adapter
//     returns that error, so adapterFilter applies its fail-CLOSED fallback
//     (defer) instead of trusting an unauthenticated "clean" verdict.
//
// The comparison is constant-time to avoid leaking the expected signature.
func verifyScannerSignature(secret string, header http.Header, body []byte) error {
	if secret == "" {
		return nil
	}
	sig := header.Get(ScannerSignatureHeader)
	if sig == "" {
		return fmt.Errorf("missing %s verdict signature (fail-closed)", ScannerSignatureHeader)
	}
	want := hex.EncodeToString(scannerHMAC(secret, body))
	if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) != 1 {
		return fmt.Errorf("invalid %s verdict signature (fail-closed)", ScannerSignatureHeader)
	}
	return nil
}

// scannerHMAC computes the HMAC-SHA256 of body under secret — the raw bytes the
// hex signature encodes. Exported-shaped as a helper so tests can synthesize a
// correctly-signed verdict without duplicating the construction.
func scannerHMAC(secret string, body []byte) []byte {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return m.Sum(nil)
}
