package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/restmail/restmail/internal/instance"
)

// DefaultConfig is used when no config is named anywhere.
const DefaultConfig = "restmail.test"

// ConfigRoot is the directory holding config/<name>/ definitions.
const ConfigRoot = "config"

// Selection is a resolved config: which definition to act on and where it lives.
type Selection struct {
	Name         string // config name, e.g. "restmail.test"
	Dir          string // directory holding manifest.yml
	ManifestPath string
}

// Resolve turns a positional argument into a Selection.
//
// Precedence: the argument, then $RESTMAIL_CONFIG, then DefaultConfig. An
// argument that looks like a path (contains a separator, or ends in a manifest
// extension) is used as-is, so a precanned definition anywhere on disk works:
//
//	restmail status                              → config/restmail.test
//	restmail status mail4.test                   → config/mail4.test
//	restmail status ./scenarios/e2e-mail1.yml    → that file
//
// Unlike the Taskfile, nothing here depends on the value being visible before a
// file is parsed, so an argument is always honoured.
func Resolve(arg, root string) (Selection, error) {
	if arg == "" {
		arg = os.Getenv("RESTMAIL_CONFIG")
	}
	if arg == "" {
		arg = DefaultConfig
	}
	if isPath(arg) {
		return fromPath(arg)
	}
	dir := filepath.Join(root, ConfigRoot, arg)
	return Selection{Name: arg, Dir: dir, ManifestPath: filepath.Join(dir, "manifest.yml")}, nil
}

func isPath(s string) bool {
	if strings.ContainsRune(s, filepath.Separator) || strings.HasPrefix(s, ".") {
		return true
	}
	switch strings.ToLower(filepath.Ext(s)) {
	case ".yml", ".yaml", ".json":
		return true
	}
	return false
}

func fromPath(p string) (Selection, error) {
	info, err := os.Stat(p)
	if err != nil {
		return Selection{}, fmt.Errorf("config %s: %w", p, err)
	}
	if info.IsDir() {
		return Selection{
			Name:         filepath.Base(strings.TrimSuffix(p, string(filepath.Separator))),
			Dir:          p,
			ManifestPath: filepath.Join(p, "manifest.yml"),
		}, nil
	}
	dir := filepath.Dir(p)
	// A manifest file names its own config: prefer the containing directory's
	// name, falling back to the file stem for a loose scenario file.
	name := filepath.Base(dir)
	if name == "." || name == string(filepath.Separator) || name == ConfigRoot {
		name = strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
	}
	return Selection{Name: name, Dir: dir, ManifestPath: p}, nil
}

// Load reads and parses the selected manifest.
func (s Selection) Load() (*instance.Manifest, error) {
	data, err := os.ReadFile(s.ManifestPath)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	m, err := instance.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.ManifestPath, err)
	}
	return m, nil
}

// Configs lists every config definition under root/config, in name order.
func Configs(root string) ([]Selection, error) {
	entries, err := os.ReadDir(filepath.Join(root, ConfigRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Selection
	for _, e := range entries {
		// Follow symlinked config dirs too: they are legacy but should still be
		// listed, and `project:check:runtime` is what objects to them.
		if !e.IsDir() && e.Type()&os.ModeSymlink == 0 {
			continue
		}
		dir := filepath.Join(root, ConfigRoot, e.Name())
		mp := filepath.Join(dir, "manifest.yml")
		if _, err := os.Stat(mp); err != nil {
			continue
		}
		out = append(out, Selection{Name: e.Name(), Dir: dir, ManifestPath: mp})
	}
	return out, nil
}
