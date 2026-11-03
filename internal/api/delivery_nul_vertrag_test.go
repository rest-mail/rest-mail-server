package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGatewayDeliveryNULExamplesWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY to replay delivery validation examples")
	}
	routers := describedRouters(t)
	document := contractDocument(t, routers.pairGenerator(t, "delivery NUL examples"))
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var description map[string]any
	if err := json.Unmarshal(encoded, &description); err != nil {
		t.Fatal(err)
	}
	const path = "/api/v1/messages/deliver"
	operation := description["paths"].(map[string]any)[path].(map[string]any)["post"].(map[string]any)
	description["paths"] = map[string]any{path: map[string]any{"post": operation}}
	selected := map[string]bool{}
	for _, name := range gatewayDeliveryExamples {
		if strings.HasSuffix(name, "_nul") || name == "folder_blank" || name == "raw_message_invalid" {
			selected[name] = true
		}
	}
	filter := func(media map[string]any) {
		examples := media["examples"].(map[string]any)
		for name := range examples {
			if !selected[name] {
				delete(examples, name)
			}
		}
		if len(examples) != len(selected) {
			t.Fatalf("description has %d validation examples, want %d", len(examples), len(selected))
		}
	}
	filter(operation["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any))
	response := operation["responses"].(map[string]any)["422"].(map[string]any)
	filter(response["content"].(map[string]any)["application/json"].(map[string]any))
	operation["responses"] = map[string]any{"422": response}
	directory := t.TempDir()
	encoded, err = marshalPairDescription(description)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "openapi.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(routers.Public)
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "run", "--endpoint", server.URL, "--reporter", "json", "--output", "report.json", "openapi.json")
	command.Dir = directory
	output, runErr := command.CombinedOutput()
	report, err := os.ReadFile(filepath.Join(directory, "report.json"))
	if err != nil {
		t.Fatalf("read report: %v; run: %v\n%s", err, runErr, output)
	}
	if runErr != nil {
		t.Fatalf("delivery validation examples: %v\n%s\n%s", runErr, output, report)
	}
	var result struct {
		Summary struct{ Total, Passing, Failing, Errors, Skipped int }
	}
	if err := json.Unmarshal(report, &result); err != nil {
		t.Fatal(err)
	}
	if result.Summary.Total != len(selected) || result.Summary.Passing != len(selected) || result.Summary.Failing != 0 || result.Summary.Errors != 0 || result.Summary.Skipped != 0 {
		t.Fatalf("not every validation example ran and passed: %s", report)
	}
}
