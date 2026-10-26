package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

func (c *customFilterValidationCalls) snapshot() []map[string]interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]interface{}(nil), c.requests...)
}

func TestCustomFilterValidationExamples(t *testing.T) {
	f, calls := newCustomFilterValidationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs["validateCustomFilter"]) < 25 {
		t.Fatal("validation examples incomplete")
	}
	for name, pair := range pairs["validateCustomFilter"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f[name]
			if !ok {
				t.Fatalf("missing scenario%s", name)
			}
			body := []byte(pair.Request.Body)
			if name == "malformed_json" {
				body = []byte("{")
			}
			if name == "request_too_large" {
				body = bytes.Repeat([]byte("x"), 513)
			}
			if name == "body_boundary" {
				if len(body) > 512 {
					t.Fatal("oversized boundary")
				}
				body = append(body, bytes.Repeat([]byte(" "), 512-len(body))...)
			}
			before := len(calls.snapshot())
			rr := customFilterValidationRequest(t, s, body)
			if rr.Code != pair.Response.Status {
				t.Fatalf("got%d %s want%d", rr.Code, rr.Body, pair.Response.Status)
			}
			assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			after := calls.snapshot()[before:]
			want := rr.Code == 200 || strings.HasPrefix(name, "sidecar_")
			if (len(after) == 1) != want || len(after) > 1 {
				t.Fatalf("sidecar calls%d want%t", len(after), want)
			}
			if len(after) == 1 {
				var request map[string]interface{}
				if err := json.Unmarshal(pair.Request.Body, &request); err != nil {
					t.Fatal(err)
				}
				if request["email"] == nil {
					delete(request, "email")
				}
				a, _ := json.Marshal(after[0])
				b, _ := json.Marshal(request)
				if !bytes.Equal(a, b) {
					t.Fatalf("sidecar payload%s want%s", a, b)
				}
			}
			if s.store != nil && len(s.store.snapshot()) != 0 {
				t.Fatal("validation accessed custom-filter storage")
			}
		})
	}
	for _, g := range gapGenerators(t) {
		for _, w := range g.Report() {
			if w.OperationID == "validateCustomFilter" {
				t.Errorf("validation warning:%v", w)
			}
		}
	}
}

func TestCustomFilterValidationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for validation exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := newCustomFilterValidationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range pairs["validateCustomFilter"] {
		t.Run(name, func(t *testing.T) {
			s, ok := f[name]
			if !ok {
				t.Fatalf("missing scenario%s", name)
			}
			checkQueueMutationPairWithVertrag(t, binary, s, http.MethodPost, customFilterValidationPath, name, pair, 513)
		})
	}
}
