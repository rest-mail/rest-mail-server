package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestForwardSuccessRetainsIndependentRequirementsWithVertrag(t *testing.T) {
	binary := os.Getenv("RESTMAIL_VERTRAG_BINARY")
	if binary == "" {
		t.Skip("set RESTMAIL_VERTRAG_BINARY for named example requirement checks")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	for index, generator := range gapGenerators(t) {
		t.Run(fmt.Sprintf("registration_%d", index), func(t *testing.T) {
			directory := t.TempDir()
			descriptionPath := filepath.Join(directory, "openapi.json")
			description, err := json.Marshal(generator.Generate())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(descriptionPath, description, 0600); err != nil {
				t.Fatal(err)
			}
			unprovenPath := filepath.Join(directory, "unproven.json")
			configuration, err := json.Marshal(map[string]any{
				"endpoint": "http://127.0.0.1:1",
				"unproven": unprovenPath,
			})
			if err != nil {
				t.Fatal(err)
			}
			configurationPath := filepath.Join(directory, "vertrag.json")
			if err := os.WriteFile(configurationPath, configuration, 0600); err != nil {
				t.Fatal(err)
			}
			classificationContext, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			output, err := exec.CommandContext(classificationContext, binary, "run", "--dry-run", "--no-color",
				"--config", configurationPath, "--operation-id", "forwardMessage",
				"--only-matching", " > 201( > |$)", descriptionPath).CombinedOutput()
			if err != nil {
				t.Fatalf("classify full-description success: %v\n%s", err, output)
			}
			unprovenData, err := os.ReadFile(unprovenPath)
			if err != nil {
				t.Fatal(err)
			}
			var unproven []struct {
				Name    string            `json:"name"`
				Example string            `json:"example"`
				Missing map[string]string `json:"missing"`
			}
			if err := json.Unmarshal(unprovenData, &unproven); err != nil {
				t.Fatal(err)
			}
			faultRetained := false
			for _, exchange := range unproven {
				if !strings.Contains(exchange.Name, "/api/v1/messages/{id}/forward >") {
					continue
				}
				if exchange.Example == "example" && strings.Contains(exchange.Name, " > 201 >") {
					t.Errorf("ordinary forward success incorrectly requires %v", exchange.Missing)
				}
				if exchange.Example == "attachment_write_failed" && exchange.Missing["fault"] == "database" {
					faultRetained = true
				}
			}
			if !faultRetained {
				t.Error("attachment failure no longer requires its database fault")
			}
			if selected := strings.Count(string(output), "skip:"); selected != 1 {
				t.Fatalf("dry-run selected %d ordinary forward successes; want one without executing HTTP", selected)
			}
			t.Log("ordinary forward success selected without a fault; fault example remains unproven; HTTP deliberately not executed")
		})
	}
}
