package realtmux_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The hook's parent rule on real tmux (SRD SR-20.7's hook bullet, SR-22.9,
// SR-14 ad.hook.ignored; PRD AC-HOOK-01; WD 2026-09-29c). The sandbox has no
// Claude Code, so a stand-in `claude` (a bash script, not exec'd away, so it
// stays the pane's process) runs the real `agent-director hook` with a real
// SessionStart payload: as a simple command (the hook's parent is the pane
// process: applied), through `sh -c '…; true'` (the parent is sh: ignored;
// the `; true` keeps sh from exec-ing the hook, build-lead decision A12), or
// from a second pane split in the spawned session, which inherits the
// instance id (the parent is the second pane's process: ignored). b.9n6: with
// a `claude` launcher first on PATH that runs the stand-in as a child instead
// of exec-ing it, the pane process is the launcher and the hook's parent its
// child (ignored, and ad.hook.launcher_detected names the launcher); a
// launcher that execs it, as that warning advises, is the pane process again
// (applied).

// The stand-in reads its mode, control directory and the CLI's path from
// these variables; the spawned pane has them from the tmux server's global
// environment (the client environment only drops AGENT_DIRECTOR_*).
const (
	hookStubModeEnv = "AD_HOOKSTUB_MODE" // direct | sh | anything else: fire nothing
	hookStubDirEnv  = "AD_HOOKSTUB_DIR"
	hookStubBinEnv  = "AD_HOOKSTUB_BIN"
)

// hookStubScript is the stand-in `claude`. It fires its hook only after the
// test writes $AD_HOOKSTUB_DIR/go, which the test does after spawn returns,
// so the pane is always recorded when the hook reads the row and this test
// never exercises the SessionStart wait (SR-22.9). A SessionStart sent before
// spawn's identity write would wait, bounded by the pending grace period, and
// be judged by the parent rule once the identity lands; only if it has not
// landed by the bound would the hook be ignored as no_pane_recorded.
// It records the would-be parent's pid ($$ of the shell that runs the hook)
// in "parent", the hook's stdout in "stdout" and its exit status in "exit".
const hookStubScript = `#!/bin/bash
# Stand-in claude for test/realtmux/hook_parent_test.go.
dir="$AD_HOOKSTUB_DIR"
case "$AD_HOOKSTUB_MODE" in
direct|sh) ;;
*) while :; do sleep 3600; done ;;
esac
for _ in {1..1500}; do
  [ -e "$dir/go" ] && break
  sleep 0.02
done
[ -e "$dir/go" ] || exit 1
if [ "$AD_HOOKSTUB_MODE" = direct ]; then
  echo $$ > "$dir/parent"
  "$AD_HOOKSTUB_BIN" hook < "$dir/payload.json" > "$dir/stdout"
  echo $? > "$dir/exit.tmp"
else
  sh -c 'echo $$ > "$AD_HOOKSTUB_DIR/parent"; "$AD_HOOKSTUB_BIN" hook < "$AD_HOOKSTUB_DIR/payload.json" > "$AD_HOOKSTUB_DIR/stdout"; echo $? > "$AD_HOOKSTUB_DIR/exit.tmp"; true'
fi
mv "$dir/exit.tmp" "$dir/exit"
while :; do sleep 3600; done
`

// launcherScript is a `claude` launcher running the stand-in at real: as a
// child (no exec, like a Volta-style shim or an npm launcher; `exit $?` keeps
// any sh from exec-ing it) or, with exec, in its own process.
func launcherScript(real string, exec bool) string {
	if exec {
		return "#!/bin/sh\nexec '" + real + "' \"$@\"\n"
	}
	return "#!/bin/sh\n'" + real + "' \"$@\"\nexit $?\n"
}

// sessionStartFixture is the real-shaped SessionStart payload shared with
// internal/hook's per-event fixtures (SR-20.6).
const sessionStartFixture = "../../internal/hook/testdata/hook-payloads/session-start-startup.json"

// buildHookCLI compiles the agent-director CLI into dir (as
// test/reboot-recovery does) and returns its absolute path.
func buildHookCLI(t *testing.T, dir string) string {
	t.Helper()
	out := filepath.Join(dir, "agent-director")
	build := exec.Command("go", "build", "-o", out, "github.com/gabemahoney/agent-director/cmd/agent-director")
	var stderr bytes.Buffer
	build.Stderr = &stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build agent-director: %v\n%s", err, stderr.String())
	}
	return out
}

// readFileTrim reads a control file the stand-in wrote, without its newline.
func readFileTrim(t testing.TB, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.TrimSpace(string(data))
}

// hookTrailRecords returns the event records for id in the trail the hook
// process wrote under the test's HOME, numbers kept as json.Number.
func hookTrailRecords(t testing.TB, home, event, id string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".agent-director", "ad-trail.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read trail: %v", err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(line))
		dec.UseNumber()
		var rec map[string]any
		if err := dec.Decode(&rec); err != nil {
			t.Fatalf("trail line is not JSON: %v", err)
		}
		if rec["event"] == event && rec["claude_instance_id"] == id {
			out = append(out, rec)
		}
	}
	return out
}

// TestHookAppliesOnlyFromThePaneProcess spawns through pkg/api on real tmux
// with the stand-in as the pane's command and checks that its SessionStart
// hook moves the row only when the hook is a direct child of the recorded
// pane process (SR-22.9); otherwise the row is untouched and the hook writes
// one ad.hook.ignored with reason pid_mismatch (SR-14), naming the pane
// process as launcher_pid, with one ad.hook.launcher_detected, when it is the
// hook parent's parent (b.9n6).
func TestHookAppliesOnlyFromThePaneProcess(t *testing.T) {
	bin := buildHookCLI(t, t.TempDir())
	payload, err := os.ReadFile(sessionStartFixture)
	if err != nil {
		t.Fatalf("read SessionStart fixture: %v", err)
	}
	var fixture struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(payload, &fixture); err != nil || fixture.SessionID == "" {
		t.Fatalf("SessionStart fixture: session_id %q, err %v", fixture.SessionID, err)
	}

	cases := []struct {
		name      string
		paneMode  string // the spawned pane's stand-in mode
		splitPane bool   // a second pane split in the session fires the hook (mode direct)
		launcher  string // "noexec" | "exec": a `claude` launcher on PATH runs the stand-in
		applied   bool
		// parentCommands are the accepted parent_command values of the
		// ignored record (sh is dash on Debian-based images).
		parentCommands []string
		// wantLauncher: the ignored record's launcher_pid is the pane
		// process, and one ad.hook.launcher_detected names it (b.9n6).
		wantLauncher bool
	}{
		{name: "simple command in the pane process", paneMode: "direct", applied: true},
		// The pane process is also sh's parent, the same structure as a
		// launcher (b.9n6). launcher_detected here is expected and accurate:
		// its advice names a shell-form hook as the other cause.
		{name: "through sh -c", paneMode: "sh", parentCommands: []string{"sh", "dash"}, wantLauncher: true},
		{name: "from a second pane", paneMode: "idle", splitPane: true, parentCommands: []string{"claude"}},
		{name: "behind a claude launcher that does not exec", paneMode: "direct", launcher: "noexec",
			parentCommands: []string{"claude"}, wantLauncher: true},
		// launcher_detected's advice followed: a wrapper that execs claude.
		{name: "behind a claude launcher that execs", paneMode: "direct", launcher: "exec", applied: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSpawnFix(t)
			home := filepath.Dir(filepath.Dir(f.DBPath))
			ctl := t.TempDir()
			if err := os.WriteFile(filepath.Join(ctl, "payload.json"), payload, 0o600); err != nil {
				t.Fatalf("write payload: %v", err)
			}
			stubDir := t.TempDir()
			stub := filepath.Join(stubDir, "claude")
			paneScript := stub // the script the pane process runs
			if tc.launcher != "" {
				// The stand-in is the "real" claude, off PATH; the launcher is on it.
				stub = filepath.Join(t.TempDir(), "claude")
				launcher := filepath.Join(stubDir, "claude")
				if err := os.WriteFile(launcher, []byte(launcherScript(stub, tc.launcher == "exec")), 0o755); err != nil {
					t.Fatalf("write claude launcher: %v", err)
				}
				paneScript = stub
				if tc.launcher == "noexec" {
					paneScript = launcher
				}
			}
			if err := os.WriteFile(stub, []byte(hookStubScript), 0o755); err != nil {
				t.Fatalf("write stand-in claude: %v", err)
			}
			t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv(hookStubModeEnv, tc.paneMode)
			t.Setenv(hookStubDirEnv, ctl)
			t.Setenv(hookStubBinEnv, bin)

			s := f.mustSpawn(t, "")
			paneID := s.Row.PaneID.(string)
			panePID := s.rt.formatInt(t, paneID, "#{pane_pid}")
			if fmt.Sprint(s.Row.PanePID) != strconv.Itoa(panePID) || s.Row.State != "pending" {
				t.Fatalf("after spawn: pane_pid %v (tmux says %d), state %v; want the pane recorded on a pending row",
					s.Row.PanePID, panePID, s.Row.State)
			}
			waitFor(t, "the pane process runs "+paneScript,
				func() bool { argv := procCmdline(t, panePID); return len(argv) >= 2 && argv[1] == paneScript },
				func() string { return fmt.Sprintf("%s, cmdline %q", procState(panePID), procCmdline(t, panePID)) })
			wantParent := panePID
			if tc.splitPane {
				out := s.rt.must(t, "split-window", "-d", "-t", paneID, "-e", hookStubModeEnv+"=direct",
					"-P", "-F", "#{pane_pid}", stub, "second-pane")
				second, err := strconv.Atoi(strings.TrimSpace(out))
				if err != nil {
					t.Fatalf("split-window reply %q is not a pane pid", out)
				}
				s.rt.trackPane(second)
				waitExeced(t, second, "bash")
				if argv := procCmdline(t, second); len(argv) < 2 || argv[1] != stub {
					t.Fatalf("second pane %d runs %q, want the stand-in claude under bash", second, argv)
				}
				if v, _ := envValue(procEnviron(t, second), "AGENT_DIRECTOR_INSTANCE_ID"); v != s.ID {
					t.Fatalf("second pane AGENT_DIRECTOR_INSTANCE_ID = %q, want the row's id %q", v, s.ID)
				}
				wantParent = second
			}

			// Only now, with the pane recorded, may the stand-in fire.
			if err := os.WriteFile(filepath.Join(ctl, "go"), nil, 0o600); err != nil {
				t.Fatalf("write go-file: %v", err)
			}
			exitPath := filepath.Join(ctl, "exit")
			waitFor(t, "the stand-in's hook exits",
				func() bool { _, err := os.Stat(exitPath); return err == nil },
				func() string { return procState(panePID) })

			if got := readFileTrim(t, exitPath); got != "0" {
				t.Errorf("hook exit status %s, want 0", got)
			}
			if got := readFileTrim(t, filepath.Join(ctl, "stdout")); got != "" {
				t.Errorf("hook stdout %q, want empty", got)
			}
			parent, err := strconv.Atoi(readFileTrim(t, filepath.Join(ctl, "parent")))
			if err != nil {
				t.Fatalf("parent file: %v", err)
			}
			if tc.splitPane && parent != wantParent {
				t.Fatalf("the hook ran under pid %d, want the second pane's process %d", parent, wantParent)
			}
			if (parent == panePID) != tc.applied {
				t.Fatalf("the hook ran under pid %d, pane process %d; the case needs parent == pane %v", parent, panePID, tc.applied)
			}

			row := readRow(t, f.DBPath, s.ID)
			ignored := hookTrailRecords(t, home, "ad.hook.ignored", s.ID)
			detected := hookTrailRecords(t, home, "ad.hook.launcher_detected", s.ID)
			if tc.applied {
				// SR-22.9: an applied SessionStart sets waiting and records the
				// payload's session id and pid/proc_starttime = the hook's parent.
				if row.State != "waiting" || fmt.Sprint(row.PID) != strconv.Itoa(panePID) ||
					fmt.Sprint(row.ProcStarttime) != fmt.Sprint(row.PaneStarttime) || row.ClaudeSessionID != fixture.SessionID {
					t.Errorf("row: state %v, pid %v, proc_starttime %v, claude_session_id %v; want waiting, pid %d, proc_starttime = pane_starttime %v, session %s",
						row.State, row.PID, row.ProcStarttime, row.ClaudeSessionID, panePID, row.PaneStarttime, fixture.SessionID)
				}
				if len(ignored) != 0 || len(detected) != 0 {
					t.Errorf("%d ad.hook.ignored and %d ad.hook.launcher_detected records for an applied hook, want none",
						len(ignored), len(detected))
				}
				return
			}

			// SR-22.9: a hook not applied changes nothing.
			if !reflect.DeepEqual(row, s.Row) {
				t.Errorf("row changed by an ignored hook:\n  before %+v\n  after  %+v", s.Row, row)
			}
			if len(ignored) != 1 {
				t.Fatalf("%d ad.hook.ignored records, want exactly one", len(ignored))
			}
			rec := ignored[0]
			want := map[string]string{
				"hook_event":      "SessionStart",
				"reason":          "pid_mismatch",
				"parent_pid":      strconv.Itoa(parent),
				"hook_session_id": fixture.SessionID,
				"row_pane_pid":    strconv.Itoa(panePID),
				"source":          "ad_hook",
			}
			for k, w := range want {
				if got := fmt.Sprint(rec[k]); got != w {
					t.Errorf("ad.hook.ignored %s = %s, want %s", k, got, w)
				}
			}
			if v, ok := rec["row_session_id"]; !ok || v != nil {
				t.Errorf("ad.hook.ignored row_session_id = %v (present %v), want null (the row records none)", v, ok)
			}
			if cmd, _ := rec["parent_command"].(string); !slices.Contains(tc.parentCommands, cmd) {
				t.Errorf("ad.hook.ignored parent_command = %v, want one of %v", rec["parent_command"], tc.parentCommands)
			}
			if !tc.wantLauncher {
				if v, ok := rec["launcher_pid"]; !ok || v != nil {
					t.Errorf("ad.hook.ignored launcher_pid = %v (present %v), want null", v, ok)
				}
				if len(detected) != 0 {
					t.Errorf("ad.hook.launcher_detected = %v, want none", detected)
				}
				return
			}
			if got := fmt.Sprint(rec["launcher_pid"]); got != strconv.Itoa(panePID) {
				t.Errorf("ad.hook.ignored launcher_pid = %s, want the pane process %d", got, panePID)
			}
			if len(detected) != 1 {
				t.Fatalf("%d ad.hook.launcher_detected records, want exactly one", len(detected))
			}
			for k, w := range map[string]string{
				"launcher_pid":     strconv.Itoa(panePID),
				"launcher_command": "claude",
				"parent_pid":       strconv.Itoa(parent),
				"parent_command":   fmt.Sprint(rec["parent_command"]),
				"source":           "ad_hook",
			} {
				if got := fmt.Sprint(detected[0][k]); got != w {
					t.Errorf("ad.hook.launcher_detected %s = %s, want %s", k, got, w)
				}
			}
			// The advice names both causes: a non-exec launcher and a
			// shell-form hook.
			advice := fmt.Sprint(detected[0]["advice"])
			for _, cause := range []string{
				"the claude on PATH is a launcher that runs Claude Code as a child instead of exec-ing it",
				"Claude Code ran the hook through a shell instead of in exec form",
			} {
				if !strings.Contains(advice, cause) {
					t.Errorf("ad.hook.launcher_detected advice = %q, want it to contain %q", advice, cause)
				}
			}
		})
	}
}
