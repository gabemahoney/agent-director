package hook_test

// hook_launcher_test.go — b.9n6: a pid_mismatch hook whose parent's parent is
// the row's pane process (a `claude` launcher that does not exec, or a
// shell-form hook) names that process as ad.hook.ignored's launcher_pid, and a
// SessionStart on a pending row also writes ad.hook.launcher_detected. The
// gate's decision is unchanged.

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// launcherAdviceText is ad.hook.launcher_detected's advice, word for word. It
// names both causes of the structure (a non-exec launcher, a shell-form hook)
// and asserts neither.
const launcherAdviceText = "The parent of this agent's hooks is a child of its pane process, not the pane process itself, " +
	"so agent-director ignores every hook of this agent (pid_mismatch). " +
	"Either the claude on PATH is a launcher that runs Claude Code as a child instead of exec-ing it " +
	"(replace it with the native claude binary or a wrapper that execs it), " +
	"or Claude Code ran the hook through a shell instead of in exec form; parent_command names that parent."

// launcherDetectedKeys is ad.hook.launcher_detected's key set, with the trail
// envelope's event and ts.
var launcherDetectedKeys = []string{
	"advice", "claude_instance_id", "event", "launcher_command", "launcher_pid",
	"parent_command", "parent_pid", "source", "ts",
}

// endSessionWords are words that would tell an operator to end a session by hand.
var endSessionWords = regexp.MustCompile(`(?i)\b(kill|end|ends|ending|stop|exit|quit|terminate|ctrl|c-c)\b`)

// TestHookLauncherDetected: a hook refused because its parent is not the pane
// process reports launcher_pid when that parent's parent is the pane process,
// and a SessionStart on a pending row also writes ad.hook.launcher_detected.
func TestHookLauncherDetected(t *testing.T) {
	pane := ssgFreshPane.PanePID
	withPane := func(state, session string) func(*testing.T, string) (*store.Store, string) {
		return func(t *testing.T, id string) (*store.Store, string) {
			return ssgSeed(t, id, state, session, apitest.WithLaunchIdentity(ssgFreshPane))
		}
	}
	noPanePastGrace := func(t *testing.T, id string) (*store.Store, string) {
		pastGrace := time.Now().Add(-config.Tmux{}.EffectivePendingGrace() - time.Second).UnixMilli()
		return ssgSeed(t, id, store.StatePending, "", apitest.WithLaunchIdentity(ssgNoPane),
			apitest.WithLaunchStartedAt(pastGrace))
	}
	sessionStart := func(t *testing.T) []byte { p, _, _ := ssgPayload(t, "startup"); return []byte(p) }
	stop := func(t *testing.T) []byte { return readPayloadFixture(t, "stop.json") }
	subagentStart := func(t *testing.T) []byte {
		p, _, _ := subPayload(t, "session-start-subagent.json", nil)
		return p
	}
	cases := []struct {
		name         string
		seed         func(*testing.T, string) (*store.Store, string)
		payload      func(*testing.T) []byte
		ppid         int    // the hook parent's parent pid; 0 = unreadable
		launcherName string // the pane process's command name; "" = unreadable
		fires        int    // hooks fired; 0 = 1
		noParentProc bool   // hc.ParentProc = nil; ppid and launcherName unused
		reason       string
		wantLauncher bool // launcher_pid = the pane pid
		wantDetected bool // one ad.hook.launcher_detected per hook
	}{
		{name: "pending SessionStart behind a launcher", seed: withPane(store.StatePending, ""), payload: sessionStart,
			ppid: pane, launcherName: "sh", reason: store.HookReasonPIDMismatch, wantLauncher: true, wantDetected: true},
		{name: "launcher command unreadable", seed: withPane(store.StatePending, ""), payload: sessionStart,
			ppid: pane, reason: store.HookReasonPIDMismatch, wantLauncher: true, wantDetected: true},
		{name: "every refused SessionStart warns again", seed: withPane(store.StatePending, ""), payload: sessionStart,
			ppid: pane, launcherName: "sh", fires: 2, reason: store.HookReasonPIDMismatch, wantLauncher: true, wantDetected: true},
		{name: "pending Stop", seed: withPane(store.StatePending, ""), payload: stop,
			ppid: pane, launcherName: "sh", reason: store.HookReasonPIDMismatch, wantLauncher: true},
		{name: "working SessionStart", seed: withPane(store.StateWorking, ssgOutgoing), payload: sessionStart,
			ppid: pane, launcherName: "sh", reason: store.HookReasonPIDMismatch, wantLauncher: true},
		{name: "ended Stop", seed: withPane(store.StateEnded, "sess-ended"), payload: stop,
			ppid: pane, launcherName: "sh", reason: store.HookReasonPIDMismatch, wantLauncher: true},
		{name: "parent's parent is another process", seed: withPane(store.StatePending, ""), payload: sessionStart,
			ppid: 1, launcherName: "sh", reason: store.HookReasonPIDMismatch},
		{name: "parent's parent unreadable", seed: withPane(store.StatePending, ""), payload: sessionStart,
			launcherName: "sh", reason: store.HookReasonPIDMismatch},
		{name: "no_pane_recorded", seed: noPanePastGrace, payload: sessionStart,
			ppid: pane, launcherName: "sh", reason: store.HookReasonNoPaneRecorded},
		{name: "subagent_event", seed: withPane(store.StatePending, ""), payload: subagentStart,
			ppid: pane, launcherName: "sh", reason: store.HookReasonSubagentEvent},
		{name: "no ParentProc wired", seed: withPane(store.StatePending, ""), payload: sessionStart,
			noParentProc: true, reason: store.HookReasonPIDMismatch},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "launcher-" + strconv.Itoa(i)
			st, _ := tc.seed(t, id)
			parent := foreignParent(t, st, id)
			hc := hookConfig(envHook(id, ""), parent)
			if tc.noParentProc {
				hc.ParentProc = nil
			} else {
				pp := hc.ParentProc.(*fakeParentProc)
				pp.ppids[parent.PID] = tc.ppid
				pp.names[pane] = tc.launcherName
			}
			prior := mustGetSpawn(t, st, id)
			payload := string(tc.payload(t))
			fires := max(tc.fires, 1)
			before := len(readTrailLines(t, trailFile()))

			for range fires {
				if out := ssgHandleWith(t, st, hc, payload, nil); out != "" {
					t.Errorf("stdout = %q; want empty", out)
				}
			}

			// SR-22.9 unchanged: the hook is refused and writes nothing.
			assertRowUnchanged(t, st, id, prior)
			ignored := hookIgnoredAfter(t, before, id)
			if len(ignored) != fires {
				t.Fatalf("ad.hook.ignored lines = %d; want %d", len(ignored), fires)
			}
			var wantLauncher any
			if tc.wantLauncher {
				wantLauncher = float64(pane)
			}
			for _, line := range ignored {
				assertStr(t, line, "reason", tc.reason)
				if got, ok := line["launcher_pid"]; !ok || got != wantLauncher {
					t.Errorf("ad.hook.ignored launcher_pid = %v (present %t); want %v", got, ok, wantLauncher)
				}
			}
			detected := linesAfter(t, before, "ad.hook.launcher_detected", id)
			if !tc.wantDetected {
				if len(detected) != 0 {
					t.Errorf("ad.hook.launcher_detected = %v; want none", detected)
				}
				return
			}
			if len(detected) != fires {
				t.Fatalf("ad.hook.launcher_detected lines = %d; want %d", len(detected), fires)
			}
			var wantCommand any
			if tc.launcherName != "" {
				wantCommand = tc.launcherName
			}
			for _, line := range detected {
				assertLauncherDetected(t, line, map[string]any{
					"claude_instance_id": id, "launcher_pid": float64(pane), "launcher_command": wantCommand,
					"parent_pid": float64(parent.PID), "parent_command": "claude",
					"advice": launcherAdviceText, "source": "ad_hook",
				})
			}
		})
	}
}

// assertLauncherDetected checks line has exactly launcherDetectedKeys with
// want's values, and that its advice names no way to end a session by hand.
func assertLauncherDetected(t *testing.T, line map[string]any, want map[string]any) {
	t.Helper()
	keys := make([]string, 0, len(line))
	for k := range line {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != strings.Join(launcherDetectedKeys, ",") {
		t.Errorf("ad.hook.launcher_detected keys = %v; want %v", keys, launcherDetectedKeys)
	}
	for k, v := range want {
		if line[k] != v {
			t.Errorf("ad.hook.launcher_detected[%q] = %v; want %v", k, line[k], v)
		}
	}
	if advice, _ := line["advice"].(string); endSessionWords.MatchString(advice) {
		t.Errorf("advice %q names a way to end a session by hand (%q)", advice, endSessionWords.FindString(advice))
	}
}
