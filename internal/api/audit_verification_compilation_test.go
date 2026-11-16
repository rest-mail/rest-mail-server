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
)

func TestAuditVerificationCompilesSuccessWithVertrag(t *testing.T) {
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
				Transactions []struct {
					Request struct {
						URI string `json:"uri"`
					} `json:"request"`
					Response struct {
						Status string `json:"status"`
					} `json:"response"`
				} `json:"transactions"`
			}
			if err := json.Unmarshal(output, &compiled); err != nil {
				t.Fatalf("decode compiled transactions: %v", err)
			}
			var successes, invalidAnchors int
			for _, transaction := range compiled.Transactions {
				requestURI, err := url.ParseRequestURI(transaction.Request.URI)
				if err != nil {
					t.Fatal(err)
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
			t.Logf("compiled audit verification: valid 200=%d, invalid-anchor 422=%d", successes, invalidAnchors)
		})
	}
}
