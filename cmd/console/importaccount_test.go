package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const cliImportHash = "$6$importsalt$ObInO4qcr134i8FQT.XNC6Ma04n9aRTAUNHmm8Yb1idoDBOXKoDTmdosY1xwfageGXo4ZSSrkIPRk9zMWfsG7/"

func TestImportAccount(t *testing.T) {
	for _, status := range []int{http.StatusCreated, http.StatusConflict, http.StatusUnauthorized, http.StatusTemporaryRedirect} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			called := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called++
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/accounts" || r.Header.Get("Authorization") != "Bearer private-key" {
					t.Error("wrong request method, path or authentication")
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body["address"] != "user@example.test" || body["password_hash"] != "{SHA512-CRYPT}"+cliImportHash {
					t.Error("import did not carry the normalized address and hash")
				}
				w.Header().Set("Location", "/another-place")
				w.WriteHeader(status)
				// Even a server echoing secrets must not make the CLI print them.
				fmt.Fprintf(w, `{"data":{"id":7},"echo":%q}`, cliImportHash)
			}))
			defer s.Close()
			var out, errs bytes.Buffer
			code := runImportAccount(context.Background(), []string{"--server", s.URL, "--address", "user@example.test", "--json"}, strings.NewReader(cliImportHash+"\n"), "private-key", s.Client(), &out, &errs)
			if (code == exitOK) != (status == http.StatusCreated) || called != 1 {
				t.Fatalf("exit %d, requests %d, stderr %s", code, called, &errs)
			}
			if strings.Contains(out.String()+errs.String(), "importsalt") || strings.Contains(out.String()+errs.String(), "private-key") {
				t.Fatal("CLI disclosed a secret")
			}
		})
	}
}

func TestImportAccountRejectsBadHashWithoutRequest(t *testing.T) {
	for _, hash := range []string{"{PLAIN}password", strings.Repeat("x", 259), cliImportHash + "\n" + cliImportHash} {
		var out, errs bytes.Buffer
		code := runImportAccount(context.Background(), []string{"--server", "http://localhost", "--address", "user@example.test"}, strings.NewReader(hash), "private-key", &http.Client{Transport: importMustNotRequest{t}}, &out, &errs)
		if code != exitUsage {
			t.Fatalf("exit %d, stderr %s", code, &errs)
		}
	}
}

type importMustNotRequest struct{ t *testing.T }

func (r importMustNotRequest) RoundTrip(*http.Request) (*http.Response, error) {
	r.t.Fatal("invalid hash made an HTTP request")
	return nil, fmt.Errorf("unexpected HTTP request")
}
