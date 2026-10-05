package store

// "~/" store path expansion (b.hvf, b.4uz): Open and OpenOrInit resolve "~/"
// against $HOME and refuse it with errNoHome when HOME is unset or empty,
// never falling back to the passwd home. The package's sandbox-guarded
// TestMain in store_test.go covers this file.

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

// tildeHome points HOME at a fresh temp dir and returns it, a unique "~/"-relative db path and
// stray, that path's passwd-home expansion, which cleanup removes so a failing run leaves nothing.
func tildeHome(t *testing.T) (home, rel, stray string) {
	t.Helper()
	dir := fmt.Sprintf("ad-store-b.hvf-%d-%d", os.Getpid(), time.Now().UnixNano())
	u, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current: %v", err)
	}
	stray = filepath.Join(u.HomeDir, dir)
	if _, err := os.Lstat(stray); !filepath.IsAbs(u.HomeDir) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("refusing to use %s (Lstat: %v); want an absent path under an absolute passwd home", stray, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stray) })
	home = t.TempDir()
	t.Setenv("HOME", home)
	return home, filepath.Join(dir, "state.db"), stray
}

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

// TestTildeStorePathUsesHOME: Open and OpenOrInit of "~/<rel>" reach the store
// at $HOME/<rel>, not the passwd home's.
func TestTildeStorePathUsesHOME(t *testing.T) {
	for _, tc := range []struct {
		name string
		seed bool // create the store at $HOME/<rel> first (Open never creates)
		open func(string) (*Store, error)
	}{
		{"OpenOrInit", false, OpenOrInit},
		{"Open", true, Open},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, rel, _ := tildeHome(t)
			abs := filepath.Join(home, rel)
			if tc.seed {
				openStoreID(t, OpenOrInit, abs)
			}
			got := openStoreID(t, tc.open, "~/"+rel)
			if _, err := os.Stat(abs); err != nil {
				t.Fatalf("%s(~/%s) with HOME=%s left no store under $HOME: %v", tc.name, rel, home, err)
			}
			if want := openStoreID(t, Open, abs); got != want {
				t.Errorf("%s(~/%s) store id = %q; the store at %s has %q", tc.name, rel, got, abs, want)
			}
		})
	}
}

// TestTildeStorePathWithoutHOMERefused: with HOME empty or unset, Open and OpenOrInit of
// "~/<rel>" fail with errNoHome and create nothing under the passwd home or the cwd (b.4uz).
func TestTildeStorePathWithoutHOMERefused(t *testing.T) {
	for _, open := range []struct {
		name string
		fn   func(string) (*Store, error)
	}{
		{"OpenOrInit", OpenOrInit},
		{"Open", Open},
	} {
		for _, unset := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/HOME unset=%t", open.name, unset), func(t *testing.T) {
				cwd := cwdfix.Temp(t)
				_, rel, stray := tildeHome(t)
				setHome(t, "", unset)

				s, err := open.fn("~/" + rel)
				if s != nil {
					_ = s.Close()
				}
				if !errors.Is(err, errNoHome) {
					t.Errorf("%s(~/%s) error = %v; want one wrapping errNoHome", open.name, rel, err)
				}
				if _, err := os.Lstat(stray); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("%s(~/%s) created %s under the passwd home (Lstat: %v)", open.name, rel, stray, err)
				}
				if entries, err := os.ReadDir(cwd); err != nil || len(entries) != 0 {
					t.Errorf("cwd %s holds %v (err %v); want nothing created there", cwd, entries, err)
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
