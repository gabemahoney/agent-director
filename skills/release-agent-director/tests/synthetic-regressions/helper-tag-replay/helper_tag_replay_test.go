// Package helpertagreplay_test is a synthetic-regression test for b.n4v.
//
// BACKGROUND (b.n4v incident class)
// ===================================
// Before Epic E1, pkg/api helpers lived in build-tagged files that were
// excluded from the standard build.  A wrong-arity call inside such a tagged
// file compiled silently under `go build ./...`, letting the bug slip through
// CI.  E1 retired build-tagged helper files and moved all seed helpers into
// pkg/api/apitest under the normal (untagged) build.  This test proves that
// introducing the same class of bug (wrong-arity call) in apitest is now
// caught by `go build ./...`.
//
// DESIGN
// ======
//  1. Module copy : the root module is copied into t.TempDir() (copyModule).
//     The test never writes to the shared worktree and never runs the go
//     command over it, so it cannot race the other packages of a parallel
//     `go test ./...` (b.jct).
//  2. Mutation    : in the copy of pkg/api/apitest/seeds.go, insert
//     `_ = SeedSpawn("only-one-arg")` at the top of the InitStore function
//     body.  SeedSpawn takes six strings and a bool; calling it with a single
//     string is a compile error — exactly the b.n4v bug class replayed.
//  3. Build       : `go build -trimpath ./...` at the copy's root.  -trimpath
//     keeps the copy's temporary directory out of the build cache keys, so
//     repeated runs reuse cached packages.  Dependencies resolve as at the repo
//     root (module cache first, then the configured GOPROXY).
//  4. Assertions  : non-zero exit AND the compiler reports seeds.go at the
//     injected line, so an incomplete copy, which fails elsewhere, cannot pass.
//  5. Cleanup     : nothing outside t.TempDir() is written; the test fails if
//     the real seeds.go is written (requireUnwritten).
package helpertagreplay_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
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

// skipCopyDir reports whether copyModule leaves out the directory at path
// (named name): what `go build ./...` skips — names starting with "." or "_",
// testdata, and nested modules (pkg/ts-bun-client is one) — plus node_modules,
// whose packages can ship Go source (flatted ships flatted.go). The real one is
// inside that nested module, so this check matters only for a node_modules
// elsewhere in the tree.
func skipCopyDir(path, name string) bool {
	if name == "node_modules" || name == "testdata" ||
		strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
		return true
	}
	_, err := os.Stat(filepath.Join(path, "go.mod"))
	return err == nil
}

// isGoSource reports whether name is a non-test Go source file name.
func isGoSource(name string) bool {
	return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
}

// holdsGoSource reports whether dir directly contains a regular non-test Go
// source file. A directory that is already gone holds none.
func holdsGoSource(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.Type().IsRegular() && isGoSource(e.Name()) {
			return true, nil
		}
	}
	return false, nil
}

// copyModule copies the module rooted at src into dst: go.mod, go.sum and, in
// each directory that holds Go source, every regular file except _test.go files
// (so a package's embedded, cgo, assembly and .syso inputs come along), outside
// the directories skipCopyDir leaves out. Other processes create and remove
// untracked trees while the walk runs, so an entry that is gone when the walk
// reaches it is skipped.
func copyModule(t *testing.T, src, dst string) {
	t.Helper()
	if err := copyModuleFiles(src, dst, nil); err != nil {
		t.Fatalf("copy module %s to %s: %v", src, dst, err)
	}
}

// copyModuleFiles is copyModule's walk. A non-nil reached is called with each
// path the walk reaches, before the walk reads it.
func copyModuleFiles(src, dst string, reached func(path string)) error {
	src = filepath.Clean(src)
	goDirs := map[string]bool{} // directories that hold Go source
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if reached != nil {
			reached(path)
		}
		if d.IsDir() {
			if path != src && skipCopyDir(path, d.Name()) {
				return filepath.SkipDir
			}
			goDirs[path], err = holdsGoSource(path)
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		packageFile := goDirs[filepath.Dir(path)] && d.Type().IsRegular() && !strings.HasSuffix(d.Name(), "_test.go")
		if !packageFile && rel != "go.mod" && rel != "go.sum" {
			return nil
		}
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// fileSnapshot is what any write to a file changes: its content, mtime or inode.
type fileSnapshot struct {
	sha256  string
	mtimeNs int64
	inode   uint64
}

func snapshotFile(t *testing.T, path string) fileSnapshot {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat %s: no inode in %T", path, info.Sys())
	}
	return fileSnapshot{fmt.Sprintf("%x", sha256.Sum256(data)), info.ModTime().UnixNano(), st.Ino}
}

// requireUnwritten fails the test if path is written after this call, including
// by cleanups registered after it (b.jct: no test may rewrite a tracked file,
// even to restore it).
func requireUnwritten(t *testing.T, path string) {
	t.Helper()
	before := snapshotFile(t, path)
	t.Cleanup(func() {
		if after := snapshotFile(t, path); after != before {
			t.Errorf("%s was written during the test:\nbefore %+v\nafter  %+v", path, before, after)
		}
	})
}

// TestHelperTagReplay demonstrates AC-1: a wrong-arity call to an apitest
// symbol is caught by `go build ./...` now that build-tagged helper files have
// been retired (b.n4v incident, Epic E1).
func TestHelperTagReplay(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: copies the module and runs `go build ./...` on the copy")
	}
	root := repoRoot(t)
	// Registered first, so it checks after every other cleanup has run.
	requireUnwritten(t, filepath.Join(root, "pkg", "api", "apitest", "seeds.go"))

	// ── 1. Copy the module ─────────────────────────────────────────────────
	modCopy := t.TempDir()
	copyModule(t, root, modCopy)
	targetFile := filepath.Join(modCopy, "pkg", "api", "apitest", "seeds.go")

	orig, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("read the copy of seeds.go: %v", err)
	}

	// ── 2. Inject the mutation into the copy ───────────────────────────────
	// The real signature is:
	//   SeedSpawn(dbPath, id, state, cwd, relayMode, sessionID string, createStore bool, opts ...SpawnOption)
	// Passing a single string arg is a compile error — the b.n4v bug class.
	const marker = "func InitStore(dbPath string) (string, error) {\n\ts, err := store.OpenOrInit(dbPath)"
	const mutated = "func InitStore(dbPath string) (string, error) {\n" +
		"\t_ = SeedSpawn(\"only-one-arg\") // wrong-arity: SeedSpawn needs six strings and a bool (b.n4v replay)\n" +
		"\ts, err := store.OpenOrInit(dbPath)"

	at := bytes.Index(orig, []byte(marker))
	if at < 0 {
		t.Fatalf("mutation marker not found in seeds.go — update the marker if InitStore was refactored")
	}
	// The injected call sits on the line after InitStore's signature.
	injectedLine := bytes.Count(orig[:at], []byte("\n")) + 2

	mutatedContent := bytes.Replace(orig, []byte(marker), []byte(mutated), 1)
	if err := os.WriteFile(targetFile, mutatedContent, 0o644); err != nil {
		t.Fatalf("write mutated seeds.go into the copy: %v", err)
	}

	// ── 3. Build: must fail ────────────────────────────────────────────────
	// -trimpath: see DESIGN step 3. GOFLAGS replaces any inherited flags (a
	// -mod=vendor would find no vendor directory in the copy); GOWORK=off keeps
	// an inherited workspace from redirecting module resolution.
	cmd := exec.Command("go", "build", "-trimpath", "./...")
	cmd.Dir = modCopy
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=readonly", "GOWORK=off")
	output, err := cmd.CombinedOutput()

	// ── 4. Assertions ──────────────────────────────────────────────────────
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		if err == nil {
			t.Fatalf("expected `go build ./...` to fail after wrong-arity mutation, but it succeeded")
		}
		t.Fatalf("run `go build ./...`: %v", err)
	}

	wantPos := fmt.Sprintf("seeds.go:%d:", injectedLine)
	if !strings.Contains(string(output), wantPos) {
		t.Fatalf("expected compiler stderr to report the injected call at %s; got:\n%s", wantPos, output)
	}

	// Green means the bug was detected — log the evidence.
	t.Logf("AC-1 verified: `go build ./...` caught the wrong-arity mutation.\nCompiler output:\n%s", output)
}

// TestCopyModuleSkipsEntriesRemovedMidWalk pins b.jct: a tree removed after the
// walk listed it (as a dist/ rebuild does) is skipped, not a copy failure, and
// the walk stays out of node_modules (see skipCopyDir).
// It tests a helper despite the no-meta-tests rule because without it, losing
// either would show only as a rare flake of TestHelperTagReplay.
func TestCopyModuleSkipsEntriesRemovedMidWalk(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	files := map[string]string{
		"go.mod":              "module example.test/copyfixture\n\ngo 1.22\n",
		"go.sum":              "",
		"main.go":             "package main\n",
		"main_test.go":        "package main\n",
		"keep/keep.go":        "package keep\n",
		"keep/embedded.txt":   "a file in a Go source directory\n",
		"docs/notes.txt":      "a file in a directory with no Go source\n",
		"node_modules/x/x.go": "package x\n",
		"vanishing/a/a.go":    "package a\n",
		"vanishing/z.go":      "package vanishing\n",
	}
	for rel, content := range files {
		path := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Once the walk has listed vanishing/ (a and z.go) and reaches a, remove
	// the whole tree: the walk then fails to open a and to read z.go.
	vanishing := filepath.Join(src, "vanishing")
	removed := false
	err := copyModuleFiles(src, dst, func(path string) {
		if path == filepath.Join(vanishing, "a") {
			removed = true
			if err := os.RemoveAll(vanishing); err != nil {
				t.Fatalf("remove %s mid-walk: %v", vanishing, err)
			}
		}
	})
	if err != nil {
		t.Fatalf("copy failed on a tree removed mid-walk: %v", err)
	}
	if !removed {
		t.Fatal("the walk never reached vanishing/a, so nothing was removed mid-walk")
	}

	var got []string
	if err := filepath.WalkDir(dst, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dst, path)
		rel = filepath.ToSlash(rel)
		got = append(got, rel)
		data, err := os.ReadFile(path)
		if err == nil && string(data) != files[rel] {
			t.Errorf("copied %s = %q, want %q", rel, data, files[rel])
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"go.mod", "go.sum", "keep/embedded.txt", "keep/keep.go", "main.go"}; !slices.Equal(got, want) {
		t.Fatalf("copied files = %q, want %q", got, want)
	}
}
