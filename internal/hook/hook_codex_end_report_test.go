package hook_test

// hook_codex_end_report_test.go — the Codex end report through hook.Handle
// (SR-6.5, MC-1, MC-3) and the fail-open points the Codex hooks added: a Codex
// SessionEnd from the agent's own process keeps the row's state and sets
// end_reported_at; a marked (agent_id) SessionStart or SessionEnd is ignored as
// subagent_event; an error in the end-report write or in the transcript heal
// leaves the hook silent. The store's write is internal/store's
// hook_end_report_test.go; the classification is classify_test.go's.

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// cxRecordedPath is the transcript path a row below already records, so its
// hooks heal nothing.
const cxRecordedPath = "/x/recorded-rollout.jsonl"

// TestCodexEndReport: a Codex SessionEnd from the row's own process, for the
// row's own session id and for another, sets the end report and keeps the state
// and the recorded session and path. The invocation's whole trail is the
// write's own state_transition (the state kept, a soft refresh) and one
// ad.hook.fired; nothing is printed.
func TestCodexEndReport(t *testing.T) {
	const ownSession, otherSession = "019f0000-0000-7000-8000-000000000018", "019f0000-0000-7000-8000-000000000001"
	for _, state := range []string{store.StatePending, store.StateWaiting, store.StateWorking} {
		for _, rowSession := range []string{ownSession, otherSession} {
			name := state + "/own session"
			if rowSession == otherSession {
				name = state + "/another session"
			}
			t.Run(name, func(t *testing.T) {
				id := "cx-end-" + strings.ReplaceAll(name, "/", "-")
				id = strings.ReplaceAll(id, " ", "-")
				st, _ := cxSeedSession(t, id, state, rowSession, apitest.WithJsonlPath(cxRecordedPath))
				prior := mustGetSpawn(t, st, id)
				payload, _, _ := codexPayload(t, "session-end-other.json", true, nil)

				run := cxFire(t, st, id, cxOwn(t, st, id), payload)

				row := mustGetSpawn(t, st, id)
				if row.EndReportedAtMillis <= 0 {
					t.Errorf("EndReportedAtMillis = %d; want set", row.EndReportedAtMillis)
				}
				if row.State != state || row.SessionID != rowSession || row.JSONLPath != cxRecordedPath {
					t.Errorf("state/session/path = %q/%q/%q; want kept %q/%q/%q", row.State, row.SessionID, row.JSONLPath, state, rowSession, cxRecordedPath)
				}
				if row.RowVersion != prior.RowVersion+1 {
					t.Errorf("RowVersion = %d; want %d (one write)", row.RowVersion, prior.RowVersion+1)
				}
				if run.Stdout != "" {
					t.Errorf("stdout = %q; want empty", run.Stdout)
				}
				if got, want := run.events(), []string{"ad.hook.fired", "ad.spawn.state_transition"}; !reflect.DeepEqual(got, want) {
					t.Fatalf("trail events = %v; want %v", got, want)
				}
				tr := run.lines("ad.spawn.state_transition")[0]
				if tr["prior_state"] != state || tr["new_state"] != state || tr["soft_refresh"] != true || tr["triggering_event_name"] != "SessionEnd" {
					t.Errorf("state_transition = %v; want %s -> %s, soft_refresh true, trigger SessionEnd", tr, state, state)
				}
				if fired := run.lines("ad.hook.fired")[0]; fired["upsert_outcome"] != string(store.UpsertUpdated) {
					t.Errorf("ad.hook.fired upsert_outcome = %v; want %s", fired["upsert_outcome"], store.UpsertUpdated)
				}
			})
		}
	}
}

// TestCodexEndReportMC1ThenMC3: after an end report (MC-1) a later SessionStart
// from the same process clears it (MC-3) with no record beyond what a
// SessionStart on a row with no report writes.
func TestCodexEndReportMC1ThenMC3(t *testing.T) {
	const session = "019f0000-0000-7000-8000-000000000018"
	end, _, _ := codexPayload(t, "session-end-other.json", true, nil)
	start, _, _ := codexPayload(t, "session-start-startup.json", true, nil)
	seed := func(id string) *store.Store {
		st, _ := cxSeedSession(t, id, store.StateWaiting, session, apitest.WithJsonlPath(cxRecordedPath))
		return st
	}

	st := seed("cx-mc-reported")
	cxFire(t, st, "cx-mc-reported", cxOwn(t, st, "cx-mc-reported"), end)
	if got := mustGetSpawn(t, st, "cx-mc-reported").EndReportedAtMillis; got <= 0 {
		t.Fatalf("MC-1: EndReportedAtMillis = %d after the SessionEnd; want set", got)
	}
	reported := cxFire(t, st, "cx-mc-reported", cxOwn(t, st, "cx-mc-reported"), start)

	control := seed("cx-mc-control")
	baseline := cxFire(t, control, "cx-mc-control", cxOwn(t, control, "cx-mc-control"), start)

	row := mustGetSpawn(t, st, "cx-mc-reported")
	if row.EndReportedAtMillis != 0 || row.State != store.StateWaiting {
		t.Errorf("MC-3: EndReportedAtMillis/state = %d/%q after the SessionStart; want 0/waiting", row.EndReportedAtMillis, row.State)
	}
	if !reflect.DeepEqual(reported.events(), baseline.events()) {
		t.Errorf("SessionStart trail after an end report = %v; want the same as with none, %v", reported.events(), baseline.events())
	}
}

// TestCodexEndReportForeignParent: a Codex SessionEnd from another process
// changes nothing and records no end report: one ad.hook.ignored and one
// ad.hook.fired.
func TestCodexEndReportForeignParent(t *testing.T) {
	const id = "cx-end-foreign"
	st, _ := cxSeedSession(t, id, store.StateWaiting, "019f0000-0000-7000-8000-000000000018", apitest.WithJsonlPath(cxRecordedPath))
	prior := mustGetSpawn(t, st, id)
	payload, _, _ := codexPayload(t, "session-end-other.json", true, nil)

	run := cxFire(t, st, id, cxForeign(t, st, id), payload)

	assertRowUnchanged(t, st, id, prior)
	if got, want := run.events(), []string{"ad.hook.fired", "ad.hook.ignored"}; !reflect.DeepEqual(got, want) {
		t.Errorf("trail events = %v; want %v", got, want)
	}
}

// TestClaudeSessionEndRecordsNoEndReport: Claude's SessionEnd is unchanged
// whether the agent flag is absent or claude: the terminal reason ends the row,
// and none records an end report.
func TestClaudeSessionEndRecordsNoEndReport(t *testing.T) {
	cases := []struct {
		fixture string
		want    string
	}{
		{"session-end-prompt-input-exit.json", store.StateEnded},
		{"session-end-clear.json", store.StateWorking},
	}
	for _, tc := range cases {
		for _, flag := range []string{"", "claude"} {
			t.Run(tc.fixture+"/--agent "+flag, func(t *testing.T) {
				id := "cx-claude-end-" + strings.TrimSuffix(tc.fixture, ".json") + flag
				st, _ := seedAgentRow(t, id, store.StateWorking)
				p := agentParent(t, st, id)
				p.Agent = flag

				cxFire(t, st, id, p, readPayloadFixture(t, tc.fixture))

				row := mustGetSpawn(t, st, id)
				if row.State != tc.want || row.EndReportedAtMillis != 0 {
					t.Errorf("state/EndReportedAtMillis = %q/%d; want %q/0", row.State, row.EndReportedAtMillis, tc.want)
				}
			})
		}
	}
}

// TestCodexMarkedSessionEventsIgnored: a Codex SessionStart or SessionEnd that
// carries agent_id (a subagent's or review thread's) is decided from the
// payload: ignored as subagent_event with no hook session id, the row left
// as it was and no end report recorded.
func TestCodexMarkedSessionEventsIgnored(t *testing.T) {
	cases := []struct{ fixture, event string }{
		{"session-start-startup.json", "SessionStart"},
		{"session-end-other.json", "SessionEnd"},
	}
	for _, tc := range cases {
		t.Run(tc.event, func(t *testing.T) {
			id := "cx-marked-" + tc.event
			st, _ := cxSeedSession(t, id, store.StateWaiting, "019f0000-0000-7000-8000-000000000018", apitest.WithJsonlPath(cxRecordedPath))
			prior := mustGetSpawn(t, st, id)
			payload, _, _ := codexPayload(t, tc.fixture, true, map[string]any{"agent_id": cxSubagentID})

			run := cxFire(t, st, id, cxOwn(t, st, id), payload)

			assertRowUnchanged(t, st, id, prior)
			if run.Stdout != "" {
				t.Errorf("stdout = %q; want empty", run.Stdout)
			}
			if got, want := run.events(), []string{"ad.hook.fired", "ad.hook.ignored"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("trail events = %v; want %v", got, want)
			}
			line := run.lines("ad.hook.ignored")[0]
			assertStr(t, line, "reason", store.HookReasonSubagentEvent)
			assertStr(t, line, "hook_event", tc.event)
			assertNull(t, line, "hook_session_id")
		})
	}
}

// failingEndReportStore is the store with an end-report write that fails.
type failingEndReportStore struct{ *store.Store }

func (failingEndReportStore) ApplyHookEndReport(string, store.HookGate, string, string, bool) (store.UpsertOutcome, store.HookApplied, error) {
	return store.UpsertError, store.HookApplied{}, errors.New("simulated end report failure")
}

// failingHealStore is the store with a transcript heal that fails.
type failingHealStore struct{ *store.Store }

func (failingHealStore) HealJsonlPath(string, string, string) (bool, error) {
	return false, errors.New("simulated heal failure")
}

var (
	_ hook.HookStore = failingEndReportStore{}
	_ hook.HookStore = failingHealStore{}
)

// TestCodexNewFailOpenPoints: an error in the end-report write, and an error in
// the heal after an applied write, make Handle return nil with nothing on
// stdout and nothing written but what already was: one ad.hook.fired, and for the
// heal the applied write's own state_transition. Each logs one line.
func TestCodexNewFailOpenPoints(t *testing.T) {
	const session = "019f0000-0000-7000-8000-000000000001"
	t.Run("end-report write", func(t *testing.T) {
		const id = "cx-failopen-end"
		st, _ := cxSeedSession(t, id, store.StateWorking, session, apitest.WithJsonlPath(cxRecordedPath))
		prior := mustGetSpawn(t, st, id)
		payload, _, _ := codexPayload(t, "session-end-other.json", true, nil)

		run := cxFireWith(t, failingEndReportStore{st}, id, cxOwn(t, st, id), payload)

		assertRowUnchanged(t, st, id, prior)
		if run.Stdout != "" {
			t.Errorf("stdout = %q; want empty", run.Stdout)
		}
		if got, want := run.events(), []string{"ad.hook.fired"}; !reflect.DeepEqual(got, want) {
			t.Errorf("trail events = %v; want %v", got, want)
		}
		if fired := run.lines("ad.hook.fired")[0]; fired["upsert_outcome"] != string(store.UpsertError) {
			t.Errorf("ad.hook.fired upsert_outcome = %v; want %s", fired["upsert_outcome"], store.UpsertError)
		}
		if !strings.Contains(run.Log, "simulated end report failure") || strings.Count(run.Log, "\n") != 1 {
			t.Errorf("log = %q; want one line naming the failure", run.Log)
		}
	})
	t.Run("heal", func(t *testing.T) {
		const id = "cx-failopen-heal"
		st, _ := cxSeedSession(t, id, store.StateWaiting, session)
		payload, _, path := codexPayload(t, "user-prompt-submit.json", true, nil)

		run := cxFireWith(t, failingHealStore{st}, id, cxOwn(t, st, id), payload)

		row := mustGetSpawn(t, st, id)
		if row.State != store.StateWorking || row.JSONLPath != "" {
			t.Errorf("state/jsonl_path = %q/%q; want the write applied (working) and the path not healed (%s)", row.State, row.JSONLPath, path)
		}
		if run.Stdout != "" {
			t.Errorf("stdout = %q; want empty", run.Stdout)
		}
		if got, want := run.events(), []string{"ad.hook.fired", "ad.spawn.state_transition"}; !reflect.DeepEqual(got, want) {
			t.Errorf("trail events = %v; want %v", got, want)
		}
		if !strings.Contains(run.Log, "simulated heal failure") || strings.Count(run.Log, "\n") != 1 {
			t.Errorf("log = %q; want one line naming the failure", run.Log)
		}
	})
}
