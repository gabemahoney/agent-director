package hook_test

// hook_gate_test.go — AC-HOOK-01, the handler-level gate for ordinary hooks
// (SR-22.9; SR-14 ad.hook.ignored; decisions A1, A4, A11). hook.Handle runs
// against a real store with S1's payload fixtures and a fake parent process
// (hook_parent_fakes_test.go): a hook applies only when its parent pid and
// start time are the row's recorded pane process. SessionStart is S11's
// (hook_gate_sessionstart_test.go); the relay, event-content and fail-open
// cases are in hook_gate_relay_test.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// gateLaunchToken is the launch token of rows seeded with an explicit launch
// identity (16 lowercase hex characters, SR-3.5).
const gateLaunchToken = "0123456789abcdef"

// gateEvent is one ordinary hook event, its fixture and the state an applied
// hook sets (soft: the state is kept).
type gateEvent struct {
	name    string
	fixture string // "" = an unknown event, built from stop.json
	event   string
	state   string
	soft    bool
}

// gateEvents is every ordinary event the settings register, and an unknown one.
var gateEvents = []gateEvent{
	{name: "UserPromptSubmit", fixture: "user-prompt-submit.json", event: "UserPromptSubmit", state: store.StateWorking},
	{name: "PreToolUse/Bash", fixture: "pre-tool-use-bash.json", event: "PreToolUse", state: store.StateWorking},
	{name: "PreToolUse/AskUserQuestion", fixture: "pre-tool-use-ask-user-question.json", event: "PreToolUse", state: store.StateAskUser},
	{name: "PostToolUse", fixture: "post-tool-use.json", event: "PostToolUse", state: store.StateWorking},
	{name: "Stop", fixture: "stop.json", event: "Stop", state: store.StateWaiting},
	{name: "Notification", fixture: "notification.json", event: "Notification", soft: true},
	{name: "PermissionRequest", fixture: "permission-request.json", event: "PermissionRequest", state: store.StateCheckPermission},
	{name: "SessionEnd/prompt_input_exit", fixture: "session-end-prompt-input-exit.json", event: "SessionEnd", state: store.StateEnded},
	{name: "SessionEnd/clear", fixture: "session-end-clear.json", event: "SessionEnd", soft: true},
	{name: "unknown", event: "FutureHookEvent", soft: true},
}

// wantState is the row's state after ev applied to a row in prior.
func (ev gateEvent) wantState(prior string) string {
	if ev.soft {
		return prior
	}
	return ev.state
}

// gateRowState is a row a hook can apply to; opts are seed options beyond the
// default pane (a pre-report-in missing row keeps its pane).
type gateRowState struct {
	name  string
	state string
	opts  []apitest.SpawnOption
}

// gateRowStates are the rows of the per-event tables.
var gateRowStates = []gateRowState{
	{name: "pending", state: store.StatePending},
	{name: "working", state: store.StateWorking},
	{name: "missing before report-in", state: store.StateMissing, opts: []apitest.SpawnOption{gatePane("")}},
}

// gatePane records apitest's test pane on a row, with paneStart ("" = NULL).
func gatePane(paneStart string) apitest.SpawnOption {
	return apitest.WithLaunchIdentity(store.LaunchIdentity{
		Token: gateLaunchToken, Socket: apitest.TestSocket,
		PaneID: apitest.TestPaneID, PanePID: apitest.TestPanePID, PaneStarttime: paneStart,
	})
}

// gateNoPane records a launch token and socket but no pane (a lost create reply).
func gateNoPane() apitest.SpawnOption {
	return apitest.WithLaunchIdentity(store.LaunchIdentity{Token: gateLaunchToken, Socket: apitest.TestSocket})
}

// gatePayload returns ev's S1 fixture, or stop.json renamed to ev.event.
func gatePayload(t *testing.T, ev gateEvent) []byte {
	t.Helper()
	if ev.fixture != "" {
		return readPayloadFixture(t, ev.fixture)
	}
	var m map[string]any
	if err := json.Unmarshal(readPayloadFixture(t, "stop.json"), &m); err != nil {
		t.Fatalf("parse stop.json: %v", err)
	}
	m["hook_event_name"] = ev.event
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal unknown payload: %v", err)
	}
	return b
}

// payloadSessionID is the session id a payload's transcript_path names.
func payloadSessionID(t *testing.T, payload []byte) string {
	t.Helper()
	var p struct {
		TranscriptPath string `json:"transcript_path"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	return strings.TrimSuffix(filepath.Base(p.TranscriptPath), ".jsonl")
}

// seedGateRow seeds id in state into the store at dbPath through apitest.SeedSpawn.
func seedGateRow(t *testing.T, dbPath, id, state, sessionID string, opts ...apitest.SpawnOption) {
	t.Helper()
	if _, err := apitest.SeedSpawn(dbPath, id, state, "", "", sessionID, false, opts...); err != nil {
		t.Fatalf("SeedSpawn(%q, %q): %v", id, state, err)
	}
}

// fireGate runs one Handle for id from parent p with env (envHook(id, "") when
// nil), failing on a Handle error (fail-open), and returns stdout.
func fireGate(t *testing.T, st hook.HookStore, id string, p hookParent, env func(string) string, payload []byte) string {
	t.Helper()
	if env == nil {
		env = envHook(id, "")
	}
	var stdout bytes.Buffer
	if err := hook.Handle(context.Background(), bytes.NewReader(payload), &stdout, st, hookConfig(env, p), newSilentLogger()); err != nil {
		t.Fatalf("Handle: %v; want nil (fail-open)", err)
	}
	return stdout.String()
}

// assertRowUnchanged fails when id's row differs from before in any field.
func assertRowUnchanged(t *testing.T, st *store.Store, id string, before store.Spawn) {
	t.Helper()
	if after := mustGetSpawn(t, st, id); !reflect.DeepEqual(after, before) {
		t.Errorf("row changed by a hook that did not apply:\n got  %+v\n want %+v", after, before)
	}
}

// oneIgnored returns id's only ad.hook.ignored line after before.
func oneIgnored(t *testing.T, before int, id string) map[string]any {
	t.Helper()
	lines := hookIgnoredAfter(t, before, id)
	if len(lines) != 1 {
		t.Fatalf("ad.hook.ignored lines = %d; want exactly 1", len(lines))
	}
	return lines[0]
}

// assertNoIgnored fails when id got any ad.hook.ignored line after before.
func assertNoIgnored(t *testing.T, before int, id string) {
	t.Helper()
	if lines := hookIgnoredAfter(t, before, id); len(lines) != 0 {
		t.Errorf("ad.hook.ignored lines = %d (%v); want none for an applied hook", len(lines), lines)
	}
}

// TestHookGateOrdinaryEventAppliesFromAgent: each ordinary event (and an unknown
// one) from the recorded pane process applies per the event table.
func TestHookGateOrdinaryEventAppliesFromAgent(t *testing.T) {
	st, dbPath := storefix.OpenTempStore(t)
	for _, rs := range gateRowStates {
		for _, ev := range gateEvents {
			t.Run(rs.name+"/"+ev.name, func(t *testing.T) {
				id := "gate-apply-" + strings.NewReplacer(" ", "-", "/", "-").Replace(rs.name+"-"+ev.name)
				seedGateRow(t, dbPath, id, rs.state, "", rs.opts...)
				agent := agentParent(t, st, id)
				payload := gatePayload(t, ev)
				prior := mustGetSpawn(t, st, id)
				before := len(readTrailLines(t, trailFile()))

				stdout := fireGate(t, st, id, agent, nil, payload)

				row := mustGetSpawn(t, st, id)
				// SR-22.9: a hook from the recorded pane process applies whatever the row's state.
				if want := ev.wantState(rs.state); row.State != want {
					t.Errorf("State = %q; want %q", row.State, want)
				}
				if row.RowVersion != prior.RowVersion+1 {
					t.Errorf("RowVersion = %d; want %d (one applied write)", row.RowVersion, prior.RowVersion+1)
				}
				// SR-22.9: an applied ordinary hook on a row with no session id records the payload's.
				if want := payloadSessionID(t, payload); row.ClaudeSessionID != want {
					t.Errorf("ClaudeSessionID = %q; want %q", row.ClaudeSessionID, want)
				}
				if row.Identity.PaneStarttime != agent.Start {
					t.Errorf("pane_starttime = %q; want %q (NULL recorded from the parent)", row.Identity.PaneStarttime, agent.Start)
				}
				if stdout != "" {
					t.Errorf("stdout = %q; want empty (state-tracking hook)", stdout)
				}
				assertNoIgnored(t, before, id)
			})
		}
	}
}

// TestHookGateOrdinaryEventIgnoredFromAnotherParent: the same events from another
// process change nothing, print nothing and write one ad.hook.ignored.
func TestHookGateOrdinaryEventIgnoredFromAnotherParent(t *testing.T) {
	st, dbPath := storefix.OpenTempStore(t)
	note := []apitest.SpawnOption{apitest.WithLivenessNote("unverified by a sweep"), apitest.WithLivenessUnverifiedSince("2026-09-30 00:00:00")}
	for _, rs := range gateRowStates {
		for _, ev := range gateEvents {
			t.Run(rs.name+"/"+ev.name, func(t *testing.T) {
				id := "gate-foreign-" + strings.NewReplacer(" ", "-", "/", "-").Replace(rs.name+"-"+ev.name)
				seedGateRow(t, dbPath, id, rs.state, "", append(append([]apitest.SpawnOption{}, rs.opts...), note...)...)
				foreign := foreignParent(t, st, id)
				payload := gatePayload(t, ev)
				prior := mustGetSpawn(t, st, id)
				before := len(readTrailLines(t, trailFile()))

				stdout := fireGate(t, st, id, foreign, nil, payload)

				// SR-22.9: another process's hook changes nothing (SR-22.3 "any → unchanged").
				assertRowUnchanged(t, st, id, prior)
				if stdout != "" {
					t.Errorf("stdout = %q; want empty", stdout)
				}
				line := oneIgnored(t, before, id)
				assertStr(t, line, "reason", store.HookReasonPIDMismatch)
				assertStr(t, line, "hook_event", ev.event)
				assertStr(t, line, "hook_session_id", payloadSessionID(t, payload))
				assertStr(t, line, "parent_command", foreign.Name)
				assertNull(t, line, "row_session_id")
				if got := line["parent_pid"]; got != float64(foreign.PID) {
					t.Errorf("parent_pid = %v; want %d", got, foreign.PID)
				}
				if got := line["row_pane_pid"]; got != float64(apitest.TestPanePID) {
					t.Errorf("row_pane_pid = %v; want %d", got, apitest.TestPanePID)
				}
			})
		}
	}
}

// TestHookGateNotApplied: the other hooks the gate rejects, with their reasons
// (decision A1); the parent is the row's agent unless the case changes it.
func TestHookGateNotApplied(t *testing.T) {
	cases := []struct {
		name   string
		state  string
		opts   []apitest.SpawnOption
		parent func(p hookParent) hookParent
		reason string
		pane   bool // the row records a pane (row_pane_pid set)
	}{
		{name: "pending row with no pane", state: store.StatePending, opts: []apitest.SpawnOption{gateNoPane()},
			reason: store.HookReasonNoPaneRecorded},
		{name: "ended row with no pane", state: store.StateEnded,
			reason: store.HookReasonNoPaneRecorded},
		{name: "no pane and unreadable parent start", state: store.StatePending, opts: []apitest.SpawnOption{gateNoPane()},
			parent: func(p hookParent) hookParent { p.Start = ""; return p }, reason: store.HookReasonNoPaneRecorded},
		{name: "unreadable parent start", state: store.StateWorking,
			parent: func(p hookParent) hookParent { p.Start = ""; return p }, reason: store.HookReasonPIDMismatch, pane: true},
		{name: "pane pid reused with another start", state: store.StateWorking,
			opts:   []apitest.SpawnOption{gatePane(procstarttimefix.DarwinProcStarttime)},
			parent: func(p hookParent) hookParent { p.Start = procstarttimefix.LinuxProcStarttime; return p },
			reason: store.HookReasonPIDMismatch, pane: true},
	}
	st, dbPath := storefix.OpenTempStore(t)
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "gate-reject-" + string(rune('a'+i))
			seedGateRow(t, dbPath, id, tc.state, "", tc.opts...)
			p := agentParent(t, st, id)
			if tc.parent != nil {
				p = tc.parent(p)
			}
			prior := mustGetSpawn(t, st, id)
			before := len(readTrailLines(t, trailFile()))

			stdout := fireGate(t, st, id, p, nil, readPayloadFixture(t, "stop.json"))

			// SR-22.9: a row with no pane matches no hook; an unreadable or different start never matches.
			assertRowUnchanged(t, st, id, prior)
			if stdout != "" {
				t.Errorf("stdout = %q; want empty", stdout)
			}
			line := oneIgnored(t, before, id)
			assertStr(t, line, "reason", tc.reason)
			if tc.pane {
				if got := line["row_pane_pid"]; got != float64(apitest.TestPanePID) {
					t.Errorf("row_pane_pid = %v; want %d", got, apitest.TestPanePID)
				}
			} else {
				assertNull(t, line, "row_pane_pid")
			}
		})
	}
}

// TestHookGatePaneStarttime: a NULL pane_starttime lets the pid decide and is
// recorded; a recorded one applies for the same start and is kept.
func TestHookGatePaneStarttime(t *testing.T) {
	cases := []struct {
		name      string
		paneStart string // "" = NULL
		parent    string
	}{
		{name: "NULL: pid decides, start recorded", parent: procstarttimefix.LinuxProcStarttime},
		{name: "recorded: same start applies", paneStart: procstarttimefix.DarwinProcStarttime, parent: procstarttimefix.DarwinProcStarttime},
	}
	st, dbPath := storefix.OpenTempStore(t)
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "gate-start-" + string(rune('a'+i))
			seedGateRow(t, dbPath, id, store.StateWorking, "", gatePane(tc.paneStart))
			p := hookParent{PID: apitest.TestPanePID, Start: tc.parent, Name: "claude"}
			before := len(readTrailLines(t, trailFile()))

			fireGate(t, st, id, p, nil, readPayloadFixture(t, "stop.json"))

			row := mustGetSpawn(t, st, id)
			if row.State != store.StateWaiting {
				t.Errorf("State = %q; want waiting (applied)", row.State)
			}
			// SR-22.9: the first applied hook records the start time when the create could not read it.
			if row.Identity.PaneStarttime != tc.parent {
				t.Errorf("pane_starttime = %q; want %q", row.Identity.PaneStarttime, tc.parent)
			}
			assertNoIgnored(t, before, id)
		})
	}
}

// TestHookGateSessionIDRecordedNotGate: the session id is recorded, never a
// gate: a differing id applies (row's kept); a row with none records it.
func TestHookGateSessionIDRecordedNotGate(t *testing.T) {
	const rowSession = "sess-row-recorded"
	cases := []struct {
		name       string
		rowSession string
		wantID     func(hookID string) string
		wantPath   func(path string) string
	}{
		{name: "row session differs: applied, kept", rowSession: rowSession,
			wantID: func(string) string { return rowSession }, wantPath: func(string) string { return "" }},
		{name: "row has none: recorded", wantID: func(id string) string { return id }, wantPath: func(p string) string { return p }},
	}
	st, dbPath := storefix.OpenTempStore(t)
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "gate-session-" + string(rune('a'+i))
			seedGateRow(t, dbPath, id, store.StateWorking, tc.rowSession)
			hookSession := "sess-hook-" + string(rune('a'+i))
			transcript := writeTranscript(t, hookSession)
			payload := []byte(`{"hook_event_name":"Stop","session_id":"` + hookSession + `","transcript_path":"` + transcript + `"}`)
			before := len(readTrailLines(t, trailFile()))

			fireGate(t, st, id, agentParent(t, st, id), nil, payload)

			row := mustGetSpawn(t, st, id)
			// SR-22.9: the session id is no longer a gate.
			if row.State != store.StateWaiting {
				t.Errorf("State = %q; want waiting (applied whatever the session id)", row.State)
			}
			if want := tc.wantID(hookSession); row.ClaudeSessionID != want {
				t.Errorf("ClaudeSessionID = %q; want %q", row.ClaudeSessionID, want)
			}
			if want := tc.wantPath(transcript); row.JSONLPath != want {
				t.Errorf("JSONLPath = %q; want %q", row.JSONLPath, want)
			}
			assertNoIgnored(t, before, id)
		})
	}
}
