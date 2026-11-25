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
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
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
		if row.JSONLPath != transcript || row.SessionID != session {
			t.Errorf("jsonl_path/session = %q/%q; want %q/%q preserved", row.JSONLPath, row.SessionID, transcript, session)
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

	// b.v2c AC1/AC4, b.xfc: identity may precede the file; a later ordinary
	// main-agent hook must fill the path without needing another SessionStart.
	t.Run("transcript appears later", func(t *testing.T) {
		for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop"} {
			t.Run(event, func(t *testing.T) {
				const id = "ic-idle-then-heal"
				path := filepath.Join(t.TempDir(), "sess-idle.jsonl")
				st, _ := seedAgentRow(t, id, store.StateWorking)
				agent := agentParent(t, st, id)
				handleFrom(t, st, id, agent, sessionStart(path))
				row := mustGetSpawn(t, st, id)
				if row.JSONLPath != "" || row.SessionID != "sess-idle" {
					t.Fatalf("before the transcript: jsonl_path/session = %q/%q; want NULL/sess-idle", row.JSONLPath, row.SessionID)
				}
				assertIdentity(t, row, agent)
				if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
					t.Fatalf("write transcript: %v", err)
				}
				payload := payloadWith(t, "stop.json", map[string]any{"hook_event_name": event, "transcript_path": path})
				handleFrom(t, st, id, agent, string(payload))
				healed := mustGetSpawn(t, st, id)
				if healed.JSONLPath != path || healed.SessionID != row.SessionID {
					t.Errorf("after the transcript: path/session = %q/%q; want %q/%q", healed.JSONLPath, healed.SessionID, path, row.SessionID)
				}
				assertIdentity(t, healed, agent)
			})
		}
	})
}

// TestClaudeHookTranscriptFill (b.xfc) pins the same-session, path-only exception
// and its guards. All observations are direct store reads, never active checks.
func TestClaudeHookTranscriptFill(t *testing.T) {
	const session = "sess-claude-fill"
	cases := []struct {
		name, event, agentID string
		absent, empty        bool
		recorded, other      bool
		foreign, noPane      bool
		heal                 bool
	}{
		{name: "Stop", event: "Stop", heal: true},
		{name: "UserPromptSubmit", event: "UserPromptSubmit", heal: true},
		{name: "soft refresh", event: "Notification", heal: true},
		{name: "file absent", event: "Stop", absent: true},
		{name: "path empty", event: "Stop", empty: true},
		{name: "path already recorded", event: "Stop", recorded: true},
		{name: "different session", event: "Stop", other: true},
		{name: "agent_id", event: "Stop", agentID: "child-1"},
		{name: "foreign parent", event: "Stop", foreign: true},
		{name: "no pane", event: "Stop", noPane: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const id = "claude-fill"
			// This directory is independent of cwd/CLAUDE_CONFIG_DIR: only the
			// hook can supply this exact path, including its directory.
			pathSession := session
			if tc.other {
				pathSession = "sess-other"
			}
			path := filepath.Join(t.TempDir(), pathSession+".jsonl")
			if !tc.absent {
				if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.empty {
				path = ""
			}
			opts := []apitest.SpawnOption{apitest.WithLaunchIdentity(ssgFreshPane), apitest.WithLifeNumber(2)}
			keptPath := ""
			if tc.recorded {
				keptPath = writeTranscript(t, session) // same basename, different directory
				opts = append(opts, apitest.WithJsonlPath(keptPath))
			}
			if tc.noPane {
				opts = append(opts, apitest.WithNoPane())
			}
			st, _ := ssgSeed(t, id, store.StateWorking, session, opts...)
			parent := agentParent(t, st, id)
			if tc.foreign {
				parent = foreignParent(t, st, id)
			}
			prior := mustGetSpawn(t, st, id)
			payload := payloadWith(t, "stop.json", map[string]any{
				"hook_event_name": tc.event, "transcript_path": path,
				"session_id": session, "agent_id": tc.agentID,
				"notification_type": "auth_success",
			})

			run := cxFire(t, st, id, parent, payload)

			row := mustGetSpawn(t, st, id)
			if tc.foreign || tc.noPane {
				assertRowUnchanged(t, st, id, prior)
			} else {
				wantState := store.StateWaiting
				if tc.event != "Stop" {
					wantState = store.StateWorking
				}
				if row.State != wantState {
					t.Errorf("state = %q; want ordinary hook state %q", row.State, wantState)
				}
				wantVersion := prior.RowVersion + 1
				if tc.heal {
					wantVersion++
				}
				if row.RowVersion != wantVersion {
					t.Errorf("row_version = %d; want %d", row.RowVersion, wantVersion)
				}
			}
			if row.SessionID != prior.SessionID || row.PID != prior.PID || row.ProcStarttime != prior.ProcStarttime ||
				row.Identity != prior.Identity || row.LifeNumber != prior.LifeNumber {
				t.Errorf("hook fill changed bound session/process/launch/life: before %+v; after %+v", prior, row)
			}
			wantPath, wantEvents := keptPath, 0
			if tc.heal {
				wantPath, wantEvents = path, 1
			}
			if row.JSONLPath != wantPath {
				t.Errorf("jsonl_path = %q; want %q", row.JSONLPath, wantPath)
			}
			healed := run.lines("ad.session.jsonl_healed")
			if len(healed) != wantEvents {
				t.Errorf("healed events = %v; want %d", healed, wantEvents)
			}
			if len(healed) == 1 && tc.heal {
				for key, want := range map[string]any{"instance_id": id, "session_id": session, "jsonl_path": path, "source": "ad_spawn_store"} {
					if healed[0][key] != want {
						t.Errorf("healed event %s = %v; want %v", key, healed[0][key], want)
					}
				}
			}
			if run.Stdout != "" {
				t.Errorf("stdout = %q; want empty", run.Stdout)
			}
			if tc.heal {
				repeated := cxFire(t, st, id, parent, payload)
				after := mustGetSpawn(t, st, id)
				if after.JSONLPath != path || after.RowVersion != row.RowVersion+1 || len(repeated.lines("ad.session.jsonl_healed")) != 0 {
					t.Errorf("repeated hook path/version/events = %q/%d/%v; want %q/%d/none", after.JSONLPath, after.RowVersion,
						repeated.lines("ad.session.jsonl_healed"), path, row.RowVersion+1)
				}
			}
		})
	}
}

// TestClaudeHookHealFailOpen (b.xfc): failure of the optional path write leaves
// the applied ordinary transition intact and logs the error without stdout.
func TestClaudeHookHealFailOpen(t *testing.T) {
	const id, session = "claude-fill-failure", "sess-claude-fill-failure"
	st, _ := ssgSeed(t, id, store.StateWaiting, session, apitest.WithLaunchIdentity(ssgFreshPane))
	path := writeTranscript(t, session)
	prior := mustGetSpawn(t, st, id)
	payload := payloadWith(t, "user-prompt-submit.json", map[string]any{"transcript_path": path, "session_id": session})

	run := cxFireWith(t, failingHealStore{st}, id, agentParent(t, st, id), payload)

	row := mustGetSpawn(t, st, id)
	if row.State != store.StateWorking || row.JSONLPath != "" || row.SessionID != session || row.RowVersion != prior.RowVersion+1 {
		t.Errorf("after failed heal: state/path/session/version = %q/%q/%q/%d; want working/NULL/%q/%d",
			row.State, row.JSONLPath, row.SessionID, row.RowVersion, session, prior.RowVersion+1)
	}
	if run.Stdout != "" || len(run.lines("ad.session.jsonl_healed")) != 0 ||
		len(run.lines("ad.hook.fired")) != 1 || len(run.lines("ad.spawn.state_transition")) != 1 {
		t.Errorf("failed heal stdout/trail = %q/%v; want ordinary hook only", run.Stdout, run.Trail)
	}
	if !strings.Contains(run.Log, "simulated heal failure") || strings.Count(run.Log, "\n") != 1 {
		t.Errorf("log = %q; want one line naming the heal failure", run.Log)
	}
}

// TestClaudeHookHealedPathArchived (b.xfc): the ordinary hook's exact path is
// archived on reuse or rotation without get/status/list/sweep healing it first.
func TestClaudeHookHealedPathArchived(t *testing.T) {
	for _, operation := range []string{"reuse", "rotation"} {
		t.Run(operation, func(t *testing.T) {
			const id, session, life = "claude-fill-archive", "sess-claude-archive", int64(2)
			cfg := t.TempDir()
			path := filepath.Join(t.TempDir(), session+".jsonl")
			st, dbPath := seedAgentRow(t, id, store.StatePending, apitest.WithLifeNumber(life),
				apitest.WithLaunchIdentity(ssgFreshPane), apitest.WithExtraEnv(map[string]string{"CLAUDE_CONFIG_DIR": cfg}))
			parent := agentParent(t, st, id)
			handleFrom(t, st, id, parent, sessionStart(path))
			if row := mustGetSpawn(t, st, id); row.SessionID != session || row.JSONLPath != "" {
				t.Fatalf("provisional SessionStart = %q/%q; want %q/NULL", row.SessionID, row.JSONLPath, session)
			}
			if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			stop := payloadWith(t, "stop.json", map[string]any{"transcript_path": path, "session_id": session})
			run := cxFire(t, st, id, parent, stop)
			if row := mustGetSpawn(t, st, id); row.JSONLPath != path || len(run.lines("ad.session.jsonl_healed")) != 1 {
				// Keep going so the red run also proves the archive loses the path.
				t.Errorf("after Stop: path/events = %q/%v; want %q/one heal", row.JSONLPath, run.lines("ad.session.jsonl_healed"), path)
			}

			wantSession, wantPath, wantState := "", "", store.StatePending
			if operation == "reuse" {
				// End without another path-bearing hook: only Stop can heal.
				end := payloadWith(t, "session-end-prompt-input-exit.json", map[string]any{"transcript_path": ""})
				handleFrom(t, st, id, parent, string(end))
				finished := mustGetSpawn(t, st, id)
				if finished.State != store.StateEnded {
					t.Fatalf("SessionEnd left state %q; want ended", finished.State)
				}
				now := time.Now()
				fresh := storefix.WithExit(t, st, id, store.Spawn{
					Agent: store.AgentClaude, CWD: finished.CWD, TmuxSessionName: finished.TmuxSessionName,
					RelayMode: "off", ExtraEnv: finished.ExtraEnv, StartedAt: now,
					LaunchStartedAtMillis: now.UnixMilli(), Identity: ssgNoPane,
				})
				result, archived, _, err := st.ResetForReuse(id, finished.Snapshot, finished.Agent, fresh)
				if err != nil || result != store.CondApplied || archived != session {
					t.Fatalf("ResetForReuse = %v, archived %q, %v; want applied, %q", result, archived, err, session)
				}
			} else {
				wantSession, wantState = "sess-claude-incoming", store.StateWaiting
				wantPath = writeTranscript(t, wantSession)
				handleFrom(t, st, id, parent, sessionStart(wantPath))
			}
			row := mustGetSpawn(t, st, id)
			if row.SessionID != wantSession || row.JSONLPath != wantPath || row.State != wantState || row.LifeNumber != life {
				t.Errorf("current session/path/state/life = %q/%q/%q/%d; want %q/%q/%q/%d",
					row.SessionID, row.JSONLPath, row.State, row.LifeNumber, wantSession, wantPath, wantState, life)
			}
			if got, want := ssgHistory(t, dbPath, id), []ssgArchived{{session, path, life}}; !reflect.DeepEqual(got, want) {
				t.Errorf("session_history = %+v; want %+v", got, want)
			}

			// The first API read is after the archive; recovery cannot fix history.
			got, err := api.Get(st, api.RelayView{}, id)
			if err != nil {
				t.Fatalf("Get after archival: %v", err)
			}
			if len(got.PriorSessions) != 1 {
				t.Fatalf("prior_sessions = %+v; want one outgoing session", got.PriorSessions)
			}
			if old := got.PriorSessions[0]; old.SessionID != session || old.JSONLPath != path {
				t.Errorf("prior_sessions[0] = %+v; want %q at %q", old, session, path)
			}
		})
	}
}

// compile-time guard: the tests assert against a real store via HookStore.
var _ hook.HookStore = (*store.Store)(nil)
