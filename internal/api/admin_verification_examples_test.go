package api

import (
	"github.com/restmail/restmail/internal/examplepairs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/restmail/restmail"
)

func TestAdminVerificationExamples(t *testing.T) {
	scenarios := adminVerificationScenarios(t)
	pairs, err := examplepairs.Read()
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range adminVerificationPaths {
		if len(pairs[operation]) < 15 {
			t.Fatalf("incomplete examples for %s", operation)
		}
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				s, ok := scenarios[name]
				if !ok {
					t.Fatalf("missing scenario %s", name)
				}
				req := httptest.NewRequest(http.MethodGet, publicDiscoveryURI(path, pair), nil)
				if s.token != "" {
					req.Header.Set("Authorization", "Bearer "+s.token)
				}
				if name == "request_too_large" {
					req.ContentLength = 9
				}
				rr := httptest.NewRecorder()
				s.handler.ServeHTTP(rr, req)
				if rr.Code != pair.Response.Status || rr.Header().Get("Content-Type") != pair.Response.MediaType {
					t.Fatalf("got %d %s %s, want %d %s", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String(), pair.Response.Status, pair.Response.MediaType)
				}
				assertPublicExampleBody(t, pair.Response.Body, rr.Body.Bytes())
			})
		}
	}
	for _, g := range gapGenerators(t) {
		for _, warning := range g.Report() {
			if _, ok := adminVerificationPaths[warning.OperationID]; ok && gapWarningApplies(map[string]gapOperation{"verifyAuditLog": {}, "verifyDelivery": {}}, warning) {
				t.Errorf("verification warning: %v", warning)
			}
		}
	}
}

func TestGapReportReconcilesConditionalExamplesWithoutHidingFailures(t *testing.T) {
	files := fstest.MapFS{"pairs.json": {Data: []byte(`{"conditional":{"good":{"request":{},"response":{"status":200,"body":{"value":1}}},"bad":{"request":{},"response":{"status":200,"body":{"value":"wrong type"}}}},"unknown":{"old":{"request":{},"response":{"status":200,"body":{}}}}}`)}}
	enabled := oapifly.New(oapifly.Config{Examples: files, ExamplesFile: "pairs.json"})
	enabled.Register(oapifly.Endpoint{Method: "GET", Path: "/conditional", OperationID: "conditional", Responses: []oapifly.EndpointResponse{{Status: 200, Type: oapifly.TypeOf[struct {
		Value int `json:"value"`
	}]()}}})
	disabled := oapifly.New(oapifly.Config{Examples: files, ExamplesFile: "pairs.json"})
	disabled.Register(oapifly.Endpoint{Method: "GET", Path: "/other", OperationID: "other", Responses: []oapifly.EndpointResponse{{Status: 204}}})
	operations := map[string]gapOperation{"conditional": {OperationID: "conditional"}}
	var invalid, unknown, conditionalOrphans int
	for _, generator := range []*oapifly.Generator{disabled, enabled} {
		for _, warning := range generator.Report() {
			if !gapWarningApplies(operations, warning) {
				if warning.OperationID != "conditional" || warning.Kind != "example-orphaned" {
					t.Fatalf("hidden unrelated failure: %+v", warning)
				}
				conditionalOrphans++
				continue
			}
			if warning.Kind == "example-invalid" && warning.OperationID == "conditional" {
				invalid++
			}
			if warning.Kind == "example-orphaned" && warning.OperationID == "unknown" {
				unknown++
			}
		}
	}
	if conditionalOrphans != 2 || invalid != 1 || unknown != 2 {
		t.Fatalf("conditional orphans=%d, invalid=%d, unknown=%d", conditionalOrphans, invalid, unknown)
	}
}

func TestAdminVerificationExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for admin verification exchanges")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	scenarios := adminVerificationScenarios(t)
	pairs, err := oapifly.ReadExamples(restmail.Examples, "openapi-example.json")
	if err != nil {
		t.Fatal(err)
	}
	for operation, path := range adminVerificationPaths {
		for name, pair := range pairs[operation] {
			t.Run(operation+"/"+name, func(t *testing.T) {
				s := scenarios[name]
				checkMailboxLookupWithVertrag(t, binary, s.handler, s.token, path, name, pair)
			})
		}
	}
}
