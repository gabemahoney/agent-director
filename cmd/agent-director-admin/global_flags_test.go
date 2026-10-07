package main_test

// global_flags_test.go covers agent-director's global flags on
// agent-director-admin (b.vqr): --store-path and --home, before or after the
// verb, open the store agent-director created with the same flag,
// --tmux-command is the tmux kill-finished runs, and --home ~ with no HOME is
// refused (b.38a).

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// withFlags returns args with flags put after them when after, else before.
func withFlags(flags []string, after bool, args ...string) []string {
	if after {
		return append(args, flags...)
	}
	return append(slices.Clone(flags), args...)
}

// TestAdminGlobalFlagsOpenMainCLIStore: agent-director creates a store with a
// global flag; agent-director-admin delete with the same flag, before or after
// the verb, removes a row of it that agent-director then no longer finds, and
// no store is created under HOME.
func TestAdminGlobalFlagsOpenMainCLIStore(t *testing.T) {
	cases := []struct {
		name  string
		flags func(dir string) (flags []string, db string)
		after bool
	}{
		{"--store-path before the verb", func(dir string) ([]string, string) {
			db := filepath.Join(dir, "custom.db")
			return []string{"--store-path", db}, db
		}, false},
		{"--home= after the verb", func(dir string) ([]string, string) {
			return []string{"--home=" + dir}, stateDB(dir)
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			flags, db := tc.flags(t.TempDir())
			if _, stderr, code := runMain(t, home, withFlags(flags, false, "list")...); code != 0 {
				t.Fatalf("agent-director %q list: exit = %d, stderr = %q; want 0", flags, code, stderr)
			}
			id, err := apitest.SeedSpawn(db, "", store.StateEnded, "", "", "", false)
			if err != nil {
				t.Fatalf("seed a row in the store agent-director created: %v", err)
			}
			get := withFlags(flags, false, "get", "--claude-instance-id", id)
			if _, stderr, code := runMain(t, home, get...); code != 0 {
				t.Fatalf("agent-director %q: exit = %d, stderr = %q; want the seeded row", get, code, stderr)
			}

			stdout, stderr, code := runAdmin(t, home, withFlags(flags, tc.after, "delete", "--claude-instance-id", id)...)

			if code != 0 || stderr != "" {
				t.Fatalf("delete exit = %d, stderr = %q; want 0 and empty", code, stderr)
			}
			var res struct {
				Results map[string]string `json:"results"`
			}
			if err := json.Unmarshal([]byte(stdout), &res); err != nil || !maps.Equal(res.Results, map[string]string{id: "ok"}) {
				t.Errorf("stdout = %q; want results {%s: ok}", stdout, id)
			}
			stdout, stderr, code = runMain(t, home, get...)
			assertOnlyEnvelope(t, stdout, stderr, code, "ErrSpawnNotFound")
			if _, err := os.Stat(stateDB(home)); !os.IsNotExist(err) {
				t.Errorf("a store under HOME after the runs (stat: %v); want only the flag's store", err)
			}
		})
	}
}

// TestAdminHomeTildeWithoutHOMERefused: with HOME unset, --home ~ is
// ErrInvalidFlags with exit 1, as on agent-director, and creates nothing in the
// cwd (b.38a; empty HOME is internal/clisetup's TestGlobalFlagsApplyWithoutHOME).
func TestAdminHomeTildeWithoutHOMERefused(t *testing.T) {
	cwd := t.TempDir()

	stdout, stderr, code := runBinIn(t, adminPath, cwd, fakeTmuxEnv(t, t.TempDir()), "--home", "~", "delete", "--claude-instance-id", "x")

	env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags")
	if want := `--home "~": HOME is unset or empty, so there is no home directory to expand "~" against`; env.ErrDescription != want {
		t.Errorf("err_description = %q; want %q", env.ErrDescription, want)
	}
	if entries, err := os.ReadDir(cwd); err != nil || len(entries) != 0 {
		t.Errorf("cwd holds %v (err %v); want nothing created there", entries, err)
	}
}

// TestAdminTmuxCommandFlag: kill-finished with --tmux-command after the verb
// runs that tmux for every tmux call and ends the row's session.
func TestAdminTmuxCommandFlag(t *testing.T) {
	r := seedFinishedWithSession(t)
	tmuxCmd := filepath.Join(t.TempDir(), "other-tmux")
	if err := os.Symlink(faketmuxfix.Binary(t), tmuxCmd); err != nil {
		t.Fatalf("symlink the fake tmux: %v", err)
	}

	stdout, stderr, code := runAdmin(t, r.home,
		withFlags([]string{"--tmux-command", tmuxCmd}, true, "kill-finished", "--claude-instance-id", r.id)...)

	if code != 0 || stderr != "" {
		t.Fatalf("kill-finished exit = %d, stderr = %q; want 0 and empty", code, stderr)
	}
	assertKillSent(t, stdout, true)
	recs := faketmuxfix.ReadLog(t, filepath.Join(r.home, "fake-tmux.log"))
	if len(recs) == 0 {
		t.Fatal("no tmux call logged")
	}
	for i, argv := range recs {
		if argv[0] != tmuxCmd {
			t.Errorf("tmux call %d ran %q; want --tmux-command's %q", i, argv[0], tmuxCmd)
		}
	}
	if left := sessionsLeft(t, r.socket); len(left) != 0 {
		t.Errorf("sessions after kill-finished = %+v; want none", left)
	}
}
