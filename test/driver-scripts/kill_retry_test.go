package driverscripts_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The CLI writes this envelope to stderr and exits 1 when the pane's agent
// outlives its exit wait (b.69m). Only err_name grants retry authority.
const killRetryStub = `#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >> "$CALLS"
n=$(wc -l < "$CALLS")
if [ "$n" -le "$FAILURES" ]; then
    case "$ERROR_NAME" in
        malformed) echo 'not JSON' >&2 ;;
        empty) : ;;
        *) printf '{"err_name":"%s","err_description":"%s"}\n' "$ERROR_NAME" 'tmux: agent process still running: instance id-test: tmux session \"e1-happy\": a kill was sent to the agent\u0027s pane and to its labelled session, and the agent process (pid 1356) was still running after the kill exit wait of 5 s; retry kill later; never delete this row; ErrTmuxKillFailed' >&2 ;;
    esac
    exit 1
fi
printf '{"kill_sent":false}\n'
`

// b.cbb: an exiting tmux server is retryable too, under the same cap.
func TestKillWithRetry(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failures  int
		errorName string
		wantCalls int
		wantSleep string
		wantOK    bool
	}{
		{"immediate success", 0, "", 1, "", true},
		{"one transient", 1, "ErrTmuxKillFailed", 2, "1\n", true},
		{"three transients", 3, "ErrTmuxKillFailed", 4, "1\n2\n4\n", true},
		{"last attempt succeeds", 5, "ErrTmuxKillFailed", 6, "1\n2\n4\n8\n8\n", true},
		{"cap", 20, "ErrTmuxKillFailed", 6, "1\n2\n4\n8\n8\n", false},
		{"conflict", 1, "ErrTmuxSessionConflict", 1, "", false},
		{"unresponsive", 1, "ErrTmuxUnresponsive", 2, "1\n", true},
		{"unresponsive cap", 20, "ErrTmuxUnresponsive", 6, "1\n2\n4\n8\n8\n", false},
		{"unavailable", 1, "ErrTmuxNotAvailable", 1, "", false},
		{"malformed", 1, "malformed", 1, "", false},
		{"empty", 1, "empty", 1, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			calls, sleeps := filepath.Join(dir, "calls"), filepath.Join(dir, "sleeps")
			for name, body := range map[string]string{
				"agent-director": killRetryStub,
				"sleep":          "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> \"$SLEEPS\"\n",
				"calls":          "",
				"sleeps":         "",
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			r := run(t, []string{
				"PATH=" + dir + ":" + os.Getenv("PATH"),
				"CALLS=" + calls, "SLEEPS=" + sleeps,
				"FAILURES=" + strconv.Itoa(tc.failures), "ERROR_NAME=" + tc.errorName,
			}, "", "bash", "-c", `set -euo pipefail; source "$1"; kill_with_retry --instance-id id-test`, "test", driverScript(t, "kill-retry.sh"))
			if (r.code == 0) != tc.wantOK {
				t.Errorf("want success=%t: %s", tc.wantOK, r)
			}
			read := func(path string) string {
				t.Helper()
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				return string(b)
			}
			if got := read(calls); got != strings.Repeat("kill --instance-id id-test\n", tc.wantCalls) {
				t.Errorf("calls = %q, want %d identical calls", got, tc.wantCalls)
			}
			if got := read(sleeps); got != tc.wantSleep {
				t.Errorf("backoff = %q, want %q", got, tc.wantSleep)
			}
			if tc.wantOK && r.stdout != "{\"kill_sent\":false}\n" {
				t.Errorf("success stdout = %q, want only final kill JSON", r.stdout)
			}
			if tc.failures > 0 {
				want := tc.errorName
				if want == "malformed" {
					want = "not JSON"
				}
				if want != "empty" && !strings.Contains(r.stderr, want) {
					t.Errorf("kill error output missing %q: %s", want, r)
				}
			}
			if !tc.wantOK && r.stdout != "" {
				t.Errorf("failed kill stdout = %q, want empty", r.stdout)
			}
		})
	}
}
