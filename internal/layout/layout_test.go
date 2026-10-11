package layout_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/layout"
	"github.com/gabemahoney/agent-director/internal/testsupport/cwdfix"
)

// The paths below are written out literally, not built from the package's own
// constants, so a wrong constant fails here instead of agreeing with itself.

// TestDirAndJoin: AD's directory is <home>/.config/agent-director and entries
// join under it; with an empty home the path is relative, never rooted.
func TestDirAndJoin(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"Dir", layout.Dir("/home/u"), "/home/u/.config/agent-director"},
		{"Dir empty home is relative", layout.Dir(""), ".config/agent-director"},
		{"Join no elements", layout.Join("/home/u"), "/home/u/.config/agent-director"},
		{"Join store", layout.Join("/home/u", layout.StoreFile), "/home/u/.config/agent-director/state.db"},
		{"Join nested", layout.Join("/home/u", layout.TemplatesDir, "t.toml"), "/home/u/.config/agent-director/templates/t.toml"},
		{"Join help-hook binary", layout.Join("/home/u", layout.BinDir, layout.BinaryName), "/home/u/.config/agent-director/bin/agent-director"},
		{"Join empty home is relative", layout.Join("", layout.TrailFile), ".config/agent-director/ad-trail.jsonl"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q; want %q", tc.name, tc.got, tc.want)
		}
	}
}

// TestNames pins the entry names and the "~/" display forms that descriptions,
// flag help and the version result's config_dir print.
func TestNames(t *testing.T) {
	for _, tc := range []struct{ name, got, want string }{
		{"DirRel", layout.DirRel, ".config/agent-director"},
		{"Display", layout.Display, "~/.config/agent-director"},
		{"ConfigFile", layout.ConfigFile, "config.toml"},
		{"StoreFile", layout.StoreFile, "state.db"},
		{"ErrorLogFile", layout.ErrorLogFile, "errors.log"},
		{"TrailFile", layout.TrailFile, "ad-trail.jsonl"},
		{"TemplatesDir", layout.TemplatesDir, "templates"},
		{"BinDir", layout.BinDir, "bin"},
		{"BinaryName", layout.BinaryName, "agent-director"},
		{"DisplayConfigPath", layout.DisplayConfigPath, "~/.config/agent-director/config.toml"},
		{"DisplayStorePath", layout.DisplayStorePath, "~/.config/agent-director/state.db"},
		{"DisplayErrorLogPath", layout.DisplayErrorLogPath, "~/.config/agent-director/errors.log"},
		{"DisplayTemplatesDir", layout.DisplayTemplatesDir, "~/.config/agent-director/templates"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q; want %q", tc.name, tc.got, tc.want)
		}
	}
}

// oldName is the pre-move directory name the presence rules examine.
const oldName = ".agent-director"

// mkdir creates dir (and parents) under home.
func mkdir(t *testing.T, home string, elem ...string) string {
	t.Helper()
	p := filepath.Join(append([]string{home}, elem...)...)
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

// symlink creates name under home as a link holding target verbatim.
func symlink(t *testing.T, home, name, target string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(home, name)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, name)); err != nil {
		t.Fatal(err)
	}
}

// writeFile creates a regular file under home, parents included.
func writeFile(t *testing.T, home, name string) {
	t.Helper()
	p := filepath.Join(home, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// presenceCase builds a home tree and states the verdict of each rule on it.
type presenceCase struct {
	name        string
	build       func(t *testing.T, home string)
	oldPresent  bool
	newPresent  bool
	onlyOld     bool
	storeExists bool
}

func presenceCases() []presenceCase {
	newDir := func(t *testing.T, home string) { mkdir(t, home, ".config", "agent-director") }
	return []presenceCase{
		{name: "empty home", build: func(*testing.T, string) {}},
		{name: "old directory only", oldPresent: true, onlyOld: true,
			build: func(t *testing.T, home string) { mkdir(t, home, oldName) }},
		{name: "old regular file only", oldPresent: true, onlyOld: true,
			build: func(t *testing.T, home string) { writeFile(t, home, oldName) }},
		{name: "new directory only", newPresent: true, build: newDir},
		{name: "both directories", oldPresent: true, newPresent: true,
			build: func(t *testing.T, home string) {
				mkdir(t, home, oldName)
				newDir(t, home)
			}},
		{name: "old directory and a store in the new one", oldPresent: true, newPresent: true, storeExists: true,
			build: func(t *testing.T, home string) {
				mkdir(t, home, oldName)
				writeFile(t, home, ".config/agent-director/state.db")
			}},
		{name: "old dangling link elsewhere counts as present", oldPresent: true, onlyOld: true,
			build: func(t *testing.T, home string) { symlink(t, home, oldName, filepath.Join(home, "nowhere")) }},
		{name: "old link to another directory counts as present", oldPresent: true, onlyOld: true,
			build: func(t *testing.T, home string) { symlink(t, home, oldName, mkdir(t, home, "elsewhere")) }},
		{name: "old link into a subdirectory of the new one counts as present", oldPresent: true, newPresent: true,
			build: func(t *testing.T, home string) {
				newDir(t, home)
				symlink(t, home, oldName, filepath.Join(home, ".config", "agent-director", "bin"))
			}},
		{name: "old link to a sibling named alike counts as present", oldPresent: true, onlyOld: true,
			build: func(t *testing.T, home string) {
				mkdir(t, home, ".config", "agent-director-2")
				symlink(t, home, oldName, filepath.Join(home, ".config", "agent-director-2"))
			}},
		{name: "compat link, absolute target, new directory present", newPresent: true,
			build: func(t *testing.T, home string) {
				newDir(t, home)
				symlink(t, home, oldName, filepath.Join(home, ".config", "agent-director"))
			}},
		{name: "compat link, relative target, new directory present", newPresent: true,
			build: func(t *testing.T, home string) {
				newDir(t, home)
				symlink(t, home, oldName, ".config/agent-director")
			}},
		{name: "compat link, unclean relative target", newPresent: true,
			build: func(t *testing.T, home string) {
				newDir(t, home)
				symlink(t, home, oldName, "./.config/../.config/agent-director/")
			}},
		{name: "compat link, target does not exist yet", build: func(t *testing.T, home string) {
			symlink(t, home, oldName, ".config/agent-director")
		}},
		{name: "compat link to a regular file in place of the new directory", build: func(t *testing.T, home string) {
			writeFile(t, home, ".config/agent-director")
			symlink(t, home, oldName, ".config/agent-director")
		}},
		{name: "new directory reached through a link", newPresent: true,
			build: func(t *testing.T, home string) {
				symlink(t, home, ".config/agent-director", mkdir(t, home, "real-ad"))
			}},
		{name: "new name is a dangling link", build: func(t *testing.T, home string) {
			symlink(t, home, ".config/agent-director", filepath.Join(home, "nowhere"))
		}},
		{name: "new name is a regular file", build: func(t *testing.T, home string) {
			writeFile(t, home, ".config/agent-director")
		}},
		{name: "old directory and a regular file at the new name", oldPresent: true, onlyOld: true,
			build: func(t *testing.T, home string) {
				mkdir(t, home, oldName)
				writeFile(t, home, ".config/agent-director")
			}},
		{name: "store file in the new directory", newPresent: true, storeExists: true,
			build: func(t *testing.T, home string) { writeFile(t, home, ".config/agent-director/state.db") }},
		{name: "store link to an existing file", newPresent: true, storeExists: true,
			build: func(t *testing.T, home string) {
				writeFile(t, home, "elsewhere.db")
				symlink(t, home, ".config/agent-director/state.db", filepath.Join(home, "elsewhere.db"))
			}},
		{name: "store link dangling", newPresent: true,
			build: func(t *testing.T, home string) {
				symlink(t, home, ".config/agent-director/state.db", filepath.Join(home, "gone.db"))
			}},
		{name: "store only in the old directory", oldPresent: true, onlyOld: true,
			build: func(t *testing.T, home string) { writeFile(t, home, oldName+"/state.db") }},
	}
}

// TestPresenceRules: each rule's verdict on a table of home trees.
func TestPresenceRules(t *testing.T) {
	for _, tc := range presenceCases() {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			tc.build(t, home)
			if got := layout.OldPresent(home); got != tc.oldPresent {
				t.Errorf("OldPresent = %v; want %v", got, tc.oldPresent)
			}
			if got := layout.NewPresent(home); got != tc.newPresent {
				t.Errorf("NewPresent = %v; want %v", got, tc.newPresent)
			}
			if got := layout.OnlyOldPresent(home); got != tc.onlyOld {
				t.Errorf("OnlyOldPresent = %v; want %v", got, tc.onlyOld)
			}
			if got := layout.StoreExists(home); got != tc.storeExists {
				t.Errorf("StoreExists = %v; want %v", got, tc.storeExists)
			}
		})
	}
}

// TestOldDir: the old directory is <home>/.agent-director.
func TestOldDir(t *testing.T) {
	if got, want := layout.OldDir("/home/u"), "/home/u/.agent-director"; got != want {
		t.Errorf("OldDir = %q; want %q", got, want)
	}
}

// TestRelativeHomeIsAbsent: a home that is not an absolute path has nothing
// present, even when the working directory holds both names, so nothing is
// ever looked up relative to the working directory.
func TestRelativeHomeIsAbsent(t *testing.T) {
	wd := cwdfix.Temp(t)
	mkdir(t, wd, "h", oldName)
	mkdir(t, wd, "h", ".config", "agent-director")
	writeFile(t, wd, "h/.config/agent-director/state.db")
	for _, home := range []string{"", "h", "./h"} {
		if layout.OldPresent(home) || layout.NewPresent(home) || layout.OnlyOldPresent(home) || layout.StoreExists(home) {
			t.Errorf("home %q: a rule reported present; want all absent", home)
		}
	}
}

// TestStoreExistsUnreadableCountsAsExisting: a refusal other than not-exist
// reads as existing, so the open reports it rather than the old-layout
// refusal. A path through a file reads as absent.
func TestStoreExistsUnreadableCountsAsExisting(t *testing.T) {
	t.Run("directory without search permission", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		home := t.TempDir()
		dir := mkdir(t, home, ".config", "agent-director")
		if err := os.Chmod(dir, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		if !layout.StoreExists(home) {
			t.Error("StoreExists = false; want true for a permission refusal")
		}
	})
	t.Run("new name is a file", func(t *testing.T) {
		home := t.TempDir()
		writeFile(t, home, ".config/agent-director")
		if layout.StoreExists(home) {
			t.Error("StoreExists = true; want false when AD's directory is a file")
		}
	})
}

// TestRulesCreateNothing: the presence rules only look; none creates a file
// or directory, whatever the tree.
func TestRulesCreateNothing(t *testing.T) {
	home := t.TempDir()
	mkdir(t, home, oldName)
	layout.OldPresent(home)
	layout.NewPresent(home)
	layout.OnlyOldPresent(home)
	layout.StoreExists(home)
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != oldName {
		t.Errorf("home entries after the rules = %v; want only %s", entries, oldName)
	}
}
