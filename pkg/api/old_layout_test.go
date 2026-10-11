package api_test

// old_layout_test.go is SR-11.7's "Which directory is there" through api.New:
// the one table of home trees that api.New refuses with ErrOldLayout, leaving
// the tree byte for byte as it was, and the ones it does not refuse.

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// oldLayoutDir is the old directory's name under HOME: an old-layout fixture.
const oldLayoutDir = ".agent-director"

// snapTree describes every entry under root without following links: its
// mode, and its bytes or its link target.
func snapTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		detail := ""
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			detail, err = os.Readlink(p)
		case info.Mode().IsRegular():
			var data []byte
			data, err = os.ReadFile(p)
			detail = string(data)
		}
		rel, _ := filepath.Rel(root, p)
		out[rel] = fmt.Sprintf("%v %s", info.Mode(), detail)
		return err
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return out
}

// trailSize is the size of the shared api trail file, -1 while it is absent.
func trailSize(t *testing.T) int64 {
	t.Helper()
	info, err := os.Stat(apiTrailFilePath())
	if errors.Is(err, os.ErrNotExist) {
		return -1
	}
	if err != nil {
		t.Fatalf("stat trail: %v", err)
	}
	return info.Size()
}

// oldLayoutHome is the fixture helpers' view of one throwaway HOME.
type oldLayoutHome string

func (h oldLayoutHome) old() string { return filepath.Join(string(h), oldLayoutDir) }
func (h oldLayoutHome) cfg() string { return adConfigDir(string(h)) }

// oldWithStore makes the old directory, holding a store.
func (h oldLayoutHome) oldWithStore(t *testing.T) {
	apitest.SeedStore(t, filepath.Join(h.old(), "state.db"))
}

// mkdirAll makes dir, with its parents.
func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

// symlink makes the link name pointing at target.
func symlink(t *testing.T, target, name string) {
	t.Helper()
	mkdirAll(t, filepath.Dir(name))
	if err := os.Symlink(target, name); err != nil {
		t.Fatalf("symlink %s: %v", name, err)
	}
}

// TestNewOldLayout: with the default store about to be opened, api.New refuses
// with ErrOldLayout when the old directory is present (anything at its name,
// but a link to ~/.config/agent-director) and no store exists in the new
// one, and then touches nothing; every other tree gets today's result.
func TestNewOldLayout(t *testing.T) {
	// Serial: it sets HOME with t.Setenv and compares the shared trail file's size.
	const installPhrase = "run this release's install.sh"
	rows := []struct {
		name      string
		create    bool
		build     func(t *testing.T, h oldLayoutHome) api.Options // the tree, and the options when not just the create flag
		refused   bool
		wantErr   error // when not refused
		wantStore bool  // a store at the default path afterwards, when not refused
	}{
		{name: "old dir holding a store", refused: true,
			build: func(t *testing.T, h oldLayoutHome) api.Options { h.oldWithStore(t); return api.Options{} }},
		{name: "old dir holding a store, CreateIfMissing", create: true, refused: true,
			build: func(t *testing.T, h oldLayoutHome) api.Options { h.oldWithStore(t); return api.Options{} }},
		{name: "old name is a plain file", create: true, refused: true,
			build: func(t *testing.T, h oldLayoutHome) api.Options {
				if err := os.WriteFile(h.old(), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				return api.Options{}
			}},
		{name: "dangling link to elsewhere", create: true, refused: true,
			build: func(t *testing.T, h oldLayoutHome) api.Options { symlink(t, "gone", h.old()); return api.Options{} }},
		{name: "link to another directory", create: true, refused: true,
			build: func(t *testing.T, h oldLayoutHome) api.Options {
				mkdirAll(t, filepath.Join(string(h), "elsewhere"))
				symlink(t, filepath.Join(string(h), "elsewhere"), h.old())
				return api.Options{}
			}},
		{name: "new dir without a store, old dir present", create: true, refused: true,
			build: func(t *testing.T, h oldLayoutHome) api.Options {
				mkdirAll(t, h.old())
				mkdirAll(t, h.cfg())
				return api.Options{}
			}},

		{name: "compatibility link, target absent", wantErr: store.ErrStoreNotInitialized,
			build: func(t *testing.T, h oldLayoutHome) api.Options { symlink(t, h.cfg(), h.old()); return api.Options{} }},
		{name: "compatibility link, target absent, CreateIfMissing", create: true, wantStore: true,
			build: func(t *testing.T, h oldLayoutHome) api.Options { symlink(t, h.cfg(), h.old()); return api.Options{} }},
		{name: "compatibility link, target present", wantErr: store.ErrStoreNotInitialized,
			build: func(t *testing.T, h oldLayoutHome) api.Options {
				mkdirAll(t, h.cfg())
				symlink(t, h.cfg(), h.old())
				return api.Options{}
			}},
		{name: "compatibility link, target present, CreateIfMissing", create: true, wantStore: true,
			build: func(t *testing.T, h oldLayoutHome) api.Options {
				mkdirAll(t, h.cfg())
				symlink(t, h.cfg(), h.old())
				return api.Options{}
			}},
		{name: "compatibility link with a relative target, CreateIfMissing", create: true, wantStore: true,
			build: func(t *testing.T, h oldLayoutHome) api.Options {
				symlink(t, filepath.Join(".config", "agent-director"), h.old())
				return api.Options{}
			}},
		{name: "both dirs, store in the new one", wantStore: true,
			build: func(t *testing.T, h oldLayoutHome) api.Options {
				h.oldWithStore(t)
				apitest.SeedStore(t, filepath.Join(h.cfg(), "state.db"))
				return api.Options{}
			}},
		{name: "new dir is a link to a directory holding the store", wantStore: true,
			build: func(t *testing.T, h oldLayoutHome) api.Options {
				h.oldWithStore(t)
				apitest.SeedStore(t, filepath.Join(string(h), "moved", "state.db"))
				symlink(t, filepath.Join(string(h), "moved"), h.cfg())
				return api.Options{}
			}},
		{name: "StorePath set", create: true,
			build: func(t *testing.T, h oldLayoutHome) api.Options {
				h.oldWithStore(t)
				return api.Options{StorePath: filepath.Join(t.TempDir(), "s.db")}
			}},
		{name: "db_path set in the new config",
			build: func(t *testing.T, h oldLayoutHome) api.Options {
				h.oldWithStore(t)
				apitest.SeedStore(t, filepath.Join(string(h), "named.db"))
				writeConfig(t, filepath.Join(h.cfg(), "config.toml"), "[store]\ndb_path = \"~/named.db\"\n")
				return api.Options{}
			}},
		{name: "db_path set to the default path itself", create: true, wantStore: true,
			build: func(t *testing.T, h oldLayoutHome) api.Options {
				h.oldWithStore(t)
				writeConfig(t, filepath.Join(h.cfg(), "config.toml"), "[store]\ndb_path = \"~/.config/agent-director/state.db\"\n")
				return api.Options{}
			}},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			opts := tc.build(t, oldLayoutHome(home))
			opts.CreateIfMissing = tc.create
			before, trailBefore := snapTree(t, home), trailSize(t)

			c, err := api.New(opts)
			if c != nil {
				_ = c.Close()
			}
			if tc.refused {
				if c != nil {
					t.Error("New returned a Client; want none")
				}
				adviceAssertAdvice(t, err, api.ErrOldLayout, installPhrase)
				if after := snapTree(t, home); !maps.Equal(before, after) {
					t.Errorf("home tree changed by the refusal:\nbefore %v\nafter  %v", before, after)
				}
				if got := trailSize(t); got != trailBefore {
					t.Errorf("trail size %d -> %d; want no line written", trailBefore, got)
				}
				return
			}
			if errors.Is(err, api.ErrOldLayout) || !errors.Is(err, tc.wantErr) || (err == nil) != (tc.wantErr == nil) {
				t.Fatalf("New = %v; want %v", err, tc.wantErr)
			}
			if _, statErr := os.Stat(filepath.Join(adConfigDir(home), "state.db")); (statErr == nil) != tc.wantStore {
				t.Errorf("default store: stat %v; want it present: %v", statErr, tc.wantStore)
			}
		})
	}
}
