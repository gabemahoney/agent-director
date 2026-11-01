package store_test

// confirmations_test.go (b.x9o): the main-field evidence the store keeps
// (confirmations.go). Every spawns write either keeps the row's state record
// byte for byte or, where it changes the raw value, interval or launch,
// records its own evidence source with its own clock; terminal moves keep a
// process record only when they acted on that process's proven exit.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// evidenceSeedMs is the time of the records stampEvidence gives a seeded row.
const evidenceSeedMs = int64(1_790_000_000_000)

// stampEvidence gives id's row applicable records made at evidenceSeedMs: a
// state record of its raw value, interval and launch from an agent event, and,
// where it records a pane process, that process alive (exited on a finished
// row). It returns the row as read after.
func stampEvidence(t *testing.T, f *v5Store, id string) store.Spawn {
	t.Helper()
	execRaw(t, f.path, `UPDATE spawns SET state_evidence = json_object('v', state, 'life', COALESCE(life_number, 0),
	    'token', launch_token, 'epoch', permission_epoch, 'at', ?, 'src', 'agent_event', 'checked', ?) WHERE instance_id = ?`,
		evidenceSeedMs, evidenceSeedMs, id)
	sp := rvExamine(t, f, id)
	value := store.ProcessAlive
	if !store.IsLiveState(sp.State) {
		value = store.ProcessExited
	}
	if rec, ok := sp.ProcessEvidenceAfter(store.ProcessAttempt{PID: sp.Identity.PanePID, Start: sp.Identity.PaneStarttime, Value: value}, evidenceSeedMs); ok {
		execRaw(t, f.path, `UPDATE spawns SET process_evidence = ? WHERE instance_id = ?`, rec, id)
	}
	sp = rvExamine(t, f, id)
	if c := sp.StateConfirmation(store.SettleRead{}); c.Status != store.ConfirmationConfirmed {
		t.Fatalf("seeded state record does not apply: %+v; the write checks would be vacuous", c)
	}
	return sp
}

// evidenceSources is the state record source of each writer's change of the
// raw value, interval or launch: "" records none, so the new value reads
// unknown; "prior" writes back the record the failed launch's examined row
// held. A writer missing here must not change any of them.
var evidenceSources = map[string]string{
	"ApplyHookTransition": store.EvidenceAgentEvent, "ApplyHookTransitionResult": store.EvidenceAgentEvent,
	"RecordSessionStartIdentity": store.EvidenceAgentEvent, "ApplyHookWaitingIfWorking": store.EvidenceAgentEvent,
	"InsertRelayRequest": store.EvidenceAgentEvent,
	"MoveToPending":      store.EvidenceCallerCommand, "ResetForReuse": store.EvidenceCallerCommand,
	"EndHeldLaunch": store.EvidenceTmux, "MarkEndedIfSameLife": store.EvidenceProcess,
	"RepairCheckPermissionIfSameLife": store.EvidenceStore,
	"MarkMissingIfSameLife":           "",
	"RestoreAfterFailedResume":        "prior", "RestoreAfterFailedReuse": "prior",
}

// TestEvidenceEveryWrite runs every spawns write (rowVersionWrites) over a row
// holding applicable records. A write that leaves the raw value, interval
// and launch as they were keeps the state record byte for byte, so a soft
// refresh, a same-value hook or a held transition never redates it; any
// other records its writer's source at its own time, none, or the failed
// launch's prior record. A terminal move these direct writers make proves no
// process exit, so it clears the process record; a new launch clears it; a
// restore brings back the prior launch's; every other write between live
// states keeps it.
func TestEvidenceEveryWrite(t *testing.T) {
	for _, c := range rowVersionWrites() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newV5Store(t)
			id := f.seed(c.state, c.session, append(rowVersionSeed(), c.opts...)...)
			orig := stampEvidence(t, f, id)
			if c.setup != nil {
				c.setup(t, f, id)
			}
			before := rvExamine(t, f, id)
			from := time.Now().UnixMilli()
			c.write(t, f, id)
			to := time.Now().UnixMilli()
			after := rvExamine(t, f, id)
			writer, _, _ := strings.Cut(c.name, "/")

			got := after.StateConfirmation(store.SettleRead{})
			switch src, known := evidenceSources[writer]; {
			case after.State == before.State && after.PermissionEpoch == before.PermissionEpoch &&
				after.LifeNumber == before.LifeNumber && after.Identity.Token == before.Identity.Token:
				if after.StateEvidence != before.StateEvidence {
					t.Errorf("state record %s -> %s; want it kept by a write that changes no value, interval or launch", before.StateEvidence, after.StateEvidence)
				}
			case !known:
				t.Errorf("%s changed the state's value, interval or launch but has no evidence source", writer)
			case src == "":
				if got != store.UnknownConfirmation(store.ReasonNoEvidence) {
					t.Errorf("state record %+v; want unknown: %s names no evidence", got, writer)
				}
			case src == "prior":
				if want := orig.StateConfirmation(store.SettleRead{}); got != want {
					t.Errorf("restored state record %+v; want the prior launch's %+v", got, want)
				}
			default:
				if got.Status != store.ConfirmationConfirmed || got.Source != src || got.ConfirmedAtMillis < from ||
					got.ConfirmedAtMillis > to || got.CheckedAtMillis != got.ConfirmedAtMillis || got.Reason != "" {
					t.Errorf("state record %+v; want confirmed by %s at the write's own time in [%d, %d]", got, src, from, to)
				}
			}

			live := store.IsLiveState
			switch {
			case strings.HasPrefix(writer, "Restore"):
				v, rec := after.ProcessConfirmation()
				if wv, want := orig.ProcessConfirmation(); v != wv || rec != want {
					t.Errorf("restored process record %s %+v; want the prior launch's %s %+v", v, rec, wv, want)
				}
			case live(before.State) && !live(after.State), !live(before.State) && after.State == store.StatePending:
				if after.ProcessEvidence != "" {
					t.Errorf("process record %s kept across %s -> %s", after.ProcessEvidence, before.State, after.State)
				}
			case live(before.State) && live(after.State):
				if after.ProcessEvidence != before.ProcessEvidence {
					t.Errorf("process record %s -> %s; want it kept", before.ProcessEvidence, after.ProcessEvidence)
				}
			}
		})
	}
}

// TestStateEvidenceFollowsTheSettledValue: while a raw change settles, a read
// reports the settled value with that value's own record, moved with it to
// settled_evidence; once settled, the raw value's. A later interval of the
// same value is stamped afresh, never with the earlier interval's time, and an
// immediate write leaves no settled record behind. The writer settles at once
// (no settle time) and the reader with settleT, so each hook's old raw value
// becomes the settled one; each new raw record's times are pinned so the
// records are told apart without waiting for the clock.
func TestStateEvidenceFollowsTheSettledValue(t *testing.T) {
	f := newV5Store(t)
	f.s.SetSettleMs(0)
	id := f.seed(store.StateWorking, "", rowVersionSeed()...)
	hook := func(event string, pinMs int64) store.Confirmation {
		t.Helper()
		if got := storefix.ApplyAgentHook(t, f.s, id, event, ""); !got.Applied {
			t.Fatalf("%s = %+v; want applied", event, got)
		}
		if pinMs > 0 {
			execRaw(t, f.path, `UPDATE spawns SET state_evidence = json_set(state_evidence, '$.at', ?, '$.checked', ?) WHERE instance_id = ?`, pinMs, pinMs, id)
		}
		c := rvExamine(t, f, id).StateConfirmation(store.SettleRead{})
		if c.Status != store.ConfirmationConfirmed || c.Source != store.EvidenceAgentEvent || (pinMs > 0 && c.ConfirmedAtMillis != pinMs) {
			t.Fatalf("raw record after %s = %+v; want confirmed by agent_event", event, c)
		}
		return c
	}
	read := func(nowMs int64) (string, store.Confirmation) {
		t.Helper()
		r := store.SettleRead{NowMs: nowMs, SettleMs: settleT}
		sp, err := f.s.GetSpawnSettled(id, r)
		if err != nil {
			t.Fatal(err)
		}
		return sp.ReportedState, sp.StateConfirmation(r)
	}

	waiting := hook("Stop", 1000)
	working := hook("PreToolUse", 2000)
	_, stamp := settleOf(t, f, id)
	if got, c := read(stamp); got != store.StateWaiting || c != waiting {
		t.Errorf("settling read = %s %+v; want waiting with its own record %+v", got, c, waiting)
	}
	if got, c := read(stamp + settleT); got != store.StateWorking || c != working {
		t.Errorf("settled read = %s %+v; want working with its record %+v", got, c, working)
	}
	again := hook("Stop", 0)
	_, stamp = settleOf(t, f, id)
	if got, c := read(stamp + settleT); got != store.StateWaiting || c != again || c == waiting {
		t.Errorf("a new waiting interval reads %s %+v; want its own record %+v, never the earlier %+v", got, c, again, waiting)
	}
	if got, c := read(stamp); got != store.StateWorking || c != working {
		t.Errorf("settling read = %s %+v; want working with its record %+v", got, c, working)
	}
	if prior, res, err := f.s.MarkMissingIfSameLife(id, rvExamine(t, f, id).Snapshot); err != nil || res != store.CondApplied || prior != store.StateWaiting {
		t.Fatalf("MarkMissingIfSameLife = %q, %v, %v", prior, res, err)
	}
	if sp := rvExamine(t, f, id); sp.SettledStateEvidence != "" {
		t.Errorf("settled record %s survived an immediate write", sp.SettledStateEvidence)
	}
}

// TestTerminalMoveProcessEvidence: the shared checks' guarded terminal moves
// store, in their own statement, the exited record of exactly the process
// whose proven exit they acted on, bound to the row's life and launch at the
// judgement's time, and report recorded only then. A move naming no process
// the row records, a pid-only or zero exit, a Cursor row and a move that did
// not apply store or report none; a move that fails rolls its state, family
// and process record back together.
func TestTerminalMoveProcessEvidence(t *testing.T) {
	const at = int64(1_790_000_123_456)
	pane := fullIdentity()
	proven := store.ProcessExit{PID: pane.PanePID, Start: pane.PaneStarttime, AtMillis: at}
	moves := map[string]func(s *store.Store, sp store.Spawn, exit store.ProcessExit) (store.CondResult, bool, error){
		"RecoveryWrites.MarkMissingIfSameLifeFrom": func(s *store.Store, sp store.Spawn, exit store.ProcessExit) (store.CondResult, bool, error) {
			_, res, recorded, err := s.RecoveryWrites(sp).MarkMissingIfSameLifeFrom(sp.InstanceID, sp.Snapshot, store.EvidenceProcess, exit)
			return res, recorded, err
		},
		"RecoveryWrites.MarkEndedIfSameLifeFrom": func(s *store.Store, sp store.Spawn, exit store.ProcessExit) (store.CondResult, bool, error) {
			_, res, recorded, err := s.RecoveryWrites(sp).MarkEndedIfSameLifeFrom(sp.InstanceID, sp.Snapshot, exit)
			return res, recorded, err
		},
		"Store.MarkEndedIfSameLifeFrom": func(s *store.Store, sp store.Spawn, exit store.ProcessExit) (store.CondResult, bool, error) {
			_, res, recorded, err := s.MarkEndedIfSameLifeFrom(sp.InstanceID, sp.Snapshot, exit)
			return res, recorded, err
		},
	}
	for name, move := range moves {
		for _, tc := range []struct {
			name    string
			agent   string
			exit    store.ProcessExit
			stale   bool // another write advanced the row after the examination
			applied bool
			stored  bool
		}{
			{"proven exit", store.AgentCodex, proven, false, true, true},
			{"another process", store.AgentCodex, store.ProcessExit{PID: pane.PanePID + 1, Start: pane.PaneStarttime, AtMillis: at}, false, true, false},
			{"another start", store.AgentCodex, store.ProcessExit{PID: pane.PanePID, Start: "other", AtMillis: at}, false, true, false},
			{"pid only", store.AgentCodex, store.ProcessExit{PID: pane.PanePID, AtMillis: at}, false, true, false},
			{"zero exit", store.AgentCodex, store.ProcessExit{}, false, true, false},
			{"Cursor row", store.AgentCursor, proven, false, true, false},
			{"stale snapshot", store.AgentCodex, proven, true, false, false},
			{"absent row", store.AgentCodex, proven, false, false, false},
		} {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				f := newV5Store(t)
				id := f.seed(store.StateWorking, "", append(rowVersionSeed(), apitest.WithAgent(tc.agent), apitest.WithEndReportedAt(rvEndReported))...)
				sp := stampEvidence(t, f, id)
				if tc.stale {
					rvSoftRefresh(t, f, id)
				}
				// Only a Codex row moves to ended.
				applied := tc.applied && (tc.agent == store.AgentCodex || strings.Contains(name, "Missing"))
				target := sp
				if tc.name == "absent row" {
					target.InstanceID = "absent-" + id
				}
				res, recorded, err := move(f.s, target, tc.exit)
				if tc.name == "absent row" && res != store.CondAbsent {
					t.Fatalf("move of an absent row = %v; want CondAbsent", res)
				}
				if err != nil || (res == store.CondApplied) != applied || recorded != tc.stored {
					t.Fatalf("move = %v, recorded %v, %v; want applied %v, recorded %v", res, recorded, err, applied, tc.stored)
				}
				after := rvExamine(t, f, id)
				v, rec := after.ProcessConfirmation()
				switch {
				case !applied:
					if after.ProcessEvidence != sp.ProcessEvidence {
						t.Errorf("a move that did not apply changed the process record to %s", after.ProcessEvidence)
					}
				case tc.stored:
					want := store.Confirmation{Status: store.ConfirmationConfirmed, ConfirmedAtMillis: at, Source: store.EvidenceProcess, CheckedAtMillis: at}
					if v != store.ProcessExited || rec != want {
						t.Errorf("process record %s %+v; want exited %+v, bound to the row's life and launch", v, rec, want)
					}
				case after.ProcessEvidence != "":
					t.Errorf("process record %s; want none: the move proved no exit of a process the row records", after.ProcessEvidence)
				}
			})
		}
	}
	t.Run("failed move rolls back together", func(t *testing.T) {
		t.Parallel()
		f := newV5Store(t)
		id := f.seed(store.StateCheckPermission, "", append(rowVersionSeed(), apitest.WithAgent(store.AgentCodex))...)
		seedRequest(t, f, id)
		sp := stampEvidence(t, f, id)
		storefix.InjectWriteFailure(t, f.path, storefix.WriteFailPermissionDecision, id)
		if _, res, recorded, err := f.s.RecoveryWrites(sp).MarkMissingIfSameLifeFrom(id, sp.Snapshot, store.EvidenceProcess, proven); err == nil || recorded {
			t.Fatalf("mark with its request close failing = %v, recorded %v, %v; want an error, nothing recorded", res, recorded, err)
		}
		after := rvExamine(t, f, id)
		if after.State != sp.State || after.StateEvidence != sp.StateEvidence || after.ProcessEvidence != sp.ProcessEvidence ||
			after.FamilyState != sp.FamilyState || after.RowVersion != sp.RowVersion {
			t.Errorf("failed mark left %+v; want the examined row %+v unchanged", after, sp)
		}
	})
}

// TestProcessEvidenceAfter: a successful attempt stamps its value with source
// process; a failed one keeps the last applicable value, time and source and
// records only its own time and reason. Nothing is made for a Cursor row, a
// process the row does not record, or an alive sample of a finished row (one
// taken before its terminal move), while exited and failed attempts there
// still are.
func TestProcessEvidenceAfter(t *testing.T) {
	f := newV5Store(t)
	pane := fullIdentity()
	attempt := func(value, reason string) store.ProcessAttempt {
		return store.ProcessAttempt{PID: pane.PanePID, Start: pane.PaneStarttime, Value: value, Reason: reason}
	}
	row := func(state string, opts ...apitest.SpawnOption) store.Spawn {
		return stampEvidence(t, f, f.seed(state, "", append(rowVersionSeed(), opts...)...))
	}
	working, ended, missing, cursor := row(store.StateWorking), row(store.StateEnded), row(store.StateMissing), row(store.StateWorking, apitest.WithAgent(store.AgentCursor))
	const atMs = evidenceSeedMs + 2000
	fresh := store.Confirmation{Status: store.ConfirmationConfirmed, ConfirmedAtMillis: atMs, Source: store.EvidenceProcess, CheckedAtMillis: atMs}
	kept := store.Confirmation{Status: store.ConfirmationNotConfirmed, ConfirmedAtMillis: evidenceSeedMs, Source: store.EvidenceProcess, CheckedAtMillis: atMs, Reason: store.ReasonUnreadable}
	for _, tc := range []struct {
		name    string
		sp      store.Spawn
		attempt store.ProcessAttempt
		value   string // "" when nothing may be stored
		want    store.Confirmation
	}{
		{"alive on a live row", working, attempt(store.ProcessAlive, ""), store.ProcessAlive, fresh},
		{"unreadable keeps the older value and time", working, attempt(store.ProcessUnknown, store.ReasonUnreadable), store.ProcessAlive, kept},
		{"another process", working, store.ProcessAttempt{PID: pane.PanePID + 1, Start: pane.PaneStarttime, Value: store.ProcessAlive}, "", store.Confirmation{}},
		{"Cursor row", cursor, attempt(store.ProcessAlive, ""), "", store.Confirmation{}},
		{"alive on an ended row", ended, attempt(store.ProcessAlive, ""), "", store.Confirmation{}},
		{"alive on a missing row", missing, attempt(store.ProcessAlive, ""), "", store.Confirmation{}},
		{"exited on an ended row", ended, attempt(store.ProcessExited, ""), store.ProcessExited, fresh},
		{"unreadable on a missing row", missing, attempt(store.ProcessUnknown, store.ReasonUnreadable), store.ProcessExited, kept},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, ok := tc.sp.ProcessEvidenceAfter(tc.attempt, atMs)
			if ok != (tc.value != "") {
				t.Fatalf("ProcessEvidenceAfter = %q, %v; want ok %v", rec, ok, tc.value != "")
			}
			if !ok {
				return
			}
			sp := tc.sp
			sp.ProcessEvidence = rec
			if v, c := sp.ProcessConfirmation(); v != tc.value || c != tc.want {
				t.Errorf("record %s reads %s %+v; want %s %+v", rec, v, c, tc.value, tc.want)
			}
		})
	}
}

// TestProcessEvidenceWriteOutcomes: a process record write, alone or in a
// proven terminal mark, that is refused busy or fails before its COMMIT
// leaves nothing durable, while one whose COMMIT landed but whose
// acknowledgement was lost is reported unconfirmed, never as a confirmed
// write, and a reopened store shows what is durable.
func TestProcessEvidenceWriteOutcomes(t *testing.T) {
	pane := fullIdentity()
	for _, writer := range []string{"RecordProcessEvidence", "RecoveryWrites.MarkMissingIfSameLifeFrom"} {
		for _, cut := range []string{"busy", "failed statement", "lost commit acknowledgement"} {
			t.Run(writer+"/"+cut, func(t *testing.T) {
				f := newV5Store(t)
				id := f.seed(store.StateWorking, "", rowVersionSeed()...)
				sp := rvExamine(t, f, id)
				rec, ok := sp.ProcessEvidenceAfter(store.ProcessAttempt{PID: pane.PanePID, Start: pane.PaneStarttime, Value: store.ProcessAlive}, evidenceSeedMs)
				if !ok {
					t.Fatal("no record for the seeded process")
				}
				release := func() {}
				switch cut {
				case "busy":
					release = apitest.HoldWriteLock(t, f.path, time.Minute)
				case "failed statement":
					store.ObserveDecisionBoundaryForTest(t, f.s, f.path, func(_ context.Context, q string) error {
						if strings.Contains(q, "UPDATE spawns") && strings.Contains(q, "process_evidence") {
							return errors.New("injected statement failure")
						}
						return nil
					}, nil)
				default:
					store.ObserveDecisionBoundaryForTest(t, f.s, f.path, nil, func(context.Context) error { return errors.New("synthetic lost commit result") })
				}
				var err error
				if writer == "RecordProcessEvidence" {
					_, err = f.s.RecordProcessEvidence(sp, rec)
				} else {
					var recorded bool
					exit := store.ProcessExit{PID: pane.PanePID, Start: pane.PaneStarttime, AtMillis: evidenceSeedMs}
					if _, _, recorded, err = f.s.RecoveryWrites(sp).MarkMissingIfSameLifeFrom(id, sp.Snapshot, store.EvidenceProcess, exit); recorded {
						t.Error("a mark that returned an error reported its record stored")
					}
				}
				release()
				if err == nil || store.CommitUnconfirmed(err) != (cut == "lost commit acknowledgement") || errors.Is(err, store.ErrStoreBusy) != (cut == "busy") {
					t.Fatalf("write error %v; want one that says %s", err, cut)
				}
				reopened, oerr := store.Open(f.path)
				if oerr != nil {
					t.Fatal(oerr)
				}
				defer reopened.Close()
				after, oerr := reopened.GetSpawn(id)
				if oerr != nil {
					t.Fatal(oerr)
				}
				v, c := after.ProcessConfirmation()
				switch {
				case cut != "lost commit acknowledgement" && (after.State != store.StateWorking || after.ProcessEvidence != ""):
					t.Errorf("failed write left state %s, process record %q; want nothing durable", after.State, after.ProcessEvidence)
				case cut == "lost commit acknowledgement" && writer == "RecordProcessEvidence" && after.ProcessEvidence != rec:
					t.Errorf("landed record %q; want %q durable", after.ProcessEvidence, rec)
				case cut == "lost commit acknowledgement" && writer != "RecordProcessEvidence" && (after.State != store.StateMissing || v != store.ProcessExited || c.ConfirmedAtMillis != evidenceSeedMs):
					t.Errorf("landed mark reads %s, %s %+v; want missing with the exited record durable", after.State, v, c)
				}
			})
		}
	}
}
