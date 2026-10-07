package hook_test

// hook_pane_grandparent_test.go — b.9n6, b.zde: only a pending row's
// SessionStart refused with pid_mismatch reads the hook parent's parent pid;
// when that is the row's pane process (a `claude` launcher that does not exec,
// or a shell-form hook) it writes ad.hook.pane_is_grandparent. No
// ad.hook.ignored carries launcher_pid. The gate's decision is unchanged.

import (
	"context"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// paneIsGrandparentAdviceText is ad.hook.pane_is_grandparent's advice, word
// for word. It names both causes (a non-exec launcher, a shell-form hook) and
// asserts neither.
const paneIsGrandparentAdviceText = "The parent of this agent's hooks is a child of its pane process, not the pane process itself, " +
	"so agent-director ignores every hook of this agent (pid_mismatch). " +
	"Either the claude on PATH is a launcher that runs Claude Code as a child instead of exec-ing it " +
	"(replace it with the native claude binary or a wrapper that execs it), " +
	"or Claude Code ran the hook through a shell instead of in exec form; parent_command names that parent."

// paneIsGrandparentKeys is ad.hook.pane_is_grandparent's key set, with the
// trail envelope's event and ts.
var paneIsGrandparentKeys = []string{
	"advice", "claude_instance_id", "event", "pane_command", "pane_pid",
	"parent_command", "parent_pid", "source", "ts",
}

// endSessionWords are words that would tell an operator to end a session by hand.
var endSessionWords = regexp.MustCompile(`(?i)\b(kill|end|ends|ending|stop|exit|quit|terminate|ctrl|c-c)\b`)

// TestHookPaneIsGrandparent: the parent's parent pid is read once per pending
// SessionStart refused with pid_mismatch and never for any other hook; a pane
// grandparent writes one ad.hook.pane_is_grandparent per such hook.
func TestHookPaneIsGrandparent(t *testing.T) {
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
	fixture := func(name string) func(*testing.T) []byte {
		return func(t *testing.T) []byte { return readPayloadFixture(t, name) }
	}
	subagentStart := func(t *testing.T) []byte {
		p, _, _ := subPayload(t, "session-start-subagent.json", nil)
		return p
	}
	cases := []struct {
		name         string
		seed         func(*testing.T, string) (*store.Store, string)
		payload      func(*testing.T) []byte
		noExec       bool   // a no-verb run (hook.HandleNoExecForm), not hook.Handle
		ppid         int    // the hook parent's parent pid; 0 = unreadable
		paneName     string // the pane process's command name; "" = unreadable
		fires        int    // hooks fired; 0 = 1
		noParentProc bool   // hc.ParentProc = nil; ppid and paneName unused
		reason       string
		wantReads    int  // parent-pid reads per hook
		wantEvent    bool // one ad.hook.pane_is_grandparent per hook
	}{
		{name: "pending SessionStart, pane is grandparent", seed: withPane(store.StatePending, ""), payload: sessionStart,
			ppid: pane, paneName: "sh", reason: store.HookReasonPIDMismatch, wantReads: 1, wantEvent: true},
		{name: "pane command unreadable", seed: withPane(store.StatePending, ""), payload: sessionStart,
			ppid: pane, reason: store.HookReasonPIDMismatch, wantReads: 1, wantEvent: true},
		{name: "every refused SessionStart warns again", seed: withPane(store.StatePending, ""), payload: sessionStart,
			ppid: pane, paneName: "sh", fires: 2, reason: store.HookReasonPIDMismatch, wantReads: 1, wantEvent: true},
		{name: "parent's parent is another process", seed: withPane(store.StatePending, ""), payload: sessionStart,
			ppid: 1, paneName: "sh", reason: store.HookReasonPIDMismatch, wantReads: 1},
		{name: "parent's parent unreadable", seed: withPane(store.StatePending, ""), payload: sessionStart,
			paneName: "sh", reason: store.HookReasonPIDMismatch, wantReads: 1},
		{name: "pending Stop", seed: withPane(store.StatePending, ""), payload: fixture("stop.json"),
			ppid: pane, paneName: "sh", reason: store.HookReasonPIDMismatch},
		{name: "pending PreToolUse", seed: withPane(store.StatePending, ""), payload: fixture("pre-tool-use-bash.json"),
			ppid: pane, paneName: "sh", fires: 2, reason: store.HookReasonPIDMismatch},
		{name: "working SessionStart", seed: withPane(store.StateWorking, ssgOutgoing), payload: sessionStart,
			ppid: pane, paneName: "sh", reason: store.HookReasonPIDMismatch},
		{name: "ended SessionStart", seed: withPane(store.StateEnded, "sess-ended"), payload: sessionStart,
			ppid: pane, paneName: "sh", reason: store.HookReasonPIDMismatch},
		{name: "ended Stop", seed: withPane(store.StateEnded, "sess-ended"), payload: fixture("stop.json"),
			ppid: pane, paneName: "sh", reason: store.HookReasonPIDMismatch},
		{name: "no_pane_recorded", seed: noPanePastGrace, payload: sessionStart,
			ppid: pane, paneName: "sh", reason: store.HookReasonNoPaneRecorded},
		{name: "subagent_event", seed: withPane(store.StatePending, ""), payload: subagentStart,
			ppid: pane, paneName: "sh", reason: store.HookReasonSubagentEvent},
		{name: "no_exec_form", seed: withPane(store.StatePending, ""), payload: sessionStart, noExec: true,
			ppid: pane, paneName: "sh", reason: store.HookReasonNoExecForm},
		{name: "no ParentProc wired", seed: withPane(store.StatePending, ""), payload: sessionStart,
			noParentProc: true, reason: store.HookReasonPIDMismatch},
	}
	start := len(readTrailLines(t, trailFile()))
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "pane-grandparent-" + strconv.Itoa(i)
			st, _ := tc.seed(t, id)
			parent := foreignParent(t, st, id)
			hc := hookConfig(envHook(id, ""), parent)
			pp := hc.ParentProc.(*fakeParentProc)
			if tc.noParentProc {
				hc.ParentProc = nil
			} else {
				pp.ppids[parent.PID] = tc.ppid
				pp.names[pane] = tc.paneName
			}
			prior := mustGetSpawn(t, st, id)
			payload := tc.payload(t)
			fires := max(tc.fires, 1)
			before := len(readTrailLines(t, trailFile()))

			for range fires {
				if tc.noExec {
					if !hook.HandleNoExecForm(context.Background(), payload, hc) {
						t.Fatalf("HandleNoExecForm = false; want true for a hook payload")
					}
				} else if out := ssgHandleWith(t, st, hc, string(payload), nil); out != "" {
					t.Errorf("stdout = %q; want empty", out)
				}
			}

			if got, want := pp.ppidReads.Load(), int64(tc.wantReads*fires); got != want {
				t.Errorf("parent-pid reads = %d; want %d", got, want)
			}
			// SR-22.9 unchanged: the hook is refused and writes nothing.
			assertRowUnchanged(t, st, id, prior)
			ignored := hookIgnoredAfter(t, before, id)
			if len(ignored) != fires {
				t.Fatalf("ad.hook.ignored lines = %d; want %d", len(ignored), fires)
			}
			for _, line := range ignored {
				assertStr(t, line, "reason", tc.reason)
				assertKeySet(t, "ad.hook.ignored", line, gateIgnoredKeys)
			}
			events := linesAfter(t, before, "ad.hook.pane_is_grandparent", id)
			if !tc.wantEvent {
				if len(events) != 0 {
					t.Errorf("ad.hook.pane_is_grandparent = %v; want none", events)
				}
				return
			}
			if len(events) != fires {
				t.Fatalf("ad.hook.pane_is_grandparent lines = %d; want %d", len(events), fires)
			}
			var wantCommand any
			if tc.paneName != "" {
				wantCommand = tc.paneName
			}
			for _, line := range events {
				assertPaneIsGrandparent(t, line, map[string]any{
					"claude_instance_id": id, "pane_pid": float64(pane), "pane_command": wantCommand,
					"parent_pid": float64(parent.PID), "parent_command": "claude",
					"advice": paneIsGrandparentAdviceText, "source": "ad_hook",
				})
			}
		})
	}
	// The trail-format rename (b.zde), pinned once: no case wrote the old name.
	for _, line := range readTrailLines(t, trailFile())[start:] {
		if line["event"] == "ad.hook.launcher_detected" {
			t.Errorf("ad.hook.launcher_detected = %v; want none (renamed ad.hook.pane_is_grandparent)", line)
		}
	}
}

// assertPaneIsGrandparent checks line has exactly paneIsGrandparentKeys with
// want's values, and that its advice names no way to end a session by hand.
func assertPaneIsGrandparent(t *testing.T, line map[string]any, want map[string]any) {
	t.Helper()
	assertKeySet(t, "ad.hook.pane_is_grandparent", line, paneIsGrandparentKeys)
	for k, v := range want {
		if line[k] != v {
			t.Errorf("ad.hook.pane_is_grandparent[%q] = %v; want %v", k, line[k], v)
		}
	}
	if advice, _ := line["advice"].(string); endSessionWords.MatchString(advice) {
		t.Errorf("advice %q names a way to end a session by hand (%q)", advice, endSessionWords.FindString(advice))
	}
}
