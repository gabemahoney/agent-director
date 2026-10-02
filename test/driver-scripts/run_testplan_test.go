package driverscripts_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDBReset counts its runs in $RESETS, removes the case's marker $M and
// fails on run number $FAIL_RESET_AT.
const fakeDBReset = `#!/usr/bin/env bash
n=$(( $(cat "$RESETS" 2>/dev/null || echo 0) + 1 ))
echo "$n" > "$RESETS"
rm -f "$M"
[ "$n" != "${FAIL_RESET_AT:-}" ] || exit 9
`

// shellPlan is a driver dir holding run-testplan.sh and fakeDBReset, and a
// plan "plan" whose one case t2.p exits 7 if its marker survived a reset and 1
// otherwise.
func shellPlan(t *testing.T) (runner, root string) {
	t.Helper()
	src, err := os.ReadFile(driverScript(t, "run-testplan.sh"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files := map[string]string{
		"driver/run-testplan.sh": string(src),
		"driver/db-reset.sh":     fakeDBReset,
		"tp/plan/t1.p.md":        "---\nid: t1.p\ntitle: rerun probe\nchildren:\n- t2.p\n---\n",
		"tp/plan/t2.p/t2.p.md":   "---\nid: t2.p\n---\n```bash\n[ ! -e \"$M\" ] || exit 7\ntouch \"$M\"\nexit 1\n```\n",
	}
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "driver", "run-testplan.sh"), filepath.Join(dir, "tp")
}

// TestShellModeRerunStartsFromAFreshReset: a failing case's xtrace rerun runs
// after a db-reset of its own, so its trace shows the case's failure, not the
// first run's leftovers; a reset that fails skips the rerun and says so (b.ai5).
func TestShellModeRerunStartsFromAFreshReset(t *testing.T) {
	for _, tc := range []struct {
		name          string
		failResetAt   string
		want, notWant []string // substrings of the case's details
	}{
		{"rerun after a fresh reset", "", []string{"xtrace_tail: ", "+ exit 1"}, []string{"+ exit 7", "rerun skipped"}},
		{"reset before the rerun fails", "2", []string{"xtrace rerun skipped: db-reset failed before it"}, []string{"xtrace_tail"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, root := shellPlan(t)
			state := t.TempDir()
			resets := filepath.Join(state, "resets")
			r := run(t, []string{
				"EPIC=plan", "TESTPLAN_ROOT=" + root, "DRIVER_MODE=shell",
				"M=" + filepath.Join(state, "m"), "RESETS=" + resets, "FAIL_RESET_AT=" + tc.failResetAt,
			}, "", "bash", runner)
			var got struct{ Case, Status, Details string }
			for _, line := range strings.Split(r.stdout, "\n") {
				var rec struct{ Case, Status, Details string }
				if json.Unmarshal([]byte(line), &rec) == nil && rec.Case == "t2.p" {
					got = rec
				}
			}
			if r.code != 1 || got.Status != "fail" {
				t.Fatalf("want exit 1 and case t2.p failed: %s", r)
			}
			if n, _ := os.ReadFile(resets); string(n) != "2\n" {
				t.Errorf("db-reset ran %q times, want 2 (before the case and before its rerun)", n)
			}
			for _, s := range tc.want {
				if !strings.Contains(got.Details, s) {
					t.Errorf("details lack %q: %q", s, got.Details)
				}
			}
			for _, s := range tc.notWant {
				if strings.Contains(got.Details, s) {
					t.Errorf("details contain %q: %q", s, got.Details)
				}
			}
		})
	}
}
