package filters

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	oapifly "github.com/antimatter-studios/go-oapifly"
)

// sidecarDir is the sidecar's project, where its document and example pairs live.
const sidecarDir = "../../../projects/js-filter-sidecar"

// The committed js-filter-sidecar.json is the document the sidecar's Go types and its
// example pairs make, so neither can change without the other (#677). Run with
// SIDECAR_DESCRIPTION_UPDATE=1 to write it again.
func TestSidecarDescriptionIsCommitted(t *testing.T) {
	var pkg struct {
		Version string `json:"version"`
	}
	raw, err := os.ReadFile(filepath.Join(sidecarDir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatal(err)
	}

	g := SidecarDescription(pkg.Version, os.DirFS(sidecarDir))
	doc, err := g.JSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range g.Report() {
		if w.Kind == oapifly.WarnExampleMissing && w.Method == "POST" && w.Path == "/validate" && w.Status == "500" {
			// A failed worker: no request makes one, so no pair can show it.
			continue
		}
		t.Errorf("the sidecar's description: %s", w.Message)
	}
	doc = append(doc, '\n')

	path := filepath.Join(sidecarDir, "js-filter-sidecar.json")
	if os.Getenv("SIDECAR_DESCRIPTION_UPDATE") == "1" {
		if err := os.WriteFile(path, doc, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	committed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(committed, doc) {
		t.Errorf("%s is not the document the sidecar's types and examples make; run SIDECAR_DESCRIPTION_UPDATE=1 go test -run TestSidecarDescriptionIsCommitted ./internal/pipeline/filters/", path)
	}
}
