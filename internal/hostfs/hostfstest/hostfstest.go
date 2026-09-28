// Package hostfstest builds fixture host trees for tests.
package hostfstest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enr/terminus/internal/hostfs"
)

// Symlink marks a fixture entry as a symbolic link to the given target.
const Symlink = "symlink:"

// New writes the files (host path → content) under a temporary root and returns its FS.
// A content starting with Symlink creates a symbolic link instead.
func New(t testing.TB, files map[string]string) hostfs.FS {
	t.Helper()
	root := t.TempDir()
	for p, content := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if target, ok := strings.CutPrefix(content, Symlink); ok {
			if err := os.Symlink(target, full); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return hostfs.FS{Root: root}
}
