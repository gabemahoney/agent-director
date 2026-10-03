// Package tsbunclientmodulestub_test is a synthetic-regression test for b.jct.
//
// BACKGROUND (b.jct)
// ==================
// pkg/ts-bun-client/go.mod is a stub with no Go code. It makes the bun client's
// tree a separate Go module, so the root module's ./... walks (go build, test,
// vet, list) never enter it. In the release coverage phase the coverage.bun-test
// gate rewrites that tree's dist/ and node_modules/ while coverage.go-root's
// `go test ./... -race` walks the repo; without the stub that walk fails when
// dist/ vanishes under it. `make test-sandbox` runs go and bun one after the
// other, so a lost stub would otherwise show only as a rare release failure.
//
// DESIGN
// ======
// Read-only checks of the real tree, no go or bun command:
//  1. The stub exists and its module path does not start with the root
//     module's, so repo-root finders that match the root module's line never
//     stop at it (test/envelope-diff/harness.go matches it as a substring).
//  2. No Go source sits in the stub module outside node_modules/ and dist/:
//     it would silently drop out of the root module's ./... walks.
package tsbunclientmodulestub_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot walks up from the package's working directory (set by `go test` to
// the package directory) until it finds a go.mod file.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("repoRoot: could not find go.mod walking up from %s", dir)
		}
		dir = parent
	}
}

// modulePath returns the module path that the go.mod file at path declares.
func modulePath(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		code, _, _ := strings.Cut(line, "//")
		if fields := strings.Fields(code); len(fields) == 2 && fields[0] == "module" {
			return strings.Trim(fields[1], "\"`")
		}
	}
	t.Fatalf("%s declares no module path", path)
	return ""
}

// TestTsBunClientGoModStub pins b.jct: pkg/ts-bun-client stays an empty Go
// module outside the root module's path.
func TestTsBunClientGoModStub(t *testing.T) {
	root := repoRoot(t)
	pkgDir := filepath.Join(root, "pkg", "ts-bun-client")

	t.Run("exists outside the root module's path", func(t *testing.T) {
		stub := filepath.Join(pkgDir, "go.mod")
		if _, err := os.Stat(stub); err != nil {
			t.Fatalf("%v: restore the stub, or the root module's ./... walks enter "+
				"pkg/ts-bun-client while the coverage.bun-test gate rewrites its dist/ "+
				"and node_modules/ (b.jct)", err)
		}
		rootPath := modulePath(t, filepath.Join(root, "go.mod"))
		if stubPath := modulePath(t, stub); strings.HasPrefix(stubPath, rootPath) {
			t.Fatalf("%s declares module %q, which starts with the root module's path %q: "+
				"repo-root finders that match the root go.mod's module line would stop "+
				"at the stub; use a path outside it (b.jct)", stub, stubPath, rootPath)
		}
	})

	t.Run("holds no Go source", func(t *testing.T) {
		var goFiles []string
		err := filepath.WalkDir(pkgDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				// bun runs (the coverage.bun-test gate) may remove entries mid-walk.
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				// bun owns these and rewrites them; node_modules ships stray Go
				// packages (flatted) that are not this repo's code.
				if d.Name() == "node_modules" || d.Name() == "dist" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(d.Name(), ".go") {
				goFiles = append(goFiles, path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", pkgDir, err)
		}
		if len(goFiles) > 0 {
			t.Fatalf("Go files in the pkg/ts-bun-client stub module are outside the "+
				"root module and drop out of its go build/test/vet ./... (b.jct); "+
				"move them out of pkg/ts-bun-client:\n  %s", strings.Join(goFiles, "\n  "))
		}
	})
}
