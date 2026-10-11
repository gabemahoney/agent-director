package main_test

// old_layout_test.go covers agent-director-admin on a home that holds only the
// old ~/.agent-director (SR-11.7, bug b.nx4): each store-opening verb refuses
// with ErrOldLayout and creates nothing, help and version create nothing, and
// --store-path is not refused. The presence rules themselves are pkg/api's and
// internal/layout's tests.

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oldLayoutHome returns a HOME holding only ~/.agent-director with a store, a
// config and a template, none valid for any reader (a run that opened one
// would fail with another error than ErrOldLayout).
func oldLayoutHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".agent-director")
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, body := range map[string]string{
		"state.db": "not a database: the old store\n", "config.toml": "this is [not toml\n",
		filepath.Join("templates", "t.toml"): "agent = \"claude\"\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return home
}

// plainEnv is a run's environment: PATH and HOME=home, no tmux directory made
// under home, so the home holds only what the run itself writes.
func plainEnv(home string) []string {
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
}

// homeSnapshot maps every path under home, relative, to its contents (a
// directory, marked by a trailing "/", to ""), so two snapshots are equal only
// when no path and no byte changed.
func homeSnapshot(t *testing.T, home string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == home {
			return err
		}
		rel, _ := filepath.Rel(home, p)
		if d.IsDir() {
			snap[rel+"/"] = ""
			return nil
		}
		body, rerr := os.ReadFile(p)
		snap[rel] = string(body)
		return rerr
	})
	if err != nil {
		t.Fatalf("walk %s: %v", home, err)
	}
	return snap
}

// assertSnapshot fails unless home still holds exactly before.
func assertSnapshot(t *testing.T, home string, before map[string]string) {
	t.Helper()
	if after := homeSnapshot(t, home); !maps.Equal(after, before) {
		t.Errorf("home %s = %v; want it as seeded, %v", home, after, before)
	}
}

// TestAdminOldLayoutRefusesStoreOpeningVerbs: with only the old directory,
// kill-finished and delete exit 1 with only the ErrOldLayout envelope, whose
// description names this release's install.sh, and the home keeps every path
// and byte: no new directory, no ~/.config, no errors-log or trail line.
func TestAdminOldLayoutRefusesStoreOpeningVerbs(t *testing.T) {
	for _, verb := range []string{"kill-finished", "delete"} {
		t.Run(verb, func(t *testing.T) {
			home := oldLayoutHome(t)
			before := homeSnapshot(t, home)

			stdout, stderr, code := runBinIn(t, adminPath, "", plainEnv(home), verb, "--instance-id", "id-old-layout")

			env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrOldLayout")
			for _, want := range []string{"install.sh", "~/.config/agent-director"} {
				if !strings.Contains(env.ErrDescription, want) {
					t.Errorf("err_description = %q; want it to name %q", env.ErrDescription, want)
				}
			}
			assertSnapshot(t, home, before)
		})
	}
}

// TestAdminOldLayoutHelpAndVersionCreateNothing: help, version and their
// aliases open no store and create nothing under an old-layout home.
func TestAdminOldLayoutHelpAndVersionCreateNothing(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--help"}, {"version"}, {"-v"}, {"kill-finished", "--help"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			home := oldLayoutHome(t)
			before := homeSnapshot(t, home)

			stdout, stderr, code := runBinIn(t, adminPath, "", plainEnv(home), args...)

			if code != 0 || stderr != "" || stdout == "" {
				t.Errorf("exit=%d stdout=%.80q stderr=%q; want 0, output and empty stderr", code, stdout, stderr)
			}
			assertSnapshot(t, home, before)
		})
	}
}

// TestAdminOldLayoutNotRefusedWithStorePath: --store-path names the store, so
// the old directory is not looked at; kill-finished gets as far as the store's
// own answer for an id it does not hold, and the home is still as seeded.
func TestAdminOldLayoutNotRefusedWithStorePath(t *testing.T) {
	home := oldLayoutHome(t)
	before := homeSnapshot(t, home)
	storeFile := filepath.Join(t.TempDir(), "s.db")

	stdout, stderr, code := runBinIn(t, adminPath, "", plainEnv(home),
		"--store-path", storeFile, "kill-finished", "--instance-id", "id-absent")

	if code != 1 || stdout != "" || strings.Contains(stderr, "ErrOldLayout") {
		t.Errorf("exit=%d stdout=%q stderr=%q; want a refusal of the absent id, not ErrOldLayout", code, stdout, stderr)
	}
	if _, err := os.Stat(storeFile); err != nil {
		t.Errorf("no store at --store-path: %v", err)
	}
	assertSnapshot(t, home, before)
}
