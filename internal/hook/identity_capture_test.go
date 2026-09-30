package hook_test

// identity_capture_test.go — SR-6 identity + transcript capture on the
// SessionStart hook, driven in-process through hook.Handle against a real
// storefix store, from a hook whose parent is the row's recorded pane process
// (hook_parent_fakes_test.go).
//
// These are the handler-level tests for the SessionStart write site
// (RecordSessionStartIdentity). Store-level column semantics are pinned
// separately in internal/store; here we assert the HANDLER wiring: what
// pid/proc_starttime/jsonl_path land on the row (read back via GetSpawn, never
// internals). SR-22.9: the recorded identity is the hook's parent (getppid()
// and its start time); there is no resolver walk.
//
// Each behavior is pinned so the OPPOSITE behavior fails:
//   - refresh: a SessionStart over a stale identity records the parent's
//     (fails under keep-old behavior).
//   - no-clobber: PreToolUse/Stop leave identity+jsonl_path untouched.
//   - unreadable parent: nothing is written, one ad.hook.ignored.
//   - preserve-when-empty: SessionStart with empty transcript_path preserves
//     session id / jsonl_path while identity is re-recorded.

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// writeTranscript creates a real .jsonl transcript file named <sessionID>.jsonl
// under a fresh temp dir and returns its absolute path (b.v2c AC1). The
// SessionStart hook stats the payload's transcript_path and only records
// jsonl_path when the file is actually present on disk. The basename is the
// session id because the classifier derives ClaudeSessionID from it.
func writeTranscript(t *testing.T, sessionID string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), sessionID+".jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("writeTranscript(%q): %v", sessionID, err)
	}
	return path
}

// handleFrom drives one in-process Handle call (no relay) for id's row from a
// hook whose parent is p. Any Handle error is fatal — these paths are all
// fail-open (return nil), so a non-nil error is itself a bug.
func handleFrom(t *testing.T, st hook.HookStore, id string, p hookParent, payload string) {
	t.Helper()
	if err := hook.Handle(context.Background(), strings.NewReader(payload), io.Discard, st,
		hookConfig(envHook(id, ""), p), nil); err != nil {
		t.Fatalf("Handle(%q): %v", payload, err)
	}
}

// sessionStart is a SessionStart payload reporting transcript path.
func sessionStart(path string) string {
	return `{"hook_event_name":"SessionStart","transcript_path":"` + path + `"}`
}

// mustGetSpawn reads id's row.
func mustGetSpawn(t *testing.T, st *store.Store, id string) store.Spawn {
	t.Helper()
	row, err := st.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn(%q): %v", id, err)
	}
	return row
}

// TestSessionStartRecordsParentIdentity: a SessionStart from the row's own
// agent lands the parent's pid/start time and the payload's transcript_path.
func TestSessionStartRecordsParentIdentity(t *testing.T) {
	const id = "ic-record-identity"
	transcript := writeTranscript(t, "sess-abc123")
	st, _ := seedAgentRow(t, id, store.StateWorking)
	agent := agentParent(t, st, id)

	handleFrom(t, st, id, agent, sessionStart(transcript))

	row := mustGetSpawn(t, st, id)
	// SR-22.9: pid/proc_starttime = the hook's parent (the pane process), not a resolver's walk.
	if row.PID != agent.PID || row.ProcStarttime != agent.Start {
		t.Errorf("PID/ProcStarttime = %d/%q; want the parent %d/%q", row.PID, row.ProcStarttime, agent.PID, agent.Start)
	}
	if row.JSONLPath != transcript {
		t.Errorf("JSONLPath = %q; want %q (payload transcript_path)", row.JSONLPath, transcript)
	}
}

// TestSessionStartRefreshesIdentity: a SessionStart over a row carrying a
// stale process identity records the parent's. FAILS under keep-old behavior.
func TestSessionStartRefreshesIdentity(t *testing.T) {
	const id = "ic-refresh-identity"
	const stalePID = 1111
	// SR-22.9: only the pane process's hooks apply, so the stale identity is seeded, not written by a second agent.
	st, _ := seedAgentRow(t, id, store.StateWorking,
		apitest.WithPID(stalePID), apitest.WithProcStarttime(procstarttimefix.DarwinProcStarttime))
	agent := agentParent(t, st, id)

	handleFrom(t, st, id, agent, sessionStart("/x/second.jsonl"))

	row := mustGetSpawn(t, st, id)
	if row.PID != agent.PID {
		t.Errorf("PID = %d; want %d (refreshed, not kept-old %d)", row.PID, agent.PID, stalePID)
	}
	if row.ProcStarttime != agent.Start {
		t.Errorf("ProcStarttime = %q; want %q (refreshed, not kept-old %q)", row.ProcStarttime, agent.Start, procstarttimefix.DarwinProcStarttime)
	}
}

// TestNonSessionStartLeavesIdentityUntouched: after a good SessionStart,
// PreToolUse and Stop from the same agent do not clobber the recorded
// identity or jsonl_path.
func TestNonSessionStartLeavesIdentityUntouched(t *testing.T) {
	const id = "ic-noclobber"
	transcript := writeTranscript(t, "sess-noclobber")
	st, _ := seedAgentRow(t, id, store.StateWorking)
	agent := agentParent(t, st, id)

	handleFrom(t, st, id, agent, sessionStart(transcript))
	before := mustGetSpawn(t, st, id)

	for _, payload := range []string{
		`{"hook_event_name":"PreToolUse","tool_name":"Bash"}`,
		`{"hook_event_name":"Stop"}`,
	} {
		handleFrom(t, st, id, agent, payload)
	}

	after := mustGetSpawn(t, st, id)
	if after.PID != before.PID || after.ProcStarttime != before.ProcStarttime {
		t.Errorf("identity clobbered: got %d/%q; want %d/%q (unchanged)", after.PID, after.ProcStarttime, before.PID, before.ProcStarttime)
	}
	if after.JSONLPath != before.JSONLPath {
		t.Errorf("JSONLPath clobbered: got %q; want %q (unchanged)", after.JSONLPath, before.JSONLPath)
	}
	if after.PID != agent.PID || after.JSONLPath != transcript {
		t.Errorf("recorded identity lost: PID=%d JSONLPath=%q; want %d/%q", after.PID, after.JSONLPath, agent.PID, transcript)
	}
	if after.State != store.StateWaiting {
		t.Errorf("State = %q; want waiting (Stop applied)", after.State)
	}
}

// TestSessionStartUnreadableParentRecordsNothing: a parent whose start time
// cannot be read never matches (SR-22.9, decision A1), so nothing is written —
// not even jsonl_path — Handle returns nil and one ad.hook.ignored is logged.
func TestSessionStartUnreadableParentRecordsNothing(t *testing.T) {
	const id = "ic-unreadable-parent"
	transcript := writeTranscript(t, "sess-failopen")
	st, _ := seedAgentRow(t, id, store.StateWorking)
	parent := agentParent(t, st, id)
	parent.Start = ""
	before := len(readTrailLines(t, trailFile()))
	prior := mustGetSpawn(t, st, id)

	// SR-22.9: the old resolver-failure premise (record NULL identity, still write jsonl_path) is gone.
	handleFrom(t, st, id, parent, sessionStart(transcript))

	row := mustGetSpawn(t, st, id)
	if row.PID != 0 || row.ProcStarttime != "" || row.JSONLPath != "" || row.State != prior.State {
		t.Errorf("row written by an unmatched hook: PID=%d proc=%q jsonl=%q state=%q; want 0/\"\"/\"\"/%q",
			row.PID, row.ProcStarttime, row.JSONLPath, row.State, prior.State)
	}
	if row.RowVersion != prior.RowVersion {
		t.Errorf("RowVersion = %d; want %d (unchanged)", row.RowVersion, prior.RowVersion)
	}
	ignored := hookIgnoredAfter(t, before, id)
	if len(ignored) != 1 {
		t.Fatalf("ad.hook.ignored lines = %d; want 1", len(ignored))
	}
	assertStr(t, ignored[0], "reason", store.HookReasonPIDMismatch)
}

// TestSessionStartEmptyTranscriptPreservesButRerecords: a SessionStart with an
// EMPTY transcript_path preserves the row's claude_session_id / jsonl_path
// (COALESCE) while the identity is re-recorded from the parent.
func TestSessionStartEmptyTranscriptPreservesButRerecords(t *testing.T) {
	const id = "ic-empty-transcript"
	const sessionID = "sess-preserve"
	const stalePID = 3333
	transcript := writeTranscript(t, sessionID)
	st, dbPath := storefix.OpenTempStore(t)
	// SR-22.9: identity A is seeded; a second pane process's SessionStart would be ignored.
	if _, err := apitest.SeedSpawn(dbPath, id, store.StateWorking, "", "", sessionID, false,
		apitest.WithJsonlPath(transcript), apitest.WithPID(stalePID),
		apitest.WithProcStarttime(procstarttimefix.DarwinProcStarttime)); err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	agent := agentParent(t, st, id)

	handleFrom(t, st, id, agent, sessionStart(""))

	after := mustGetSpawn(t, st, id)
	if after.JSONLPath != transcript {
		t.Errorf("JSONLPath = %q; want %q (preserved on empty transcript)", after.JSONLPath, transcript)
	}
	if after.ClaudeSessionID != sessionID {
		t.Errorf("ClaudeSessionID = %q; want %q (preserved on empty transcript)", after.ClaudeSessionID, sessionID)
	}
	if after.PID != agent.PID || after.ProcStarttime != agent.Start {
		t.Errorf("identity = %d/%q; want %d/%q (re-recorded even on empty transcript, not kept-old %d)",
			after.PID, after.ProcStarttime, agent.PID, agent.Start, stalePID)
	}
}

// TestSessionStartIdleSessionDoesNotRecordDeadPointer is the b.v2c AC1 / AC4
// REGRESSION test for the idle-session case: a fresh Claude session writes no
// .jsonl transcript until its first user turn, so the handler stats the path
// and records NULL when the file is absent. Identity is still recorded.
func TestSessionStartIdleSessionDoesNotRecordDeadPointer(t *testing.T) {
	const id = "ic-idle-no-transcript"
	absent := filepath.Join(t.TempDir(), "sess-idle.jsonl")
	st, _ := seedAgentRow(t, id, store.StateWorking)
	agent := agentParent(t, st, id)

	handleFrom(t, st, id, agent, sessionStart(absent))

	row := mustGetSpawn(t, st, id)
	if row.JSONLPath != "" {
		t.Errorf("JSONLPath = %q; want \"\" (NULL — never assert a dead pointer for an un-messaged session)", row.JSONLPath)
	}
	if row.PID != agent.PID {
		t.Errorf("PID = %d; want %d (identity recorded even without a transcript)", row.PID, agent.PID)
	}
	// The session id is still recorded so find-missing can recompose the path.
	if row.ClaudeSessionID != "sess-idle" {
		t.Errorf("ClaudeSessionID = %q; want \"sess-idle\"", row.ClaudeSessionID)
	}
}

// TestSessionStartHealsWhenTranscriptAppears is the b.v2c AC4 companion: once
// the transcript appears, a re-fired SessionStart records the verified path.
func TestSessionStartHealsWhenTranscriptAppears(t *testing.T) {
	const id = "ic-heal-on-turn"
	transcriptPath := filepath.Join(t.TempDir(), "sess-heal.jsonl")
	st, _ := seedAgentRow(t, id, store.StateWorking)
	agent := agentParent(t, st, id)

	handleFrom(t, st, id, agent, sessionStart(transcriptPath))
	if row := mustGetSpawn(t, st, id); row.JSONLPath != "" {
		t.Fatalf("precondition: JSONLPath = %q; want \"\" before the transcript exists", row.JSONLPath)
	}

	if err := os.WriteFile(transcriptPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	handleFrom(t, st, id, agent, sessionStart(transcriptPath))

	if row := mustGetSpawn(t, st, id); row.JSONLPath != transcriptPath {
		t.Errorf("JSONLPath = %q; want %q (healed once the transcript appeared)", row.JSONLPath, transcriptPath)
	}
}

// compile-time guard: the test asserts against a real store via HookStore.
var _ hook.HookStore = (*store.Store)(nil)
