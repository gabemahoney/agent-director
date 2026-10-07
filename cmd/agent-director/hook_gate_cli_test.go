package main_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The hook gate through the built CLI (SR-22.9): only a real process proves
// the production wiring (os.Getppid, probe.NewProcChecker, the /proc command
// name). Every hook here is a child of this test process, so the test process
// is the hook's parent, the process the gate compares with the row's recorded
// pane process. Every hook also runs with test/fake-tmux first on PATH and
// its call log set, and must leave that log empty: the hook path makes no
// tmux call (SR-22.9).

// gateHookDeadline bounds one hook run; a hook still alive is killed and fails
// the test.
const gateHookDeadline = 30 * time.Second

// gateRelayTimeoutSeconds is the relay window a relay-on home configures, so an
// applied relayed PermissionRequest ends on its own with the timeout deny.
const gateRelayTimeoutSeconds = 1

// gateHome is a throwaway HOME whose hooks run with the fake tmux first on
// PATH, logging every call to <home>/fake-tmux.log.
type gateHome struct {
	home, fakeDir string
}

func newGateHome(t *testing.T) gateHome {
	t.Helper()
	return gateHome{home: t.TempDir(), fakeDir: buildFakeTmux(t)}
}

// seed creates a pending row with relayMode and the given launch identity.
func (h gateHome) seed(t *testing.T, id, relayMode string, identity apitest.SpawnOption) {
	t.Helper()
	if _, err := apitest.SeedSpawn(stateDB(h.home), id, store.StatePending, "", relayMode, "", true, identity); err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
}

// hook pipes payload into the built CLI's hook verb for id, as a child of this
// test process, with the environment an agent's pane gives it (TMUX and
// TMUX_PANE set, the fake tmux first on PATH). relayMode "" leaves
// AGENT_DIRECTOR_RELAY_MODE unset. It fails the test unless the hook exits 0
// within gateHookDeadline and made no tmux call, and returns its stdout.
func (h gateHome) hook(t *testing.T, id, relayMode, payload string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), gateHookDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath, "hook")
	cmd.Env = []string{
		"PATH=" + h.fakeDir + ":" + os.Getenv("PATH"),
		"HOME=" + h.home,
		"AGENT_DIRECTOR_INSTANCE_ID=" + id,
		"FAKE_TMUX_LOG=" + filepath.Join(h.home, "fake-tmux.log"),
		"TMUX_TMPDIR=" + spawnTmuxTmpdir(t, h.home),
		"TMUX=" + apitest.TestSocket + ",1,0",
		"TMUX_PANE=" + apitest.TestPaneID,
	}
	if relayMode != "" {
		cmd.Env = append(cmd.Env, hook.EnvRelayMode+"="+relayMode)
	}
	cmd.WaitDelay = time.Second
	cmd.Stdin = strings.NewReader(payload)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("hook still running after %s; stderr=%q", gateHookDeadline, stderr.String())
	}
	if err != nil {
		t.Fatalf("hook: %v (exit %d); want exit 0\nstderr=%s", err, cmd.ProcessState.ExitCode(), stderr.String())
	}
	assertNoTmuxCall(t, h.home)
	return stdout.String()
}

// assertNoTmuxCall fails when the fake tmux logged any call under home.
func assertNoTmuxCall(t *testing.T, home string) {
	t.Helper()
	if invs := fakeTmuxInvocations(t, home); len(invs) != 0 {
		t.Errorf("hook path called tmux %d time(s): %q; want none (SR-22.9)", len(invs), invs)
	}
}

// row reads id's row through the store.
func (h gateHome) row(t *testing.T, id string) store.Spawn {
	t.Helper()
	st, err := store.Open(stateDB(h.home))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	sp, err := st.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn(%s): %v", id, err)
	}
	return sp
}

// transcript plants a transcript file named <sessionID>.jsonl under home (the
// SessionStart write records jsonl_path only for a file that exists) and
// returns its path.
func (h gateHome) transcript(t *testing.T, sessionID string) string {
	t.Helper()
	path := filepath.Join(h.home, ".claude", "projects", "gate", sessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir transcript parent: %v", err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

// trailEvents returns the lines of home's trail whose event is name.
func trailEvents(t *testing.T, home, name string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range readTrailLines(t, home) {
		if l["event"] == name {
			out = append(out, l)
		}
	}
	return out
}

// liveStart returns pid's real start time, failing the test when unreadable.
func liveStart(t *testing.T, pid int) string {
	t.Helper()
	start, alive, known := probe.NewProcChecker().StartTime(pid)
	if !known || !alive || start == "" {
		t.Fatalf("StartTime(%d) = (%q, alive=%v, known=%v); want a live start time", pid, start, alive, known)
	}
	return start
}

// paneIdentity is a launch identity whose pane process is pid with start time
// start ("" = NULL); withTestProcessPane covers this test process with its
// recorded start.
func paneIdentity(pid int, start string) apitest.SpawnOption {
	return apitest.WithLaunchIdentity(store.LaunchIdentity{
		Token:         "5eed0000000000b2",
		Socket:        apitest.TestSocket,
		PaneID:        apitest.TestPaneID,
		PanePID:       pid,
		PaneStarttime: start,
	})
}

// TestHookGateCLIOwnPaneApplies: a row whose pane process is this test
// process (the hooks' real parent) takes a SessionStart and then a Stop; both
// apply. SR-22.9: an applied SessionStart records pid/proc_starttime = the
// hook's parent; a NULL pane_starttime lets the pid alone decide and the
// write records the parent's start time.
func TestHookGateCLIOwnPaneApplies(t *testing.T) {
	pid := os.Getpid()
	start := liveStart(t, pid)
	cases := []struct {
		name      string
		identity  apitest.SpawnOption
		paneStart string // the row's recorded pane_starttime; "" = NULL
	}{
		{name: "recorded_start", identity: withTestProcessPane(t), paneStart: start},
		{name: "null_start", identity: paneIdentity(pid, ""), paneStart: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newGateHome(t)
			const id = "id-gate-own"
			h.seed(t, id, "off", tc.identity)
			if got := h.row(t, id).Identity.PaneStarttime; got != tc.paneStart {
				t.Fatalf("seeded pane_starttime = %q; want %q", got, tc.paneStart)
			}
			transcript := h.transcript(t, "gate-own-uuid")

			out := h.hook(t, id, "", `{"hook_event_name":"SessionStart","transcript_path":"`+transcript+`"}`)
			if out != "" {
				t.Errorf("SessionStart stdout = %q; want empty", out)
			}
			sp := h.row(t, id)
			if sp.State != store.StateWaiting || sp.ClaudeSessionID != "gate-own-uuid" || sp.JSONLPath != transcript {
				t.Errorf("after SessionStart: state=%q session=%q jsonl=%q; want waiting, gate-own-uuid, %q",
					sp.State, sp.ClaudeSessionID, sp.JSONLPath, transcript)
			}
			if sp.PID != pid || sp.ProcStarttime != start {
				t.Errorf("after SessionStart: pid=%d proc_starttime=%q; want %d, %q (the hook's parent)",
					sp.PID, sp.ProcStarttime, pid, start)
			}
			if sp.Identity.PanePID != pid || sp.Identity.PaneStarttime != start {
				t.Errorf("after SessionStart: pane_pid=%d pane_starttime=%q; want %d, %q",
					sp.Identity.PanePID, sp.Identity.PaneStarttime, pid, start)
			}

			out = h.hook(t, id, "", `{"hook_event_name":"Stop"}`)
			if out != "" {
				t.Errorf("Stop stdout = %q; want empty", out)
			}
			after := h.row(t, id)
			if after.State != store.StateWaiting || after.RowVersion != sp.RowVersion+1 {
				t.Errorf("after Stop: state=%q row_version=%d; want waiting, %d (the Stop applied)",
					after.State, after.RowVersion, sp.RowVersion+1)
			}
			if after.PID != pid || after.ProcStarttime != start {
				t.Errorf("after Stop: pid=%d proc_starttime=%q; want %d, %q", after.PID, after.ProcStarttime, pid, start)
			}

			fired := trailEvents(t, h.home, "ad.hook.fired")
			if len(fired) != 2 {
				t.Fatalf("ad.hook.fired lines = %d; want 2", len(fired))
			}
			for i, f := range fired {
				if f["upsert_outcome"] != string(store.UpsertUpdated) {
					t.Errorf("ad.hook.fired[%d] upsert_outcome = %v; want %s", i, f["upsert_outcome"], store.UpsertUpdated)
				}
			}
			if ign := trailEvents(t, h.home, "ad.hook.ignored"); len(ign) != 0 {
				t.Errorf("ad.hook.ignored lines = %v; want none", ign)
			}
		})
	}
}

// TestHookGateCLIForeignPaneIgnored: a row whose pane process is another live
// process (this test's own parent, with its real start time) ignores the
// test's hooks. SR-22.9/SR-14: nothing changes, exit 0, empty stdout, and one
// ad.hook.ignored naming the hook's real parent (this test process). The pane
// process started that parent as a child, as a `claude` launcher that does not
// exec does, so the pending row's SessionStart also writes one
// ad.hook.pane_is_grandparent (b.9n6, b.zde).
func TestHookGateCLIForeignPaneIgnored(t *testing.T) {
	pid := os.Getpid()
	other := os.Getppid()
	if other == pid || other <= 0 {
		t.Fatalf("test parent pid %d unusable as another process (test pid %d)", other, pid)
	}
	otherStart := liveStart(t, other)
	wantCommand, ok := probe.NewCommandNameReader().CommandName(pid)
	if !ok || wantCommand == "" {
		t.Fatalf("CommandName(test pid %d) = (%q, %v); want the test process's name", pid, wantCommand, ok)
	}
	var paneCommand any // the pane process's name; null when unreadable
	if name, ok := probe.NewCommandNameReader().CommandName(other); ok {
		paneCommand = name
	}
	cases := []struct {
		name          string
		event         string
		transcript    bool
		wantHookSessn any  // hook_session_id: the payload's, nil when none
		wantEvent     bool // one ad.hook.pane_is_grandparent (a SessionStart on the pending row)
	}{
		{name: "SessionStart", event: "SessionStart", transcript: true, wantHookSessn: "gate-foreign-uuid", wantEvent: true},
		{name: "Stop", event: "Stop", wantHookSessn: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newGateHome(t)
			const id = "id-gate-foreign"
			h.seed(t, id, "off", paneIdentity(other, otherStart))
			payload := `{"hook_event_name":"` + tc.event + `"}`
			if tc.transcript {
				payload = `{"hook_event_name":"` + tc.event + `","transcript_path":"` + h.transcript(t, "gate-foreign-uuid") + `"}`
			}
			before := h.row(t, id)

			if out := h.hook(t, id, "", payload); out != "" {
				t.Errorf("stdout = %q; want empty", out)
			}

			after := h.row(t, id)
			if after.State != before.State || after.RowVersion != before.RowVersion ||
				!after.LastSeenAt.Equal(before.LastSeenAt) || after.ClaudeSessionID != before.ClaudeSessionID ||
				after.JSONLPath != before.JSONLPath || after.PID != before.PID ||
				after.ProcStarttime != before.ProcStarttime || after.Identity != before.Identity {
				t.Errorf("row changed by an ignored hook:\nbefore=%+v\nafter =%+v", before, after)
			}

			ign := trailEvents(t, h.home, "ad.hook.ignored")
			if len(ign) != 1 {
				t.Fatalf("ad.hook.ignored lines = %d; want exactly 1: %v", len(ign), ign)
			}
			want := map[string]any{
				"claude_instance_id": id,
				"hook_event":         tc.event,
				"reason":             store.HookReasonPIDMismatch,
				"parent_pid":         float64(pid),
				"parent_command":     wantCommand,
				"hook_session_id":    tc.wantHookSessn,
				"row_session_id":     nil,
				"row_pane_pid":       float64(other),
				"source":             "ad_hook",
			}
			for k, v := range want {
				got, present := ign[0][k]
				if !present || got != v {
					t.Errorf("ad.hook.ignored %s = %v (present=%v); want %v", k, got, present, v)
				}
			}
			events := trailEvents(t, h.home, "ad.hook.pane_is_grandparent")
			if !tc.wantEvent {
				if len(events) != 0 {
					t.Errorf("ad.hook.pane_is_grandparent = %v; want none", events)
				}
			} else if len(events) != 1 {
				t.Errorf("ad.hook.pane_is_grandparent lines = %d; want exactly 1: %v", len(events), events)
			} else {
				want := map[string]any{
					"claude_instance_id": id,
					"pane_pid":           float64(other),
					"pane_command":       paneCommand,
					"parent_pid":         float64(pid),
					"parent_command":     wantCommand,
					"source":             "ad_hook",
				}
				for k, v := range want {
					if got, present := events[0][k]; !present || got != v {
						t.Errorf("ad.hook.pane_is_grandparent %s = %v (present=%v); want %v", k, got, present, v)
					}
				}
				if advice, _ := events[0]["advice"].(string); !strings.Contains(advice, "a wrapper that execs it") {
					t.Errorf("ad.hook.pane_is_grandparent advice = %q; want the pane-is-grandparent advice", advice)
				}
			}
			// A11: the ignored hook's ad.hook.fired reports no_change.
			fired := trailEvents(t, h.home, "ad.hook.fired")
			if len(fired) != 1 || fired[0]["upsert_outcome"] != string(store.UpsertNoChange) {
				t.Errorf("ad.hook.fired = %v; want one line with upsert_outcome %s", fired, store.UpsertNoChange)
			}
		})
	}
}

// TestHookGateCLINoTmuxCall: with the fake tmux first on PATH and its call log
// set, an applied SessionStart and an applied relayed PermissionRequest
// (relay on) leave the log empty. SR-22.9: no resolver walk and no tmux call
// on the hook path. The control first proves the fake logs every socket-form
// call to FAKE_TMUX_LOG, so an empty log is not vacuous.
func TestHookGateCLINoTmuxCall(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	assertFakeTmuxLogs(t, fakeDir)

	deny := hook.EncodeDecision(hook.EventNamePermissionRequest, "deny", "") + "\n"
	cases := []struct {
		name       string
		relayMode  string
		payload    string
		wantStdout string
		wantState  string
		wantDenied bool // a stored request decided deny/timeout (the relay flow ran)
	}{
		{
			name:       "session_start",
			relayMode:  "off",
			payload:    `{"hook_event_name":"SessionStart"}`,
			wantStdout: "",
			wantState:  store.StateWaiting,
		},
		{
			name:       "relayed_permission_request",
			relayMode:  hook.RelayModeOn,
			payload:    `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"}}`,
			wantStdout: deny,
			wantState:  store.StateWorking,
			wantDenied: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := gateHome{home: t.TempDir(), fakeDir: fakeDir}
			const id = "id-gate-notmux"
			h.seed(t, id, tc.relayMode, withTestProcessPane(t))
			writeRelayTimeout(t, h.home)

			// h.hook asserts the fake's log is empty after the hook.
			if out := h.hook(t, id, tc.relayMode, tc.payload); out != tc.wantStdout {
				t.Errorf("stdout = %q; want %q", out, tc.wantStdout)
			}
			if got := h.row(t, id).State; got != tc.wantState {
				t.Errorf("state = %q; want %q (the hook applied)", got, tc.wantState)
			}
			if tc.wantDenied {
				assertOneTimeoutDeny(t, h.home, id)
			}
		})
	}
}

// assertFakeTmuxLogs runs the fake tmux directly with a socket-form call and
// fails unless it logged that call to FAKE_TMUX_LOG.
func assertFakeTmuxLogs(t *testing.T, fakeDir string) {
	t.Helper()
	home := t.TempDir()
	cmd := exec.Command(filepath.Join(fakeDir, "tmux"), "-u", "-S", apitest.TestSocket, "list-sessions")
	cmd.Env = []string{
		"FAKE_TMUX_LOG=" + filepath.Join(home, "fake-tmux.log"),
		"FAKE_TMUX_TABLES=" + t.TempDir(),
	}
	_ = cmd.Run() // only the log matters; an empty table may answer non-zero
	if invs := fakeTmuxInvocations(t, home); len(invs) != 1 {
		t.Fatalf("control: fake tmux logged %d call(s); want 1 (%q)", len(invs), invs)
	}
}

// writeRelayTimeout writes home's config with a short relay window so an
// applied relayed PermissionRequest ends on its own.
func writeRelayTimeout(t *testing.T, home string) {
	t.Helper()
	cfg := filepath.Join(directorDir(home), "config.toml")
	body := "[relay]\ntimeout_seconds = " + strconv.Itoa(gateRelayTimeoutSeconds) + "\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// assertOneTimeoutDeny checks id holds exactly one permission request, decided
// deny for timeout: the gated INSERT applied and the relay flow ran to its end.
func assertOneTimeoutDeny(t *testing.T, home, id string) {
	t.Helper()
	st, err := store.Open(stateDB(home))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	reqs, err := st.PermissionRequestsForSpawn(id)
	if err != nil {
		t.Fatalf("PermissionRequestsForSpawn: %v", err)
	}
	if len(reqs) != 1 || reqs[0].Decision != "deny" || reqs[0].DecisionReason != store.DecisionReasonTimeout {
		t.Errorf("permission requests = %+v; want one decided deny/%s", reqs, store.DecisionReasonTimeout)
	}
}

// gateWaitInside is how far inside its pending grace period the waiting case's
// launch start sits: the hook must wait about this long for the bound.
const gateWaitInside = 3 * time.Second

// gateWaitFloor is the least of the grace the waiting case must have left
// when its hook starts, so a hook that does not wait (a few ms) stays clearly
// apart from one that does. Setup gets gateWaitInside - gateWaitFloor.
const gateWaitFloor = time.Second

// gateWaitEarly is how much sooner than the grace left at its start a waiting
// hook may return: the stored launch start is truncated to the millisecond.
const gateWaitEarly = 250 * time.Millisecond

// gatePromptLimit bounds a hook that must not wait: well below the 10 s margin
// every no-wait launch start keeps from any other grace boundary.
const gatePromptLimit = 5 * time.Second

// TestHookGateCLISessionStartGrace: SessionStart on a no-pane pending row waits
// until launch start + the configured (or default) grace, then is ignored as
// no_pane_recorded (SR-22.9, SR-13.4). The CLI has no injected clock, so every
// launch start except the waiting case's sits 10 s or more from any boundary.
func TestHookGateCLISessionStartGrace(t *testing.T) {
	minimum := config.PendingGraceMinimumSeconds(config.DefaultCreateTimeoutMs, config.DefaultPipeCloseWaitMs)
	def := int64(config.DefaultPendingGraceSeconds)
	if def-minimum < 20 {
		t.Fatalf("default %d s - minimum %d s < 20 s: no 10 s margin either side", def, minimum)
	}
	sec := func(s int64) time.Duration { return time.Duration(s) * time.Second }
	configured := []apitest.TmuxSetting{apitest.TmuxInt(config.TmuxPendingGraceSeconds, minimum)}
	cases := []struct {
		name     string
		settings []apitest.TmuxSetting // the [tmux] table written; none = defaults
		grace    time.Duration         // the effective grace the hook must use
		age      time.Duration         // launch start this long before the hook
		wait     bool                  // the hook waits until launch start + grace
	}{
		{name: "configured_grace_waits", settings: configured, grace: sec(minimum), age: sec(minimum) - gateWaitInside, wait: true},
		{name: "configured_grace_past", settings: configured, grace: sec(minimum), age: sec(minimum+def) / 2},
		{name: "default_grace_past", grace: sec(def), age: sec(def) + 10*time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newGateHome(t)
			const id = "id-gate-grace"
			apitest.WriteTmuxConfig(t, filepath.Join(directorDir(h.home), "config.toml"), tc.settings...)
			// The store exists before the launch start is taken, so the
			// waiting case's timed window holds only the row insert.
			st, err := store.OpenOrInit(stateDB(h.home))
			if err != nil {
				t.Fatalf("store.OpenOrInit: %v", err)
			}
			if err := st.Close(); err != nil {
				t.Fatalf("store.Close: %v", err)
			}
			launch := time.Now().Add(-tc.age)
			noPane := store.LaunchIdentity{Token: "5eed0000000000c3", Socket: apitest.TestSocket}
			if _, err := apitest.SeedSpawn(stateDB(h.home), id, store.StatePending, "", "off", "", false,
				apitest.WithLaunchIdentity(noPane), apitest.WithLaunchStartedAt(launch.UnixMilli())); err != nil {
				t.Fatalf("SeedSpawn: %v", err)
			}
			before := h.row(t, id)
			if before.Identity.PanePID != 0 || before.LaunchStartedAtMillis != launch.UnixMilli() {
				t.Fatalf("seeded pane_pid=%d launch_started_at=%d; want 0, %d",
					before.Identity.PanePID, before.LaunchStartedAtMillis, launch.UnixMilli())
			}
			// The waiting hook's expected wait is the grace left just before
			// it runs, not gateWaitInside: setup time is not charged to it.
			left := time.Until(launch.Add(tc.grace))
			if tc.wait && left < gateWaitFloor {
				t.Fatalf("setup took too long: %s left of the grace; want at least %s", left, gateWaitFloor)
			}

			// h.hook fails the test after gateHookDeadline and on any tmux call.
			began := time.Now()
			out := h.hook(t, id, "", `{"hook_event_name":"SessionStart"}`)
			elapsed := time.Since(began)
			if out != "" {
				t.Errorf("stdout = %q; want empty", out)
			}
			if tc.wait {
				if elapsed < left-gateWaitEarly || elapsed > left+gatePromptLimit {
					t.Errorf("hook took %s; want about %s, the grace left at its start (it waits until launch start + %s)",
						elapsed, left, tc.grace)
				}
			} else if elapsed > gatePromptLimit {
				t.Errorf("hook took %s; want under %s (launch start + %s already past)", elapsed, gatePromptLimit, tc.grace)
			}

			after := h.row(t, id)
			if after.State != store.StatePending || after.RowVersion != before.RowVersion ||
				after.Identity != before.Identity || after.ClaudeSessionID != before.ClaudeSessionID {
				t.Errorf("row changed by an ignored SessionStart:\nbefore=%+v\nafter =%+v", before, after)
			}
			ign := trailEvents(t, h.home, "ad.hook.ignored")
			if len(ign) != 1 || ign[0]["reason"] != store.HookReasonNoPaneRecorded || ign[0]["hook_event"] != "SessionStart" {
				t.Errorf("ad.hook.ignored = %v; want one SessionStart line with reason %s", ign, store.HookReasonNoPaneRecorded)
			}
		})
	}
}
