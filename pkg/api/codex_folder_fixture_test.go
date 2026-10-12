package api_test

// codex_folder_fixture_test.go is the shared Codex-folder fixture of the pkg/api
// tests (the pre-trust tests, and any test that checks a Codex launch leaves
// Codex's folder alone): a per-test folder named by an absolute, a relative or
// a "~"-prefixed CODEX_HOME, or by none (Codex's default .codex under the test
// HOME), seeded with stand-in config.toml, hooks.json, rules/ and auth files; a
// whole-tree snapshot (paths, modes, bytes) of the folder and of every other
// place the name could resolve to; and one compare helper. It never touches the
// real ~/.codex. A test that sets HOME or the working directory is
// "// Serial:"; the fixture itself sets neither, except through
// seedRelativeCodexFolder, which moves the working directory (cwdfix.Temp).

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/cwdfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// codexSeedFiles is what a Codex folder holds as seeded: path under the folder,
// content, mode. An empty directory is a path ending in "/".
var codexSeedFiles = []struct {
	path, content string
	mode          os.FileMode
}{
	{"config.toml", "model = \"stand-in\"\n\n[tui]\ntheme = \"dark\"\n", 0o644},
	{"hooks.json", `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo user-hook"}]}]}}`, 0o644},
	{"rules/default.rules", "prefix_rule(pattern=[\"ls\"], decision=\"allow\")\n", 0o644},
	{"auth.json", `{"OPENAI_API_KEY":"stand-in-not-a-key"}`, 0o600},
	{"sessions/", "", 0o755},
}

// codexFolder is a seeded Codex folder: named is CODEX_HOME as the extra env
// gives it ("" leaves it unset), dir where the folder is, and snap the tree of
// every watched root as seeded.
type codexFolder struct {
	named, dir string
	watch      []string
	snap       treeSnapshot
}

// seedCodexFolder seeds an absolute CODEX_HOME at dir (made as needed).
func seedCodexFolder(t *testing.T, dir string) codexFolder {
	t.Helper()
	return newCodexFolder(t, dir, dir)
}

// seedDefaultCodexFolder seeds Codex's default folder, .codex under home (the
// test's HOME), and leaves CODEX_HOME unset.
func seedDefaultCodexFolder(t *testing.T, home string) codexFolder {
	t.Helper()
	return newCodexFolder(t, "", filepath.Join(home, ".codex"))
}

// seedRelativeCodexFolder seeds the relative CODEX_HOME "rel": the process
// moves into a fresh temp dir until the test ends (cwdfix.Temp; not parallel)
// and the folder is seeded in its rel, where "rel" resolves from there. The
// whole working directory is watched.
func seedRelativeCodexFolder(t *testing.T) codexFolder {
	t.Helper()
	wd := cwdfix.Temp(t)
	f := newCodexFolder(t, "rel", filepath.Join(wd, "rel"), wd)
	return f
}

// seedTildeCodexFolder seeds the "~"-prefixed CODEX_HOME "~/codex": nothing
// expands it, so it names a relative path "~/codex" under the working directory
// (cwdfix.Temp; not parallel), seeded there, while home/codex, where a shell
// would send it, is seeded too. The working directory and home/codex are watched.
func seedTildeCodexFolder(t *testing.T, home string) codexFolder {
	t.Helper()
	wd := cwdfix.Temp(t)
	seedCodexTree(t, filepath.Join(home, "codex"))
	return newCodexFolder(t, "~/codex", filepath.Join(wd, "~", "codex"), wd, filepath.Join(home, "codex"))
}

// seedWhitespaceCodexFolder seeds the whitespace-only CODEX_HOME "   ", which is
// no usable path: the folder is seeded at the working directory's "   "
// (cwdfix.Temp; not parallel), where Codex would resolve it.
func seedWhitespaceCodexFolder(t *testing.T) codexFolder {
	t.Helper()
	wd := cwdfix.Temp(t)
	return newCodexFolder(t, "   ", filepath.Join(wd, "   "), wd)
}

// newCodexFolder seeds dir, then snapshots it and every extra watched root.
func newCodexFolder(t *testing.T, named, dir string, extra ...string) codexFolder {
	t.Helper()
	seedCodexTree(t, dir)
	f := codexFolder{named: named, dir: dir, watch: append([]string{dir}, extra...)}
	f.snap = snapshotTrees(t, f.watch)
	return f
}

// seedCodexTree writes codexSeedFiles under dir.
func seedCodexTree(t *testing.T, dir string) {
	t.Helper()
	for _, s := range codexSeedFiles {
		p := filepath.Join(dir, s.path)
		if strings.HasSuffix(s.path, "/") {
			if err := os.MkdirAll(p, s.mode); err != nil {
				t.Fatalf("mkdir %s: %v", p, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, []byte(s.content), s.mode); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		if err := os.Chmod(p, s.mode); err != nil {
			t.Fatalf("chmod %s: %v", p, err)
		}
	}
}

// extraEnv is the extra env giving a launch's CODEX_HOME as f names it (none
// when f is the default folder).
func (f codexFolder) extraEnv() map[string]string {
	if f.named == "" {
		return map[string]string{}
	}
	return map[string]string{"CODEX_HOME": f.named}
}

// env is the seed option giving a row's CODEX_HOME as f names it.
func (f codexFolder) env() apitest.SpawnOption { return apitest.WithExtraEnv(f.extraEnv()) }

// assertUnchanged fails, naming the paths, unless every watched root is
// exactly as seeded: no path added, removed or changed in kind, mode or bytes.
func (f codexFolder) assertUnchanged(t *testing.T, when string) {
	t.Helper()
	if diff := f.snap.diff(snapshotTrees(t, f.watch)); len(diff) > 0 {
		t.Errorf("%s: the Codex folder %q changed: %s", when, f.named, strings.Join(diff, "; "))
	}
}

// treeEntry is one path of a snapshot: its type and mode, its bytes when a
// regular file, its target when a link.
type treeEntry struct {
	mode   fs.FileMode
	bytes  []byte
	target string
}

// treeSnapshot is every path under some roots, keyed by path.
type treeSnapshot map[string]treeEntry

// snapshotTrees records every path under each root (the root included), without
// following links. A root that does not exist records nothing.
func snapshotTrees(t *testing.T, roots []string) treeSnapshot {
	t.Helper()
	snap := treeSnapshot{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			info, err := os.Lstat(p)
			if err != nil {
				return err
			}
			e := treeEntry{mode: info.Mode()}
			switch {
			case info.Mode().IsRegular():
				if e.bytes, err = os.ReadFile(p); err != nil {
					return err
				}
			case info.Mode()&fs.ModeSymlink != 0:
				if e.target, err = os.Readlink(p); err != nil {
					return err
				}
			}
			snap[p] = e
			return nil
		})
		if err != nil {
			t.Fatalf("snapshot %s: %v", root, err)
		}
	}
	return snap
}

// diff lists, sorted, each path that is added, removed or changed from s to now.
func (s treeSnapshot) diff(now treeSnapshot) []string {
	var out []string
	for p, was := range s {
		cur, ok := now[p]
		switch {
		case !ok:
			out = append(out, "removed "+p)
		case cur.mode != was.mode:
			out = append(out, "mode of "+p+" "+was.mode.String()+" -> "+cur.mode.String())
		case !bytes.Equal(cur.bytes, was.bytes) || cur.target != was.target:
			out = append(out, "content of "+p)
		}
	}
	for p := range now {
		if _, ok := s[p]; !ok {
			out = append(out, "added "+p)
		}
	}
	sort.Strings(out)
	return out
}

// captureStderr runs fn with file descriptor 2 pointed at a pipe and returns what
// was written to it: where a launch's pre-trust warning goes (the warning's
// writer is os.Stderr as the process started, so replacing os.Stderr would not
// catch it). Process-wide, so the test is "// Serial:".
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved, err := syscall.Dup(2)
	if err != nil {
		t.Fatalf("dup stderr: %v", err)
	}
	if err := dupOnto(int(w.Fd()), 2); err != nil {
		t.Fatalf("redirect stderr: %v", err)
	}
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	func() {
		defer func() {
			if err := dupOnto(saved, 2); err != nil {
				t.Errorf("restore stderr: %v", err)
			}
			_ = syscall.Close(saved)
			_ = w.Close()
		}()
		fn()
	}()
	out := <-done
	_ = r.Close()
	return out
}
