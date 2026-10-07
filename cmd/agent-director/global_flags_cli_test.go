package main_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestGlobalFlagsOpenTheirStore: --store-path and --home (b.32k), in either
// form, reach the store a store-opening verb opens, and "~" values expand
// against HOME, never a literal "~" under the cwd (b.38a); nothing lands in
// the cwd or, for --home, under the inherited HOME.
func TestGlobalFlagsOpenTheirStore(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags func(dir string) []string
		store func(home, dir string) string // where the store must be
	}{
		{"--store-path", func(dir string) []string { return []string{"--store-path", filepath.Join(dir, "s.db")} },
			func(_, dir string) string { return filepath.Join(dir, "s.db") }},
		{"--store-path=", func(dir string) []string { return []string{"--store-path=" + filepath.Join(dir, "s.db")} },
			func(_, dir string) string { return filepath.Join(dir, "s.db") }},
		{"--home", func(dir string) []string { return []string{"--home", dir} },
			func(_, dir string) string { return stateDB(dir) }},
		{"--home ~", func(string) []string { return []string{"--home", "~"} },
			func(home, _ string) string { return stateDB(home) }},
		{"--home ~/sub", func(string) []string { return []string{"--home", "~/sub"} },
			func(home, _ string) string { return stateDB(filepath.Join(home, "sub")) }},
		{"--store-path ~/s.db", func(string) []string { return []string{"--store-path", "~/s.db"} },
			func(home, _ string) string { return filepath.Join(home, "s.db") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, dir, cwd := t.TempDir(), t.TempDir(), t.TempDir()
			if _, stderr, code := runInDir(t, cwd, home, append(tc.flags(dir), "list")...); code != 0 {
				t.Fatalf("list: exit=%d want 0; stderr=%q", code, stderr)
			}
			if _, err := os.Stat(tc.store(home, dir)); err != nil {
				t.Errorf("no store at %s: %v", tc.store(home, dir), err)
			}
			assertHomeTree(t, cwd)
			if tc.name == "--home" {
				assertHomeTree(t, home)
			}
		})
	}
}

// TestGlobalFlag_EmptyTwoTokenValue_Refused: `--flag ""` exits 1 with the
// `--flag=` form's envelope and creates nothing under HOME (no store, config
// or trail), on every dispatch path (b.pu2); so does a flag with no value at
// all. The parse itself is internal/clisetup's TestParseGlobalFlagsMissingValue.
func TestGlobalFlag_EmptyTwoTokenValue_Refused(t *testing.T) {
	relayAttempt := []string{"trail-emit", "relay-attempt", "--token", "5b3c8f0e-2d4a-4c6b-9e1f-7a8b9c0d1e2f",
		"--endpoint", "http://127.0.0.1:9/r", "--outcome", "200", "--instance-id", "pu2-x"}
	for _, tc := range []struct {
		name, flag string
		argv       []string
	}{
		// The hook payload runInDir pipes would make a no-verb run write a trail record.
		{"no verb", "--store-path", []string{"--store-path", ""}},
		{"help", "--home", []string{"--home", "", "help"}},
		{"version", "--tmux-command", []string{"--tmux-command", "", "version"}},
		{"list", "--home", []string{"--home", "", "list"}},
		{"trail-emit", "--store-path", append([]string{"--store-path", ""}, relayAttempt...)},
		{"no value at the tail", "--store-path", []string{"list", "--store-path"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			stdout, stderr, code := runInDir(t, home, home, tc.argv...)
			env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags")
			if want := tc.flag + " requires a value"; env.ErrDescription != want {
				t.Errorf("err_description = %q; want %q", env.ErrDescription, want)
			}
			assertHomeTree(t, home)
		})
	}
}

// TestGlobalFlag_HomeTildeWithoutHOME_Refused: with HOME empty or unset,
// `--home ~` and `--home ~/x` are ErrInvalidFlags with exit 1 and open
// nothing, under the passwd home or the cwd (b.38a). With no HOME and no
// --home, every store-opening call is ErrStoreOpen, `--store-path ~/…`
// included, because the config path cannot be expanded; the store's own
// refusal of a "~/" path is internal/store's TestRefusedStorePathCreatesNothing (b.4uz).
func TestGlobalFlag_HomeTildeWithoutHOME_Refused(t *testing.T) {
	const noHome = `: HOME is unset or empty, so there is no home directory to expand "~" against`
	for _, tc := range []struct {
		name, wantErr, wantDesc string
		argv                    []string
	}{
		{"--home ~ list", "ErrInvalidFlags", `--home "~"` + noHome, []string{"--home", "~", "list"}},
		{"--home=~/x list", "ErrInvalidFlags", `--home "~/x"` + noHome, []string{"--home=~/x", "list"}},
		{"--home ~ version", "ErrInvalidFlags", `--home "~"` + noHome, []string{"--home", "~", "version"}},
		{"--store-path ~/s.db list", "ErrStoreOpen", "api: expand config path: expand tilde: $HOME is not defined",
			[]string{"--store-path", "~/s.db", "list"}},
	} {
		for _, unset := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/HOME unset=%t", tc.name, unset), func(t *testing.T) {
				passwdDir := passwdAgentDir(t)
				cwd := t.TempDir()
				environ := []string{"PATH=" + os.Getenv("PATH")}
				if !unset {
					environ = append(environ, "HOME=")
				}
				stdout, stderr, code := mustRun(t, cliOpts{dir: cwd, env: environ, deadline: surfaceDeadline}, tc.argv...)
				env := assertOnlyEnvelope(t, stdout, stderr, code, tc.wantErr)
				if env.ErrDescription != tc.wantDesc {
					t.Errorf("err_description = %q; want %q", env.ErrDescription, tc.wantDesc)
				}
				if _, err := os.Lstat(passwdDir); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("%q created %s under the passwd home (Lstat: %v)", tc.argv, passwdDir, err)
				}
				assertHomeTree(t, cwd)
			})
		}
	}
}
