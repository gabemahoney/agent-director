// Package treefix is macrun's reusable test tree fixture: it writes a
// throwaway tree of files, rooted at a Go module, under a test's temporary
// directory. A macrun test that needs a tree of files on disk must use
// Write, never its own tree writer.
package treefix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Write writes files, slash-separated paths relative to the root mapped to
// their contents, under t.TempDir() and returns the root. A path ending in /
// is made an empty directory. The root gets a go.mod unless files has one.
func Write(t testing.TB, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	if _, ok := files["go.mod"]; !ok {
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/tree\n\ngo 1.22\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if strings.HasSuffix(name, "/") {
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
