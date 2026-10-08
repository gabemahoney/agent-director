package spawn

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
)

// TestPreTrustWritesThroughSymlink pins b.6rh: a symlinked .claude.json stays a
// link, its resolved file gets the entry, the lock is the literal <link>.lock.
func TestPreTrustWritesThroughSymlink(t *testing.T) {
	const cwd = "/tmp/symlink-cwd"
	cases := []struct {
		name   string
		home   bool        // the link is the stubbed $HOME/.claude.json, not CLAUDE_CONFIG_DIR's
		links  [][2]string // each link under the dir and what it holds
		target string      // the regular file the links resolve to, under the dir
	}{
		{name: "relative link beside it",
			links: [][2]string{{".claude.json", "real.json"}}, target: "real.json"},
		{name: "relative link into another dir",
			links: [][2]string{{".claude.json", "dotfiles/claude.json"}}, target: "dotfiles/claude.json"},
		{name: "home link, absolute into another dir", home: true,
			links: [][2]string{{".claude.json", "$DIR/dotfiles/claude.json"}}, target: "dotfiles/claude.json"},
		{name: "chain across dirs, absolute then relative to its own dir",
			links:  [][2]string{{".claude.json", "$DIR/mid/claude.json"}, {"mid/claude.json", "../dotfiles/claude.json"}},
			target: "dotfiles/claude.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, env := t.TempDir(), map[string]string(nil)
			if tc.home {
				dir = filepath.Dir(withStubClaudeJSON(t))
			} else {
				env = map[string]string{"CLAUDE_CONFIG_DIR": dir}
			}
			seedSymlinks(t, dir, tc.target, tc.links)
			want := []string{tc.target} // the whole tree: target, links and their dirs
			for _, l := range tc.links {
				want = append(want, l[0])
			}
			for _, p := range want {
				if d := filepath.Dir(p); d != "." && !slices.Contains(want, d) {
					want = append(want, d)
				}
			}
			target := filepath.Join(dir, tc.target)
			before, err := os.Stat(target)
			if err != nil {
				t.Fatalf("stat %s: %v", tc.target, err)
			}
			var underLock []string
			onBeforeCommit(t, func() { underLock = treeEntries(t, dir) })
			warn := capturePreTrustWarn(t)

			if got := PreTrust(cwd, env, false, config.PreTrust{}); got != PreTrustOK {
				t.Fatalf("PreTrust = %q (warning %q); want ok", got, warn)
			}
			for _, l := range tc.links {
				assertSymlink(t, filepath.Join(dir, l[0]), strings.ReplaceAll(l[1], "$DIR", dir))
			}
			if got := readClaudeJSON(t, target); !trusts(got, cwd) || got["userID"] != "u" {
				t.Errorf("%s = %v; want projects[%q] trusted and userID kept", tc.target, got, cwd)
			}
			// Atomic: a rename puts a new file (inode) at the target; an
			// in-place write through the link would keep the old one.
			if after, err := os.Stat(target); err != nil {
				t.Errorf("stat %s after: %v", tc.target, err)
			} else if os.SameFile(before, after) {
				t.Errorf("%s is the same file as before; want a new file renamed into place (atomic write)", tc.target)
			}
			if wantLocked := sorted(append(slices.Clone(want), ".claude.json.lock")); !reflect.DeepEqual(underLock, wantLocked) {
				t.Errorf("entries under the lock = %q; want %q (only the link's own lock)", underLock, wantLocked)
			}
			assertTree(t, dir, want)
		})
	}
}

// TestPreTrustUnresolvableSymlinkWritesNothing pins b.6rh: a link that stops
// resolving before the write fails pre-trust, kept as is, and leaves no file.
func TestPreTrustUnresolvableSymlinkWritesNothing(t *testing.T) {
	cases := []struct {
		name string
		loop bool // the target is replaced by a link back to .claude.json; else removed
	}{
		{name: "target removed"},
		{name: "target turned into a loop", loop: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			link, target := filepath.Join(dir, ".claude.json"), filepath.Join(dir, "dotfiles", "claude.json")
			seedSymlinks(t, dir, "dotfiles/claude.json", [][2]string{{".claude.json", "dotfiles/claude.json"}})
			onBeforeCommit(t, func() {
				if err := os.Remove(target); err != nil {
					t.Fatalf("remove target: %v", err)
				}
				if tc.loop {
					if err := os.Symlink(link, target); err != nil {
						t.Fatalf("symlink: %v", err)
					}
				}
			})
			warn := capturePreTrustWarn(t)

			if got := PreTrust("/tmp/unresolvable-cwd", map[string]string{"CLAUDE_CONFIG_DIR": dir}, false, config.PreTrust{}); got != PreTrustFailed {
				t.Fatalf("PreTrust = %q; want failed", got)
			}
			assertOneFailedLine(t, warn.String(), link, "resolve symlink")
			assertSymlink(t, link, "dotfiles/claude.json")
			want := []string{".claude.json", "dotfiles"}
			if tc.loop {
				want = append(want, "dotfiles/claude.json")
			}
			assertTree(t, dir, want)
		})
	}
}

// seedSymlinks makes target under dir, seeded with lockTestSeed, and each link
// (its path under dir, and what it holds with "$DIR" standing for dir).
func seedSymlinks(t *testing.T, dir, target string, links [][2]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(target)), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seedFile(t, filepath.Join(dir, target), lockTestSeed)
	for _, l := range links {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(l[0])), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.Symlink(strings.ReplaceAll(l[1], "$DIR", dir), filepath.Join(dir, l[0])); err != nil {
			t.Fatalf("symlink: %v", err)
		}
	}
}

// onBeforeCommit runs f as preTrustBeforeCommit, under the lock just before the
// write, for the test's life.
func onBeforeCommit(t *testing.T, f func()) {
	t.Helper()
	saved := preTrustBeforeCommit
	preTrustBeforeCommit = f
	t.Cleanup(func() { preTrustBeforeCommit = saved })
}

// assertSymlink fails unless path is still a symlink holding dest.
func assertSymlink(t *testing.T, path, dest string) {
	t.Helper()
	info, err := os.Lstat(path)
	switch {
	case err != nil:
		t.Errorf("lstat %s: %v; want a symlink still", path, err)
		return
	case info.Mode()&os.ModeSymlink == 0:
		t.Errorf("%s has mode %s; want a symlink still", path, info.Mode())
		return
	}
	if got, err := os.Readlink(path); err != nil || got != dest {
		t.Errorf("readlink %s = %q, %v; want %q", path, got, err, dest)
	}
}

// assertTree fails unless dir holds exactly the entries want (paths relative
// to dir), so no temp file or lock dir is left anywhere in it.
func assertTree(t *testing.T, dir string, want []string) {
	t.Helper()
	if got := treeEntries(t, dir); !reflect.DeepEqual(got, sorted(want)) {
		t.Errorf("entries under %s = %q; want %q", dir, got, sorted(want))
	}
}

// treeEntries lists every entry under dir, links not followed, relative to dir
// and sorted.
func treeEntries(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		out = append(out, rel)
		return err
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return sorted(out)
}

// sorted returns a sorted copy of s.
func sorted(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}
