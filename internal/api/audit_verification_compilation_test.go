package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
)

type compiledContractTransaction struct {
	Request struct {
		URI  string `json:"uri"`
		Body string `json:"body"`
	} `json:"request"`
	Response struct {
		Status string `json:"status"`
		Body   string `json:"body"`
	} `json:"response"`
}

func compileFullContract(t *testing.T, binary string, generator *oapifly.Generator) []compiledContractTransaction {
	t.Helper()
	description, err := json.Marshal(generator.Generate())
	if err != nil {
		t.Fatal(err)
	}
	descriptionPath := filepath.Join(t.TempDir(), "openapi.json")
	if err := os.WriteFile(descriptionPath, description, 0600); err != nil {
		t.Fatal(err)
	}
	compileContext, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(compileContext, binary, "compile", descriptionPath).Output()
	if err != nil {
		if failure, ok := err.(*exec.ExitError); ok {
			t.Fatalf("compile full description: %v\n%s", err, failure.Stderr)
		}
		t.Fatal(err)
	}
	var compiled struct {
		Transactions []compiledContractTransaction `json:"transactions"`
	}
	if err := json.Unmarshal(output, &compiled); err != nil {
		t.Fatalf("decode compiled transactions: %v", err)
	}
	return compiled.Transactions
}

func TestAuditVerificationCompilesSuccessWithVertrag(t *testing.T) {
	pairs := publishedContractPairs(t)
	for _, expected := range []struct {
		operation, name string
		status          int
	}{
		{"verifyAuditLog", "found", http.StatusOK},
		{"verifyAuditLog", "invalid_anchor", http.StatusUnprocessableEntity},
		{"listAuditEntries", "available", http.StatusOK},
	} {
		if pair, found := pairs[expected.operation][expected.name]; !found || pair.Response.Status != expected.status {
			t.Errorf("%s/%s must exist at status %d", expected.operation, expected.name, expected.status)
		}
	}
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for whole-description compilation")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	for index, generator := range gapGenerators(t) {
		t.Run(fmt.Sprintf("registration_%d", index), func(t *testing.T) {
			var successes, listings, invalidAnchors int
			for _, transaction := range compileFullContract(t, binary, generator) {
				requestURI, err := url.ParseRequestURI(transaction.Request.URI)
				if err != nil {
					t.Fatal(err)
				}
				if requestURI.Path == "/api/v1/admin/audit" && transaction.Response.Status == "200" {
					if requestURI.Query().Get("limit") != "20" {
						t.Errorf("audit list success compiled with unexpected URI %s", transaction.Request.URI)
					}
					listings++
				}
				if requestURI.Path != "/api/v1/admin/audit/verify" {
					continue
				}
				switch transaction.Response.Status {
				case "200":
					if requestURI.Query().Get("anchor") != "" {
						t.Errorf("unanchored success compiled with unexpected URI %s", transaction.Request.URI)
					}
					successes++
				case "422":
					if requestURI.Query().Get("anchor") == "bad" {
						invalidAnchors++
					}
				}
			}
			if successes != 1 || invalidAnchors != 1 {
				t.Fatalf("compiled audit verification: %s=%d, invalid anchor %s=%d; want one of each", http.StatusText(http.StatusOK), successes, http.StatusText(http.StatusUnprocessableEntity), invalidAnchors)
			}
			if listings != 1 {
				t.Fatalf("compiled audit listings: 200=%d; want one", listings)
			}
			t.Logf("compiled audit: verify 200=%d, list 200=%d, invalid-anchor 422=%d", successes, listings, invalidAnchors)
		})
	}
}
