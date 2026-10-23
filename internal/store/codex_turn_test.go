package store

// codex_turn_test.go — b.x72's store rules for a Codex turn: which rows a read
// may end (CodexOpenTurns, over a row's family), the guard of that end
// (EndCodexTurn), the turn a held UserPromptSubmit records, the turn a Stop or
// Interrupt records, which tool hooks may move a row to working, and a late
// tool hook racing a turn end. The path through hook.Handle and the API reads
// is internal/hook/hook_codex_turn_end_test.go's.

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const (
	cxTurnID   = "01a16668-4862-7930-b160-2863caf991d1"
	cxNextTurn = "01a16668-4862-7930-b160-000000000002"
	cxSession  = "01a16668-480f-7080-adb4-b91464140a96"
)

// cxTurnGate is the gate, and the state, the hook handler gives event
// (UserPromptSubmit, Stop, Interrupt, PreToolUse or PostToolUse) from the test
// pane's agent's main thread carrying turn ("" for none).
func cxTurnGate(event, turn string) (HookGate, string) {
	g := agentGate(event, "")
	g.CodexTurn = turn
	switch event {
	case "UserPromptSubmit":
		g.CodexTurnStart = true
	case "Stop", "Interrupt":
		g.CodexTurnEnd = true
		return g, StateWaiting
	default:
		g.CodexToolTurn = true
	}
	return g, StateWorking
}

// cxApply applies the hook of gate g, moving id's row to state.
func cxApply(t *testing.T, s *Store, id string, g HookGate, state string) (UpsertOutcome, HookApplied) {
	t.Helper()
	outcome, applied, err := s.ApplyHookTransitionResult(id, g, state, false, g.Event, "", false)
	if err != nil {
		t.Fatalf("%s: %v", g.Event, err)
	}
	return outcome, applied
}

// cxTurnHook applies event from id's own agent's main thread carrying turn
// ("" for none), as cxTurnGate gives it.
func cxTurnHook(t *testing.T, s *Store, id, event, turn string) (UpsertOutcome, HookApplied) {
	t.Helper()
	g, state := cxTurnGate(event, turn)
	return cxApply(t, s, id, g, state)
}

// cxOpenRow adds Codex row id, a child of parent ("" for none), whose agent
// reported in as session with its rollout at the returned path and began turn:
// raw working, the turn open.
func cxOpenRow(t *testing.T, s *Store, id, parent, session, turn string) (rollout string) {
	t.Helper()
	rollout = filepath.Join(t.TempDir(), "rollout-"+session+".jsonl")
	if err := insertAgentRow(s, Spawn{InstanceID: id, ParentID: parent, Agent: AgentCodex, CWD: t.TempDir(), RelayMode: "off"}); err != nil {
		t.Fatal(err)
	}
	if err := agentSessionStart(s, id, session, rollout, true); err != nil {
		t.Fatal(err)
	}
	if _, a := cxTurnHook(t, s, id, "UserPromptSubmit", turn); !a.Applied {
		t.Fatalf("%s: UserPromptSubmit = %+v; want applied", id, a)
	}
	return rollout
}

// cxTurnRow is a store holding one Codex row, cxOpenRow's, in turn cxTurnID.
func cxTurnRow(t *testing.T) (s *Store, path, id, rollout string) {
	t.Helper()
	s, path = openTempStore(t)
	id = "codex-turn"
	return s, path, id, cxOpenRow(t, s, id, "", cxSession, cxTurnID)
}

// cxTurns is id's raw codex_turn and codex_turn_ended, as quote() literals.
func cxTurns(t *testing.T, path, id string) (turn, ended string) {
	t.Helper()
	r := readRawSpawn(t, path, id, []string{"codex_turn", "codex_turn_ended"})
	return r["codex_turn"], r["codex_turn_ended"]
}

// cxQuote is turn as cxTurns reads it: NULL for "".
func cxQuote(turn string) string {
	if turn == "" {
		return "NULL"
	}
	return "'" + turn + "'"
}

// cxRawRow is every column of id's row, as quote() literals.
func cxRawRow(t *testing.T, s *Store, path, id string) map[string]string {
	t.Helper()
	return readRawSpawn(t, path, id, tableColumnNames(t, s.db, "spawns"))
}

// TestEndCodexTurnGuard (b.x72): a read's turn end applies only to the row the
// read found open, as it still is: a Codex row, raw working, whose current
// turn is the one the rollout ended, not recorded ended, with that rollout. It
// moves the row to waiting, records the turn ended, advances row_version,
// keeps last_seen_at and emits one state transition naming task_complete.
// Anything else changes nothing and emits nothing.
func TestEndCodexTurnGuard(t *testing.T) {
	for _, tc := range []struct {
		name     string
		after    func(t *testing.T, s *Store, id string) // the row's hooks after the read listed its turn
		ended    func(CodexOpenTurn) CodexOpenTurn       // the turn the read found ended
		openNext string                                  // the turn CodexOpenTurns lists afterwards; "": none
		moved    bool
	}{
		{name: "the open turn", moved: true},
		{name: "the turn moved on", after: func(t *testing.T, s *Store, id string) {
			cxTurnHook(t, s, id, "UserPromptSubmit", cxNextTurn)
		}, openNext: cxNextTurn},
		{name: "another rollout", ended: func(o CodexOpenTurn) CodexOpenTurn {
			o.JSONLPath += ".other"
			return o
		}, openNext: cxTurnID},
		{name: "already ended, working again", after: func(t *testing.T, s *Store, id string) {
			cxTurnHook(t, s, id, "Stop", cxTurnID)
			cxTurnHook(t, s, id, "PreToolUse", "")
		}},
		{name: "not working", after: func(t *testing.T, s *Store, id string) {
			cxTurnHook(t, s, id, "Stop", "")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, path, id, rollout := cxTurnRow(t)
			listed, err := s.CodexOpenTurns("")
			if want := []CodexOpenTurn{{id, cxSession, rollout, cxTurnID}}; err != nil || !reflect.DeepEqual(listed, want) {
				t.Fatalf("CodexOpenTurns = %+v, %v; want %+v", listed, err, want)
			}
			if tc.after != nil {
				tc.after(t, s, id)
			}
			open, err := s.CodexOpenTurns(id)
			if err != nil {
				t.Fatal(err)
			}
			if tc.moved {
				tc.openNext = cxTurnID
			}
			if (tc.openNext == "") != (len(open) == 0) || (len(open) == 1 && open[0].Turn != tc.openNext) || len(open) > 1 {
				t.Fatalf("CodexOpenTurns(%s) = %+v; want turn %q", id, open, tc.openNext)
			}
			ended := listed[0]
			if tc.ended != nil {
				ended = tc.ended(ended)
			}
			before, mark := cxRawRow(t, s, path, id), TrailMark(t)

			moved, err := s.EndCodexTurn(ended, DefaultLockWait)

			if err != nil || moved != tc.moved {
				t.Fatalf("EndCodexTurn = %v, %v; want %v", moved, err, tc.moved)
			}
			after, lines := cxRawRow(t, s, path, id), TrailEventsSince(t, mark, "ad.spawn.state_transition", id)
			if !tc.moved {
				if !reflect.DeepEqual(after, before) || len(lines) != 0 {
					t.Fatalf("a turn end that did not apply changed the row or emitted %d lines:\n before %v\n after  %v", len(lines), before, after)
				}
				return
			}
			if after["state"] != "'waiting'" || after["codex_turn_ended"] != "'"+cxTurnID+"'" || after["last_seen_at"] != before["last_seen_at"] ||
				after["row_version"] == before["row_version"] {
				t.Errorf("after the turn end: state %s, codex_turn_ended %s, last_seen_at %s (was %s), row_version %s (was %s); want waiting, the turn, kept, advanced",
					after["state"], after["codex_turn_ended"], after["last_seen_at"], before["last_seen_at"], after["row_version"], before["row_version"])
			}
			if len(lines) != 1 {
				t.Fatalf("%d state transition lines; want 1", len(lines))
			}
			for k, v := range map[string]string{"prior_state": StateWorking, "new_state": StateWaiting,
				"triggering_event_name": "task_complete", "cause": "codex_rollout_task_complete"} {
				assertTrailStr(t, lines[0], k, v)
			}
			if again, err := s.EndCodexTurn(ended, DefaultLockWait); err != nil || again {
				t.Errorf("a second EndCodexTurn = %v, %v; want false", again, err)
			}
			if open, err := s.CodexOpenTurns(""); err != nil || len(open) != 0 {
				t.Errorf("CodexOpenTurns after the end = %+v, %v; want none", open, err)
			}
		})
	}
}

// TestCodexLateToolHookRacingTurnEnd (b.x72): a PostToolUse of the row's turn
// reads the turn open, then the turn ends (a read's EndCodexTurn, or Stop)
// before its move to working. The move matches nothing and the hook gets the
// soft refresh it would have got without the race (UpsertUpdated, the version
// advanced, one soft-refresh line waiting to waiting), never a held report,
// so the row stays waiting with the turn recorded ended.
func TestCodexLateToolHookRacingTurnEnd(t *testing.T) {
	for _, end := range []struct {
		name string
		end  func(t *testing.T, s *Store, id, rollout string)
	}{
		{"rollout read", func(t *testing.T, s *Store, id, rollout string) {
			if moved, err := s.EndCodexTurn(CodexOpenTurn{id, cxSession, rollout, cxTurnID}, DefaultLockWait); err != nil || !moved {
				t.Fatalf("EndCodexTurn = %v, %v; want moved", moved, err)
			}
		}},
		{"stop", func(t *testing.T, s *Store, id, _ string) {
			if _, a := cxTurnHook(t, s, id, "Stop", cxTurnID); !a.Applied {
				t.Fatalf("Stop = %+v; want applied", a)
			}
		}},
	} {
		t.Run(end.name, func(t *testing.T) {
			s, path, id, rollout := cxTurnRow(t)
			ran, mark := false, 0
			var ended Spawn
			t.Cleanup(func() { s.hookBeforeUpdateForTest = nil })
			s.hookBeforeUpdateForTest = func() {
				s.hookBeforeUpdateForTest = nil // the Stop below must not re-enter
				ran = true
				end.end(t, s, id, rollout)
				ended, mark = mustGetSpawn(t, s, id), TrailMark(t)
			}

			outcome, applied := cxTurnHook(t, s, id, "PostToolUse", cxTurnID)

			if !ran {
				t.Fatal("the seam never ran between the hook's read and its UPDATE; the test proves nothing")
			}
			if outcome != UpsertUpdated || applied != (HookApplied{Applied: true}) {
				t.Errorf("PostToolUse = %v, %+v; want the soft refresh (UpsertUpdated, applied)", outcome, applied)
			}
			if got := readRawSpawn(t, path, id, []string{"state", "codex_turn_ended"}); got["state"] != "'waiting'" || got["codex_turn_ended"] != "'"+cxTurnID+"'" {
				t.Errorf("row after the race = %v; want waiting with the turn ended", got)
			}
			if after := mustGetSpawn(t, s, id); after.RowVersion != ended.RowVersion+1 {
				t.Errorf("row_version = %d; want %d, the soft refresh's advance", after.RowVersion, ended.RowVersion+1)
			}
			lines := TrailEventsSince(t, mark, "ad.spawn.state_transition", id)
			if len(lines) != 1 {
				t.Fatalf("%d state transition lines after the turn end; want 1", len(lines))
			}
			assertSpawnStateTransitionFields(t, lines[0], id, StateWaiting, StateWaiting, "PostToolUse", true)
		})
	}
}

// TestCodexHeldUserPromptSubmitRecordsTurn (b.x72, review finding 1): a
// UserPromptSubmit held by a permission request records its turn as the row's
// current turn ("" as none) in the held report's one write, under the hook's
// process check. The state is kept, idle_since cleared, and row_version
// advances only when idle_since was set or the turn changes. Once the request
// is answered and the new turn's tool hook moves the row to working, a read
// lists the new turn, so the older turn's task_complete cannot end it.
func TestCodexHeldUserPromptSubmitRecordsTurn(t *testing.T) {
	held := HookApplied{Applied: true}
	for _, tc := range []struct {
		name        string
		turn        string // the held UserPromptSubmit's
		idle        bool   // idle_since set before it
		foreign     bool   // from another process than the row's agent
		wantTurn    string // codex_turn afterwards; "": NULL
		wantBump    int64
		wantApplied HookApplied
	}{
		{name: "a new turn", turn: cxNextTurn, wantTurn: cxNextTurn, wantBump: 1, wantApplied: held},
		{name: "a new turn, idle_since set", turn: cxNextTurn, idle: true, wantTurn: cxNextTurn, wantBump: 1, wantApplied: held},
		{name: "the same turn", turn: cxTurnID, wantTurn: cxTurnID, wantApplied: held},
		{name: "the same turn, idle_since set", turn: cxTurnID, idle: true, wantTurn: cxTurnID, wantBump: 1, wantApplied: held},
		{name: "no turn", wantBump: 1, wantApplied: held},
		{name: "another process", turn: cxNextTurn, foreign: true, wantTurn: cxTurnID,
			wantApplied: HookApplied{Reason: HookReasonPIDMismatch}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, path, id, rollout := cxTurnRow(t)
			if err := agentHook(s, id, StateCheckPermission, false, "PermissionRequest"); err != nil {
				t.Fatal(err)
			}
			if err := agentPermissionRequest(s, id, tokenA, "Bash", `{}`, 0, ""); err != nil {
				t.Fatal(err)
			}
			if tc.idle {
				withRaw(t, path, func(db *sql.DB) {
					mustExec(t, db, `UPDATE spawns SET idle_since = '2026-10-01 00:00:00' WHERE instance_id = ?`, id)
				})
			}
			before, mark := mustGetSpawn(t, s, id), TrailMark(t)
			g, state := cxTurnGate("UserPromptSubmit", tc.turn)
			if tc.foreign {
				g.ParentPID++
			}

			outcome, applied := cxApply(t, s, id, g, state)

			if outcome != UpsertNoChange || applied != tc.wantApplied {
				t.Errorf("UserPromptSubmit = %v, %+v; want %v, %+v", outcome, applied, UpsertNoChange, tc.wantApplied)
			}
			after := mustGetSpawn(t, s, id)
			if after.State != StateCheckPermission || after.RowVersion != before.RowVersion+tc.wantBump || (applied.Applied && after.IdleSince != "") {
				t.Errorf("row = state %q, row_version %d, idle_since %q; want %q, %d, cleared when applied",
					after.State, after.RowVersion, after.IdleSince, StateCheckPermission, before.RowVersion+tc.wantBump)
			}
			if turn, ended := cxTurns(t, path, id); turn != cxQuote(tc.wantTurn) || ended != "NULL" {
				t.Errorf("codex_turn %s, codex_turn_ended %s; want %s, NULL", turn, ended, cxQuote(tc.wantTurn))
			}
			lines := TrailEventsSince(t, mark, "ad.spawn.state_transition", id)
			if !applied.Applied {
				if len(lines) != 0 {
					t.Errorf("%d state transition lines; want none", len(lines))
				}
				return
			}
			if len(lines) != 1 {
				t.Fatalf("%d state transition lines; want 1", len(lines))
			}
			assertSpawnStateTransitionFields(t, lines[0], id, StateCheckPermission, StateCheckPermission, "UserPromptSubmit", false)
			if tc.wantTurn != cxNextTurn {
				return
			}

			if _, err := s.DecidePermissionRequest(id, tokenA, "allow", "", ""); err != nil {
				t.Fatal(err)
			}
			if o, a := cxTurnHook(t, s, id, "PreToolUse", cxNextTurn); o != UpsertUpdated || !a.Applied {
				t.Fatalf("the new turn's PreToolUse = %v, %+v; want applied", o, a)
			}
			open, err := s.CodexOpenTurns(id)
			if want := []CodexOpenTurn{{id, cxSession, rollout, cxNextTurn}}; err != nil || !reflect.DeepEqual(open, want) {
				t.Fatalf("CodexOpenTurns = %+v, %v; want %+v", open, err, want)
			}
			if moved, err := s.EndCodexTurn(CodexOpenTurn{id, cxSession, rollout, cxTurnID}, DefaultLockWait); err != nil || moved {
				t.Errorf("EndCodexTurn of the older turn = %v, %v; want false", moved, err)
			}
			if got := mustGetSpawn(t, s, id).State; got != StateWorking {
				t.Errorf("state = %q; want working", got)
			}
		})
	}
}

// TestCodexTurnEndRecordsTurn (b.x72, round-2 suggestion 2 and round-3
// suggestion 1): a main-thread Stop or Interrupt that carries a turn moves
// the row to waiting and records that turn as codex_turn_ended. It records it
// as codex_turn too when the row has no current turn, its current turn has
// ended, or it is the current turn, so a turn Codex began with no
// UserPromptSubmit becomes current at its end; while another turn is current
// and open, codex_turn keeps it. One carrying no turn records nothing.
func TestCodexTurnEndRecordsTurn(t *testing.T) {
	for _, event := range []string{"Stop", "Interrupt"} {
		for _, tc := range []struct {
			name, turn string
			prior      string // before the hook: "ended" (cxTurnID ended by event), "none" (cxTurnID ended by event, then a UserPromptSubmit with no turn, so codex_turn is NULL and codex_turn_ended set), "": none
			wantTurn   string // codex_turn afterwards; "": NULL
			wantEnded  string // codex_turn_ended afterwards; "": NULL
		}{
			{"another turn while the current one is open", cxNextTurn, "", cxTurnID, cxNextTurn},
			{"a later turn with no UserPromptSubmit, the current one ended", cxNextTurn, "ended", cxNextTurn, cxNextTurn},
			{"a turn while none is current", cxNextTurn, "none", cxNextTurn, cxNextTurn},
			{"the current turn", cxTurnID, "", cxTurnID, cxTurnID},
			{"no turn", "", "", cxTurnID, ""},
		} {
			t.Run(event+"/"+tc.name, func(t *testing.T) {
				s, path, id, _ := cxTurnRow(t)
				switch tc.prior {
				case "ended":
					cxTurnHook(t, s, id, event, cxTurnID)
				case "none":
					cxTurnHook(t, s, id, event, cxTurnID)
					cxTurnHook(t, s, id, "UserPromptSubmit", "")
					if turn, ended := cxTurns(t, path, id); turn != "NULL" || ended != cxQuote(cxTurnID) {
						t.Fatalf("turns before the end = %s, %s; want NULL, %s", turn, ended, cxQuote(cxTurnID))
					}
				}

				outcome, applied := cxTurnHook(t, s, id, event, tc.turn)

				if outcome != UpsertUpdated || !applied.Applied {
					t.Fatalf("%s = %v, %+v; want UpsertUpdated, applied", event, outcome, applied)
				}
				if got := mustGetSpawn(t, s, id).State; got != StateWaiting {
					t.Errorf("state = %q; want waiting", got)
				}
				if turn, ended := cxTurns(t, path, id); turn != cxQuote(tc.wantTurn) || ended != cxQuote(tc.wantEnded) {
					t.Errorf("turns = %s, %s; want %s, %s", turn, ended, cxQuote(tc.wantTurn), cxQuote(tc.wantEnded))
				}
			})
		}
	}
}

// TestCodexLateTurnEndKeepsNewerTurn (b.x72, round-3 suggestion 1): T1's
// Stop or Interrupt lands after T2's UserPromptSubmit (Esc and typing racing).
// It moves the row to waiting and records T1 ended, but T2 stays the current
// turn, so T2's next tool hook moves the row back to working, not a soft
// refresh, and a read can still end T2.
func TestCodexLateTurnEndKeepsNewerTurn(t *testing.T) {
	for _, end := range []string{"Stop", "Interrupt"} {
		for _, tool := range []string{"PreToolUse", "PostToolUse"} {
			t.Run(end+"/"+tool, func(t *testing.T) {
				s, path, id, rollout := cxTurnRow(t)
				if _, a := cxTurnHook(t, s, id, "UserPromptSubmit", cxNextTurn); !a.Applied {
					t.Fatalf("T2's UserPromptSubmit = %+v; want applied", a)
				}

				if o, a := cxTurnHook(t, s, id, end, cxTurnID); o != UpsertUpdated || !a.Applied {
					t.Fatalf("T1's late %s = %v, %+v; want UpsertUpdated, applied", end, o, a)
				}
				if got := mustGetSpawn(t, s, id).State; got != StateWaiting {
					t.Errorf("state after T1's late %s = %q; want waiting", end, got)
				}
				if turn, ended := cxTurns(t, path, id); turn != cxQuote(cxNextTurn) || ended != cxQuote(cxTurnID) {
					t.Errorf("turns after T1's late %s = %s, %s; want %s, %s", end, turn, ended, cxQuote(cxNextTurn), cxQuote(cxTurnID))
				}
				mark := TrailMark(t)

				outcome, applied := cxTurnHook(t, s, id, tool, cxNextTurn)

				if outcome != UpsertUpdated || applied != (HookApplied{Applied: true}) {
					t.Errorf("T2's %s = %v, %+v; want UpsertUpdated, applied", tool, outcome, applied)
				}
				if got := mustGetSpawn(t, s, id).State; got != StateWorking {
					t.Errorf("state after T2's %s = %q; want working", tool, got)
				}
				lines := TrailEventsSince(t, mark, "ad.spawn.state_transition", id)
				if len(lines) != 1 {
					t.Fatalf("%d state transition lines; want 1", len(lines))
				}
				assertSpawnStateTransitionFields(t, lines[0], id, StateWaiting, StateWorking, tool, false)
				open, err := s.CodexOpenTurns(id)
				if want := []CodexOpenTurn{{id, cxSession, rollout, cxNextTurn}}; err != nil || !reflect.DeepEqual(open, want) {
					t.Errorf("CodexOpenTurns = %+v, %v; want %+v", open, err, want)
				}
			})
		}
	}
}

// TestCodexToolHookTurnRules (b.x72, review finding 2 and the engineer's
// NULL-turn deviation): a main-thread PreToolUse or PostToolUse moves the row
// to working only when its turn is the row's current turn (codex_turn) and
// not recorded ended. Any other is a soft refresh: the state kept, one
// soft-refresh line. A Stop or Interrupt records its turn as current once the
// current turn has ended, so after a later turn's end with no
// UserPromptSubmit the older turn's hook is late. A
// row with no current turn (a UserPromptSubmit with none, after a turn end)
// falls back to the earlier rule: the hook is late only when its turn is the
// one recorded ended. No tool hook changes either recorded turn.
func TestCodexToolHookTurnRules(t *testing.T) {
	type step func(t *testing.T, s *Store, id, rollout string)
	hook := func(event, turn string) step {
		return func(t *testing.T, s *Store, id, _ string) {
			if _, a := cxTurnHook(t, s, id, event, turn); !a.Applied {
				t.Fatalf("%s %q = %+v; want applied", event, turn, a)
			}
		}
	}
	checkPermission := func(t *testing.T, s *Store, id, _ string) {
		if err := agentHook(s, id, StateCheckPermission, false, "PermissionRequest"); err != nil {
			t.Fatal(err)
		}
	}
	readEnds := func(turn string) step {
		return func(t *testing.T, s *Store, id, rollout string) {
			if moved, err := s.EndCodexTurn(CodexOpenTurn{id, cxSession, rollout, turn}, DefaultLockWait); err != nil || !moved {
				t.Fatalf("EndCodexTurn(%s) = %v, %v; want moved", turn, moved, err)
			}
		}
	}
	noTurn := hook("UserPromptSubmit", "")
	for _, tc := range []struct {
		name  string
		setup []step // after cxTurnRow: turn cxTurnID current, raw working
		want  string // the state after the tool hook of cxTurnID
		soft  bool
		null  bool // codex_turn NULL before the tool hook: the fallback rule
	}{
		{name: "the current turn, working", want: StateWorking},
		{name: "the current turn, check_permission", setup: []step{checkPermission}, want: StateWorking},
		{name: "the current turn after its Stop", setup: []step{hook("Stop", cxTurnID)}, want: StateWaiting, soft: true},
		{name: "the current turn after its Interrupt", setup: []step{hook("Interrupt", cxTurnID)}, want: StateWaiting, soft: true},
		{name: "an older turn while the next runs", setup: []step{hook("UserPromptSubmit", cxNextTurn), checkPermission},
			want: StateCheckPermission, soft: true},
		{name: "an older turn after the next one's Stop",
			setup: []step{hook("Stop", cxTurnID), hook("UserPromptSubmit", cxNextTurn), hook("Stop", cxNextTurn)},
			want:  StateWaiting, soft: true},
		{name: "an older turn after a later turn's Stop with no UserPromptSubmit",
			setup: []step{hook("Stop", cxTurnID), hook("Stop", cxNextTurn)}, want: StateWaiting, soft: true},
		{name: "an older turn after a later turn's Interrupt with no UserPromptSubmit",
			setup: []step{hook("Interrupt", cxTurnID), hook("Interrupt", cxNextTurn)}, want: StateWaiting, soft: true},
		{name: "an errored turn after the next one's end by a read",
			setup: []step{hook("UserPromptSubmit", cxNextTurn), readEnds(cxNextTurn)}, want: StateWaiting, soft: true},
		{name: "no current turn, none ended", setup: []step{noTurn, checkPermission}, want: StateWorking, null: true},
		{name: "no current turn, another ended", setup: []step{hook("Stop", cxNextTurn), noTurn, checkPermission},
			want: StateWorking, null: true},
		{name: "no current turn, its turn ended", setup: []step{hook("Stop", cxTurnID), noTurn, checkPermission},
			want: StateCheckPermission, soft: true, null: true},
	} {
		for _, event := range []string{"PreToolUse", "PostToolUse"} {
			t.Run(tc.name+"/"+event, func(t *testing.T) {
				s, path, id, rollout := cxTurnRow(t)
				for _, st := range tc.setup {
					st(t, s, id, rollout)
				}
				before, mark := mustGetSpawn(t, s, id), TrailMark(t)
				turn, ended := cxTurns(t, path, id)
				if (turn == "NULL") != tc.null {
					t.Fatalf("codex_turn before the hook = %s; want NULL %v, or the case tests the wrong rule", turn, tc.null)
				}

				outcome, applied := cxTurnHook(t, s, id, event, cxTurnID)

				if outcome != UpsertUpdated || applied != (HookApplied{Applied: true}) {
					t.Errorf("%s = %v, %+v; want UpsertUpdated, applied", event, outcome, applied)
				}
				after := mustGetSpawn(t, s, id)
				if after.State != tc.want || after.RowVersion != before.RowVersion+1 {
					t.Errorf("row = %q at row_version %d; want %q at %d", after.State, after.RowVersion, tc.want, before.RowVersion+1)
				}
				if t2, e2 := cxTurns(t, path, id); t2 != turn || e2 != ended {
					t.Errorf("turns = %s, %s; want them kept: %s, %s", t2, e2, turn, ended)
				}
				lines := TrailEventsSince(t, mark, "ad.spawn.state_transition", id)
				if len(lines) != 1 {
					t.Fatalf("%d state transition lines; want 1", len(lines))
				}
				assertSpawnStateTransitionFields(t, lines[0], id, before.State, tc.want, event, tc.soft)
			})
		}
	}
}

// TestCodexOpenTurnsFamily (b.x72, review finding 3): CodexOpenTurns(id)
// lists the open turn of id's row and of every descendant down stored parent
// links, whose turns its family_state counts, but no sibling, ancestor or
// unrelated row; "" lists every row's. A family row whose turn is not open
// (ended by Stop) is left out.
func TestCodexOpenTurnsFamily(t *testing.T) {
	s, _ := openTempStore(t)
	rows := map[string]CodexOpenTurn{}
	for _, r := range []struct{ id, parent string }{
		{"grandparent", ""}, {"parent", "grandparent"}, {"sibling", "grandparent"},
		{"child", "parent"}, {"grandchild", "child"}, {"stopped-child", "parent"}, {"unrelated", ""},
	} {
		session, turn := cxSession+"-"+r.id, cxTurnID+"-"+r.id
		rows[r.id] = CodexOpenTurn{r.id, session, cxOpenRow(t, s, r.id, r.parent, session, turn), turn}
	}
	cxTurnHook(t, s, "stopped-child", "Stop", rows["stopped-child"].Turn)
	for _, tc := range []struct {
		id   string
		want []string
	}{
		{"parent", []string{"parent", "child", "grandchild"}},
		{"child", []string{"child", "grandchild"}},
		{"grandchild", []string{"grandchild"}},
		{"stopped-child", nil},
		{"grandparent", []string{"grandparent", "parent", "sibling", "child", "grandchild"}},
		{"", []string{"grandparent", "parent", "sibling", "child", "grandchild", "unrelated"}},
		{"no-such-row", nil},
	} {
		t.Run("id "+tc.id, func(t *testing.T) {
			var want []CodexOpenTurn
			for _, id := range tc.want {
				want = append(want, rows[id])
			}
			got, err := s.CodexOpenTurns(tc.id)
			byID := func(a, b CodexOpenTurn) int { return strings.Compare(a.InstanceID, b.InstanceID) }
			slices.SortFunc(got, byID)
			slices.SortFunc(want, byID)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("CodexOpenTurns(%q) = %+v, %v; want %+v", tc.id, got, err, want)
			}
		})
	}
}
