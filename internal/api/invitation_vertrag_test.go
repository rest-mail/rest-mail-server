package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

func TestInvitationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY to replay invitation examples")
	}
	useRealGrants(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, examples := range pairs {
		if operation != "inviteAccount" && operation != "acceptInvitation" {
			continue
		}
		for name, pair := range examples {
			t.Run(operation+"/"+name, func(t *testing.T) {
				mode := map[string]string{"insufficient_permissions": "denied", "missing": "missing_account", "existing_credentials": "established_account", "invalid_token": "expired", "wrong_purpose": "wrong_purpose", "unavailable": "unavailable"}[name]
				if name == "internal_error" {
					mode = "credential_failure"
					if operation == "inviteAccount" {
						mode = "mint_failure"
					}
				}
				f := newInvitationFixture(t, mode)
				path := "/api/v1/auth/invitation"
				uri := path
				if operation == "inviteAccount" {
					path = "/api/v1/admin/mailboxes/{id}/invitation"
					uri = strings.ReplaceAll(path, "{id}", "12")
					if name == "invalid_id" {
						uri = strings.ReplaceAll(path, "{id}", "0")
					}
				}
				doc := contractDocument(t, describedRouters(t).generator("invitation examples"))
				op := doc["paths"].(map[string]any)[path].(map[string]any)["post"].(map[string]any)
				doc["paths"] = map[string]any{path: map[string]any{"post": op}}
				status := strconv.Itoa(pair.Response.Status)
				response := op["responses"].(map[string]any)[status].(map[string]any)
				for _, v := range response["content"].(map[string]any) {
					media := v.(map[string]any)
					selected := media["examples"].(map[string]any)[name]
					if selected == nil {
						t.Fatal("response example missing")
					}
					media["examples"] = map[string]any{name: selected}
				}
				op["responses"] = map[string]any{status: response}
				if request, ok := op["requestBody"].(map[string]any); ok {
					for _, v := range request["content"].(map[string]any) {
						media := v.(map[string]any)
						if examples, ok := media["examples"].(map[string]any); ok {
							media["examples"] = map[string]any{name: examples[name]}
						}
					}
				}
				// The hook sends the exact published pair, including invalid path/body
				// examples. Compiler placeholders never change the described constraints.
				if params, ok := op["parameters"].([]any); ok {
					for _, v := range params {
						p := v.(map[string]any)
						delete(p, "examples")
						p["example"] = 12
					}
				}
				dir := t.TempDir()
				data, _ := json.Marshal(doc)
				if err := os.WriteFile(filepath.Join(dir, "openapi.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
				body := ""
				if len(pair.Request.Body) > 0 {
					body = string(pair.Request.Body)
				}
				if operation == "inviteAccount" && name == "request_too_large" {
					body = strings.Repeat("x", 513)
				}
				hook := "import vertrag_hooks as hooks\n@hooks.before_each\ndef exact_invitation_pair(transaction):\n    transaction['fullPath'] = " + strconv.Quote(uri) + "\n    transaction['request']['body'] = " + strconv.Quote(body) + "\n"
				if err := os.WriteFile(filepath.Join(dir, "pair.py"), []byte(hook), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "vertrag.yml"), []byte("language: python\nhookfiles: ./pair.py\n"), 0600); err != nil {
					t.Fatal(err)
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					rr := httptest.NewRecorder()
					f.handler.ServeHTTP(rr, r)
					if operation == "inviteAccount" && rr.Code == 201 {
						// The dedicated route test verifies entropy, persisted hash, lifetime
						// and secrecy headers before this volatile illustration is normalized.
						var payload map[string]any
						if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
							t.Error(err)
							return
						}
						data := payload["data"].(map[string]any)
						data["token"] = invitationExampleToken
						data["expires_at"] = "2030-01-01T00:15:00Z"
						normalized, _ := json.Marshal(payload)
						rr.Body.Reset()
						_, _ = rr.Body.Write(normalized)
					}
					for k, values := range rr.Header() {
						for _, value := range values {
							w.Header().Add(k, value)
						}
					}
					w.WriteHeader(rr.Code)
					_, _ = w.Write(rr.Body.Bytes())
				}))
				defer server.Close()
				args := []string{"run", "--endpoint", server.URL, "--reporter", "json", "--output", "report.json"}
				if operation == "inviteAccount" && name != "unauthenticated" {
					args = append(args, "--header", "Authorization: Bearer "+f.token)
				}
				args = append(args, "openapi.json")
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, binary, args...)
				cmd.Dir = dir
				output, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("invitation exchange failed: %v\n%s", err, output)
				}
				data, err = os.ReadFile(filepath.Join(dir, "report.json"))
				if err != nil {
					t.Fatal(err)
				}
				var report struct {
					Summary struct{ Total, Passing, Failing, Errors, Skipped int }
				}
				if err := json.Unmarshal(data, &report); err != nil {
					t.Fatal(err)
				}
				if report.Summary.Total != 1 || report.Summary.Passing != 1 || report.Summary.Failing != 0 || report.Summary.Errors != 0 || report.Summary.Skipped != 0 {
					t.Fatalf("invitation exchange did not execute and pass: %s", data)
				}
			})
		}
	}
}
