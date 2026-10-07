package store

// The spawns row (SR-5.1, SR-5.2, SR-10, SRD §3.2): the insert and both reads,
// the ad.spawn.state_transition line a hook write emits (SR-A-2), the
// multi-request hold, SessionStart's kept values and SetParentID. The hook
// gate itself is pinned in hook_gate_test.go.

import (
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"
)

// TestInsertPendingRoundTrip: an inserted row reads back pending with its
// fields, extra env included, through GetSpawn and ListSpawns alike; a nil
// extra env is stored '{}' and reads as an empty map; the v3 columns read as
// stored; a repeated id or a parent naming no row fails the insert; an absent
// id is ErrSpawnNotFound.
func TestInsertPendingRoundTrip(t *testing.T) {
	s, _ := openTempStore(t)
	full := Spawn{ClaudeInstanceID: "11111111-aaaa-4bbb-8ccc-000000000001", CWD: "/tmp/x", TmuxSessionName: "cd-x",
		ClaudeArgs: []string{"--model", "opus"}, RelayMode: "off", Labels: map[string]string{"role": "researcher"},
		ExtraEnv: map[string]string{"CLAUDE_CONFIG_DIR": "/home/u/.claude-alt", "EMPTY": ""}}
	bare := Spawn{ClaudeInstanceID: "bare", CWD: "/tmp", TmuxSessionName: "cd-bare", RelayMode: "on"}
	for _, sp := range []Spawn{full, bare} {
		if err := s.InsertPending(sp); err != nil {
			t.Fatalf("InsertPending(%s): %v", sp.ClaudeInstanceID, err)
		}
	}
	got, err := s.GetSpawn(full.ClaudeInstanceID)
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	if got.State != StatePending || got.CWD != full.CWD || got.TmuxSessionName != full.TmuxSessionName ||
		got.RelayMode != "off" || !reflect.DeepEqual(got.ClaudeArgs, full.ClaudeArgs) || !reflect.DeepEqual(got.Labels, full.Labels) ||
		!reflect.DeepEqual(got.ExtraEnv, full.ExtraEnv) || got.StartedAt.IsZero() || got.EndedAt != nil ||
		got.PID != 0 || got.ProcStarttime != "" || got.LivenessUnverifiedSince != "" || got.LivenessNote != "" {
		t.Errorf("GetSpawn = %+v; want the inserted pending row", got)
	}

	var raw sql.NullString
	if err := s.db.QueryRow(`SELECT extra_env FROM spawns WHERE claude_instance_id = 'bare'`).Scan(&raw); err != nil || raw.String != "{}" {
		t.Errorf("nil extra_env stored as %+v, %v; want '{}'", raw, err)
	}
	if b := mustGetSpawn(t, s, "bare"); b.ExtraEnv == nil || len(b.ExtraEnv) != 0 {
		t.Errorf("nil extra_env read as %#v; want an empty non-nil map", b.ExtraEnv)
	}
	if _, err := s.db.Exec(`UPDATE spawns SET pid = 4242, proc_starttime = '123', liveness_unverified_since = '2026-09-20T00:00:00Z',
		liveness_note = 'note', extra_env = '{"A":"1"}' WHERE claude_instance_id = 'bare'`); err != nil {
		t.Fatalf("populate v3 columns: %v", err)
	}
	if b := mustGetSpawn(t, s, "bare"); b.PID != 4242 || b.ProcStarttime != "123" || b.LivenessUnverifiedSince != "2026-09-20T00:00:00Z" ||
		b.LivenessNote != "note" || !reflect.DeepEqual(b.ExtraEnv, map[string]string{"A": "1"}) {
		t.Errorf("populated v3 columns read as %+v", b)
	}
	listed, err := s.ListSpawns(ListFilters{})
	if err != nil || len(listed) != 2 {
		t.Fatalf("ListSpawns = %d rows, %v; want 2", len(listed), err)
	}
	for _, l := range listed {
		if g := mustGetSpawn(t, s, l.ClaudeInstanceID); !reflect.DeepEqual(l, g) {
			t.Errorf("ListSpawns %+v differs from GetSpawn %+v", l, g)
		}
	}

	if err := s.InsertPending(bare); err == nil {
		t.Error("a second insert of the same id succeeded; want a collision")
	}
	if err := s.InsertPending(Spawn{ClaudeInstanceID: "orphan", ParentID: "no-such-parent", CWD: "/tmp",
		TmuxSessionName: "cd-o", RelayMode: "off"}); err == nil {
		t.Error("an insert naming no parent row succeeded; want the foreign key refused")
	}
	if _, err := s.GetSpawn("absent"); !errors.Is(err, ErrSpawnNotFound) {
		t.Errorf("GetSpawn(absent) err = %v; want ErrSpawnNotFound", err)
	}
	if _, err := s.GetSpawnState("absent"); !errors.Is(err, ErrSpawnNotFound) {
		t.Errorf("GetSpawnState(absent) err = %v; want ErrSpawnNotFound", err)
	}
}

// TestHookTransitionTrailLine: each applied hook write emits one
// ad.spawn.state_transition line with its prior and new state, trigger and
// soft_refresh, a same-state write included; the ended transition sets
// ended_at and a later one clears it; a write the gate refuses, or to an
// absent row, emits none and changes nothing (SR-A-2.1, SR-A-2.2, SRD §3.2).
func TestHookTransitionTrailLine(t *testing.T) {
	s, _ := openTempStore(t)
	const id = "trail-transitions"
	if err := insertAgentRow(s, Spawn{ClaudeInstanceID: id, CWD: "/tmp", TmuxSessionName: "cd-tt", RelayMode: "off"}); err != nil {
		t.Fatalf("insertAgentRow: %v", err)
	}
	steps := []struct {
		trigger, to string
		soft        bool
		prior, want string
	}{
		{"Stop", StateWaiting, false, StatePending, StateWaiting},
		{"Stop", StateWaiting, false, StateWaiting, StateWaiting},
		{"PreToolUse", "", true, StateWaiting, StateWaiting},
		{"SessionEnd", StateEnded, false, StateWaiting, StateEnded},
		{"Stop", StateWaiting, false, StateEnded, StateWaiting},
	}
	for _, st := range steps {
		if st.prior == StateEnded {
			// Another process carrying the id cannot move the ended row (SR-22.9).
			ended, mark := mustGetSpawn(t, s, id), TrailMark(t)
			applied, err := s.ApplyHookTransition(id, foreignGate(st.trigger, ""), st.to, false, st.trigger, "", false)
			if err != nil || applied != (HookApplied{Reason: HookReasonPIDMismatch}) || len(TrailEventsSince(t, mark, "ad.spawn.state_transition", id)) != 0 {
				t.Errorf("foreign hook on the ended row = %+v, %v; want pid_mismatch and no line", applied, err)
			}
			if got := mustGetSpawn(t, s, id); got.State != StateEnded || got.EndedAtText != ended.EndedAtText {
				t.Errorf("foreign hook changed the ended row to %q, %q", got.State, got.EndedAtText)
			}
		}
		mark := TrailMark(t)
		if err := agentHook(s, id, st.to, st.soft, st.trigger); err != nil {
			t.Fatalf("%s: %v", st.trigger, err)
		}
		lines := TrailEventsSince(t, mark, "ad.spawn.state_transition", id)
		if len(lines) != 1 {
			t.Fatalf("%s %s→%s: %d transition lines; want 1", st.trigger, st.prior, st.want, len(lines))
		}
		assertSpawnStateTransitionFields(t, lines[0], id, st.prior, st.want, st.trigger, st.soft)
		got := mustGetSpawn(t, s, id)
		if got.State != st.want || (got.EndedAt != nil) != (st.want == StateEnded) {
			t.Errorf("after %s: state %q, ended_at %v; want %q, set only when ended", st.trigger, got.State, got.EndedAt, st.want)
		}
		if got.EndedAt != nil && time.Since(*got.EndedAt) > 5*time.Second {
			t.Errorf("ended_at = %v; want now", got.EndedAt)
		}
	}

	mark := TrailMark(t)
	if applied, err := s.ApplyHookTransition("ghost", agentGate("Stop", ""), StateWaiting, false, "Stop", "", false); err != nil || applied != (HookApplied{}) {
		t.Errorf("transition on an absent row = %+v, %v; want a silent no-op", applied, err)
	}
	gate := agentGate("SessionStart", "session-x")
	gate.SessionStart = true
	if applied, changed, err := s.RecordSessionStartIdentity("ghost", gate, "/x/ghost.jsonl", true); err != nil || changed || applied != (HookApplied{}) {
		t.Errorf("SessionStart on an absent row = %+v, %v, %v; want a silent no-op", applied, changed, err)
	}
	if n := len(trailEventsSince(t, mark, "ad.spawn.state_transition")); n != 0 {
		t.Errorf("absent-row writes emitted %d transition lines; want 0", n)
	}
}

// TestStateMachineMultiRowRetention pins SR-5.1/SR-5.2: a row with several
// open requests stays check_permission, with a no-op transition line, until
// the last is decided; then the working transition applies.
func TestStateMachineMultiRowRetention(t *testing.T) {
	s, _ := openTempStore(t)
	const id = "sm-multi-row"
	seedSpawnForPerm(t, s, id, "on")
	if err := agentHook(s, id, StateCheckPermission, false, "test_seed"); err != nil {
		t.Fatalf("to check_permission: %v", err)
	}
	for _, tok := range []string{tokenA, tokenB} {
		if err := agentPermissionRequest(s, id, tok, "Bash", `{"cmd":"ls"}`, 0, ""); err != nil {
			t.Fatalf("insert %s: %v", tok, err)
		}
	}
	for _, tok := range []string{tokenA, tokenB} {
		if _, err := s.DecidePermissionRequest(id, tok, "allow", "", ""); err != nil {
			t.Fatalf("decide %s: %v", tok, err)
		}
		want := StateCheckPermission // held while tokenB is open
		if tok == tokenB {
			want = StateWorking
		}
		mark := TrailMark(t)
		if err := agentHook(s, id, StateWorking, false, "test_seed"); err != nil {
			t.Fatalf("working after deciding %s: %v", tok, err)
		}
		lines := TrailEventsSince(t, mark, "ad.spawn.state_transition", id)
		if len(lines) != 1 {
			t.Fatalf("after deciding %s: %d transition lines; want 1", tok, len(lines))
		}
		assertSpawnStateTransitionFields(t, lines[0], id, StateCheckPermission, want, "test_seed", false)
		if state, err := s.GetSpawnState(id); err != nil || state != want {
			t.Errorf("after deciding %s: state %q, %v; want %q", tok, state, err, want)
		}
	}
}

// TestRecordSessionStartIdentity: SessionStart records the session id and
// path, and the pane process as pid and proc_starttime (SR-22.9); a later one
// reporting no id and no path keeps the recorded ones.
func TestRecordSessionStartIdentity(t *testing.T) {
	s, _ := openTempStore(t)
	const id = "ss-identity"
	if err := insertAgentRow(s, Spawn{ClaudeInstanceID: id, CWD: "/tmp", TmuxSessionName: "cd-ss", RelayMode: "off"}); err != nil {
		t.Fatalf("insertAgentRow: %v", err)
	}
	pane := testPane()
	for _, in := range [][2]string{{"session-abc", "/x/abc.jsonl"}, {"", ""}} {
		if err := agentSessionStart(s, id, in[0], in[1], in[1] != ""); err != nil {
			t.Fatalf("SessionStart(%q): %v", in[0], err)
		}
		if got := mustGetSpawn(t, s, id); got.State != StateWaiting || got.ClaudeSessionID != "session-abc" ||
			got.JSONLPath != "/x/abc.jsonl" || got.PID != pane.PanePID || got.ProcStarttime != pane.PaneStarttime {
			t.Errorf("after SessionStart(%q) = %+v; want waiting, session-abc, /x/abc.jsonl, the pane process", in[0], got)
		}
	}
}

// TestSetParentID: the parent is written, an empty one stored NULL, and an
// absent row is ErrSpawnNotFound (it backs apitest.SeedParentChild).
func TestSetParentID(t *testing.T) {
	s, _ := openTempStore(t)
	for _, id := range []string{"parent-1", "child-1"} {
		if err := s.InsertPending(Spawn{ClaudeInstanceID: id, CWD: "/tmp", TmuxSessionName: "cd-" + id, RelayMode: "off"}); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	for _, parent := range []string{"parent-1", ""} {
		if err := s.SetParentID("child-1", parent); err != nil {
			t.Fatalf("SetParentID(%q): %v", parent, err)
		}
		var raw sql.NullString
		if err := s.db.QueryRow(`SELECT parent_id FROM spawns WHERE claude_instance_id = 'child-1'`).Scan(&raw); err != nil ||
			raw != (sql.NullString{String: parent, Valid: parent != ""}) || mustGetSpawn(t, s, "child-1").ParentID != parent {
			t.Errorf("parent_id after SetParentID(%q) = %+v, %v", parent, raw, err)
		}
	}
	if err := s.SetParentID("does-not-exist", "parent-1"); !errors.Is(err, ErrSpawnNotFound) {
		t.Errorf("SetParentID(absent) err = %v; want ErrSpawnNotFound", err)
	}
}
