package store

// trail_emit_test.go — table-driven tests for ad.row_mutation.committed
// emission across (writer_process × mutation_kind × decision) combinations,
// and for ad.spawn.state_transition around resume's move, restore and the
// resumed agent's SessionStart.
//
// Singleton note: trail.Emit uses a process-level sync.Once whose file path
// is locked in on the first call. TestMain (store_test.go) redirects HOME via
// os.Setenv before any test runs, so the singleton resolves its file under
// <home>/.agent-director/. Individual tests capture a line-count checkpoint
// before the operation under test and assert only on lines added since that
// checkpoint.

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
)

// storeTrailDir is the trail directory (<home>/.agent-director) for this test
// binary. Set by TestMain in store_test.go before any test function runs.
var storeTrailDir string

var storeTSRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3,}Z$`)

// storeTrailFile returns the trail file path used by the singleton.
func storeTrailFile() string { return filepath.Join(storeTrailDir, "ad-trail.jsonl") }

// readStoreTrailLines parses every JSONL line from the store trail file.
// Returns nil when the file does not exist yet.
func readStoreTrailLines(t *testing.T) []map[string]any {
	t.Helper()
	f, err := os.Open(storeTrailFile())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("readStoreTrailLines: %v", err)
	}
	defer f.Close()
	var rows []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("readStoreTrailLines: unmarshal %q: %v", sc.Text(), err)
		}
		rows = append(rows, m)
	}
	if sc.Err() != nil {
		t.Fatalf("readStoreTrailLines: scan: %v", sc.Err())
	}
	return rows
}

// rowMutationsAt reads trail lines added after prevCount and returns the
// ad.row_mutation.committed lines among them.
func rowMutationsAt(t *testing.T, prevCount int) []map[string]any {
	t.Helper()
	all := readStoreTrailLines(t)
	var out []map[string]any
	for _, row := range all[prevCount:] {
		if row["event"] == "ad.row_mutation.committed" {
			out = append(out, row)
		}
	}
	return out
}

// assertRowMutationFields checks all required top-level fields of an
// ad.row_mutation.committed event.
func assertRowMutationFields(t *testing.T, row map[string]any, writerProcess, mutationKind string, wantDecision, wantReason any) {
	t.Helper()

	ts, ok := row["ts"].(string)
	if !ok || !storeTSRe.MatchString(ts) {
		t.Errorf("[ts] = %v; want SR-A-7.9 timestamp", row["ts"])
	}
	assertTrailStr(t, row, "event", "ad.row_mutation.committed")
	assertTrailStr(t, row, "source", "ad_store")
	assertTrailStr(t, row, "writer_process", writerProcess)
	assertTrailStr(t, row, "mutation_kind", mutationKind)

	if v, ok := row["claude_instance_id"].(string); !ok || v == "" {
		t.Errorf("[claude_instance_id] = %v; want non-empty string", row["claude_instance_id"])
	}
	if v, ok := row["request_token"].(string); !ok || v == "" {
		t.Errorf("[request_token] = %v; want non-empty string", row["request_token"])
	}
	if _, ok := row["request_id"].(float64); !ok {
		t.Errorf("[request_id] = %v (%T); want float64", row["request_id"], row["request_id"])
	}
	if v, ok := row["tool_name"].(string); !ok || v == "" {
		t.Errorf("[tool_name] = %v; want non-empty string", row["tool_name"])
	}

	assertTrailNullable(t, row, "decision", wantDecision)
	assertTrailNullable(t, row, "decision_reason", wantReason)
}

// assertTrailStr checks row[key] == want.
func assertTrailStr(t *testing.T, row map[string]any, key, want string) {
	t.Helper()
	got, ok := row[key]
	if !ok {
		t.Errorf("field %q missing", key)
		return
	}
	if got != want {
		t.Errorf("[%q] = %v; want %q", key, got, want)
	}
}

// assertTrailNullable checks row[key]: nil means JSON null; a string means
// a non-null string value must match.
func assertTrailNullable(t *testing.T, row map[string]any, key string, want any) {
	t.Helper()
	got, exists := row[key]
	if !exists {
		t.Errorf("field %q missing", key)
		return
	}
	if want == nil {
		if got != nil {
			t.Errorf("[%q] = %v; want null", key, got)
		}
		return
	}
	wantStr, ok := want.(string)
	if !ok {
		t.Fatalf("assertTrailNullable: want must be nil or string; got %T", want)
	}
	if got != wantStr {
		t.Errorf("[%q] = %v; want %q", key, got, wantStr)
	}
}

// TestRowMutationEmit is the primary parameterized test covering representative
// (writer_process × mutation_kind × decision) cells.
//
// Skipped cells:
//   - find_missing + insert: the find_missing reconciler never calls UpsertOpenPermissionRequest.
func TestRowMutationEmit(t *testing.T) {
	type tc struct {
		name          string
		writerProcess string
		mutationKind  string // "insert" or "update"
		wantDecision  any    // nil for insert; "allow"/"deny" for update
		wantReason    any    // nil or string
		callDecision  string // passed to DecidePermissionRequest (update path only)
		callReason    string // passed as reason to DecidePermissionRequest
		skipReason    string
	}
	cases := []tc{
		{
			name: "hook_insert", writerProcess: WriterProcessHook,
			mutationKind: "insert", wantDecision: nil, wantReason: nil,
		},
		{
			name: "decide_insert", writerProcess: WriterProcessDecide,
			mutationKind: "insert", wantDecision: nil, wantReason: nil,
		},
		{
			// find_missing reconciler only decides existing open rows — it never
			// calls UpsertOpenPermissionRequest.
			name: "find_missing_insert", writerProcess: WriterProcessFindMissing,
			mutationKind: "insert",
			skipReason:   "find_missing reconciler never calls UpsertOpenPermissionRequest",
		},
		{
			name: "decide_update_allow", writerProcess: WriterProcessDecide,
			mutationKind: "update", wantDecision: "allow", wantReason: nil,
			callDecision: "allow", callReason: "",
		},
		{
			name: "decide_update_deny_operator", writerProcess: WriterProcessDecide,
			mutationKind: "update", wantDecision: "deny", wantReason: "operator",
			callDecision: "deny", callReason: "operator",
		},
		{
			name: "find_missing_update_deny", writerProcess: WriterProcessFindMissing,
			mutationKind: "update", wantDecision: "deny", wantReason: WriterProcessFindMissing,
			callDecision: "deny", callReason: WriterProcessFindMissing,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if tc.skipReason != "" {
				t.Skip(tc.skipReason)
			}

			s := openTestStore(t)
			id := "trail-emit-" + tc.name
			seedSpawnForPerm(t, s, id, "on")

			var before int
			if tc.mutationKind == "insert" {
				before = len(readStoreTrailLines(t))
				if err := agentPermissionRequest(s, id, tokenA, "Bash", `{}`, 0, tc.writerProcess); err != nil {
					t.Fatalf("UpsertOpenPermissionRequest: %v", err)
				}
			} else {
				// Setup: insert a row first (emits its own trail event — not under test).
				if err := agentPermissionRequest(s, id, tokenA, "Bash", `{}`, 0, ""); err != nil {
					t.Fatalf("setup UpsertOpenPermissionRequest: %v", err)
				}
				// Capture checkpoint after setup so only the update event is counted.
				before = len(readStoreTrailLines(t))
				updated, err := s.DecidePermissionRequest(id, tokenA, tc.callDecision, tc.callReason, tc.writerProcess)
				if err != nil {
					t.Fatalf("DecidePermissionRequest: %v", err)
				}
				if !updated {
					t.Fatalf("DecidePermissionRequest returned updated=false; want true")
				}
			}

			mutations := rowMutationsAt(t, before)
			if len(mutations) != 1 {
				t.Fatalf("want 1 ad.row_mutation.committed after checkpoint %d; got %d", before, len(mutations))
			}
			assertRowMutationFields(t, mutations[0], tc.writerProcess, tc.mutationKind, tc.wantDecision, tc.wantReason)
		})
	}
}

// TestRowMutationNoEmitOnFailedWrite asserts that a UNIQUE constraint
// collision (failed write) emits ZERO ad.row_mutation.committed lines.
func TestRowMutationNoEmitOnFailedWrite(t *testing.T) {
	s := openTestStore(t)
	const id = "trail-no-emit-collision"
	seedSpawnForPerm(t, s, id, "on")

	// Initial successful insert — its trail event is not under test.
	if err := agentPermissionRequest(s, id, tokenA, "Bash", `{}`, 0, WriterProcessHook); err != nil {
		t.Fatalf("initial upsert: %v", err)
	}

	before := len(readStoreTrailLines(t))

	// Duplicate insert → UNIQUE constraint collision → rollback, no emit.
	err := agentPermissionRequest(s, id, tokenA, "Bash", `{}`, 0, WriterProcessHook)
	if !errors.Is(err, ErrRequestTokenCollision) {
		t.Fatalf("collision upsert: err = %v; want ErrRequestTokenCollision", err)
	}
	if got := rowMutationsAt(t, before); len(got) != 0 {
		t.Errorf("failed write emitted %d ad.row_mutation.committed; want 0", len(got))
	}
}

// TestRowMutationNoEmitOnAlreadyDecided asserts that a second
// DecidePermissionRequest call on an already-decided row (sql.ErrNoRows from
// RETURNING, i.e. RowsAffected==0) emits ZERO ad.row_mutation.committed lines.
func TestRowMutationNoEmitOnAlreadyDecided(t *testing.T) {
	s := openTestStore(t)
	const id = "trail-no-emit-already-decided"
	seedSpawnForPerm(t, s, id, "on")

	if err := agentPermissionRequest(s, id, tokenA, "Bash", `{}`, 0, ""); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// First decide succeeds and emits a trail event — not under test.
	if _, err := s.DecidePermissionRequest(id, tokenA, "allow", "", WriterProcessDecide); err != nil {
		t.Fatalf("first decide: %v", err)
	}

	before := len(readStoreTrailLines(t))

	// Second decide → sql.ErrNoRows (RETURNING returns zero rows) → must NOT emit.
	updated, err := s.DecidePermissionRequest(id, tokenA, "deny", "", WriterProcessDecide)
	if err != nil {
		t.Fatalf("second decide: %v", err)
	}
	if updated {
		t.Fatalf("second decide returned updated=true; want false (row already decided)")
	}
	if got := rowMutationsAt(t, before); len(got) != 0 {
		t.Errorf("already-decided path emitted %d ad.row_mutation.committed; want 0", len(got))
	}
}

// trailResumeToken is a well-formed launch token (SR-3.5) for the move.
const trailResumeToken = "0123456789abcdef"

// trailMoveStart is a fixed launch start (Unix ms) for the move; no test
// asserts it, so a constant keeps the move deterministic.
const trailMoveStart int64 = 1767225600000

// trailSeedFinished inserts a pending row and finishes it as state, then
// returns the row as resume examines it. A missing row is marked by
// MarkSpawnMissing, a non-hook write; an ended row can only be reached by a
// hook transition today, so its seed is the agent's own hook, after the
// create's identity write records its pane (SR-22.9: no hook applies to a row
// with no pane).
func trailSeedFinished(t *testing.T, s *Store, id, state string) Spawn {
	t.Helper()
	if err := insertAgentRow(s, Spawn{
		ClaudeInstanceID: id, CWD: "/tmp", TmuxSessionName: "cd-trail-" + id, RelayMode: "off",
	}); err != nil {
		t.Fatalf("insertAgentRow(%s): %v", id, err)
	}
	switch state {
	case StateEnded:
		if err := agentHook(s, id, StateEnded, false, "test_seed"); err != nil {
			t.Fatalf("seed ended: ApplyHookTransition(%s): %v", id, err)
		}
	case StateMissing:
		prior, err := s.MarkSpawnMissing(id)
		if err != nil || prior != StatePending {
			t.Fatalf("seed missing: MarkSpawnMissing(%s) = %q, %v; want %q, nil", id, prior, err, StatePending)
		}
	default:
		t.Fatalf("trailSeedFinished: state %q is not ended or missing", state)
	}
	sp, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn(%s): %v", id, err)
	}
	if sp.State != state {
		t.Fatalf("seeded state = %q; want %q", sp.State, state)
	}
	return sp
}

// trailMove applies resume's move to pending on the examined row and fails
// the test unless it applied.
func trailMove(t *testing.T, s *Store, examined Spawn) int64 {
	t.Helper()
	res, movedVersion, err := s.MoveToPending(examined.ClaudeInstanceID, examined.Snapshot,
		trailMoveStart, trailResumeToken, "/tmp/trail-sock", "")
	if err != nil || res != CondApplied {
		t.Fatalf("MoveToPending(%s) = %v, %d, %v; want CondApplied", examined.ClaudeInstanceID, res, movedVersion, err)
	}
	return movedVersion
}

// TestTrailResumeWritesEmitNoStateTransition pins SR-14 and SR-18.13:
// ad.spawn.state_transition comes only from hook-driven writes, so resume's
// move to pending and its restore, each applied, add none to the store trail
// (and no store trail line of any kind names the row).
func TestTrailResumeWritesEmitNoStateTransition(t *testing.T) {
	for _, write := range []string{"move", "restore"} {
		for _, finished := range []string{StateEnded, StateMissing} {
			write, finished := write, finished
			t.Run(write+"_"+finished, func(t *testing.T) {
				s := openTestStore(t)
				id := "trail-resume-" + write + "-" + finished
				examined := trailSeedFinished(t, s, id, finished)

				var before int
				switch write {
				case "move":
					before = len(readStoreTrailLines(t))
					trailMove(t, s, examined)
				case "restore":
					movedVersion := trailMove(t, s, examined)
					before = len(readStoreTrailLines(t))
					prior := ResumePrior{
						State:                   examined.State,
						EndedAtText:             examined.EndedAtText,
						PID:                     examined.PID,
						ProcStarttime:           examined.ProcStarttime,
						LivenessUnverifiedSince: examined.LivenessUnverifiedSince,
						LivenessNote:            examined.LivenessNote,
						Identity:                examined.Identity,
					}
					res, err := s.RestoreAfterFailedResume(id, movedVersion, prior)
					if err != nil || res != CondApplied {
						t.Fatalf("RestoreAfterFailedResume = %v, %v; want CondApplied", res, err)
					}
				}

				if got := spawnStateTransitionLines(t, before); len(got) != 0 {
					t.Errorf("applied %s emitted %d ad.spawn.state_transition; want 0: %v", write, len(got), got)
				}
				for _, row := range readStoreTrailLines(t)[before:] {
					if row["claude_instance_id"] == id {
						t.Errorf("applied %s emitted trail event %v naming the row; want none", write, row["event"])
					}
				}

				// The write did apply: the row is where the write put it.
				got, err := s.GetSpawn(id)
				if err != nil {
					t.Fatalf("GetSpawn: %v", err)
				}
				wantState := StatePending
				if write == "restore" {
					wantState = finished
				}
				if got.State != wantState {
					t.Errorf("state after %s = %q; want %q", write, got.State, wantState)
				}
			})
		}
	}
}

// TestTrailSessionStartAfterResumeMovePriorPending pins SR-14's SessionStart
// part: on a resumed row the move made the row pending, so SessionStart's
// ad.spawn.state_transition records prior_state pending, not the finished
// state the row had before resume. The move clears the pane, so the resumed
// agent's SessionStart applies only after the create's identity write records
// the new pane (SR-22.9).
func TestTrailSessionStartAfterResumeMovePriorPending(t *testing.T) {
	for _, finished := range []string{StateEnded, StateMissing} {
		finished := finished
		t.Run(finished, func(t *testing.T) {
			s := openTestStore(t)
			id := "trail-sessionstart-after-move-" + finished
			examined := trailSeedFinished(t, s, id, finished)
			movedVersion := trailMove(t, s, examined)
			if err := recordPaneAt(s, id, movedVersion, trailResumeToken); err != nil {
				t.Fatalf("identity write after the move: %v", err)
			}

			before := len(readStoreTrailLines(t))
			if err := agentSessionStart(s, id, "sess-resumed", "", false); err != nil {
				t.Fatalf("SessionStart: %v", err)
			}
			lines := spawnStateTransitionLines(t, before)
			if len(lines) != 1 {
				t.Fatalf("want 1 ad.spawn.state_transition after SessionStart; got %d: %v", len(lines), lines)
			}
			assertSpawnStateTransitionFields(t, lines[0], id, StatePending, StateWaiting, "SessionStart", false)

			got, err := s.GetSpawn(id)
			if err != nil {
				t.Fatalf("GetSpawn: %v", err)
			}
			if got.State != StateWaiting {
				t.Errorf("state after SessionStart = %q; want waiting", got.State)
			}
			if got.LaunchStartedAtMillis != 0 {
				t.Errorf("launch_started_at after SessionStart = %d; want cleared (0)", got.LaunchStartedAtMillis)
			}
		})
	}
}

// TestTrailSessionStartAfterResumeMoveNoPaneIgnored pins SR-22.9's inverse: a
// SessionStart after the move and before the identity write finds no pane, so
// it is not applied (no_pane_recorded), emits no ad.spawn.state_transition and
// leaves the row pending.
func TestTrailSessionStartAfterResumeMoveNoPaneIgnored(t *testing.T) {
	for _, finished := range []string{StateEnded, StateMissing} {
		finished := finished
		t.Run(finished, func(t *testing.T) {
			s := openTestStore(t)
			id := "trail-sessionstart-no-pane-" + finished
			trailMove(t, s, trailSeedFinished(t, s, id, finished))
			moved, err := s.GetSpawn(id)
			if err != nil {
				t.Fatalf("GetSpawn after the move: %v", err)
			}

			before := len(readStoreTrailLines(t))
			applied, err := fireSessionStart(s, id, agentGate("SessionStart", "sess-resumed"), "", false)
			if err != nil {
				t.Fatalf("SessionStart: %v", err)
			}
			if want := (HookApplied{Reason: HookReasonNoPaneRecorded}); applied != want {
				t.Errorf("SessionStart = %+v; want %+v", applied, want)
			}
			if got := spawnStateTransitionLines(t, before); len(got) != 0 {
				t.Errorf("ignored SessionStart emitted %d ad.spawn.state_transition; want 0: %v", len(got), got)
			}
			got, err := s.GetSpawn(id)
			if err != nil {
				t.Fatalf("GetSpawn: %v", err)
			}
			if !reflect.DeepEqual(got, moved) {
				t.Errorf("row changed by an ignored SessionStart:\n before %+v\n after  %+v", moved, got)
			}
		})
	}
}
