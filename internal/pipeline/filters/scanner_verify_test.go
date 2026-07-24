package filters

import (
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/restmail/restmail/internal/pipeline"
)

// scannerStub is a mock external content-scanner. It answers the /ping health
// probe with 200 and returns a caller-supplied verdict body + status on the scan
// path, optionally signing the body with signSecret (empty = send no signature).
type scannerStub struct {
	scanPath   string // "/checkv2" (rspamd) or "/scan" (clamav)
	status     int    // HTTP status for the scan response
	body       string // verdict body
	signSecret string // if non-empty, sign body with this secret
	pingDown   bool   // if true, /ping returns 503 (scanner unhealthy)
}

func (s scannerStub) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) {
		if s.pingDown {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(s.scanPath, func(w http.ResponseWriter, _ *http.Request) {
		if s.signSecret != "" {
			w.Header().Set(ScannerSignatureHeader, hex.EncodeToString(scannerHMAC(s.signSecret, []byte(s.body))))
		}
		status := s.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(s.body))
	})
	return httptest.NewServer(mux)
}

func runScanner(t *testing.T, factory pipeline.FilterFactory, url string) *pipeline.FilterResult {
	t.Helper()
	f, err := factory([]byte(`{"url":"` + url + `","timeout_ms":2000}`))
	if err != nil {
		t.Fatalf("build filter: %v", err)
	}
	res, err := f.Execute(context.Background(), unitEmail())
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return res
}

// ── verifyScannerSignature unit ─────────────────────────────────────────

func TestVerifyScannerSignature(t *testing.T) {
	const secret = "s3cr3t"
	body := []byte(`{"action":"no action"}`)
	good := hex.EncodeToString(scannerHMAC(secret, body))

	t.Run("no secret skips verification", func(t *testing.T) {
		if err := verifyScannerSignature("", http.Header{}, body); err != nil {
			t.Fatalf("empty secret should skip: %v", err)
		}
	})
	t.Run("valid signature passes", func(t *testing.T) {
		h := http.Header{ScannerSignatureHeader: {good}}
		if err := verifyScannerSignature(secret, h, body); err != nil {
			t.Fatalf("valid signature rejected: %v", err)
		}
	})
	t.Run("missing signature fails", func(t *testing.T) {
		if err := verifyScannerSignature(secret, http.Header{}, body); err == nil {
			t.Fatal("missing signature accepted")
		}
	})
	t.Run("tampered signature fails", func(t *testing.T) {
		h := http.Header{ScannerSignatureHeader: {good}}
		if err := verifyScannerSignature(secret, h, []byte(`{"action":"reject"}`)); err == nil {
			t.Fatal("tampered body accepted")
		}
	})
	t.Run("wrong secret fails", func(t *testing.T) {
		h := http.Header{ScannerSignatureHeader: {good}}
		if err := verifyScannerSignature("other", h, body); err == nil {
			t.Fatal("wrong secret accepted")
		}
	})
}

// ── rspamd end-to-end (HMAC + fail-closed) ──────────────────────────────

func TestRspamd_ValidSignedCleanVerdict_Passes(t *testing.T) {
	const secret = "scanner-key"
	stub := scannerStub{
		scanPath:   "/checkv2",
		body:       `{"action":"no action","score":0.0,"required_score":10.0,"symbols":{}}`,
		signSecret: secret,
	}
	srv := stub.server(t)
	defer srv.Close()

	res := runScanner(t, NewRspamdWithSecret(secret), srv.URL)
	if res.Action != pipeline.ActionContinue {
		t.Fatalf("valid signed clean verdict: action = %q, want continue", res.Action)
	}
}

func TestRspamd_TamperedSignature_FailsClosed(t *testing.T) {
	const secret = "scanner-key"
	// Body signed with a DIFFERENT secret — simulates a MITM/rogue scanner that
	// cannot forge the real signature.
	stub := scannerStub{
		scanPath:   "/checkv2",
		body:       `{"action":"no action","score":0.0,"required_score":10.0,"symbols":{}}`,
		signSecret: "attacker-key",
	}
	srv := stub.server(t)
	defer srv.Close()

	res := runScanner(t, NewRspamdWithSecret(secret), srv.URL)
	if res.Action == pipeline.ActionContinue {
		t.Fatal("tampered signature must NOT pass as clean")
	}
	if res.Action != pipeline.ActionDefer {
		t.Fatalf("tampered signature: action = %q, want defer (fail-closed)", res.Action)
	}
}

func TestRspamd_MissingSignature_FailsClosed(t *testing.T) {
	const secret = "scanner-key"
	stub := scannerStub{
		scanPath: "/checkv2",
		body:     `{"action":"no action","score":0.0}`,
		// signSecret empty: server returns no signature header.
	}
	srv := stub.server(t)
	defer srv.Close()

	res := runScanner(t, NewRspamdWithSecret(secret), srv.URL)
	if res.Action != pipeline.ActionDefer {
		t.Fatalf("missing signature: action = %q, want defer (fail-closed)", res.Action)
	}
}

func TestRspamd_TransportError_FailsClosed(t *testing.T) {
	// /ping OK but the scan endpoint 500s — a transport/scan error must defer,
	// never continue.
	stub := scannerStub{scanPath: "/checkv2", status: http.StatusInternalServerError, body: "boom"}
	srv := stub.server(t)
	defer srv.Close()

	res := runScanner(t, NewRspamdWithSecret(""), srv.URL)
	if res.Action != pipeline.ActionDefer {
		t.Fatalf("scan error: action = %q, want defer (fail-closed)", res.Action)
	}
}

func TestRspamd_ScannerUnhealthy_FailsClosed(t *testing.T) {
	stub := scannerStub{scanPath: "/checkv2", pingDown: true, body: `{"action":"no action"}`}
	srv := stub.server(t)
	defer srv.Close()

	res := runScanner(t, NewRspamdWithSecret(""), srv.URL)
	if res.Action != pipeline.ActionDefer {
		t.Fatalf("unhealthy scanner: action = %q, want defer (fail-closed)", res.Action)
	}
}

func TestRspamd_MissingVerdict_FailsClosed(t *testing.T) {
	// Empty/unknown action field: an ambiguous verdict must defer, not pass clean.
	stub := scannerStub{scanPath: "/checkv2", body: `{"score":0.0}`}
	srv := stub.server(t)
	defer srv.Close()

	res := runScanner(t, NewRspamdWithSecret(""), srv.URL)
	if res.Action == pipeline.ActionContinue {
		t.Fatal("missing verdict must NOT pass as clean")
	}
	if res.Action != pipeline.ActionDefer {
		t.Fatalf("missing verdict: action = %q, want defer (fail-closed)", res.Action)
	}
}

func TestRspamd_NoSecret_UnsignedCleanVerdict_Passes(t *testing.T) {
	// Documents backward compatibility: with no scanner secret configured, an
	// unsigned clean verdict is accepted (verification disabled), but the
	// fail-closed fallback above still governs unreachable/errored scanners.
	stub := scannerStub{scanPath: "/checkv2", body: `{"action":"no action","score":0.0}`}
	srv := stub.server(t)
	defer srv.Close()

	res := runScanner(t, NewRspamdWithSecret(""), srv.URL)
	if res.Action != pipeline.ActionContinue {
		t.Fatalf("no-secret unsigned clean verdict: action = %q, want continue", res.Action)
	}
}

// ── clamav end-to-end (HMAC + fail-closed) ──────────────────────────────

func TestClamAV_ValidSignedCleanVerdict_Passes(t *testing.T) {
	const secret = "av-key"
	stub := scannerStub{scanPath: "/scan", body: `{"status":"OK","description":""}`, signSecret: secret}
	srv := stub.server(t)
	defer srv.Close()

	res := runScanner(t, NewClamAVWithSecret(secret), srv.URL)
	if res.Action != pipeline.ActionContinue {
		t.Fatalf("valid signed clean verdict: action = %q, want continue", res.Action)
	}
}

func TestClamAV_TamperedSignature_FailsClosed(t *testing.T) {
	const secret = "av-key"
	stub := scannerStub{scanPath: "/scan", body: `{"status":"OK"}`, signSecret: "attacker-key"}
	srv := stub.server(t)
	defer srv.Close()

	res := runScanner(t, NewClamAVWithSecret(secret), srv.URL)
	if res.Action != pipeline.ActionDefer {
		t.Fatalf("tampered signature: action = %q, want defer (fail-closed)", res.Action)
	}
}

func TestClamAV_UnrecognizedStatus_FailsClosed(t *testing.T) {
	// Neither OK nor FOUND: an ambiguous verdict must defer, not pass clean.
	stub := scannerStub{scanPath: "/scan", body: `{"status":"WEIRD"}`}
	srv := stub.server(t)
	defer srv.Close()

	res := runScanner(t, NewClamAVWithSecret(""), srv.URL)
	if res.Action == pipeline.ActionContinue {
		t.Fatal("unrecognized status must NOT pass as clean")
	}
	if res.Action != pipeline.ActionDefer {
		t.Fatalf("unrecognized status: action = %q, want defer (fail-closed)", res.Action)
	}
}

func TestClamAV_Infected_Rejects(t *testing.T) {
	const secret = "av-key"
	stub := scannerStub{scanPath: "/scan", body: `{"status":"FOUND","description":"Eicar-Test-Signature"}`, signSecret: secret}
	srv := stub.server(t)
	defer srv.Close()

	res := runScanner(t, NewClamAVWithSecret(secret), srv.URL)
	if res.Action != pipeline.ActionReject {
		t.Fatalf("infected verdict: action = %q, want reject", res.Action)
	}
}
