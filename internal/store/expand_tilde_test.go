package store

// "~/" store path expansion (b.hvf, b.4uz): Open and OpenOrInit resolve "~/"
// against $HOME and refuse it with errNoHome when HOME is unset or empty,
// never falling back to the passwd home. They refuse "" with errEmptyPath (b.8up).

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/cwdfix"
)

// setHome sets HOME to home for the test, or unsets it; the original is
// restored at cleanup.
func setHome(t *testing.T, home string, unset bool) {
	t.Helper()
	t.Setenv("HOME", home)
	if unset {
		if err := os.Unsetenv("HOME"); err != nil {
			t.Fatalf("Unsetenv HOME: %v", err)
		}
	}
}

// tildeRel returns a unique "~/"-relative store path and stray, its expansion
// under the passwd home, which cleanup removes so a failing run leaves nothing.
func tildeRel(t *testing.T) (rel, stray string) {
	t.Helper()
	u, err := user.Current()
	if err != nil || !filepath.IsAbs(u.HomeDir) {
		t.Fatalf("user.Current = %+v, %v; want an absolute passwd home", u, err)
	}
	dir := fmt.Sprintf("ad-store-b.hvf-%d-%d", os.Getpid(), time.Now().UnixNano())
	stray = filepath.Join(u.HomeDir, dir)
	if _, err := os.Lstat(stray); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("refusing to use %s (Lstat: %v); want an absent path", stray, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stray) })
	return filepath.Join(dir, "state.db"), stray
}

// TestTildeStorePathUsesHOME: Open and OpenOrInit of "~/<rel>" reach the store
// at $HOME/<rel>, not the passwd home's; OpenOrInit, with nothing under $HOME,
// creates it there.
func TestTildeStorePathUsesHOME(t *testing.T) {
	for name, open := range map[string]func(string) (*Store, error){"OpenOrInit": OpenOrInit, "Open": Open} {
		rel, _ := tildeRel(t)
		setHome(t, t.TempDir(), false)
		abs := filepath.Join(os.Getenv("HOME"), rel)
		if name == "Open" {
			openStoreID(t, OpenOrInit, abs) // Open never creates, so the store exists first
		}
		got := openStoreID(t, open, "~/"+rel)
		if want := openStoreID(t, Open, abs); got != want {
			t.Errorf("%s(~/%s) store id = %q; the store at %s has %q", name, rel, got, abs, want)
		}
	}
}

// TestRefusedStorePathCreatesNothing: Open and OpenOrInit refuse "~/<rel>"
// with errNoHome when HOME is empty or unset, never falling back to the passwd
// home (b.4uz), and refuse "" with errEmptyPath (b.8up). Either way they
// create nothing: not in the cwd, not at the "~/" path's passwd-home
// expansion, and not under a set HOME.
func TestRefusedStorePathCreatesNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		// tilde opens "~/<rel>" with HOME empty, or unset if unset is also
		// true; otherwise "" is opened with HOME a fresh temp dir.
		tilde, unset bool
		wantErr      error
	}{
		{"tilde HOME empty", true, false, errNoHome},
		{"tilde HOME unset", true, true, errNoHome},
		{"empty path HOME set", false, false, errEmptyPath},
	} {
		for name, open := range map[string]func(string) (*Store, error){"OpenOrInit": OpenOrInit, "Open": Open} {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				cwd := cwdfix.Temp(t)
				var path, home, stray string
				empty := []string{cwd} // dirs that must stay empty
				if tc.tilde {
					var rel string
					rel, stray = tildeRel(t)
					path = "~/" + rel
				} else {
					home = t.TempDir()
					empty = append(empty, home)
				}
				setHome(t, home, tc.unset)
				s, err := open(path)
				if s != nil {
					_ = s.Close()
				}
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("%s(%q) error = %v; want one wrapping %v", name, path, err, tc.wantErr)
				}
				if tc.tilde {
					if _, err := os.Lstat(stray); !errors.Is(err, fs.ErrNotExist) {
						t.Errorf("%s(%q) created %s under the passwd home (Lstat: %v)", name, path, stray, err)
					}
				}
				for _, dir := range empty {
					if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
						t.Errorf("%s(%q): %s holds %v (err %v); want nothing created there", name, path, dir, entries, err)
					}
				}
			})
		}
	}
}

// TestExpandTilde: "~/" joins $HOME and is refused with errNoHome when HOME is
// unset or empty; any other path is returned unchanged, with or without HOME.
func TestExpandTilde(t *testing.T) {
	const home = "/b-hvf/home"
	for _, tc := range []struct {
		name, home, path, want string
		unset                  bool
		wantErr                error
	}{
		{"tilde joins HOME", home, "~/a/x.db", "/b-hvf/home/a/x.db", false, nil},
		{"empty HOME refused", "", "~/a/x.db", "", false, errNoHome},
		{"unset HOME refused", "", "~/a/x.db", "", true, errNoHome},
		{"absolute unchanged", home, "/abs/x.db", "/abs/x.db", false, nil},
		{"relative unchanged", home, "rel/x.db", "rel/x.db", false, nil},
		{"tilde-user unchanged", home, "~other/x.db", "~other/x.db", false, nil},
		{"absolute unchanged without HOME", "", "/abs/x.db", "/abs/x.db", true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setHome(t, tc.home, tc.unset)
			got, err := expandTilde(tc.path)
			if got != tc.want || !errors.Is(err, tc.wantErr) { // a nil wantErr wants a nil err
				t.Errorf("expandTilde(%q) = %q, %v; want %q, %v", tc.path, got, err, tc.want, tc.wantErr)
			}
		})
	}
}
