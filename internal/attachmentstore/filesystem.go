package attachmentstore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const FilesystemRoot = "/attachments"

var ErrPathEscape = errors.New("attachment path escapes storage root")

// ResolveFilesystemPath confines legacy reads and migration to the attachment
// tree, both lexically and after resolving symlinks.
func ResolveFilesystemPath(root, ref string) (string, error) {
	clean := filepath.Clean(ref)
	if (clean != root && !strings.HasPrefix(clean, root+string(os.PathSeparator))) || strings.Contains(clean, "..") {
		return "", ErrPathEscape
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	realPath, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(realRoot, realPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", ErrPathEscape
	}
	return realPath, nil
}
