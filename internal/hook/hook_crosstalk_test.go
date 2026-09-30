package hook_test

// hook_crosstalk_test.go — cross-talk proofs through hook.Handle and a real
// store (SR-22.9): TLA+ ci_G3b_ren and "ended" sticks (AC-HOOK-03's hook half),
// a nested Claude and a teammate pane (AC-HOOK-04). Every process carries the
// row's id; only the row's recorded pane process (the agent) moves the row.
// An ignored hook leaves every column and the history as they were, records no
// permission request, prints nothing and writes one ad.hook.ignored.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// ctAgentSession is the agent's session id before its /clear.
const ctAgentSession = "ct-agent-session"

// crossTalkHook is one hook: a testdata/hook-payloads fixture, the session id
// it reports and whether the relay is on.
type crossTalkHook struct {
	fixture string
	session string
	relay   bool
}

// crossTalkRig is one row under test, its agent (the recorded pane process)
// and the transcripts' directory.
type crossTalkRig struct {
	st      *store.Store
	dbPath  string
	id      string
	agent   hookParent
	transcr string
}

// newWorkingAgent seeds id with a recorded pane; the agent reports in
// (startup SessionStart) and takes a prompt, so the row is working.
func newWorkingAgent(t *testing.T, id string) crossTalkRig {
	t.Helper()
	st, dbPath := seedAgentRow(t, id, store.StateWorking)
	r := crossTalkRig{st: st, dbPath: dbPath, id: id, agent: agentParent(t, st, id), transcr: t.TempDir()}
	r.applied(t, crossTalkHook{fixture: "session-start-startup.json", session: ctAgentSession})
	r.applied(t, crossTalkHook{fixture: "user-prompt-submit.json", session: ctAgentSession})
	row := mustGetSpawn(t, st, id)
	if row.State != store.StateWorking || row.ClaudeSessionID != ctAgentSession ||
		row.Identity.PaneStarttime != r.agent.Start {
		t.Fatalf("precondition: state=%q session=%q pane_starttime=%q; want working/%q/%q",
			row.State, row.ClaudeSessionID, row.Identity.PaneStarttime, ctAgentSession, r.agent.Start)
	}
	return r
}

// otherProcess is a non-pane process carrying the row's id: foreignParent
// moved by n pids, so each role has its own parent_pid.
func (r crossTalkRig) otherProcess(t *testing.T, n int) hookParent {
	t.Helper()
	p := foreignParent(t, r.st, r.id)
	p.PID += n
	if p.PID == r.agent.PID {
		t.Fatalf("otherProcess(%d) = the pane pid %d", n, p.PID)
	}
	return p
}

// payload is h's fixture reporting h.session, with the transcript on disk so
// an applied hook would record it.
func (r crossTalkRig) payload(t *testing.T, h crossTalkHook) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(readPayloadFixture(t, h.fixture), &m); err != nil {
		t.Fatalf("fixture %s: %v", h.fixture, err)
	}
	path := filepath.Join(r.transcr, h.session+".jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	m["session_id"] = h.session
	m["transcript_path"] = path
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal %s: %v", h.fixture, err)
	}
	return string(out)
}

// fire runs Handle for h from parent from and returns stdout; the 1 s relay
// window on the real poll clock makes a wrongly applied relayed hook print a
// decision, not block.
func (r crossTalkRig) fire(t *testing.T, from hookParent, h crossTalkHook) string {
	t.Helper()
	mode := ""
	if h.relay {
		mode = hook.RelayModeOn
	}
	hc := hookConfig(envHook(r.id, mode), from)
	hc.Cfg = config.Relay{TimeoutSeconds: 1}
	if h.relay {
		hc.Clock = hook.DefaultPollClock()
	}
	var out bytes.Buffer
	if err := hook.Handle(context.Background(), strings.NewReader(r.payload(t, h)), &out, r.st, hc, nil); err != nil {
		t.Fatalf("Handle(%s): %v", h.fixture, err)
	}
	return out.String()
}

// applied fires h as the agent and asserts no ad.hook.ignored was written.
func (r crossTalkRig) applied(t *testing.T, h crossTalkHook) {
	t.Helper()
	before := len(readTrailLines(t, trailFile()))
	if out := r.fire(t, r.agent, h); out != "" {
		t.Errorf("%s from the agent: stdout = %q; want empty", h.fixture, out)
	}
	if got := hookIgnoredAfter(t, before, r.id); len(got) != 0 {
		t.Fatalf("%s from the agent: ad.hook.ignored = %v; want none (the agent's own hook applies)", h.fixture, got)
	}
}

// columns reads every spawns column and every session_history entry of the row.
func (r crossTalkRig) columns(t *testing.T) (apitest.SpawnColumns, []apitest.HistoryEntry) {
	t.Helper()
	cols, err := apitest.ReadSpawnColumns(r.dbPath, r.id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns: %v", err)
	}
	hist, err := apitest.ReadSessionHistoryAllLives(r.dbPath, r.id)
	if err != nil {
		t.Fatalf("ReadSessionHistoryAllLives: %v", err)
	}
	return cols, hist
}

// ignored fires h from another process and asserts SR-22.9's outcome: nothing
// written, empty stdout, one ad.hook.ignored (pid_mismatch) naming the parent.
func (r crossTalkRig) ignored(t *testing.T, from hookParent, h crossTalkHook) {
	t.Helper()
	colsBefore, histBefore := r.columns(t)
	row := mustGetSpawn(t, r.st, r.id)
	before := len(readTrailLines(t, trailFile()))

	out := r.fire(t, from, h)

	colsAfter, histAfter := r.columns(t)
	if !reflect.DeepEqual(colsAfter, colsBefore) {
		t.Errorf("%s from pid %d changed the row:\n got  %+v\n want %+v", h.fixture, from.PID, colsAfter, colsBefore)
	}
	if !reflect.DeepEqual(histAfter, histBefore) {
		t.Errorf("%s from pid %d changed the session history: got %+v; want %+v", h.fixture, from.PID, histAfter, histBefore)
	}
	if out != "" {
		t.Errorf("%s from pid %d: stdout = %q; want empty (no decision)", h.fixture, from.PID, out)
	}
	open, err := r.st.OpenPermissionRequestsForSpawn(r.id)
	if err != nil {
		t.Fatalf("OpenPermissionRequestsForSpawn: %v", err)
	}
	if len(open) != 0 {
		t.Errorf("%s from pid %d: %d permission requests recorded; want 0", h.fixture, from.PID, len(open))
	}
	lines := hookIgnoredAfter(t, before, r.id)
	if len(lines) != 1 {
		t.Fatalf("%s from pid %d: ad.hook.ignored lines = %d; want 1", h.fixture, from.PID, len(lines))
	}
	got := lines[0]
	assertStr(t, got, "hook_event", hook.PeekEventName(readPayloadFixture(t, h.fixture)))
	assertStr(t, got, "reason", store.HookReasonPIDMismatch)
	assertStr(t, got, "parent_command", from.Name)
	assertStr(t, got, "hook_session_id", h.session)
	assertStr(t, got, "row_session_id", row.ClaudeSessionID)
	assertStr(t, got, "source", "ad_hook")
	if got["parent_pid"] != float64(from.PID) {
		t.Errorf("parent_pid = %v; want %d", got["parent_pid"], from.PID)
	}
	if got["row_pane_pid"] != float64(r.agent.PID) {
		t.Errorf("row_pane_pid = %v; want %d (the recorded pane pid)", got["row_pane_pid"], r.agent.PID)
	}
}

// TestCrossTalkLeftoverCannotEndWorkingAgent is ci_G3b_ren's hook half
// (AC-HOOK-03): a leftover's SessionEnd, Stop and PermissionRequest change
// nothing; the agent's own SessionEnd ends the row. The row staying live is
// what Epic 18's kill opt-in refuses as the live row (the kill half, Epic 18).
func TestCrossTalkLeftoverCannotEndWorkingAgent(t *testing.T) {
	const leftoverSession = "ct-leftover-earlier-life"
	r := newWorkingAgent(t, "ct-g3b-ren")
	leftover := r.otherProcess(t, 0)

	for _, h := range []crossTalkHook{
		{fixture: "session-end-prompt-input-exit.json", session: leftoverSession},
		{fixture: "stop.json", session: leftoverSession},
		{fixture: "permission-request.json", session: leftoverSession, relay: true},
	} {
		t.Run(h.fixture, func(t *testing.T) { r.ignored(t, leftover, h) })
	}
	if row := mustGetSpawn(t, r.st, r.id); row.State != store.StateWorking || row.EndedAtText != "" {
		t.Fatalf("after the leftover: state=%q ended_at=%q; want working, NULL", row.State, row.EndedAtText)
	}

	r.applied(t, crossTalkHook{fixture: "session-end-prompt-input-exit.json", session: ctAgentSession})

	row := mustGetSpawn(t, r.st, r.id)
	if row.State != store.StateEnded || row.EndedAtText == "" {
		t.Errorf("after the agent's own SessionEnd: state=%q ended_at=%q; want ended with ended_at set", row.State, row.EndedAtText)
	}
}

// TestCrossTalkEndedSticks: the leftover's hooks leave a row its agent ended
// ended, ended_at unchanged (AC-HOOK-03; SR-9.4).
func TestCrossTalkEndedSticks(t *testing.T) {
	const leftoverSession = "ct-leftover-after-end"
	r := newWorkingAgent(t, "ct-ended-sticks")
	leftover := r.otherProcess(t, 0)
	r.applied(t, crossTalkHook{fixture: "session-end-prompt-input-exit.json", session: ctAgentSession})
	ended := mustGetSpawn(t, r.st, r.id)
	if ended.State != store.StateEnded || ended.EndedAtText == "" {
		t.Fatalf("precondition: state=%q ended_at=%q; want ended with ended_at set", ended.State, ended.EndedAtText)
	}

	for _, h := range []crossTalkHook{
		{fixture: "user-prompt-submit.json", session: leftoverSession},
		{fixture: "pre-tool-use-bash.json", session: leftoverSession},
		{fixture: "stop.json", session: leftoverSession},
		{fixture: "session-start-startup.json", session: leftoverSession},
	} {
		t.Run(h.fixture, func(t *testing.T) { r.ignored(t, leftover, h) })
	}

	row := mustGetSpawn(t, r.st, r.id)
	if row.State != store.StateEnded || row.EndedAtText != ended.EndedAtText || row.ClaudeSessionID != ctAgentSession {
		t.Errorf("after the leftover: state=%q ended_at=%q session=%q; want ended/%q/%q",
			row.State, row.EndedAtText, row.ClaudeSessionID, ended.EndedAtText, ctAgentSession)
	}
}

// TestCrossTalkNestedAndTeammateThenOwnClearAndResume is AC-HOOK-04: a nested
// Claude's and a teammate's hooks are ignored; the agent's own /clear and
// in-session /resume afterwards apply and record the new session ids.
func TestCrossTalkNestedAndTeammateThenOwnClearAndResume(t *testing.T) {
	cases := []struct {
		name    string
		offset  int // the process's pid = foreignParent's + offset
		session string
		hooks   []string
		relay   map[string]bool
	}{
		{
			name:    "nested claude",
			offset:  1,
			session: "ct-nested-session",
			hooks: []string{"session-start-startup.json", "session-start-clear.json",
				"stop.json", "session-end-prompt-input-exit.json"},
		},
		{
			name:    "teammate pane",
			offset:  2,
			session: "ct-teammate-session",
			hooks: []string{"session-start-startup.json", "permission-request.json",
				"session-end-prompt-input-exit.json"},
			relay: map[string]bool{"permission-request.json": true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newWorkingAgent(t, "ct-"+strings.ReplaceAll(tc.name, " ", "-"))
			// The nested process's parent is the agent's shell and its
			// grandparent the pane process; the hook reads only its own
			// parent, so the pane process never enters the gate.
			other := r.otherProcess(t, tc.offset)
			for _, f := range tc.hooks {
				h := crossTalkHook{fixture: f, session: tc.session, relay: tc.relay[f]}
				t.Run(f, func(t *testing.T) { r.ignored(t, other, h) })
			}
			row := mustGetSpawn(t, r.st, r.id)
			if row.State != store.StateWorking || row.ClaudeSessionID != ctAgentSession {
				t.Fatalf("after the %s: state=%q session=%q; want working/%q", tc.name, row.State, row.ClaudeSessionID, ctAgentSession)
			}

			r.applied(t, crossTalkHook{fixture: "session-end-clear.json", session: ctAgentSession})
			for _, step := range []struct{ fixture, session, replaced string }{
				{"session-start-clear.json", "ct-agent-after-clear", ctAgentSession},
				{"session-start-resume.json", "ct-agent-resumed", "ct-agent-after-clear"},
			} {
				r.applied(t, crossTalkHook{fixture: step.fixture, session: step.session})
				r.assertOwnSessionStart(t, step.fixture, step.session, step.replaced)
			}
		})
	}
}

// assertOwnSessionStart: the row is waiting with the new session id, its
// transcript and the agent as pid, and the replaced session is archived.
func (r crossTalkRig) assertOwnSessionStart(t *testing.T, fixture, session, replaced string) {
	t.Helper()
	row := mustGetSpawn(t, r.st, r.id)
	wantPath := filepath.Join(r.transcr, session+".jsonl")
	if row.State != store.StateWaiting || row.ClaudeSessionID != session || row.JSONLPath != wantPath {
		t.Errorf("%s: state=%q session=%q jsonl=%q; want waiting/%q/%q", fixture, row.State, row.ClaudeSessionID, row.JSONLPath, session, wantPath)
	}
	if row.PID != r.agent.PID || row.ProcStarttime != r.agent.Start {
		t.Errorf("%s: pid/proc_starttime = %d/%q; want the agent %d/%q", fixture, row.PID, row.ProcStarttime, r.agent.PID, r.agent.Start)
	}
	_, hist := r.columns(t)
	for _, e := range hist {
		if e.ClaudeSessionID == replaced {
			return
		}
	}
	t.Errorf("%s: session_history = %+v; want the replaced session %q archived", fixture, hist, replaced)
}
