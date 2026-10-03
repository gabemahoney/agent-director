package store

// "~/" store path expansion (b.hvf): Open and OpenOrInit resolve "~/" against
// $HOME, falling back to the passwd home only when HOME is unset or empty.
// The package's sandbox-guarded TestMain in store_test.go covers this file.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"testing"
	"time"
)

// tildeHome points HOME at a fresh temp dir and returns it with a unique
// "~/"-relative db path; at cleanup it removes what a passwd-home expansion
// of that path would have created, so a failing run leaves nothing behind.
func tildeHome(t *testing.T) (home, rel string) {
	t.Helper()
	dir := fmt.Sprintf("ad-store-b.hvf-%d-%d", os.Getpid(), time.Now().UnixNano())
	u, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current: %v", err)
	}
	stray := filepath.Join(u.HomeDir, dir)
	if _, err := os.Lstat(stray); !filepath.IsAbs(u.HomeDir) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("refusing to use %s (Lstat: %v); want an absent path under an absolute passwd home", stray, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stray) })
	home = t.TempDir()
	t.Setenv("HOME", home)
	return home, filepath.Join(dir, "state.db")
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
			home, rel := tildeHome(t)
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

// TestExpandTilde: "~/" joins $HOME, or the passwd home when HOME is unset or
// empty; any other path is returned unchanged.
func TestExpandTilde(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current: %v", err)
	}
	const home = "/b-hvf/home"
	for _, tc := range []struct {
		name, home, path, want string
		unset                  bool
	}{
		{"tilde joins HOME", home, "~/a/x.db", "/b-hvf/home/a/x.db", false},
		{"empty HOME falls back to passwd home", "", "~/a/x.db", filepath.Join(u.HomeDir, "a/x.db"), false},
		{"unset HOME falls back to passwd home", "", "~/a/x.db", filepath.Join(u.HomeDir, "a/x.db"), true},
		{"absolute unchanged", home, "/abs/x.db", "/abs/x.db", false},
		{"relative unchanged", home, "rel/x.db", "rel/x.db", false},
		{"tilde-user unchanged", home, "~other/x.db", "~other/x.db", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", tc.home) // restores the original HOME at cleanup
			if tc.unset {
				if err := os.Unsetenv("HOME"); err != nil {
					t.Fatalf("Unsetenv HOME: %v", err)
				}
			}
			got, err := expandTilde(tc.path)
			if err != nil || got != tc.want {
				t.Errorf("expandTilde(%q) = %q, %v; want %q, nil", tc.path, got, err, tc.want)
			}
		})
	}
}
