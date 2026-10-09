package hook_test

// hook_subagent_test.go — SR-22.9 "Subagent and in-process teammate hooks",
// SR-14 subagent_event: hooks carrying agent_id, fired from the row's own pane
// process, through hook.Handle against a real store. Helpers are in
// hook_gate_sessionstart_test.go, hook_gate_test.go and hook_parent_fakes_test.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// subPayload returns fixture with its transcript moved to a file on disk and
// extra keys set, plus the session id and transcript path it reports.
func subPayload(t *testing.T, fixture string, extra map[string]any) (payload []byte, sessionID, path string) {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(readPayloadFixture(t, fixture), &p); err != nil {
		t.Fatalf("fixture %s: %v", fixture, err)
	}
	sessionID, _ = p["session_id"].(string)
	path = writeTranscript(t, sessionID)
	p["transcript_path"] = path
	for k, v := range extra {
		p[k] = v
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return raw, sessionID, path
}

// subPending is a fresh spawn's pending row with its pane recorded and no session.
func subPending(t *testing.T, id string) (*store.Store, string) {
	return ssgSeed(t, id, store.StatePending, "", apitest.WithLaunchIdentity(ssgFreshPane))
}

// oneFired returns id's only ad.hook.fired line after before.
func oneFired(t *testing.T, before int, id string) map[string]any {
	t.Helper()
	fired := linesAfter(t, before, "ad.hook.fired", id)
	if len(fired) != 1 {
		t.Fatalf("ad.hook.fired lines = %d; want 1", len(fired))
	}
	return fired[0]
}

// TestSubagentLifecycleHookIgnored: a SessionStart or SessionEnd with agent_id
// changes nothing and logs one subagent_event; none when no row has the id.
func TestSubagentLifecycleHookIgnored(t *testing.T) {
	noRow := func(t *testing.T, _ string) (*store.Store, string) { return storefix.OpenTempStore(t) }
	cases := []struct {
		name       string
		fixture    string
		event      string
		seed       func(*testing.T, string) (*store.Store, string)
		hasRow     bool
		rowSession any // row_session_id; nil = null
	}{
		{"SessionStart on a pending row", "session-start-subagent.json", "SessionStart", subPending, true, nil},
		{"SessionStart on a live row", "session-start-subagent.json", "SessionStart", ssgLive(store.StateWorking), true, ssgOutgoing},
		{"SessionEnd on a pending row", "session-end-subagent.json", "SessionEnd", subPending, true, nil},
		{"SessionEnd on a live row", "session-end-subagent.json", "SessionEnd", ssgLive(store.StateWaiting), true, ssgOutgoing},
		{"SessionStart for an id with no row", "session-start-subagent.json", "SessionStart", noRow, false, nil},
		{"SessionEnd for an id with no row", "session-end-subagent.json", "SessionEnd", noRow, false, nil},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "sub-lifecycle-" + strconv.Itoa(i)
			st, dbPath := tc.seed(t, id)
			payload, sessionID, _ := subPayload(t, tc.fixture, nil)
			parent := hookParent{PID: ssgFreshPane.PanePID, Start: ssgFreshPane.PaneStarttime, Name: "claude"}
			var prior apitest.SpawnColumns
			var priorHistory []ssgArchived
			if tc.hasRow {
				parent = agentParent(t, st, id)
				var err error
				if prior, err = apitest.ReadSpawnColumns(dbPath, id); err != nil {
					t.Fatalf("ReadSpawnColumns: %v", err)
				}
				priorHistory = ssgHistory(t, dbPath, id)
			}
			before := len(readTrailLines(t, trailFile()))

			if out := fireGate(t, st, id, parent, nil, payload); out != "" {
				t.Errorf("stdout = %q; want empty", out)
			}

			assertStr(t, oneFired(t, before, id), "upsert_outcome", string(store.UpsertNoChange))
			if !tc.hasRow {
				// Decision A2: no row, no ad.hook.ignored, and none created.
				assertNoIgnored(t, before, id)
				if _, err := st.GetSpawn(id); !errors.Is(err, store.ErrSpawnNotFound) {
					t.Errorf("GetSpawn(%q) err = %v; want ErrSpawnNotFound", id, err)
				}
				return
			}
			// SR-22.9: nothing written — the live row does not end, no session archived.
			if after, err := apitest.ReadSpawnColumns(dbPath, id); err != nil || !reflect.DeepEqual(after, prior) {
				t.Errorf("row changed by a subagent %s (err %v):\n got %+v\nwant %+v", tc.event, err, after, prior)
			}
			if got := ssgHistory(t, dbPath, id); !reflect.DeepEqual(got, priorHistory) {
				t.Errorf("session_history = %+v; want %+v (unchanged)", got, priorHistory)
			}
			line := oneIgnored(t, before, id)
			assertKeySet(t, "ad.hook.ignored", line, gateIgnoredKeys)
			want := map[string]any{
				"claude_instance_id": id, "hook_event": tc.event, "reason": store.HookReasonSubagentEvent,
				"parent_pid": float64(parent.PID), "parent_command": "claude", "hook_session_id": sessionID,
				"row_session_id": tc.rowSession, "row_pane_pid": float64(ssgFreshPane.PanePID),
				"source": "ad_hook",
			}
			for k, v := range want {
				if line[k] != v {
					t.Errorf("ad.hook.ignored[%q] = %v; want %v", k, line[k], v)
				}
			}
		})
	}
}

// TestSubagentOrdinaryHookApplies: another hook with agent_id applies but
// records no session; agent_type alone applies and records it (SR-22.9).
func TestSubagentOrdinaryHookApplies(t *testing.T) {
	const outgoingPath = "/x/" + ssgOutgoing + ".jsonl"
	cases := []struct {
		name        string
		fixture     string
		seed        func(*testing.T, string) (*store.Store, string)
		wantState   string
		wantPayload bool   // the payload's session id and transcript path are recorded
		wantSession string // otherwise
		wantPath    string // otherwise
	}{
		{"PreToolUse with agent_id, row with no session", "pre-tool-use-subagent.json", subPending,
			store.StateWorking, false, "", ""},
		{"PreToolUse with agent_id, row with a session", "pre-tool-use-subagent.json", ssgLive(store.StateWaiting),
			store.StateWorking, false, ssgOutgoing, outgoingPath},
		{"SessionStart with agent_type only", "session-start-agent-type-only.json", subPending,
			store.StateWaiting, true, "", ""},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "sub-ordinary-" + strconv.Itoa(i)
			st, _ := tc.seed(t, id)
			payload, sessionID, path := subPayload(t, tc.fixture, nil)
			if tc.wantPayload {
				tc.wantSession, tc.wantPath = sessionID, path
			}
			before := len(readTrailLines(t, trailFile()))

			if out := fireGate(t, st, id, agentParent(t, st, id), nil, payload); out != "" {
				t.Errorf("stdout = %q; want empty", out)
			}

			row := mustGetSpawn(t, st, id)
			if row.State != tc.wantState {
				t.Errorf("state = %q; want %q (the hook applies)", row.State, tc.wantState)
			}
			if row.ClaudeSessionID != tc.wantSession || row.JSONLPath != tc.wantPath {
				t.Errorf("session/path = %q/%q; want %q/%q", row.ClaudeSessionID, row.JSONLPath, tc.wantSession, tc.wantPath)
			}
			assertNoIgnored(t, before, id)
			assertStr(t, oneFired(t, before, id), "upsert_outcome", string(store.UpsertUpdated))
		})
	}
}

// TestSubagentRelayedPermissionRequestRelays: a relayed PermissionRequest with
// agent_id from the row's agent still records its request, with that agent_id
// (b.146 rule 2), and answers.
func TestSubagentRelayedPermissionRequestRelays(t *testing.T) {
	const id = "sub-relay"
	st, _ := seedAgentRow(t, id, store.StateWorking)
	payload, _, _ := subPayload(t, "permission-request.json", map[string]any{"agent_id": "a1d2e3f4a5b6c7d8"})
	before := len(readTrailLines(t, trailFile()))
	hc := hookConfig(envWith(id), agentParent(t, st, id))
	hc.RelayTimeout = 10 * time.Second

	var stdout bytes.Buffer
	if err := hook.Handle(context.Background(), bytes.NewReader(payload), &stdout, st, hc, newSilentLogger()); err != nil {
		t.Fatalf("Handle: %v; want nil", err)
	}

	rows, err := st.PermissionRequestsForSpawn(id)
	if err != nil || len(rows) != 1 || rows[0].AgentID != "a1d2e3f4a5b6c7d8" {
		t.Errorf("permission requests = %+v (err %v); want 1 recorded with agent_id a1d2e3f4a5b6c7d8", rows, err)
	}
	assertDenyEnvelope(t, &stdout) // the relay's timeout answer
	assertNoIgnored(t, before, id)
}
