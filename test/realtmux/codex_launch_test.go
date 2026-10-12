package realtmux_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite" // the store file driver, for agePendingLaunch

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The Codex launch on real tmux (E4-T2 T7; SRD SR-5.0, SR-10.1, SR-14.3,
// SR-22.9; RN-21, Q5(c)). The sandbox has no Codex, so a stand-in `codex` (a
// bash script, see codex_standin_test.go) is the pane's process: it records
// its arguments and environment and then runs AD's own composed SessionStart
// hook command with `bash -c` and Codex's SessionStart payload, as Codex
// does. The command is the one in the recorded launch, with only the program
// path swapped: the spawning process is this test binary, so its path in the
// command is the built agent-director's instead. Each test has a temp HOME
// (so no ~/.codex), CODEX_HOME unset and SHELL pointing at /bin/false (the
// stand-in uses the account's login shell, never $SHELL).
//
// The stand-in is reached from PATH (no [launch] codex_path) or through
// [launch] codex_path with no `codex` on PATH. A `codex` launcher that runs
// it as a child (the npm launcher's shape) makes the pane process the
// launcher and the hook's parent its child: the hook is ignored
// (pid_mismatch), the row stays pending and one ad.hook.pane_is_grandparent
// names the pane process; the advice it carries, followed word for word, makes
// the next launch's hook apply.

// codexFix is the spawn fixture with the built CLI and the stand-in's record
// directory.
type codexFix struct {
	*spawnFix
	bin  string // the built agent-director
	root string // the stand-in's record directory root
	home string // the test's HOME
}

// newCodexFix is newSpawnFix for Codex: no `codex` on PATH yet, the CLI built,
// the stand-in's record directory and hook run switched on in the environment
// the tmux server gives its panes, CODEX_HOME unset and SHELL bad.
func newCodexFix(t *testing.T) *codexFix {
	t.Helper()
	f := newSpawnFix(t)
	if p, err := exec.LookPath("codex"); err == nil {
		t.Fatalf("a codex is already on PATH (%s): the cases reach their own stand-in by PATH or by [launch] codex_path", p)
	}
	cf := &codexFix{
		spawnFix: f,
		bin:      buildHookCLI(t, t.TempDir()),
		root:     t.TempDir(),
		home:     filepath.Dir(filepath.Dir(filepath.Dir(f.DBPath))),
	}
	unsetEnv(t, "CODEX_HOME")
	t.Setenv(codexRecordDirEnv, cf.root)
	t.Setenv(codexHookEnv, "1")
	t.Setenv("SHELL", "/bin/false")
	t.Cleanup(func() {
		if _, err := os.Stat(filepath.Join(cf.home, ".codex")); err == nil {
			t.Errorf("%s exists: a Codex launch must create no Codex folder", filepath.Join(cf.home, ".codex"))
		}
	})
	return cf
}

// program writes script as an executable file called name in a new directory
// and returns its path.
func program(t *testing.T, name, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

// onPath puts the directory of program first on PATH.
func onPath(t *testing.T, program string) {
	t.Helper()
	t.Setenv("PATH", filepath.Dir(program)+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// useCodexPath rewrites the fixture's config with [launch] codex_path = p,
// keeping its [tmux] timeouts, and reopens the client over the same store: the
// client reads the config once, at api.New.
func (f *codexFix) useCodexPath(t *testing.T, p string) {
	t.Helper()
	cfgPath := filepath.Join(f.home, ".config", "agent-director", "config.toml")
	apitest.WriteKeysConfig(t, cfgPath,
		apitest.ConfigKeys{CodexPath: p},
		apitest.TmuxInt(config.TmuxQueryTimeoutMs, clientTimeouts.Query.Milliseconds()),
		apitest.TmuxInt(config.TmuxActionTimeoutMs, clientTimeouts.Action.Milliseconds()),
		apitest.TmuxInt(config.TmuxCreateTimeoutMs, clientTimeouts.Create.Milliseconds()))
	reopened, err := api.New(api.Options{StorePath: f.DBPath, ConfigPath: cfgPath})
	if err != nil {
		t.Fatalf("reopen the client with [launch] codex_path: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	f.API = reopened
}

// spawnCodex spawns a Codex agent with a new instance id (no pre-trust).
func (f *codexFix) spawnCodex(t *testing.T) spawned {
	t.Helper()
	return f.mustLaunchWith(t, api.SpawnParams{InstanceID: newInstanceID("cx"), Agent: "codex"})
}

// waitPaneRuns waits until the pane process runs script (bash and sh show the
// interpreter first and the script second in argv), and returns the pane pid.
func waitPaneRuns(t *testing.T, s spawned, script string) int {
	t.Helper()
	paneID := s.Row.PaneID.(string)
	panePID := s.rt.formatInt(t, paneID, "#{pane_pid}")
	if fmt.Sprint(s.Row.PanePID) != strconv.Itoa(panePID) || s.Row.State != "pending" {
		t.Fatalf("after spawn: pane_pid %v (tmux says %d), state %v; want the pane recorded on a pending row",
			s.Row.PanePID, panePID, s.Row.State)
	}
	waitFor(t, "the pane process runs "+script,
		func() bool { argv := procCmdline(t, panePID); return len(argv) >= 2 && argv[1] == script },
		func() string { return fmt.Sprintf("%s, cmdline %q", procState(panePID), procCmdline(t, panePID)) })
	return panePID
}

// codexHook is what the stand-in's hook run left.
type codexHook struct {
	Rec       codexRecord
	SessionID string // the payload's session id
	Exit      string
	Stdout    string
	Shell     string
}

// fire has the stand-in run AD's SessionStart hook command from its recorded
// launch, through its login shell with -c, with the Codex SessionStart payload
// of cwd on stdin; the launch's recorded program path is swapped for the
// built CLI's. It returns once the hook has exited.
func (f *codexFix) fire(t *testing.T, id, cwd string) codexHook {
	t.Helper()
	rec := codexRecordOf(f.root, id)
	rec.waitRecorded(t)
	command, exe := sessionStartCommand(t, rec.launch(t))
	quoted := "'" + exe + "'"
	if !strings.Contains(command, quoted) {
		t.Fatalf("SessionStart command %q does not quote its program path %q", command, exe)
	}
	hookCmd := strings.Replace(command, quoted, "'"+f.bin+"'", 1)
	if err := os.WriteFile(rec.path("hookcmd"), []byte(hookCmd), 0o600); err != nil {
		t.Fatalf("write hookcmd: %v", err)
	}
	sid := rec.writePayload(t, cwd)
	// Only now, with the pane recorded and everything in place, may it fire.
	if err := os.WriteFile(rec.path("go"), nil, 0o600); err != nil {
		t.Fatalf("write go-file: %v", err)
	}
	waitFor(t, "the stand-in's hook exits", func() bool { return rec.has("hook.exit") },
		func() string { return "no hook.exit in " + rec.dir })
	return codexHook{
		Rec: rec, SessionID: sid,
		Exit:   readFileTrim(t, rec.path("hook.exit")),
		Stdout: readFileTrim(t, rec.path("hook.out")),
		Shell:  readFileTrim(t, rec.path("shell")),
	}
}

// assertHookRan checks what every hook run shares: the login shell was bash
// or zsh (never the $SHELL of /bin/false), the exit status 0 and nothing on
// stdout (a startup file's output would be there).
func (h codexHook) assertHookRan(t *testing.T) {
	t.Helper()
	if base := filepath.Base(h.Shell); base != "bash" && base != "zsh" {
		t.Errorf("the hook ran through %q, want the account's login shell, bash or zsh (never $SHELL)", h.Shell)
	}
	if h.Exit != "0" {
		t.Errorf("hook exit status %s, want 0 (stderr: %q)", h.Exit, readFileTrim(t, h.Rec.path("hook.err")))
	}
	if h.Stdout != "" {
		t.Errorf("hook stdout %q, want empty", h.Stdout)
	}
}

// TestCodexHookAppliesOnlyFromThePaneProcess spawns a Codex agent on real tmux
// with the stand-in (or a launcher that runs it) as the pane's command, from
// PATH or from [launch] codex_path, and checks that its SessionStart hook
// moves the row only when the hook is a direct child of the pane process
// (SR-22.9). An applied hook takes the row from pending to waiting and records
// the payload's session id; otherwise the row is untouched, the hook writes one
// ad.hook.ignored with reason pid_mismatch and, for a launcher, one
// ad.hook.pane_is_grandparent that names the pane process and the two ways out
// (RN-21).
func TestCodexHookAppliesOnlyFromThePaneProcess(t *testing.T) {
	cases := []struct {
		name string
		// launcher: "" the stand-in is the program; "noexec" | "exec" a codex
		// launcher runs it.
		launcher string
		// codexPath: [launch] codex_path names the program, with no codex on
		// PATH; otherwise the program is first on PATH.
		codexPath       bool
		applied         bool
		wantGrandparent bool
	}{
		{name: "stand-in codex from PATH", applied: true},
		{name: "stand-in codex through [launch] codex_path, none on PATH", codexPath: true, applied: true},
		{name: "behind a codex launcher that does not exec, on PATH", launcher: "noexec", wantGrandparent: true},
		{name: "behind a codex launcher that does not exec, named by [launch] codex_path",
			launcher: "noexec", codexPath: true, wantGrandparent: true},
		// The other half of the advice: a wrapper that execs the program.
		{name: "behind a codex launcher that execs, on PATH", launcher: "exec", applied: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newCodexFix(t)
			standIn := program(t, "codex", codexStubScript)
			launched, paneScript := standIn, standIn // the program named, the script the pane process runs
			if tc.launcher != "" {
				launched = program(t, "codex", launcherScript(standIn, tc.launcher == "exec"))
				if tc.launcher == "noexec" {
					paneScript = launched
				}
			}
			if tc.codexPath {
				f.useCodexPath(t, launched)
			} else {
				onPath(t, launched)
			}

			s := f.spawnCodex(t)
			panePID := waitPaneRuns(t, s, paneScript)
			if s.Row.Agent != "codex" {
				t.Fatalf("row agent %v, want codex", s.Row.Agent)
			}
			h := f.fire(t, s.ID, f.CWD)
			h.assertHookRan(t)

			row := readRow(t, f.DBPath, s.ID)
			ignored := hookTrailRecords(t, f.home, "ad.hook.ignored", s.ID)
			detected := hookTrailRecords(t, f.home, "ad.hook.pane_is_grandparent", s.ID)
			if tc.applied {
				// SR-22.9: an applied SessionStart sets waiting and records the
				// payload's session id and pid/proc_starttime = the hook's parent.
				if row.State != "waiting" || fmt.Sprint(row.PID) != strconv.Itoa(panePID) ||
					fmt.Sprint(row.ProcStarttime) != fmt.Sprint(row.PaneStarttime) || row.SessionID != h.SessionID {
					t.Errorf("row: state %v, pid %v, proc_starttime %v, session_id %v; want waiting, pid %d, proc_starttime = pane_starttime %v, session %s",
						row.State, row.PID, row.ProcStarttime, row.SessionID, panePID, row.PaneStarttime, h.SessionID)
				}
				if len(ignored) != 0 || len(detected) != 0 {
					t.Errorf("%d ad.hook.ignored and %d ad.hook.pane_is_grandparent records for an applied hook, want none",
						len(ignored), len(detected))
				}
				return
			}

			// SR-22.9: a hook not applied changes nothing.
			if !reflect.DeepEqual(row, s.Row) {
				t.Errorf("row changed by an ignored hook:\n  before %+v\n  after  %+v", s.Row, row)
			}
			f.assertIgnoredAndGrandparent(t, s.ID, panePID, h.SessionID, ignored, detected)
		})
	}
}

// assertIgnoredAndGrandparent checks the one pid_mismatch record and the one
// pane_is_grandparent record a launcher's hook leaves, and returns the latter's
// advice.
func (f *codexFix) assertIgnoredAndGrandparent(t *testing.T, id string, panePID int, sessionID string, ignored, detected []map[string]any) string {
	t.Helper()
	if len(ignored) != 1 {
		t.Fatalf("%d ad.hook.ignored records, want exactly one", len(ignored))
	}
	rec := ignored[0]
	parent := fmt.Sprint(rec["parent_pid"])
	for k, w := range map[string]string{
		"hook_event":      "SessionStart",
		"reason":          "pid_mismatch",
		"parent_command":  "codex", // the stand-in, the launcher's child
		"hook_session_id": sessionID,
		"row_pane_pid":    strconv.Itoa(panePID),
		"source":          "ad_hook",
	} {
		if got := fmt.Sprint(rec[k]); got != w {
			t.Errorf("ad.hook.ignored %s = %s, want %s", k, got, w)
		}
	}
	if parent == strconv.Itoa(panePID) {
		t.Errorf("ad.hook.ignored parent_pid = %s, the pane process: the hook's parent must be the launcher's child", parent)
	}
	if v, ok := rec["row_session_id"]; !ok || v != nil {
		t.Errorf("ad.hook.ignored row_session_id = %v (present %v), want null (the row records none)", v, ok)
	}
	if len(detected) != 1 {
		t.Fatalf("%d ad.hook.pane_is_grandparent records, want exactly one", len(detected))
	}
	for k, w := range map[string]string{
		"pane_pid":       strconv.Itoa(panePID),
		"pane_command":   "codex",
		"parent_pid":     parent,
		"parent_command": "codex",
		"source":         "ad_hook",
	} {
		if got := fmt.Sprint(detected[0][k]); got != w {
			t.Errorf("ad.hook.pane_is_grandparent %s = %s, want %s", k, got, w)
		}
	}
	// The advice is Codex's, not Claude's: it names the codex launcher, the
	// shell-form hook and [launch] codex_path.
	advice := fmt.Sprint(detected[0]["advice"])
	for _, want := range []string{
		"the codex on PATH is a launcher that runs Codex as a child instead of exec-ing it",
		"Codex ran the hook through a shell instead of in exec form",
		"name the native codex program in [launch] codex_path, or use a wrapper that execs it",
	} {
		if !strings.Contains(advice, want) {
			t.Errorf("ad.hook.pane_is_grandparent advice = %q, want it to contain %q", advice, want)
		}
	}
	for _, claude := range []string{"claude_path", "Claude Code"} {
		if strings.Contains(advice, claude) {
			t.Errorf("a Codex row's advice = %q, want no mention of %q", advice, claude)
		}
	}
	return advice
}

// TestCodexAdviceFollowedLiterally follows the ad.hook.pane_is_grandparent
// advice of a Codex row word for word: with a launcher that does not exec
// first on PATH, the first agent's hook is ignored and its row stays pending;
// the advice says to name the native codex program in [launch] codex_path, so
// codex_path is set to the real stand-in and a new agent is spawned, which is
// tracked: its hook applies and no pane_is_grandparent is written for it. The
// first agent's row is not touched by the second.
func TestCodexAdviceFollowedLiterally(t *testing.T) {
	f := newCodexFix(t)
	standIn := program(t, "codex", codexStubScript)
	launcher := program(t, "codex", launcherScript(standIn, false))
	onPath(t, launcher) // the launcher stays first on PATH throughout

	first := f.spawnCodex(t)
	waitPaneRuns(t, first, launcher)
	h1 := f.fire(t, first.ID, f.CWD)
	h1.assertHookRan(t)
	panePID1 := first.Row.PanePID
	advice := f.assertIgnoredAndGrandparent(t, first.ID, mustAtoi(t, fmt.Sprint(panePID1)), h1.SessionID,
		hookTrailRecords(t, f.home, "ad.hook.ignored", first.ID),
		hookTrailRecords(t, f.home, "ad.hook.pane_is_grandparent", first.ID))
	before := readRow(t, f.DBPath, first.ID)
	if before.State != "pending" || !reflect.DeepEqual(before, first.Row) {
		t.Fatalf("the first row after its ignored hook: state %v, changed %v; want the pending row untouched",
			before.State, !reflect.DeepEqual(before, first.Row))
	}

	// The advice, followed: name the native program in [launch] codex_path.
	const step = "name the native codex program in [launch] codex_path"
	if !strings.Contains(advice, step) {
		t.Fatalf("the advice %q does not carry the step %q", advice, step)
	}
	f.useCodexPath(t, standIn)

	second := f.spawnCodex(t)
	panePID2 := waitPaneRuns(t, second, standIn)
	h2 := f.fire(t, second.ID, f.CWD)
	h2.assertHookRan(t)
	row := readRow(t, f.DBPath, second.ID)
	if row.State != "waiting" || fmt.Sprint(row.PID) != strconv.Itoa(panePID2) || row.SessionID != h2.SessionID {
		t.Errorf("after following the advice: state %v, pid %v, session_id %v; want waiting, pid %d, session %s",
			row.State, row.PID, row.SessionID, panePID2, h2.SessionID)
	}
	if n := len(hookTrailRecords(t, f.home, "ad.hook.pane_is_grandparent", second.ID)); n != 0 {
		t.Errorf("%d ad.hook.pane_is_grandparent records for the agent launched as the advice says, want none", n)
	}
	if n := len(hookTrailRecords(t, f.home, "ad.hook.ignored", second.ID)); n != 0 {
		t.Errorf("%d ad.hook.ignored records for the agent launched as the advice says, want none", n)
	}
	if after := readRow(t, f.DBPath, first.ID); !reflect.DeepEqual(after, before) {
		t.Errorf("the first row changed when the second agent reported in:\n  before %+v\n  after  %+v", before, after)
	}
}

// mustAtoi parses s as a pid.
func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("%q is not a number: %v", s, err)
	}
	return n
}

// TestCodexLaunchEnvironment: a BASH_ENV in the tmux server's environment, a
// file bash would source in a non-interactive shell, does not reach the agent
// process as a file name: AD sets it empty on every Codex launch (Q5(c)), so
// neither the stand-in's own bash nor the hook's `bash -c` sources it (a
// control `bash -c` shows the file works when the variable is set), and the
// hook's stdout stays empty. AGENT_DIRECTOR_AGENT=codex and the other
// AGENT_DIRECTOR_* variables of the launch do reach it.
func TestCodexLaunchEnvironment(t *testing.T) {
	f := newCodexFix(t)
	standIn := program(t, "codex", codexStubScript)
	onPath(t, standIn)

	marker := filepath.Join(t.TempDir(), "bash-env-sourced")
	startup := program(t, "bash-env.sh", "echo sourced >> '"+marker+"'\necho 'startup-file noise'\n")
	control := exec.Command("bash", "-c", "true")
	control.Env = append(os.Environ(), "BASH_ENV="+startup)
	if out, err := control.CombinedOutput(); err != nil || !strings.Contains(string(out), "startup-file noise") {
		t.Fatalf("control: bash -c with BASH_ENV=%s printed %q, err %v; want the file sourced", startup, out, err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatalf("control: BASH_ENV file did not write its marker: %v", err)
	}
	t.Setenv("BASH_ENV", startup) // the tmux server's global environment

	s := f.spawnCodex(t)
	panePID := waitPaneRuns(t, s, standIn)
	h := f.fire(t, s.ID, f.CWD)
	h.assertHookRan(t)
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("BASH_ENV file was sourced (by the stand-in's bash or the hook's shell): the variable reached the agent as a file name")
	}

	paneEnv := procEnviron(t, panePID)
	recorded := h.Rec.nulList(t, "env")
	for name, env := range map[string][]string{"the pane process": paneEnv, "the stand-in's record": recorded} {
		if v, ok := envValue(env, "BASH_ENV"); !ok || v != "" {
			t.Errorf("%s: BASH_ENV = %q (set %v), want set and empty", name, v, ok)
		}
		for key, want := range map[string]string{
			"AGENT_DIRECTOR_AGENT":       "codex",
			"AGENT_DIRECTOR_INSTANCE_ID": s.ID,
		} {
			if v, ok := envValue(env, key); !ok || v != want {
				t.Errorf("%s: %s = %q (set %v), want %q", name, key, v, ok, want)
			}
		}
		if _, ok := envValue(env, "AGENT_DIRECTOR_RELAY_MODE"); !ok {
			t.Errorf("%s: AGENT_DIRECTOR_RELAY_MODE is not set", name)
		}
		if v, ok := envValue(env, "CODEX_HOME"); ok {
			t.Errorf("%s: CODEX_HOME = %q, want it unset (AD never sets it)", name, v)
		}
	}
	if row := readRow(t, f.DBPath, s.ID); row.State != "waiting" {
		t.Errorf("row state %v after the hook, want waiting", row.State)
	}
}

// TestCodexLaunchRecordsTheLaunch checks what the stand-in records of a Codex
// launch that the other cases rely on: the program is run in the spawn's cwd,
// AD's settings (its SessionStart hook in exec form, the paste setting) are in
// the arguments, and none of them is a Claude one.
func TestCodexLaunchRecordsTheLaunch(t *testing.T) {
	f := newCodexFix(t)
	onPath(t, program(t, "codex", codexStubScript))

	s := f.spawnCodex(t)
	rec := codexRecordOf(f.root, s.ID)
	rec.waitRecorded(t)

	if want, err := filepath.EvalSymlinks(f.CWD); err != nil || readFileTrim(t, rec.path("cwd")) != want {
		t.Errorf("the stand-in ran in %q, want the spawn's cwd %q (%v)", readFileTrim(t, rec.path("cwd")), want, err)
	}
	l := rec.launch(t)
	command, exe := sessionStartCommand(t, l)
	if !strings.HasPrefix(command, "exec '"+exe+"' hook --agent codex") {
		t.Errorf("SessionStart command %q, want `exec '<binary>' hook --agent codex`", command)
	}
	if len(l.Of("paste")) != 1 {
		t.Errorf("the launch has %d paste settings, want 1", len(l.Of("paste")))
	}
	if slices.Contains(rec.nulList(t, "argv"), "--settings") {
		t.Errorf("the Codex launch carries Claude's --settings: %q", rec.nulList(t, "argv"))
	}
}

// TestCodexPathNamingNothingLeavesThePendingRowMissing: [launch] codex_path
// names a file that is not there. The launch still happens (the pane's program
// cannot start), the row stays pending and records no session, and once its
// launch is older than the pending grace period find-missing marks it missing
// (SR-11.1).
func TestCodexPathNamingNothingLeavesThePendingRowMissing(t *testing.T) {
	f := newCodexFix(t)
	nothing := filepath.Join(t.TempDir(), "no-such-dir", "codex")
	f.useCodexPath(t, nothing)

	id, err := f.spawnWith(api.SpawnParams{InstanceID: newInstanceID("cx"), Agent: "codex"})
	if err != nil {
		t.Fatalf("spawn: %s", describe(err))
	}
	row := readRow(t, f.DBPath, id)
	if row.State != "pending" || row.Agent != "codex" || row.SessionID != nil {
		t.Fatalf("row after spawn: state %v, agent %v, session_id %v; want a pending codex row with no session", row.State, row.Agent, row.SessionID)
	}
	panePID := mustAtoi(t, fmt.Sprint(row.PanePID))
	waitPidGone(t, panePID) // nothing to run: the pane's program never started
	if _, err := os.Stat(filepath.Join(f.root, id)); err == nil {
		t.Errorf("the stand-in recorded a launch for %s: the program named by codex_path was not it", id)
	}

	// Inside the pending grace period a pending row is left alone, whatever
	// its pane does (the agent may still be starting).
	if res, err := f.API.FindMissing(context.Background()); err != nil || slices.Contains(res.IDs, id) {
		t.Fatalf("find-missing inside the grace period: %+v, %v; want the pending row left alone", res, err)
	}
	if got := readRow(t, f.DBPath, id); got.State != "pending" {
		t.Fatalf("row state %v after a sweep inside the grace period, want pending", got.State)
	}

	agePendingLaunch(t, f.DBPath, id, 10*time.Minute)
	res, err := f.API.FindMissing(context.Background())
	if err != nil {
		t.Fatalf("find-missing: %s", describe(err))
	}
	if !slices.Contains(res.IDs, id) {
		t.Errorf("find-missing %+v after the grace period, want %s marked", res, id)
	}
	if got := readRow(t, f.DBPath, id); got.State != "missing" || got.SessionID != nil {
		t.Errorf("row after the sweep: state %v, session_id %v; want missing and no session", got.State, got.SessionID)
	}
}

// TestCodexStandInsShareTheirBlock keeps the realtmux stand-in and the Docker
// driver's (test/driver/stand-in-codex/codex) alike: the text between their
// "shared" markers is the same, so a change to what one records or how it runs
// the hook is a change to both.
func TestCodexStandInsShareTheirBlock(t *testing.T) {
	raw, err := os.ReadFile(codexDriverStandIn)
	if err != nil {
		t.Fatalf("read the driver stand-in: %v", err)
	}
	text := string(raw)
	begin := strings.Index(text, codexSharedBegin)
	end := strings.Index(text, codexSharedEnd)
	if begin < 0 || end < begin {
		t.Fatalf("%s lacks the %q ... %q markers", codexDriverStandIn, codexSharedBegin, codexSharedEnd)
	}
	if got := text[begin : end+len(codexSharedEnd)+1]; got != codexSharedBlock {
		t.Errorf("the driver stand-in's shared block differs from this package's:\n--- driver\n%s\n--- realtmux\n%s", got, codexSharedBlock)
	}
	if info, err := os.Stat(codexDriverStandIn); err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Errorf("the driver stand-in is not executable (%v, %v)", info, err)
	}
	if !strings.HasPrefix(text, "#!/bin/bash\n") {
		t.Errorf("the driver stand-in does not start with #!/bin/bash")
	}
	// Past the block it must end as the pane's own interactive shell (the
	// Docker cases type hooks into it), the realtmux copy as a sleeping loop.
	if !strings.HasSuffix(strings.TrimSpace(text[end:]), "exec bash --noprofile --norc -i") {
		t.Errorf("the driver stand-in does not end in an exec of an interactive bash")
	}
}

// agePendingLaunch moves id's launch_started_at back by age, so that a pending
// row is past the pending grace period without the test waiting for it. No
// apitest helper does this for a row a real spawn made.
func agePendingLaunch(t *testing.T, dbPath, id string, age time.Duration) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open the store file: %v", err)
	}
	defer db.Close() //nolint:errcheck
	res, err := db.Exec(`UPDATE spawns SET launch_started_at = launch_started_at - ? WHERE instance_id = ?`,
		age.Milliseconds(), id)
	if err != nil {
		t.Fatalf("age launch_started_at of %s: %v", id, err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("age launch_started_at of %s: %d rows, %v", id, n, err)
	}
}
