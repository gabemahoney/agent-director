// Package sandboxhelperdeps_test is a regression test for bug b.q6s.
//
// bin/ts-helper and test/fake-tmux/tmux were rebuilt only when a .go file in a
// hand-kept list of package directories changed (TS_HELPER_SRC_DIRS,
// FAKE_TMUX_SRC_DIRS). The lists had fallen behind the packages each binary
// links and left out embedded files, so after a change to one of those make
// kept the stale binary. Each recipe now writes a dependency file from go list
// -deps, which the Makefile includes, and a binary without one always rebuilds.
//
// It copies the Makefile under test, go.mod, go.sum and the files go list -deps
// reports for each binary into a temp tree, builds both binaries there with the
// real go, then changes one file at a time and checks make -q. It runs neither
// binary, so it needs no sandboxguard TestMain.
//
// Fails before the fix: MAKEFILE_UNDER_TEST=<pre-fix Makefile> fails the cell of
// every package the lists left out, every embedded-file cell, and each binary's
// deleted_file and no_dependency_file cells.
package sandboxhelperdeps_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/test/sandbox/internal/sandboxtest"
)

// helpers are the binaries under test: their make target and the package each
// is built from.
var helpers = []struct{ target, pkg string }{
	{"bin/ts-helper", "./test/smoke/ts-helper"},
	{"test/fake-tmux/tmux", "./test/fake-tmux"},
}

// linkedPackage is one package of this module a binary links, as go list -deps
// reports it: its directory relative to the repo root, its non-test Go files
// and its embedded files.
type linkedPackage struct {
	dir               string
	goFiles, embedded []string
}

// linkedPackages returns the packages of this module that pkg links.
func linkedPackages(t *testing.T, repo, pkg string) []linkedPackage {
	t.Helper()
	c := exec.Command("go", "list", "-deps", "-f",
		`{{if and .Module .Module.Main}}{{.Dir}}{{"\t"}}{{join .GoFiles " "}}{{"\t"}}{{join .EmbedFiles " "}}{{end}}`, pkg)
	c.Dir = repo
	c.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := c.Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pkg, err)
	}
	var pkgs []linkedPackage
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue // a package from outside this module
		}
		parts := strings.Split(line, "\t")
		dir, err := filepath.Rel(repo, parts[0])
		if err != nil {
			t.Fatalf("package dir %s: %v", parts[0], err)
		}
		pkgs = append(pkgs, linkedPackage{dir: dir, goFiles: strings.Fields(parts[1]), embedded: strings.Fields(parts[2])})
	}
	if len(pkgs) == 0 {
		t.Fatalf("go list -deps %s reported no package of this module", pkg)
	}
	return pkgs
}

// copyFile copies the file rel from src to the same path under dst.
func copyFile(t *testing.T, src, dst, rel string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(src, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dst, rel)), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(filepath.Join(dst, rel), data, 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// makeQ runs make -q target in root and returns its exit code: 0 when make
// considers target up to date, 1 when it would rebuild it.
func makeQ(t *testing.T, root, target string) int {
	t.Helper()
	c := exec.Command("make", "-q", target)
	c.Dir = root
	c.Env = sandboxtest.ScrubbedMakeEnv()
	out, err := c.CombinedOutput()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exitErr) && exitErr.ExitCode() == 1:
		return 1
	default:
		t.Fatalf("make -q %s: %v\noutput:\n%s", target, err, out)
		return -1
	}
}

// setMtime sets path's modification time, failing the test on error.
func setMtime(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// mtime returns path's modification time, failing the test on error.
func mtime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.ModTime()
}

// TestHelperBinariesRebuildOnLinkedChange (b.q6s): after a build, a change to a
// non-test Go file of any package of this module a helper links, or to a file
// it embeds, makes make -q report the helper out of date, as does adding or
// deleting a file in one of its packages or losing its dependency file. Each
// change is undone, mtimes included, before the next.
func TestHelperBinariesRebuildOnLinkedChange(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: builds both helpers with go")
	}
	sandboxtest.RequireTools(t, "skipping b.q6s helper dependency test", "make", "go")
	repo := sandboxtest.RepoRoot(t)
	root := t.TempDir()
	makefile, err := os.ReadFile(sandboxtest.MakefileUnderTest(t))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "Makefile"), makefile, 0o644); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}
	copyFile(t, repo, root, "go.mod")
	copyFile(t, repo, root, "go.sum")
	linked := map[string][]linkedPackage{}
	for _, h := range helpers {
		linked[h.target] = linkedPackages(t, repo, h.pkg)
		for _, p := range linked[h.target] {
			for _, f := range append(append([]string(nil), p.goFiles...), p.embedded...) {
				copyFile(t, repo, root, filepath.Join(p.dir, f))
			}
		}
	}

	build := exec.Command("make", helpers[0].target, helpers[1].target)
	build.Dir = root
	build.Env = sandboxtest.ScrubbedMakeEnv()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("make %s %s: %v\noutput:\n%s", helpers[0].target, helpers[1].target, err, out)
	}

	for _, h := range helpers {
		t.Run(h.target, func(t *testing.T) {
			if code := makeQ(t, root, h.target); code != 0 {
				t.Fatalf("make -q %s right after its build = %d, want 0 (up to date)", h.target, code)
			}
			later := mtime(t, filepath.Join(root, h.target)).Add(2 * time.Second)
			// touched checks that make -q reports the binary out of date once
			// rel is newer than it, then puts rel's mtime back.
			touched := func(t *testing.T, rel string) {
				path := filepath.Join(root, rel)
				before := mtime(t, path)
				setMtime(t, path, later)
				defer setMtime(t, path, before)
				if code := makeQ(t, root, h.target); code != 1 {
					t.Errorf("make -q %s after touching %s = %d, want 1 (rebuild)", h.target, rel, code)
				}
			}
			for _, p := range linked[h.target] {
				t.Run(p.dir, func(t *testing.T) {
					touched(t, filepath.Join(p.dir, p.goFiles[0]))
					if len(p.embedded) > 0 {
						touched(t, filepath.Join(p.dir, p.embedded[0]))
					}
				})
			}

			// go list -deps lists the binary's own package last.
			mainPkg := linked[h.target][len(linked[h.target])-1]
			mainDir := filepath.Join(root, mainPkg.dir)
			t.Run("added_file", func(t *testing.T) {
				dirTime := mtime(t, mainDir)
				added := filepath.Join(mainDir, "zz_added.go")
				if err := os.WriteFile(added, []byte("package main\n"), 0o644); err != nil {
					t.Fatalf("add file: %v", err)
				}
				setMtime(t, mainDir, later)
				code := makeQ(t, root, h.target)
				if err := os.Remove(added); err != nil {
					t.Fatalf("remove added file: %v", err)
				}
				setMtime(t, mainDir, dirTime)
				if code != 1 {
					t.Errorf("make -q %s after adding %s = %d, want 1 (rebuild)", h.target, added, code)
				}
			})
			t.Run("deleted_file", func(t *testing.T) {
				dirTime := mtime(t, mainDir)
				deleted := filepath.Join(mainDir, mainPkg.goFiles[0])
				data, err := os.ReadFile(deleted)
				if err != nil {
					t.Fatalf("read %s: %v", deleted, err)
				}
				fileTime := mtime(t, deleted)
				if err := os.Remove(deleted); err != nil {
					t.Fatalf("delete %s: %v", deleted, err)
				}
				setMtime(t, mainDir, later)
				code := makeQ(t, root, h.target)
				if err := os.WriteFile(deleted, data, 0o644); err != nil {
					t.Fatalf("restore %s: %v", deleted, err)
				}
				setMtime(t, deleted, fileTime)
				setMtime(t, mainDir, dirTime)
				if code != 1 {
					t.Errorf("make -q %s after deleting %s = %d, want 1 (rebuild)", h.target, deleted, code)
				}
			})
			// A binary built before its dependency file existed, such as a
			// helper left on a host from before b.q6s, must not be trusted.
			t.Run("no_dependency_file", func(t *testing.T) {
				depFile := filepath.Join(root, h.target+".d")
				data, readErr := os.ReadFile(depFile)
				if readErr == nil {
					if err := os.Remove(depFile); err != nil {
						t.Fatalf("remove %s: %v", depFile, err)
					}
				}
				code := makeQ(t, root, h.target)
				if readErr == nil {
					if err := os.WriteFile(depFile, data, 0o644); err != nil {
						t.Fatalf("restore %s: %v", depFile, err)
					}
				}
				if code != 1 {
					t.Errorf("make -q %s without %s = %d, want 1 (rebuild)", h.target, depFile, code)
				}
			})
		})
	}
}
