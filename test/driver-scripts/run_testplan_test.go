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
		"tp/plan/t2.p/t2.p.md":   "---\nid: t2.p\n---\n```bash\n[ ! -e \"$M\" ] || exit 7\ntouch \"$M\"\nset -e\ntrap 'printf \"cleanup\\n%.0s\" {1..12}' EXIT\n(false)\n```\n",
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

// The original verdict owns the result; diagnostics cannot alter shell output or JSON.
func TestShellModeRerunStartsFromAFreshReset(t *testing.T) {
	const expectedFailure = `
set +e
negative() { printf '{"error":"expected refusal"}\n' >&2; return 7; }
result=$(negative 2>"$M.json")
status=$?
set -e
[ "$status" -eq 7 ]
jq -e '.error == "expected refusal"' "$M.json" >/dev/null
[ -z "$result" ]
`
	for _, tc := range []struct {
		name, failResetAt, script string
		pass                      bool
		want, notWant             []string
	}{
		{name: "rerun after a fresh reset", want: []string{"CASE_FAILURE exit=1", "command=false", "xtrace rerun failed (exit=1)", "xtrace_tail: "}, notWant: []string{"+ exit 7", "rerun skipped"}},
		{name: "reset before the rerun fails", failResetAt: "2", want: []string{"CASE_FAILURE exit=1", "command=false", "xtrace rerun skipped: db-reset failed before it"}, notWant: []string{"xtrace_tail"}},
		{name: "expected nonzero assignments and subshells pass silently", script: expectedFailure + `
set +e
(false)
status=$?
set -e
[ "$status" -eq 1 ]
printf 'PASS\n'
`, pass: true, want: []string{"PASS"}, notWant: []string{"CASE_FAILURE", "expected refusal", "negative", "exit=7", "xtrace"}},
		{name: "rerun preserves JSON stderr and original failure", script: expectedFailure + `
set -e
mode=held
trap 'printf "cleanup\n%.0s" {1..12}' EXIT
(test "$status" -eq 0)
`, want: []string{"CASE_FAILURE exit=1", `command=test\ \"\$status\"\ -eq\ 0`, "mode=held", "xtrace rerun failed (exit=1)"}, notWant: []string{"parse error", "Invalid numeric"}},
		{name: "passing rerun cannot promote failed original", script: expectedFailure + `
set -e
if [ "$(cat "$RESETS")" = 1 ]; then
  (false)
fi
printf 'PASS\n'
`, want: []string{"CASE_FAILURE exit=1", "command=false", "xtrace rerun passed (exit=0)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, root := shellPlan(t)
			if tc.script != "" {
				body := "---\nid: t2.p\n---\n```bash\n" + tc.script + "\n```\n"
				if err := os.WriteFile(filepath.Join(root, "plan", "t2.p", "t2.p.md"), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			state := t.TempDir()
			resets := filepath.Join(state, "resets")
			r := run(t, []string{
				"EPIC=plan", "TESTPLAN_ROOT=" + root, "DRIVER_MODE=shell",
				"M=" + filepath.Join(state, "m"), "RESETS=" + resets, "FAIL_RESET_AT=" + tc.failResetAt,
			}, "", "bash", runner)
			var got struct{ Case, Status, Details string }
			for _, line := range strings.Split(strings.TrimSpace(r.stdout), "\n") {
				var rec struct{ Case, Status, Details string }
				if err := json.Unmarshal([]byte(line), &rec); err != nil {
					t.Fatalf("non-JSON driver stdout: %q: %v", line, err)
				}
				if rec.Case == "t2.p" {
					got = rec
				}
			}
			wantCode, wantStatus, wantResets := 1, "fail", "2\n"
			if tc.pass {
				wantCode, wantStatus, wantResets = 0, "pass", "1\n"
			}
			if r.code != wantCode || got.Status != wantStatus {
				t.Fatalf("want exit %d and case %s: %s", wantCode, wantStatus, r)
			}
			if n, _ := os.ReadFile(resets); string(n) != wantResets {
				t.Errorf("db-reset ran %q times, want %q", n, wantResets)
			}
			for _, s := range tc.want {
				if !strings.Contains(got.Details, s) {
					t.Errorf("details lack %q: %q; stderr %s", s, got.Details, r.stderr)
				}
			}
			for _, s := range tc.notWant {
				haystack := got.Details
				if tc.pass {
					haystack = r.stdout + r.stderr
				}
				if strings.Contains(haystack, s) {
					t.Errorf("output contains %q: %q", s, haystack)
				}
			}
			if tc.script != "" {
				raw, err := os.ReadFile(filepath.Join(state, "m.json"))
				if err != nil || !json.Valid(raw) || string(raw) != "{\"error\":\"expected refusal\"}\n" {
					t.Errorf("command stderr contaminated: %q %v", raw, err)
				}
			}
			if tc.pass && !t.Failed() {
				t.Logf("passing original: exit=0, resets=1, failure markers across stdout/stderr/JSON=0, expected refusal stderr remains exact JSON")
			}
			if !tc.pass && tc.failResetAt == "" && !strings.Contains(r.stderr, "dedicated FD 8") {
				t.Error("missing independently captured rerun trace")
			}
		})
	}
}
