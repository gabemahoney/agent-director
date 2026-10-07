package store

// The store trail (SR-14, SR-18.13, SR-A-2): ad.row_mutation.committed for
// permission_requests writes, and ad.spawn.state_transition around resume's
// and reuse's writes. trail.Emit's file is fixed by TestMain's HOME
// (store_test.go); tests read only the lines added after their mark.

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
	"time"
)

// storeTrailDir is <home>/.agent-director for this test binary, set by TestMain.
var storeTrailDir string

var storeTSRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3,}Z$`)

// readStoreTrailLines parses every line of the store trail file (nil before
// the first emit).
func readStoreTrailLines(t *testing.T) []map[string]any {
	t.Helper()
	f, err := os.Open(filepath.Join(storeTrailDir, "ad-trail.jsonl"))
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

// trailEventsSince returns the event lines written after mark (TrailMark).
func trailEventsSince(t *testing.T, mark int, event string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, row := range readStoreTrailLines(t)[mark:] {
		if row["event"] == event {
			out = append(out, row)
		}
	}
	return out
}

// assertTrailStr checks row[key] is the string want.
func assertTrailStr(t *testing.T, row map[string]any, key, want string) {
	t.Helper()
	if got, ok := row[key]; !ok || got != want {
		t.Errorf("[%q] = %v (present %v); want %q", key, got, ok, want)
	}
}

// assertTrailInt checks row[key] is the number want (JSON numbers decode as float64).
func assertTrailInt(t *testing.T, row map[string]any, key string, want int) {
	t.Helper()
	if got, ok := row[key].(float64); !ok || int(got) != want {
		t.Errorf("[%q] = %v; want %d", key, row[key], want)
	}
}

// assertSpawnStateTransitionFields checks an ad.spawn.state_transition line (SR-A-2.1).
func assertSpawnStateTransitionFields(t *testing.T, row map[string]any, instanceID, priorState, newState, trigger string, softRefresh bool) {
	t.Helper()
	if ts, ok := row["ts"].(string); !ok || !storeTSRe.MatchString(ts) {
		t.Errorf("[ts] = %v; want an RFC3339Nano timestamp", row["ts"])
	}
	for key, want := range map[string]string{"event": "ad.spawn.state_transition", "source": "ad_spawn_store",
		"claude_instance_id": instanceID, "prior_state": priorState, "new_state": newState, "triggering_event_name": trigger} {
		assertTrailStr(t, row, key, want)
	}
	if v, ok := row["soft_refresh"].(bool); !ok || v != softRefresh {
		t.Errorf("[soft_refresh] = %v; want %v", row["soft_refresh"], softRefresh)
	}
}

// TestRowMutationEmit: a committed permission_requests insert or decide emits
// one ad.row_mutation.committed line carrying its writer, kind, decision and
// reason (find_missing never inserts); the same write repeated, a collision or
// a decide of a decided row, writes and emits nothing.
func TestRowMutationEmit(t *testing.T) {
	cases := []struct {
		name, writer, kind       string
		decision, reason         string // the decide's arguments
		wantDecision, wantReason any    // nil: JSON null
	}{
		{"hook insert", WriterProcessHook, "insert", "", "", nil, nil},
		{"decide insert", WriterProcessDecide, "insert", "", "", nil, nil},
		{"decide allow", WriterProcessDecide, "update", "allow", "", "allow", nil},
		{"decide deny operator", WriterProcessDecide, "update", "deny", "operator", "deny", "operator"},
		{"find_missing deny", WriterProcessFindMissing, "update", "deny", WriterProcessFindMissing, "deny", WriterProcessFindMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := openTempStore(t)
			id := "trail-emit-" + tc.name
			seedSpawnForPerm(t, s, id, "on")
			write := func() (bool, error) {
				if tc.kind == "insert" {
					err := agentPermissionRequest(s, id, tokenA, "Bash", `{}`, 0, tc.writer)
					return err == nil, err
				}
				return s.DecidePermissionRequest(id, tokenA, tc.decision, tc.reason, tc.writer)
			}
			if tc.kind == "update" {
				if err := agentPermissionRequest(s, id, tokenA, "Bash", `{}`, 0, ""); err != nil {
					t.Fatalf("seed request: %v", err)
				}
			}
			mark := TrailMark(t)
			if ok, err := write(); err != nil || !ok {
				t.Fatalf("write = %v, %v; want applied", ok, err)
			}
			lines := trailEventsSince(t, mark, "ad.row_mutation.committed")
			if len(lines) != 1 {
				t.Fatalf("row_mutation lines = %d; want 1", len(lines))
			}
			row := lines[0]
			if ts, ok := row["ts"].(string); !ok || !storeTSRe.MatchString(ts) {
				t.Errorf("[ts] = %v; want an SR-A-7.9 timestamp", row["ts"])
			}
			for key, want := range map[string]string{"source": "ad_store", "writer_process": tc.writer,
				"mutation_kind": tc.kind, "claude_instance_id": id, "request_token": tokenA, "tool_name": "Bash"} {
				assertTrailStr(t, row, key, want)
			}
			if _, ok := row["request_id"].(float64); !ok {
				t.Errorf("[request_id] = %v; want a number", row["request_id"])
			}
			for key, want := range map[string]any{"decision": tc.wantDecision, "decision_reason": tc.wantReason} {
				if v, ok := row[key]; !ok || v != want { // a nil want is a present JSON null
					t.Errorf("[%s] = %v (present %t); want %v", key, v, ok, want)
				}
			}

			mark = TrailMark(t)
			if ok, err := write(); ok || (tc.kind == "insert") != errors.Is(err, ErrRequestTokenCollision) ||
				(tc.kind == "update" && err != nil) {
				t.Errorf("repeated %s = %v, %v; want a collision, or not applied with no error", tc.kind, ok, err)
			}
			if n := len(trailEventsSince(t, mark, "ad.row_mutation.committed")); n != 0 {
				t.Errorf("repeated %s emitted %d row_mutation lines; want 0", tc.kind, n)
			}
		})
	}
}

// Fixed launch values for the resume move and the reuse reset; the store reads
// no clock, so none is asserted beyond what the write keeps.
const (
	trailResumeToken       = "0123456789abcdef"
	trailReuseToken        = "fedcba9876543210"
	trailMoveStart   int64 = 1767225600000
)

// trailSeedFinished inserts id with its agent's pane and finishes it: ended by
// the agent's hook after a SessionStart records a session, or missing by
// find-missing's mark. It returns the row as resume or reuse examines it.
func trailSeedFinished(t *testing.T, s *Store, id, state string) Spawn {
	t.Helper()
	if err := insertAgentRow(s, Spawn{ClaudeInstanceID: id, CWD: "/tmp", TmuxSessionName: "cd-trail-" + id, RelayMode: "off"}); err != nil {
		t.Fatalf("insertAgentRow(%s): %v", id, err)
	}
	if state == StateEnded {
		if err := agentSessionStart(s, id, "sess-old", "", false); err != nil {
			t.Fatalf("seed SessionStart: %v", err)
		}
		if err := agentHook(s, id, StateEnded, false, "test_seed"); err != nil {
			t.Fatalf("seed ended: %v", err)
		}
	} else if _, res, err := s.MarkMissingIfSameLife(id, mustGetSpawn(t, s, id).Snapshot); err != nil || res != CondApplied {
		t.Fatalf("seed missing: MarkMissingIfSameLife = %v, %v", res, err)
	}
	return mustGetSpawn(t, s, id)
}

// mustGetSpawn reads id's row, failing the test on error.
func mustGetSpawn(t *testing.T, s *Store, id string) Spawn {
	t.Helper()
	sp, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn(%s): %v", id, err)
	}
	return sp
}

// trailMove applies resume's move to pending on examined; it must apply.
func trailMove(t *testing.T, s *Store, examined Spawn) int64 {
	t.Helper()
	res, v, err := s.MoveToPending(examined.ClaudeInstanceID, examined.Snapshot, trailMoveStart, trailResumeToken, "/tmp/trail-sock", "")
	if err != nil || res != CondApplied {
		t.Fatalf("MoveToPending = %v, %v; want CondApplied", res, err)
	}
	return v
}

// trailReset reads id as reuse does and resets it; it must apply. It returns
// the prior life and the reset version.
func trailReset(t *testing.T, s *Store, id string) (RawLife, int64) {
	t.Helper()
	row, found, err := s.ReadForReuse(id)
	if err != nil || !found {
		t.Fatalf("ReadForReuse(%s) = found %v, %v", id, found, err)
	}
	fresh := Spawn{CWD: "/tmp", TmuxSessionName: "cd-trail-reused-" + id, RelayMode: "off",
		StartedAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), LaunchStartedAtMillis: trailMoveStart,
		Identity: LaunchIdentity{Token: trailReuseToken, Socket: "/tmp/trail-reuse-sock"}}
	res, _, v, err := s.ResetForReuse(id, row.Snapshot, fresh)
	if err != nil || res != CondApplied {
		t.Fatalf("ResetForReuse = %v, %v; want CondApplied", res, err)
	}
	return row.Life, v
}

// TestTrailResumeAndReuseWritesEmitNoStateTransition pins SR-14 and SR-18.13:
// ad.spawn.state_transition comes only from hook writes, so resume's move and
// restore and reuse's reset (its archive included) and restore, each applied,
// add no store trail line naming the row.
func TestTrailResumeAndReuseWritesEmitNoStateTransition(t *testing.T) {
	for _, write := range []string{"resume move", "resume restore", "reuse reset", "reuse restore"} {
		for _, finished := range []string{StateEnded, StateMissing} {
			t.Run(write+"/"+finished, func(t *testing.T) {
				s, _ := openTempStore(t)
				id := "trail-" + write + "-" + finished
				examined := trailSeedFinished(t, s, id, finished)
				mark := TrailMark(t)
				switch write {
				case "resume move":
					trailMove(t, s, examined)
				case "resume restore":
					moved := trailMove(t, s, examined)
					mark = TrailMark(t)
					prior := ResumePrior{State: examined.State, EndedAtText: examined.EndedAtText, PID: examined.PID,
						ProcStarttime: examined.ProcStarttime, LivenessUnverifiedSince: examined.LivenessUnverifiedSince,
						LivenessNote: examined.LivenessNote, Identity: examined.Identity}
					if res, err := s.RestoreAfterFailedResume(id, moved, prior); err != nil || res != CondApplied {
						t.Fatalf("RestoreAfterFailedResume = %v, %v; want CondApplied", res, err)
					}
				case "reuse reset":
					trailReset(t, s, id)
				case "reuse restore":
					prior, v := trailReset(t, s, id)
					mark = TrailMark(t)
					if res, err := s.RestoreAfterFailedReuse(id, v, prior, time.Now()); err != nil || res != CondApplied {
						t.Fatalf("RestoreAfterFailedReuse = %v, %v; want CondApplied", res, err)
					}
				}
				for _, row := range readStoreTrailLines(t)[mark:] {
					if row["claude_instance_id"] == id {
						t.Errorf("%s emitted %v naming the row; want no line", write, row["event"])
					}
				}
				want := StatePending
				if write == "resume restore" || write == "reuse restore" {
					want = finished
				}
				if got := mustGetSpawn(t, s, id).State; got != want {
					t.Errorf("state after %s = %q; want %q", write, got, want)
				}
			})
		}
	}
}

// TestTrailHookAfterResumeMoveOrReuseReset pins SR-14 and SR-22.9 on a row
// resume moved or reuse reset to pending: once the launch's identity write
// records the new pane, SessionStart emits one transition with prior_state
// pending; before it, neither SessionStart nor an ordinary hook applies
// (no_pane_recorded), emits one or changes the row.
func TestTrailHookAfterResumeMoveOrReuseReset(t *testing.T) {
	sessionStart := func(s *Store, id string) (HookApplied, error) {
		return fireSessionStart(s, id, agentGate("SessionStart", "sess-new"), "", false)
	}
	ordinary := func(s *Store, id string) (HookApplied, error) {
		return s.ApplyHookTransition(id, agentGate("UserPromptSubmit", ""), StateWorking, false, "UserPromptSubmit", "", false)
	}
	hooks := []struct {
		name     string
		identity bool // the launch's identity write runs first
		hook     func(*Store, string) (HookApplied, error)
	}{
		{"session start after the identity write", true, sessionStart},
		{"session start before the identity write", false, sessionStart},
		{"ordinary hook before the identity write", false, ordinary},
	}
	toPending := map[string]func(t *testing.T, s *Store, examined Spawn) (int64, string){
		"resume move": func(t *testing.T, s *Store, examined Spawn) (int64, string) {
			return trailMove(t, s, examined), trailResumeToken
		},
		"reuse reset": func(t *testing.T, s *Store, examined Spawn) (int64, string) {
			_, v := trailReset(t, s, examined.ClaudeInstanceID)
			return v, trailReuseToken
		},
	}
	for _, h := range hooks {
		for write, move := range toPending {
			for _, finished := range []string{StateEnded, StateMissing} {
				t.Run(h.name+"/"+write+"/"+finished, func(t *testing.T) {
					s, _ := openTempStore(t)
					id := "trail-hook-" + finished
					version, token := move(t, s, trailSeedFinished(t, s, id, finished))
					if h.identity {
						if err := recordPaneAt(s, id, version, token); err != nil {
							t.Fatalf("identity write: %v", err)
						}
					}
					pending, mark := mustGetSpawn(t, s, id), TrailMark(t)
					applied, err := h.hook(s, id)
					lines := TrailEventsSince(t, mark, "ad.spawn.state_transition", id)
					got := mustGetSpawn(t, s, id)
					if !h.identity {
						if err != nil || applied != (HookApplied{Reason: HookReasonNoPaneRecorded}) {
							t.Errorf("hook = %+v, %v; want not applied, no_pane_recorded", applied, err)
						}
						if len(lines) != 0 || !reflect.DeepEqual(got, pending) {
							t.Errorf("ignored hook emitted %v or changed the row:\n before %+v\n after  %+v", lines, pending, got)
						}
						return
					}
					if err != nil || !applied.Applied || len(lines) != 1 {
						t.Fatalf("SessionStart = %+v, %v with %d transition lines; want applied with 1", applied, err, len(lines))
					}
					assertSpawnStateTransitionFields(t, lines[0], id, StatePending, StateWaiting, "SessionStart", false)
					if got.State != StateWaiting || got.LaunchStartedAtMillis != 0 {
						t.Errorf("state, launch start = %q, %d; want waiting, cleared", got.State, got.LaunchStartedAtMillis)
					}
				})
			}
		}
	}
}
