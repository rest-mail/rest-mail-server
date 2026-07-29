package fleet

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/restmail/restmail/internal/instance"
)

// Render regenerates a selection's config.env from its manifest. With check
// true nothing is written and a stale or missing target is an error, which is
// what CI wants.
//
// Same behaviour as `instance render` / `instance render -check`; the point of
// having it here is that the config is selected by argument rather than by an
// environment variable that must precede the command.
func Render(sel Selection, check bool) error {
	m, err := sel.Load()
	if err != nil {
		return err
	}
	rendered, err := instance.Render(m)
	if err != nil {
		return fmt.Errorf("render %s: %w", sel.ManifestPath, err)
	}
	target := filepath.Join(sel.Dir, "config.env")

	if check {
		existing, err := os.ReadFile(target)
		if err != nil {
			return fmt.Errorf("%s missing or unreadable — run `restmail config render %s`", target, sel.Name)
		}
		if !bytes.Equal(existing, rendered) {
			return fmt.Errorf("%s is stale — re-run `restmail config render %s`", target, sel.Name)
		}
		return nil
	}

	if err := os.WriteFile(target, rendered, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", target)
	return nil
}
