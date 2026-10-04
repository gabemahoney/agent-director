package main_test

// help_test.go covers agent-director-admin's help and version (b.vqr): every
// help path, global flags given or not, opens with the human-approval
// statement and shows or points to the global flags, help and version open no
// store and load no config, and version prints the same stamp as
// agent-director.

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Each verb's usage line, as help prints it.
var usageLines = map[string]string{
	"kill-finished": "agent-director-admin kill-finished --claude-instance-id <id>",
	"delete":        "agent-director-admin delete --claude-instance-id <id> [--claude-instance-id <id>...]",
	"help":          "agent-director-admin help",
	"version":       "agent-director-admin version",
}

// poisonedHome returns a HOME whose ~/.agent-director holds a malformed
// config and a state.db that is not a database, so a run that loads the
// config or opens the store fails; dirEntries reads it back.
func poisonedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".agent-director")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, body := range map[string]string{"config.toml": "this is [not toml\n", "state.db": "not a database\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return home
}

// dirEntries returns the names and contents of the files in home's
// ~/.agent-director.
func dirEntries(t *testing.T, home string) map[string]string {
	t.Helper()
	dir := filepath.Join(home, ".agent-director")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	out := map[string]string{}
	for _, e := range entries {
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		out[e.Name()] = string(body)
	}
	return out
}

// The global flags as the whole help lists them, and the pointer to them each
// verb's help ends with (whitespace-collapsed).
var (
	globalFlagNames = []string{"--store-path <path>", "--home <dir>", "--tmux-command <path>"}
	globalPointer   = "Global flags (--store-path, --home, --tmux-command): see 'agent-director-admin help'."
)

// TestHelpOpensWithApprovalStatement: the no-verb run (global flags only
// included), help, --help, -h and each verb's --help and -h, with or without
// global flags, exit 0 with the human-approval statement as the first line,
// then the global flags and the usage of every verb (whole help) or the usage
// of that verb only and the pointer to the global flags, opening no store and
// loading no config. "{home}" in args is the run's HOME.
func TestHelpOpensWithApprovalStatement(t *testing.T) {
	all := []string{"kill-finished", "delete", "help", "version"}
	cases := []struct {
		args  []string
		verbs []string // the verbs whose usage the help shows
	}{
		{nil, all},
		{[]string{"help"}, all},
		{[]string{"--help"}, all},
		{[]string{"-h"}, all},
		{[]string{"--home", "{home}"}, all},
		{[]string{"--store-path", "{home}/.agent-director/state.db", "help"}, all},
		{[]string{"-h", "--tmux-command={home}/no-tmux"}, all},
		{[]string{"kill-finished", "--help", "--store-path", "{home}/.agent-director/state.db"}, []string{"kill-finished"}},
		{[]string{"--home={home}", "delete", "-h"}, []string{"delete"}},
	}
	for _, v := range []string{"kill-finished", "delete", "version"} {
		for _, flag := range []string{"--help", "-h"} {
			cases = append(cases, struct {
				args  []string
				verbs []string
			}{[]string{v, flag}, []string{v}})
		}
	}
	for _, tc := range cases {
		t.Run(strings.Join(append([]string{"agent-director-admin"}, tc.args...), " "), func(t *testing.T) {
			home := poisonedHome(t)
			before := dirEntries(t, home)
			args := make([]string, len(tc.args))
			for i, a := range tc.args {
				args[i] = strings.ReplaceAll(a, "{home}", home)
			}

			stdout, stderr, code := runAdmin(t, home, args...)

			if code != 0 || stderr != "" {
				t.Fatalf("exit = %d, stderr = %q; want 0 and empty (a help that loads the config or opens the store fails here)", code, stderr)
			}
			if first, _, _ := strings.Cut(stdout, "\n"); first != approvalStatement {
				t.Errorf("first line = %q; want the human-approval statement %q", first, approvalStatement)
			}
			for v, usage := range usageLines {
				if got, want := strings.Contains(stdout, "\n"+usage+"\n"), slices.Contains(tc.verbs, v); got != want {
					t.Errorf("help shows %s's usage = %v; want %v:\n%s", v, got, want, stdout)
				}
			}
			flat := strings.Join(strings.Fields(stdout), " ")
			whole := len(tc.verbs) > 1
			for _, f := range globalFlagNames {
				if got := strings.Contains(stdout, "\n    "+f+"\n"); got != whole {
					t.Errorf("help lists the global flag %s = %v; want %v:\n%s", f, got, whole, stdout)
				}
			}
			if got := strings.Contains(flat, globalPointer); got == whole {
				t.Errorf("help points to the global flags = %v; want %v:\n%s", got, !whole, stdout)
			}
			if after := dirEntries(t, home); !maps.Equal(after, before) {
				t.Errorf("~/.agent-director changed: %v; want it as it was, %v", after, before)
			}
		})
	}
}

// TestHelpAndVersionCreateNoStore: under a HOME with no ~/.agent-director,
// help and version create none.
func TestHelpAndVersionCreateNoStore(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"kill-finished", "--help"}, {"delete", "-h"}, {"version"}} {
		t.Run(strings.Join(append([]string{"agent-director-admin"}, args...), " "), func(t *testing.T) {
			home := t.TempDir()
			if _, stderr, code := runAdmin(t, home, args...); code != 0 {
				t.Fatalf("exit = %d, stderr = %q; want 0", code, stderr)
			}
			if _, err := os.Stat(filepath.Join(home, ".agent-director")); !os.IsNotExist(err) {
				t.Errorf("~/.agent-director after the run: %v; want none created", err)
			}
		})
	}
}

// TestVersionMatchesMainBinary: agent-director-admin version prints exactly
// what agent-director version prints for the same build, the stamp the build
// set, and loads no config.
func TestVersionMatchesMainBinary(t *testing.T) {
	home := poisonedHome(t)
	adminOut, adminErr, adminCode := runAdmin(t, home, "version")
	mainOut, mainErr, mainCode := runMain(t, home, "version")
	if adminCode != 0 || adminErr != "" || mainCode != 0 || mainErr != "" {
		t.Fatalf("version: admin exit %d stderr %q, main exit %d stderr %q; want 0 and empty", adminCode, adminErr, mainCode, mainErr)
	}
	if adminOut != mainOut {
		t.Errorf("agent-director-admin version = %q; want agent-director's %q", adminOut, mainOut)
	}
	var stamp map[string]any
	if err := json.Unmarshal([]byte(adminOut), &stamp); err != nil {
		t.Fatalf("parse %q: %v", adminOut, err)
	}
	if want := map[string]any{"version": testVersion, "commit": testCommit}; !maps.Equal(stamp, want) {
		t.Errorf("version = %v; want %v", stamp, want)
	}
}
