package hook_test

// identity_capture_test.go — SR-6 identity and transcript capture on the
// SessionStart hook, through hook.Handle against a real store, from the row's
// own agent (SR-22.9: the identity recorded is the hook's parent). The store's
// columns are pinned in internal/store; a fresh row's record and an ignored
// parent's in hook_gate_sessionstart_test.go.

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
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// writeTranscript creates <sessionID>.jsonl under a fresh temp dir and returns
// its path: SessionStart records jsonl_path only for a file on disk (b.v2c).
func writeTranscript(t *testing.T, sessionID string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), sessionID+".jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("writeTranscript(%q): %v", sessionID, err)
	}
	return path
}

// handleFrom runs one Handle (no relay) for id's row from parent p; an error is fatal (fail-open).
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

// TestSessionStartIdentityCapture: each case fails under the opposite behaviour.
func TestSessionStartIdentityCapture(t *testing.T) {
	const stalePID = 1111
	stale := []apitest.SpawnOption{apitest.WithPID(stalePID), apitest.WithProcStarttime(procstarttimefix.DarwinProcStarttime)}
	assertIdentity := func(t *testing.T, row store.Spawn, agent hookParent) {
		t.Helper()
		if row.PID != agent.PID || row.ProcStarttime != agent.Start {
			t.Errorf("pid/proc_starttime = %d/%q; want the parent's %d/%q", row.PID, row.ProcStarttime, agent.PID, agent.Start)
		}
	}

	// A stale identity is replaced by the parent's, not kept.
	t.Run("refresh", func(t *testing.T) {
		const id = "ic-refresh"
		st, _ := ssgSeed(t, id, store.StateWorking, "", stale...)
		agent := agentParent(t, st, id)
		handleFrom(t, st, id, agent, sessionStart("/x/second.jsonl"))
		assertIdentity(t, mustGetSpawn(t, st, id), agent)
	})

	// An empty transcript_path keeps the session id and jsonl_path (COALESCE); the identity is still re-recorded.
	t.Run("empty transcript_path", func(t *testing.T) {
		const id, session = "ic-empty-transcript", "sess-preserve"
		transcript := writeTranscript(t, session)
		st, _ := ssgSeed(t, id, store.StateWorking, session, append(stale, apitest.WithJsonlPath(transcript))...)
		agent := agentParent(t, st, id)
		handleFrom(t, st, id, agent, sessionStart(""))
		row := mustGetSpawn(t, st, id)
		if row.JSONLPath != transcript || row.ClaudeSessionID != session {
			t.Errorf("jsonl_path/session = %q/%q; want %q/%q preserved", row.JSONLPath, row.ClaudeSessionID, transcript, session)
		}
		assertIdentity(t, row, agent)
	})

	// PreToolUse and Stop after it apply but leave the identity and jsonl_path alone.
	t.Run("no clobber", func(t *testing.T) {
		const id = "ic-noclobber"
		transcript := writeTranscript(t, "sess-noclobber")
		st, _ := seedAgentRow(t, id, store.StateWorking)
		agent := agentParent(t, st, id)
		handleFrom(t, st, id, agent, sessionStart(transcript))
		handleFrom(t, st, id, agent, `{"hook_event_name":"PreToolUse","tool_name":"Bash"}`)
		handleFrom(t, st, id, agent, `{"hook_event_name":"Stop"}`)
		row := mustGetSpawn(t, st, id)
		assertIdentity(t, row, agent)
		if row.JSONLPath != transcript || row.State != store.StateWaiting {
			t.Errorf("jsonl_path/state = %q/%q; want %q/waiting (Stop applied, path kept)", row.JSONLPath, row.State, transcript)
		}
	})

	// b.v2c AC1/AC4: an idle session has no transcript yet, so jsonl_path stays
	// NULL (never a dead pointer) while the identity and session id are
	// recorded; once the file appears a re-fired SessionStart records it.
	t.Run("transcript appears later", func(t *testing.T) {
		const id = "ic-idle-then-heal"
		path := filepath.Join(t.TempDir(), "sess-idle.jsonl")
		st, _ := seedAgentRow(t, id, store.StateWorking)
		agent := agentParent(t, st, id)
		handleFrom(t, st, id, agent, sessionStart(path))
		row := mustGetSpawn(t, st, id)
		if row.JSONLPath != "" || row.ClaudeSessionID != "sess-idle" {
			t.Errorf("before the transcript: jsonl_path/session = %q/%q; want NULL/sess-idle", row.JSONLPath, row.ClaudeSessionID)
		}
		assertIdentity(t, row, agent)
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("write transcript: %v", err)
		}
		handleFrom(t, st, id, agent, sessionStart(path))
		if row := mustGetSpawn(t, st, id); row.JSONLPath != path {
			t.Errorf("after the transcript: jsonl_path = %q; want %q", row.JSONLPath, path)
		}
	})
}

// compile-time guard: the tests assert against a real store via HookStore.
var _ hook.HookStore = (*store.Store)(nil)
