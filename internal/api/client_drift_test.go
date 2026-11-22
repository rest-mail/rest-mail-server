package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func clientTypeDrift(path string, actual, generated []byte) error {
	if !bytes.Equal(actual, generated) {
		return fmt.Errorf("%s differs from openapi-typescript output for the served description; run generate-types with RESTMAIL_OPENAPI_SOURCE naming this revision's served JSON", path)
	}
	return nil
}

func TestGeneratedClientsMatchServedDescription(t *testing.T) {
	_, served := liveDescription(t, describedRouters(t).Public)
	directory := t.TempDir()
	source, output := filepath.Join(directory, "openapi.json"), filepath.Join(directory, "types.ts")
	if err := os.WriteFile(source, served, 0600); err != nil {
		t.Fatal(err)
	}
	const generator = "openapi-typescript@7.13.0"
	command := exec.CommandContext(t.Context(), "npx", "--yes", generator, source, "-o", output)
	if log, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generate client types: %v\n%s", err, log)
	}
	generated, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, client := range []struct{ directory, types string }{
		{"admin", "src/lib/api-types.ts"}, {"webmail", "src/api/types.ts"},
	} {
		t.Run(client.directory, func(t *testing.T) {
			manifest, err := os.ReadFile(filepath.Join("../..", client.directory, "package.json"))
			if err != nil {
				t.Fatal(err)
			}
			var scripts struct{ Scripts map[string]string }
			if err := json.Unmarshal(manifest, &scripts); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(scripts.Scripts["generate-types"], "npx --yes "+generator+" ") {
				t.Fatalf("generate-types does not use the checked generator %s", generator)
			}
			path := filepath.Join("../..", client.directory, client.types)
			actual, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := clientTypeDrift(path, actual, generated); err != nil {
				t.Error(err)
			}
			stale := bytes.Replace(generated, []byte("getHealth"), []byte("staleHealth"), 1)
			if bytes.Equal(stale, generated) {
				t.Fatal("control did not mutate a generated operation")
			}
			if err := clientTypeDrift(path, stale, generated); err == nil {
				t.Fatal("stale generated operation passed the drift check")
			} else {
				t.Logf("stale-file control rejected: %v", err)
			}
		})
	}
}
