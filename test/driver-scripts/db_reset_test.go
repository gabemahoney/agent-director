package driverscripts_test

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeLogged stands in for tmux, pkill and agent-director: it logs
// "<tool> <args>" to $FAKE_TOOL_LOG and succeeds.
const fakeLogged = `#!/bin/sh
printf '%s %s\n' "$(basename "$0")" "$*" >> "$FAKE_TOOL_LOG"
`

// dbResetUnderTest is test/driver/db-reset.sh, or DB_RESET_UNDER_TEST=<path>
// to prove the fails-before direction against a pre-fix copy.
func dbResetUnderTest(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("DB_RESET_UNDER_TEST"); p != "" {
		return p
	}
	return driverScript(t, "db-reset.sh")
}

// TestDBResetRefusesOutsideTheHarness (b.8yq): unless AGENT_DIRECTOR_TEST_HARNESS
// is exactly 1, db-reset.sh exits 2 with its refusal, runs no tmux, pkill or
// agent-director, and leaves the store as it was. Never set the marker to 1 here.
func TestDBResetRefusesOutsideTheHarness(t *testing.T) {
	script := dbResetUnderTest(t)
	const wantStderr = "db-reset: refusing to run outside the Docker test harness container (AGENT_DIRECTOR_TEST_HARNESS=1 unset; test/Dockerfile sets it).\n" +
		"db-reset: it ends every tmux server of its user and deletes ~/.agent-director/state.db, so it runs only there. Nothing was changed. Run the harness with: make test-docker EPIC=<slug>\n"
	for _, tc := range []struct {
		name   string
		marker []string // env(1) assignments after -u AGENT_DIRECTOR_TEST_HARNESS
	}{
		{"unset", nil},
		{"empty", []string{"AGENT_DIRECTOR_TEST_HARNESS="}},
		{"0", []string{"AGENT_DIRECTOR_TEST_HARNESS=0"}},
		{"true", []string{"AGENT_DIRECTOR_TEST_HARNESS=true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, bin := t.TempDir(), t.TempDir()
			store := filepath.Join(home, ".agent-director")
			if err := os.Mkdir(store, 0o700); err != nil {
				t.Fatal(err)
			}
			seeded := map[string]string{}
			for _, name := range []string{"state.db", "state.db-wal", "state.db-shm"} {
				seeded[name] = "seeded " + name
				if err := os.WriteFile(filepath.Join(store, name), []byte(seeded[name]), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"tmux", "pkill", "agent-director"} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(fakeLogged), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			logPath := filepath.Join(t.TempDir(), "calls.log")

			args := append(append([]string{"-u", "AGENT_DIRECTOR_TEST_HARNESS"}, tc.marker...), "bash", script)
			r := run(t, []string{
				"HOME=" + home, "TMUX_TMPDIR=" + t.TempDir(), "FAKE_TOOL_LOG=" + logPath,
				"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
			}, "", "env", args...)

			if r.code != 2 || r.stdout != "" || r.stderr != wantStderr {
				t.Errorf("want exit 2, no stdout and stderr %q: %s", wantStderr, r)
			}
			if calls, err := os.ReadFile(logPath); err == nil {
				t.Errorf("ran %q, want nothing run", calls)
			}
			for name, want := range seeded {
				if got, err := os.ReadFile(filepath.Join(store, name)); err != nil || string(got) != want {
					t.Errorf("%s = %q (%v) after the refusal, want it untouched: %q", name, got, err, want)
				}
			}
		})
	}
}
