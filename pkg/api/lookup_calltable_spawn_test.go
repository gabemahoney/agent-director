package api_test

// lookup_calltable_spawn_test.go is plain spawn's row of the call-site table
// (lookup_calltable_test.go; SR-20.6): the one re-lookup after its create
// answers "duplicate session" (SR-9.4, SR-3.10). Spawn inserts its own row,
// so each cell builds its world on the held-name fixture (spawn_held_test.go)
// and checks the new row ended; end-write details, trail and ceiling are
// spawn_held*_test.go's.

import (
	"maps"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// callTableHeldName is the name every spawn cell requests; its holder is "$4".
const callTableHeldName = "calltable-held"

// callTableHeldWorld is one spawn cell's world: the fixture, the caller-supplied
// id, another row's id, the new row's token (set as the create returns) and
// every session seeded.
type callTableHeldWorld struct {
	heldEnv
	id, other, token string
	seeded           []tmuxfix.SeedSession
}

// seed adds ss to the socket and records them for the forbidden values.
func (w *callTableHeldWorld) seed(ss ...tmuxfix.SeedSession) {
	w.seeded = append(w.seeded, ss...)
	w.rec.SeedSessions(w.socket, ss...)
}

// holder seeds the name's holder "$4" with label (tmux.Label{}: none).
func (w *callTableHeldWorld) holder(label tmux.Label) {
	w.seed(heldSession(callTableHeldName, "$4", label, label.Kind == tmux.LabelValid))
}

// callTableHeld is spawn's cell: before runs ahead of the spawn, afterScan as
// the scan's lookup returns, afterCreate as the create returns (the first
// moment the new row's token exists); desc is the description case for p.
type callTableHeld struct {
	before, afterScan, afterCreate func(w *callTableHeldWorld)
	holderID                       string
	desc                           func(w *callTableHeldWorld, p apitest.HeldName) apitest.DescCase
}

// runCallTableSpawn runs a plain spawn with a caller-supplied id in cell's
// world: one name, the description, the scan then create then re-lookup with
// nothing on the holder, and the new row ended.
func runCallTableSpawn(t *testing.T, _ callTableVerb, _ callTableColumn, cell callTableCell) {
	w := &callTableHeldWorld{heldEnv: newHeldEnv(t), id: heldID(), other: "other-" + uuid.NewString()[:8]}
	h := cell.held
	if h.before != nil {
		h.before(w)
	}
	w.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) {
		cols, err := apitest.ReadSpawnColumns(w.dbPath, w.id)
		if err != nil {
			t.Errorf("ReadSpawnColumns as the create returned: %v", err)
		}
		w.token, _ = cols.LaunchToken.(string)
		if h.afterCreate != nil {
			h.afterCreate(w)
		}
	})
	var afterScan func()
	if h.afterScan != nil {
		afterScan = func() { h.afterScan(w) }
	}

	run := w.spawnHeld(t, w.id, callTableHeldName, afterScan)

	assertOneName(t, run.err, cell.errName)
	_, desc := errnames.Classify(run.err)
	p := apitest.HeldName{Name: callTableHeldName, SessionID: h.holderID, Row: apitest.HeldRowEnded, InstanceID: w.id}
	apitest.AssertDescription(t, desc, h.desc(w, p), append(w.forbid(w.id, w.seeded), w.token, w.other)...)
	if got := callKinds(w.rec); !reflect.DeepEqual(got, cell.calls) {
		t.Errorf("tmux calls = %v; want %v", got, cell.calls)
	}
	if n := len(w.rec.Calls()); n != 0 {
		t.Errorf("name-based tmux calls = %d; want none", n)
	}
	if after := w.rec.Sessions(w.socket); !reflect.DeepEqual(after, run.sessionsAtCreate) {
		t.Errorf("sessions changed after the create:\nat create %+v\nafter     %+v", run.sessionsAtCreate, after)
	}
	cols, err := apitest.ReadSpawnColumns(w.dbPath, w.id)
	if err != nil || cols.State != store.StateEnded {
		t.Errorf("row state = %v (err %v); want ended", cols.State, err)
	}
}

// callTableSpawn is plain spawn's row (SR-9.4): every applicable column makes
// the scan, the create and one re-lookup and returns the holder check's
// error with "the new row was ended"; the others are not applicable.
func callTableSpawn() callTableVerb {
	calls := []tmux.Call{tmux.CallLookup, tmux.CallCreate, tmux.CallLookup}
	cell := func(errName string, h callTableHeld) callTableCell {
		return callTableCell{errName: errName, calls: calls, held: h}
	}
	holder := func(label func(w *callTableHeldWorld) tmux.Label) func(w *callTableHeldWorld) {
		return func(w *callTableHeldWorld) { w.holder(label(w)) }
	}
	unlabelled := holder(func(*callTableHeldWorld) tmux.Label { return tmux.Label{} })
	otherStore := func(w *callTableHeldWorld, p apitest.HeldName) apitest.DescCase {
		return apitest.DescHeldOtherStore(p, w.storeID)
	}
	// relookup applies s to the re-lookup only, with an unlabelled holder.
	relookup := func(errName string, s tmuxfix.Script, desc func(w *callTableHeldWorld) apitest.DescCase) callTableCell {
		s.Times = 1
		return cell(errName, callTableHeld{before: unlabelled,
			afterScan: func(w *callTableHeldWorld) { w.rec.Script(w.socket, s, tmux.CallLookup) },
			desc: func(w *callTableHeldWorld, p apitest.HeldName) apitest.DescCase {
				return desc(w).AfterHeldName(p)
			}})
	}
	noServerIdentity := "the new row records no tmux server identity, so no recorded server can be restarted, rebound or gone"
	noDuplicate := noServerIdentity + `; a create finding no server or no socket answers that, not "duplicate session"`
	v := callTableVerb{
		name:   "spawn",
		run:    runCallTableSpawn,
		serial: "its world (newHeldEnv, newSpawnEnv) sets HOME and TMUX_TMPDIR with t.Setenv",
		cells: map[callTableOutcome]callTableCell{
			ctOurs: {na: "a current label cannot occur: the new row's token is fresh and its create made no session"},
			ctLeftover: cell("ErrTmuxSessionConflict", callTableHeld{holderID: "$4",
				// Placed after the scan, which would otherwise refuse it (SR-9.3).
				afterScan: func(w *callTableHeldWorld) { w.seed(w.leftover(callTableHeldName, "$4", w.id, 0)) },
				desc: func(_ *callTableHeldWorld, p apitest.HeldName) apitest.DescCase {
					return apitest.DescHeldLeftover(p)
				}}),
			// The holder vanishes between the create and the re-lookup; a bystander stays.
			ctGoneNoLabel: cell("ErrTmuxSessionCreate", callTableHeld{
				before: func(w *callTableHeldWorld) {
					w.holder(tmux.Label{})
					w.seed(tmuxfix.SeedSession{Name: "bystander-" + w.id})
					w.rec.RemoveSessionAfter(tmux.CallCreate, w.socket, "$4")
				},
				desc: func(_ *callTableHeldWorld, p apitest.HeldName) apitest.DescCase {
					return apitest.DescSessionCreateFailed(apitest.SessionCreateFailed{Name: p.Name, Duplicate: true}).AfterHeldName(p)
				}}),
			ctGoneNameUnlabelled: cell("ErrTmuxSessionConflict", callTableHeld{before: unlabelled, holderID: "$4",
				desc: func(_ *callTableHeldWorld, p apitest.HeldName) apitest.DescCase {
					return apitest.DescHeldNoValidID(p)
				}}),
			// The new row's token exists only once inserted: tmux answers the
			// create "duplicate session" and the holder appears as it returns.
			// A scripted race that real tmux cannot produce: this checks the defensive path.
			ctGoneOtherStoreRowToken: cell("ErrTmuxSessionConflict", callTableHeld{holderID: "$4",
				before: func(w *callTableHeldWorld) {
					w.rec.Script(w.socket, tmuxfix.Script{Failure: tmux.FailDuplicate, Times: 1}, tmux.CallCreate)
				},
				afterCreate: holder(func(w *callTableHeldWorld) tmux.Label {
					return tmuxfix.Valid(w.token, w.id, apitest.OtherStoreID(w.storeID))
				}),
				desc: otherStore}),
			ctGoneOtherStoreOtherToken: cell("ErrTmuxSessionConflict", callTableHeld{holderID: "$4",
				before: holder(func(w *callTableHeldWorld) tmux.Label {
					return tmuxfix.Valid(tmuxfix.OtherToken, w.id, apitest.OtherStoreID(w.storeID))
				}),
				desc: otherStore}),
			ctGoneForeignLabel: cell("ErrTmuxSessionConflict", callTableHeld{holderID: "$4",
				before: holder(func(w *callTableHeldWorld) tmux.Label {
					return tmuxfix.Valid(tmuxfix.OtherToken, w.other, w.storeID)
				}),
				desc: func(_ *callTableHeldWorld, p apitest.HeldName) apitest.DescCase {
					return apitest.DescHeldDifferentID(p)
				}}),
			ctGoneServerRestarted: {na: noServerIdentity},
			ctGoneEmptyServer:     {na: noServerIdentity},
			ctGoneNoServer:        {na: noDuplicate},
			ctGoneNoSocket:        {na: noDuplicate},
			ctDifferentRebound:    {na: noServerIdentity},
			ctDifferentRestarted:  {na: noServerIdentity},
			ctDifferentNoServer:   {na: noDuplicate},
			ctConflictScope: cell("ErrTmuxSessionConflict", callTableHeld{before: unlabelled, holderID: "$4",
				afterScan: func(w *callTableHeldWorld) { w.rec.SetScope(w.socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{}) },
				desc: func(w *callTableHeldWorld, p apitest.HeldName) apitest.DescCase {
					return apitest.DescConflictingLabels(apitest.ConflictingLabels{InstanceID: w.id, Scope: true,
						NothingWasDone: true}).AfterHeldName(p)
				}}),
			// Two sessions carrying the new row's label appear as the create returns.
			// A scripted race that real tmux cannot produce: this checks the defensive path.
			ctConflictDuplicate: cell("ErrTmuxSessionConflict", callTableHeld{before: unlabelled, holderID: "$4",
				afterCreate: func(w *callTableHeldWorld) {
					for _, n := range []string{"dup-a", "dup-b"} {
						w.seed(tmuxfix.SeedSession{Name: n + "-" + w.id, Label: tmuxfix.Valid(w.token, w.id, w.storeID)})
					}
				},
				desc: func(w *callTableHeldWorld, p apitest.HeldName) apitest.DescCase {
					var carrying []apitest.DescSession
					for _, s := range w.rec.Sessions(w.socket) {
						if s.Label == tmuxfix.Valid(w.token, w.id, w.storeID) {
							carrying = append(carrying, apitest.DescSession{Name: s.Name, ID: s.ID})
						}
					}
					return apitest.DescConflictingLabels(apitest.ConflictingLabels{InstanceID: w.id, Sessions: carrying,
						NothingWasDone: true}).AfterHeldName(p)
				}}),
			ctUnreadableTimeout: relookup("ErrTmuxUnresponsive", tmuxfix.Script{Failure: tmux.FailTimeout},
				func(*callTableHeldWorld) apitest.DescCase { return apitest.DescCallTimeout(tmux.CallLookup, boundQ) }),
			ctUnreadableUnrecognised: relookup("ErrTmuxUnresponsive", tmuxfix.Script{Failure: tmux.FailUnrecognized,
				FirstLine: callTableFirstLine(), ExitStatus: 1},
				func(*callTableHeldWorld) apitest.DescCase {
					return apitest.DescUnrecognisedReply(tmux.CallLookup, callTableFirstLine())
				}),
			ctUnreadableMalformed: relookup("ErrTmuxUnresponsive", tmuxfix.Script{Failure: tmux.FailUnrecognized, HadStdout: true},
				func(*callTableHeldWorld) apitest.DescCase { return apitest.DescUnrecognisedReply(tmux.CallLookup, "") }),
			ctUnavailableBinary: relookup("ErrTmuxNotAvailable", tmuxfix.Script{Failure: tmux.FailUnavailable},
				func(*callTableHeldWorld) apitest.DescCase { return apitest.DescTmuxNotRun() }),
			ctUnavailableSocket: relookup("ErrTmuxNotAvailable", tmuxfix.Script{Failure: tmux.FailSocketDenied},
				func(w *callTableHeldWorld) apitest.DescCase { return apitest.DescSocketPermission(w.socket) }),
			ctActionRecognised: {na: "spawn sends no action after the re-lookup: it never acts on the holder"},
			ctActionTimeout:    {na: "spawn sends no action after the re-lookup: it never acts on the holder"},
		},
	}
	maps.Copy(v.cells, callTableUnusableNA("a plain spawn records no earlier name: it validates its requested name first (ErrTmuxSessionNameInvalid)"))
	return v
}
