package hook_test

// hook_codex_handle_test.go — Codex hooks through hook.Handle (SR-6.1, SR-6.2,
// SR-6.7, MC-1; E4-T1) against a real store with the Codex payload fixtures
// (testdata/codex-hook-payloads) and a fake parent process. Covered here: the
// report-in moves from pending, the /review thread, a foreign parent, the
// subagent events of a Codex row, and the classification following --agent.
// The classification table is classify_test.go's; the end report, the marked
// SessionStart and SessionEnd and the new fail-open points are in
// hook_codex_end_report_test.go; the session id, transcript and heal are in
// hook_codex_transcript_test.go.

import (
	"bytes"
	"context"
	"log"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// cxRun is what one Handle did: its stdout, its log and the trail lines it wrote
// for the row.
type cxRun struct {
	Stdout string
	Log    string
	Trail  []map[string]any
}

// events is the run's trail event names, sorted.
func (r cxRun) events() []string {
	var out []string
	for _, l := range r.Trail {
		out = append(out, l["event"].(string))
	}
	slices.Sort(out)
	return out
}

// lines is the run's trail lines of event.
func (r cxRun) lines(event string) []map[string]any {
	var out []map[string]any
	for _, l := range r.Trail {
		if l["event"] == event {
			out = append(out, l)
		}
	}
	return out
}

// cxSeed is seedAgentRow for a Codex row.
func cxSeed(t *testing.T, id, state string, opts ...apitest.SpawnOption) (*store.Store, string) {
	t.Helper()
	return seedAgentRow(t, id, state, append([]apitest.SpawnOption{apitest.WithAgent(store.AgentCodex)}, opts...)...)
}

// cxSeedSession is cxSeed with the row recording sessionID (and no transcript path).
func cxSeedSession(t *testing.T, id, state, sessionID string, opts ...apitest.SpawnOption) (*store.Store, string) {
	t.Helper()
	return ssgSeed(t, id, state, sessionID, append([]apitest.SpawnOption{apitest.WithAgent(store.AgentCodex)}, opts...)...)
}

// cxOwn is the Codex agent of id's row: the row's own pane process, --agent codex.
func cxOwn(t *testing.T, st *store.Store, id string) hookParent {
	t.Helper()
	return codexParent(agentParent(t, st, id))
}

// cxForeign is another Codex process carrying id's instance id.
func cxForeign(t *testing.T, st *store.Store, id string) hookParent {
	t.Helper()
	return codexParent(foreignParent(t, st, id))
}

// cxFireWith runs one Handle for id with st (a double or the store itself) from
// parent p (relay off) and returns its run; a Handle error fails the test, since
// the hook is fail-open.
func cxFireWith(t *testing.T, st hook.HookStore, id string, p hookParent, payload []byte) cxRun {
	t.Helper()
	before := len(readTrailLines(t, trailFile()))
	var out, logs bytes.Buffer
	if err := hook.Handle(context.Background(), bytes.NewReader(payload), &out, st, hookConfig(envHook(id, ""), p), log.New(&logs, "", 0)); err != nil {
		t.Fatalf("Handle: %v; want nil (fail-open)", err)
	}
	run := cxRun{Stdout: out.String(), Log: logs.String()}
	for _, l := range readTrailLines(t, trailFile())[before:] {
		if l["instance_id"] == id {
			run.Trail = append(run.Trail, l)
		}
	}
	return run
}

// cxFire is cxFireWith on the real store.
func cxFire(t *testing.T, st *store.Store, id string, p hookParent, payload []byte) cxRun {
	t.Helper()
	return cxFireWith(t, st, id, p, payload)
}

// cxFixture returns the Codex fixture with its rollout written under a temp
// folder and extra keys set; see codexPayload.
func cxFixture(t *testing.T, fixture string, extra map[string]any) []byte {
	t.Helper()
	payload, _, _ := codexPayload(t, fixture, true, extra)
	return payload
}

// TestCodexReportIn: from a pending row (and one find-missing noted unreported)
// each Codex hook that moves a row does its move through Handle: one write that
// advances row_version by one, clears the liveness note, prints nothing and
// leaves no ad.hook.ignored. A soft refresh and an end report keep pending
// (SR-6.2, SR-6.5). PermissionRequest with the relay off is check_permission.
func TestCodexReportIn(t *testing.T) {
	cases := []struct {
		fixture string
		want    string
	}{
		{"session-start-startup.json", store.StateWaiting},
		{"user-prompt-submit.json", store.StateWorking},
		{"pre-tool-use-bash.json", store.StateWorking},
		{"post-tool-use-bash.json", store.StateWorking},
		{"pre-tool-use-subagent.json", store.StateWorking},
		{"permission-request-bash.json", store.StateCheckPermission},
		{"stop.json", store.StateWaiting},
		{"interrupt.json", store.StateWaiting},
		// A soft refresh: a subagent's Interrupt and its start and stop events.
		{"interrupt-subagent.json", store.StatePending},
		{"subagent-start.json", store.StatePending},
		{"subagent-stop.json", store.StatePending},
		// The end report keeps the state (SR-6.5).
		{"session-end-other.json", store.StatePending},
	}
	for _, rs := range gateRowStates[:2] {
		for _, tc := range cases {
			t.Run(rs.name+"/"+tc.fixture, func(t *testing.T) {
				id := "cx-report-in-" + strings.NewReplacer(" ", "-", ".", "-").Replace(rs.name+"-"+tc.fixture)
				st, _ := cxSeed(t, id, rs.state, rs.opts...)
				prior := mustGetSpawn(t, st, id)

				run := cxFire(t, st, id, cxOwn(t, st, id), cxFixture(t, tc.fixture, nil))

				row := mustGetSpawn(t, st, id)
				if row.State != tc.want {
					t.Errorf("State = %q; want %q", row.State, tc.want)
				}
				if row.RowVersion != prior.RowVersion+1 {
					t.Errorf("RowVersion = %d; want %d (one applied write)", row.RowVersion, prior.RowVersion+1)
				}
				if row.LivenessNote != "" || row.LivenessUnverifiedSince != "" {
					t.Errorf("liveness note/since = %q/%q; want both cleared", row.LivenessNote, row.LivenessUnverifiedSince)
				}
				if run.Stdout != "" {
					t.Errorf("stdout = %q; want empty", run.Stdout)
				}
				if got := run.lines("ad.hook.ignored"); len(got) != 0 {
					t.Errorf("ad.hook.ignored lines = %v; want none for an applied hook", got)
				}
			})
		}
	}
}

// TestCodexLifecycleSequence: one agent's hooks in order move its row from
// pending through a turn and back (SessionStart, UserPromptSubmit, Stop,
// UserPromptSubmit, Interrupt), each step one write.
func TestCodexLifecycleSequence(t *testing.T) {
	const id = "cx-sequence"
	st, _ := cxSeed(t, id, store.StatePending)
	steps := []struct {
		fixture string
		want    string
	}{
		{"session-start-startup.json", store.StateWaiting},
		{"user-prompt-submit.json", store.StateWorking},
		{"stop.json", store.StateWaiting},
		{"user-prompt-submit.json", store.StateWorking},
		{"interrupt.json", store.StateWaiting},
	}
	for i, s := range steps {
		prior := mustGetSpawn(t, st, id)
		cxFire(t, st, id, cxOwn(t, st, id), cxFixture(t, s.fixture, nil))
		row := mustGetSpawn(t, st, id)
		if row.State != s.want || row.RowVersion != prior.RowVersion+1 {
			t.Errorf("step %d (%s): state %q, row_version %d; want %q, %d", i, s.fixture, row.State, row.RowVersion, s.want, prior.RowVersion+1)
		}
	}
}

// TestCodexWorkingHold: an open permission request holds a row in
// check_permission, so a Codex UserPromptSubmit or PreToolUse does not move it
// to working (the hook is the agent's own and is not ignored).
func TestCodexWorkingHold(t *testing.T) {
	for _, fixture := range []string{"user-prompt-submit.json", "pre-tool-use-bash.json"} {
		t.Run(fixture, func(t *testing.T) {
			id := "cx-hold-" + strings.TrimSuffix(fixture, ".json")
			st, _ := cxSeed(t, id, store.StateWorking, apitest.WithRelayMode("on"))
			storefix.SeedRelayRequest(t, st, id, store.RelayRequest{RequestToken: storefix.TestRequestTokenA, ToolName: "Bash",
				ToolInput: `{}`, ToolUseID: "toolu_cx_hold", Hook: relayHookSelf, SettledAt: time.Now().Add(time.Hour)})
			if got := mustGetSpawn(t, st, id).State; got != store.StateCheckPermission {
				t.Fatalf("seeded state = %q; want check_permission", got)
			}

			run := cxFire(t, st, id, cxOwn(t, st, id), cxFixture(t, fixture, nil))

			if row := mustGetSpawn(t, st, id); row.State != store.StateCheckPermission {
				t.Errorf("State = %q; want check_permission (the open request holds the move to working)", row.State)
			}
			if got := run.lines("ad.hook.ignored"); len(got) != 0 {
				t.Errorf("ad.hook.ignored lines = %v; want none (the hook is the agent's own, only the move is held)", got)
			}
		})
	}
}

// TestCodexReviewThread: the /review thread's PreToolUse and UserPromptSubmit
// carry no agent_id, so each moves the row to working, and with no later hook
// the row stays working (the documented residual: the review thread sends no
// Stop). The thread's hooks carry the main session id and their own rollout.
func TestCodexReviewThread(t *testing.T) {
	const id, mainSession = "cx-review", "019f0000-0000-7000-8000-000000000007"
	for _, first := range []string{"user-prompt-submit-review-thread.json", "pre-tool-use-review-thread.json"} {
		t.Run(first, func(t *testing.T) {
			id := id + "-" + strings.TrimSuffix(first, ".json")
			st, _ := cxSeedSession(t, id, store.StateWaiting, mainSession)
			for _, fixture := range []string{first, "pre-tool-use-review-thread.json"} {
				payload, sid, _ := codexPayload(t, fixture, false, nil)
				if sid != mainSession {
					t.Fatalf("fixture %s session_id = %q; want the main session %q", fixture, sid, mainSession)
				}
				run := cxFire(t, st, id, cxOwn(t, st, id), payload)
				if got := mustGetSpawn(t, st, id).State; got != store.StateWorking {
					t.Errorf("state after %s = %q; want working", fixture, got)
				}
				if got := run.lines("ad.hook.ignored"); len(got) != 0 {
					t.Errorf("ad.hook.ignored lines = %v; want none", got)
				}
			}
			// No Stop follows: nothing moves the row back.
			if row := mustGetSpawn(t, st, id); row.State != store.StateWorking {
				t.Errorf("state with no later hook = %q; want working", row.State)
			}
		})
	}
}

// TestCodexHookFromForeignParent: a Codex hook whose parent is not the row's
// pane process changes nothing, prints nothing and writes one ad.hook.ignored
// (pid_mismatch) and the one ad.hook.fired, whatever the event.
func TestCodexHookFromForeignParent(t *testing.T) {
	note := []apitest.SpawnOption{apitest.WithLivenessNote("unverified by a sweep"), apitest.WithLivenessUnverifiedSince("2026-09-30 00:00:00")}
	cases := []struct{ fixture, event string }{
		{"session-start-startup.json", "SessionStart"},
		{"user-prompt-submit.json", "UserPromptSubmit"},
		{"pre-tool-use-bash.json", "PreToolUse"},
		{"permission-request-bash.json", "PermissionRequest"},
		{"stop.json", "Stop"},
		{"interrupt.json", "Interrupt"},
		{"session-end-other.json", "SessionEnd"},
		{"subagent-start.json", "SubagentStart"},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			id := "cx-foreign-" + strings.TrimSuffix(tc.fixture, ".json")
			st, _ := cxSeed(t, id, store.StateWorking, note...)
			prior := mustGetSpawn(t, st, id)
			payload, sid, _ := codexPayload(t, tc.fixture, true, nil)
			foreign := cxForeign(t, st, id)

			run := cxFire(t, st, id, foreign, payload)

			assertRowUnchanged(t, st, id, prior)
			if run.Stdout != "" {
				t.Errorf("stdout = %q; want empty", run.Stdout)
			}
			if got, want := run.events(), []string{"ad.hook.fired", "ad.hook.ignored"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("trail events = %v; want %v", got, want)
			}
			line := run.lines("ad.hook.ignored")[0]
			assertStr(t, line, "reason", store.HookReasonPIDMismatch)
			assertStr(t, line, "hook_event", tc.event)
			assertStr(t, line, "parent_command", "codex")
			if tc.fixture != "subagent-start.json" {
				assertStr(t, line, "hook_session_id", sid)
			} else {
				assertNull(t, line, "hook_session_id")
			}
			if fired := run.lines("ad.hook.fired")[0]; fired["upsert_outcome"] != string(store.UpsertNoChange) {
				t.Errorf("ad.hook.fired upsert_outcome = %v; want %s", fired["upsert_outcome"], store.UpsertNoChange)
			}
		})
	}
}

// TestCodexSubagentEventsWriteNoSubagentRows: Codex hooks capture no subagents,
// independently guarded by classification and the row tracking flag.
func TestCodexSubagentEventsWriteNoSubagentRows(t *testing.T) {
	fixtures := []string{"subagent-start.json", "subagent-stop.json", "stop.json"}
	t.Run("fresh Codex row", func(t *testing.T) {
		const id = "cx-subagent-fresh"
		st, dbPath := cxSeed(t, id, store.StateWorking)
		for _, f := range fixtures {
			cxFire(t, st, id, cxOwn(t, st, id), cxFixture(t, f, nil))
		}
		if rows := readGateSubagents(t, dbPath, id); len(rows.Entries) != 0 || len(rows.Launches) != 0 {
			t.Errorf("subagent rows = %+v; want none", rows)
		}
		got, err := api.ListSubagents(st, id)
		if err != nil || len(got.Subagents) != 0 || got.Subagents == nil {
			t.Errorf("ListSubagents = %+v, %v; want subagents []", got, err)
		}
	})
	t.Run("seeded record and launch are untouched", func(t *testing.T) {
		const id = "cx-subagent-seeded"
		st, dbPath := cxSeed(t, id, store.StateWorking)
		seedGateSubagents(t, dbPath, id)
		before := readGateSubagents(t, dbPath, id)
		for _, f := range fixtures {
			cxFire(t, st, id, cxOwn(t, st, id), cxFixture(t, f, nil))
		}
		assertGateSubagentsUnchanged(t, dbPath, id, before)
	})
	listing := map[string]any{"background_tasks": []any{map[string]any{
		"type": "subagent", "status": "running", "id": "cx-listed-new", "agent_type": "default",
	}}}
	for _, tracked := range []bool{true, false} {
		name := "classifier guard"
		if !tracked {
			name = "tracking flag guard"
		}
		t.Run(name, func(t *testing.T) {
			id := "cx-subagent-" + strings.ReplaceAll(name, " ", "-")
			var opts []apitest.SpawnOption
			if !tracked {
				opts = append(opts, apitest.WithNoSubagentTracking())
			}
			st, dbPath := cxSeed(t, id, store.StateWorking, opts...)
			if got := mustGetSpawn(t, st, id).SubagentTracking; got != tracked {
				t.Fatalf("SubagentTracking = %v; want %v", got, tracked)
			}
			aged := time.Now().Add(-time.Minute).UnixMilli()
			if err := apitest.SeedSubagent(dbPath, id, cxSubagentID,
				apitest.WithSubagentRunMode("background"), apitest.WithSubagentConfirmed(false),
				apitest.WithSubagentOpenedAt(aged)); err != nil {
				t.Fatalf("SeedSubagent: %v", err)
			}
			const useID = "toolu_cx_claimable"
			if err := apitest.SeedSubagentLaunch(dbPath, id, useID, apitest.WithClaimableOpenedAt(aged)); err != nil {
				t.Fatalf("SeedSubagentLaunch: %v", err)
			}
			before := readGateSubagents(t, dbPath, id)
			parent := agentParent(t, st, id)
			if tracked {
				parent = cxOwn(t, st, id)
			}
			type captureCase struct {
				fixture   string
				extra     map[string]any
				lifecycle string
			}
			cases := []captureCase{
				{"subagent-start.json", nil, "SubagentStart"},
				{"subagent-stop.json", nil, "SubagentStop"},
				{"stop.json", listing, ""},
			}
			if tracked {
				cases = append(cases, []captureCase{
					{"pre-tool-use-bash.json", map[string]any{"tool_name": "Agent", "tool_use_id": "toolu_cx_new", "tool_input": map[string]any{"subagent_type": "default"}}, ""},
					{"post-tool-use-bash.json", map[string]any{"hook_event_name": "PostToolUseFailure", "tool_name": "Agent", "tool_use_id": useID}, ""},
					{"post-tool-use-bash.json", map[string]any{"tool_name": "Agent", "tool_use_id": useID,
						"tool_input":    map[string]any{"subagent_type": "default", "description": "launch details"},
						"tool_response": map[string]any{"agentId": cxSubagentID, "status": "async_launched", "resolvedModel": "fixture-model"}}, ""},
				}...)
			}
			for _, tc := range cases {
				t.Run(tc.fixture+tc.lifecycle, func(t *testing.T) {
					prior := mustGetSpawn(t, st, id)
					run := cxFire(t, st, id, parent, cxFixture(t, tc.fixture, tc.extra))
					assertGateSubagentsUnchanged(t, dbPath, id, before)
					if tracked && tc.lifecycle != "" {
						row := mustGetSpawn(t, st, id)
						if row.State != prior.State || row.RowVersion != prior.RowVersion+1 {
							t.Errorf("state/version = %s/%d; want %s/%d", row.State, row.RowVersion, prior.State, prior.RowVersion+1)
						}
						transitions := run.lines("ad.spawn.state_transition")
						if len(transitions) != 1 {
							t.Fatalf("state transitions = %v; want one soft refresh", transitions)
						}
						tr := transitions[0]
						if tr["soft_refresh"] != true || tr["prior_state"] != prior.State || tr["new_state"] != prior.State || tr["triggering_event_name"] != tc.lifecycle {
							t.Errorf("state transition = %v; want state kept, soft_refresh true, trigger %s", tr, tc.lifecycle)
						}
						if strings.Contains(run.Log, "unknown event") {
							t.Errorf("log = %q; want no unknown event", run.Log)
						}
					}
				})
			}
			if tracked {
				const newID = "cx-no-flag-new"
				cxFire(t, st, id, agentParent(t, st, id), cxFixture(t, "subagent-start.json", map[string]any{"agent_id": newID}))
				after := readGateSubagents(t, dbPath, id)
				if len(after.Entries) != len(before.Entries)+1 {
					t.Fatalf("entries = %+v; want one new entry under no --agent", after.Entries)
				}
				entry := after.Entries[len(after.Entries)-1]
				if entry.SubagentID != newID || entry.Confirmed != int64(0) {
					t.Errorf("new entry = %+v; want %s unconfirmed", entry, newID)
				}
			}
		})
	}
	t.Run("Claude row still writes its entry", func(t *testing.T) {
		const id = "cx-subagent-claude"
		st, dbPath := seedAgentRow(t, id, store.StateWorking)
		cxFire(t, st, id, agentParent(t, st, id), readPayloadFixture(t, "subagent-start.json"))
		if rows := readGateSubagents(t, dbPath, id); len(rows.Entries) != 1 {
			t.Errorf("subagent entries = %d; want 1 (Claude's capture)", len(rows.Entries))
		}
	})
}

// TestHandleClassificationFollowsAgentFlag: the table that classifies a hook is
// the hook's --agent, not the row's agent column, so each case below runs a hook
// under the other agent's flag than its row's. (The grandparent advice follows
// the row's agent; TestHookPaneIsGrandparentAdviceByAgent pins that half.)
func TestHandleClassificationFollowsAgentFlag(t *testing.T) {
	cases := []struct {
		name     string
		rowAgent string
		hookAs   string // the hook's agent flag ("" = absent)
		fixture  string // "codex/..." or a Claude fixture name
		want     string
		endRep   bool
	}{
		{"Codex SessionEnd on a Claude row is an end report", store.AgentClaude, "codex", "codex/session-end-other.json", store.StateWorking, true},
		{"Claude SessionEnd on a Codex row ends it", store.AgentCodex, "", "session-end-prompt-input-exit.json", store.StateEnded, false},
		{"Claude SessionEnd on a Codex row ends it (--agent claude)", store.AgentCodex, "claude", "session-end-prompt-input-exit.json", store.StateEnded, false},
		{"Codex Interrupt on a Claude row is a turn end", store.AgentClaude, "codex", "codex/interrupt.json", store.StateWaiting, false},
		{"Codex Interrupt under no flag is unknown: state kept", store.AgentCodex, "", "codex/interrupt.json", store.StateWorking, false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "cx-flag-" + string(rune('a'+i))
			st, _ := seedAgentRow(t, id, store.StateWorking, apitest.WithAgent(tc.rowAgent))
			p := agentParent(t, st, id)
			p.Agent = tc.hookAs
			payload := readPayloadFixture(t, tc.fixture)
			if strings.HasPrefix(tc.fixture, "codex/") {
				payload, _, _ = codexPayload(t, strings.TrimPrefix(tc.fixture, "codex/"), true, nil)
			}

			cxFire(t, st, id, p, payload)

			row := mustGetSpawn(t, st, id)
			if row.State != tc.want {
				t.Errorf("State = %q; want %q", row.State, tc.want)
			}
			if got := row.EndReportedAtMillis > 0; got != tc.endRep {
				t.Errorf("end report recorded = %v; want %v", got, tc.endRep)
			}
		})
	}
}
