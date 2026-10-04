package api_test

// lookup_calltable_test.go is the SR-20.5 call-site table (SR-20.6 "every
// cell of the call-site table, including the action-failure column"): each
// lookup outcome of SR-3.3/SR-3.4, plus the first action failing once the
// lookup is Ours and each unusable recorded name of SR-3.2
// (lookup_calltable_unusable_test.go), against each single-row verb. A
// column builds the row and its tmux and process world on the kill fixture
// (kill_fixture_test.go); a verb is a small adapter (invoke, its action
// calls, its first action) with one expected cell per column. A verb that inserts its own row (plain
// spawn), writes the row's result (find-missing's sweep), deletes it
// (expire) or launches it (resume, and resume's re-lookup after "duplicate
// session") runs its own world and row check instead, and marks the columns
// that cannot arise for it not applicable, with the reason. Sequence
// details, the process wait and the ceilings are kill_test.go's. Later verb
// Epics extend this table (an adapter appended to callTableVerbs) instead of
// writing their own; reuse's rows run under TestCallTableReuse
// (lookup_calltable_reuse_test.go), so -run Reuse selects them.

import (
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// callTableOutcome names one column of the call-site table.
type callTableOutcome string

// The columns: SR-3.4's verdicts and their variants, SR-3.3's server cases
// (AC-KILL-05) and the action-failure column.
const (
	ctOurs                     callTableOutcome = "ours"
	ctLeftover                 callTableOutcome = "leftover"
	ctGoneNoLabel              callTableOutcome = "gone, no session carries the label"
	ctGoneOtherStoreRowToken   callTableOutcome = "gone, another store's label with the row's token"
	ctGoneOtherStoreOtherToken callTableOutcome = "gone, another store's label with another token"
	ctGoneForeignLabel         callTableOutcome = "gone, only another row's label"
	ctGoneNameUnlabelled       callTableOutcome = "gone, an unlabelled session holds the row's name"
	ctGoneServerRestarted      callTableOutcome = "gone, server restarted"
	ctGoneEmptyServer          callTableOutcome = "gone, the recorded server running with no session"
	ctGoneNoServer             callTableOutcome = "gone, no server running and recorded server gone"
	ctGoneNoSocket             callTableOutcome = "gone, socket missing and recorded server gone"
	ctDifferentRebound         callTableOutcome = "different server, socket rebound while the recorded server runs"
	ctDifferentRestarted       callTableOutcome = "different server, restarted and recorded server unreadable"
	ctDifferentNoServer        callTableOutcome = "different server, no server running and recorded server running"
	ctConflictScope            callTableOutcome = "provenance conflict, scope value"
	ctConflictDuplicate        callTableOutcome = "provenance conflict, duplicate label"
	ctUnreadableTimeout        callTableOutcome = "unreadable, timeout"
	ctUnreadableUnrecognised   callTableOutcome = "unreadable, unrecognised reply"
	ctUnreadableMalformed      callTableOutcome = "unreadable, malformed answer"
	ctUnavailableBinary        callTableOutcome = "tmux unavailable, missing binary"
	ctUnavailableSocket        callTableOutcome = "tmux unavailable, socket permission"
	ctActionRecognised         callTableOutcome = "action failure, recognised"
	ctActionTimeout            callTableOutcome = "action failure, timeout"
)

// callTableColumn is one outcome: the row seeded (spec), the world built
// around it, and actionFailure, which (when set) fails every call of the
// verb's first action on an Ours lookup.
type callTableColumn struct {
	outcome       callTableOutcome
	spec          killRowSpec
	world         func(t *testing.T, e *killEnv, r *killRow)
	actionFailure tmux.Failure
}

// callTableScript scripts s on every lookup of r's socket.
func callTableScript(s tmuxfix.Script) func(*testing.T, *killEnv, *killRow) {
	return func(_ *testing.T, e *killEnv, r *killRow) { e.rec.Script(r.Socket, s, tmux.CallLookup) }
}

// callTableStop stops r's server with the no-server failure f; sync puts the
// recorded server process gone in the fake, else it keeps running.
func callTableStop(f tmux.Failure, sync bool) func(*testing.T, *killEnv, *killRow) {
	return func(_ *testing.T, e *killEnv, r *killRow) {
		e.rec.StopServer(r.Socket).SetNoServerFailure(r.Socket, f)
		if sync {
			e.syncServers()
		}
	}
}

// callTableColumns returns every column: the lookup columns, then the
// unusable-name family (lookup_calltable_unusable_test.go).
func callTableColumns() []callTableColumn {
	return append(callTableLookupColumns(), callTableUnusableColumns()...)
}

// callTableLookupColumns returns the columns a lookup decides (and the action
// failures). Gone columns seed the agent process gone; the others leave it
// running, so nothing sent is not for want of an agent.
func callTableLookupColumns() []callTableColumn {
	noSession, gone := killRowSpec{NoSession: true}, killRowSpec{Agent: agentGone}
	goneNoSession := killRowSpec{NoSession: true, Agent: agentGone}
	otherStore := func(token func(killRow) string) func(*testing.T, *killEnv, *killRow) {
		return func(t *testing.T, e *killEnv, r *killRow) {
			e.seedSession(t, r, tmuxfix.WithRowSessionLabel(r.otherStore(token(*r)), true))
		}
	}
	return []callTableColumn{
		{outcome: ctOurs},
		{outcome: ctLeftover, spec: noSession, world: func(t *testing.T, e *killEnv, r *killRow) {
			e.seedSession(t, r, tmuxfix.WithRowSessionLabel(r.old(), true))
		}},
		{outcome: ctGoneNoLabel, spec: goneNoSession,
			world: func(t *testing.T, e *killEnv, r *killRow) { e.seedBystander(t, r.Socket) }},
		{outcome: ctGoneOtherStoreRowToken, spec: goneNoSession,
			world: otherStore(func(r killRow) string { return r.Token })},
		{outcome: ctGoneOtherStoreOtherToken, spec: goneNoSession,
			world: otherStore(func(killRow) string { return tmuxfix.OtherToken })},
		{outcome: ctGoneForeignLabel, spec: goneNoSession, world: func(t *testing.T, e *killEnv, r *killRow) {
			e.ensureServer(r)
			e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "foreign-" + r.ID, Label: r.foreign("foreign-" + r.ID)})
		}},
		{outcome: ctGoneNameUnlabelled, spec: goneNoSession, world: func(t *testing.T, e *killEnv, r *killRow) {
			e.ensureServer(r)
			e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: r.Name})
		}},
		{outcome: ctGoneServerRestarted, spec: gone, world: func(t *testing.T, e *killEnv, r *killRow) {
			e.rec.RestartServer(r.Socket, tmuxfix.Server{})
			e.syncServers()
			e.seedBystander(t, r.Socket)
		}},
		// b.47f: tmux's exit-empty off keeps the server up; its empty listing still names it.
		{outcome: ctGoneEmptyServer, spec: goneNoSession, world: func(_ *testing.T, e *killEnv, r *killRow) {
			e.ensureServer(r)
			e.rec.EmptyServer(r.Socket)
			e.syncServers()
		}},
		{outcome: ctGoneNoServer, spec: gone, world: callTableStop(tmux.FailNoServer, true)},
		{outcome: ctGoneNoSocket, spec: gone, world: callTableStop(tmux.FailNoSocket, true)},
		{outcome: ctDifferentRebound, world: func(t *testing.T, e *killEnv, r *killRow) {
			e.rec.RebindServer(r.Socket, tmuxfix.Server{})
			e.syncServers()
			e.seedBystander(t, r.Socket)
		}},
		{outcome: ctDifferentRestarted, world: func(t *testing.T, e *killEnv, r *killRow) {
			e.rec.RestartServer(r.Socket, tmuxfix.Server{})
			e.syncServers()
			e.pc.Set(r.Spawn.Identity.ServerPID, procfix.Unreadable())
			e.seedBystander(t, r.Socket)
		}},
		{outcome: ctDifferentNoServer, world: callTableStop(tmux.FailNoServer, false)},
		{outcome: ctConflictScope, world: func(_ *testing.T, e *killEnv, r *killRow) {
			e.rec.SetScope(r.Socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{})
		}},
		{outcome: ctConflictDuplicate, world: func(t *testing.T, e *killEnv, r *killRow) {
			e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "dup-" + r.ID, Label: r.current()})
		}},
		{outcome: ctUnreadableTimeout, world: callTableScript(tmuxfix.Script{Failure: tmux.FailTimeout})},
		{outcome: ctUnreadableUnrecognised, world: callTableScript(tmuxfix.Script{Failure: tmux.FailUnrecognized,
			FirstLine: callTableFirstLine(), ExitStatus: 1})},
		{outcome: ctUnreadableMalformed, world: callTableScript(tmuxfix.Script{Failure: tmux.FailUnrecognized, HadStdout: true})},
		{outcome: ctUnavailableBinary, world: callTableScript(tmuxfix.Script{Failure: tmux.FailUnavailable})},
		{outcome: ctUnavailableSocket, world: callTableScript(tmuxfix.Script{Failure: tmux.FailSocketDenied})},
		{outcome: ctActionRecognised, actionFailure: tmux.FailNoServer},
		{outcome: ctActionTimeout, actionFailure: tmux.FailTimeout},
	}
}

// callTableFirstLine is the replay catalogue's unrecognised lookup reply.
func callTableFirstLine() string {
	return tmuxfix.Find(tmuxfix.Replies(apitest.TestSocket), "reply/server-exited").FirstLine
}

// callTableCell is a verb's expected result in one column: the error name
// ("" for success), whether an action was sent, the socket calls in order,
// the description case of an error, and prepare, the verb's own process
// behaviour for the column (e.g. kill's pane kill ending the agent). na,
// when set, says why the column cannot arise for the verb; held is plain
// spawn's world and description (lookup_calltable_spawn_test.go); fm is
// find-missing's row result (lookup_calltable_findmissing_test.go); kept is
// expire's kept reason, "" when it deletes the row, and keptLater that of a
// later row on the same socket (lookup_calltable_expire_test.go);
// heldResume is resume's "duplicate session" arrangement (lookup_calltable_resume_test.go).
type callTableCell struct {
	errName    string
	sent       bool
	calls      []tmux.Call
	desc       func(e *killEnv, r killRow) apitest.DescCase
	prepare    func(e *killEnv, r killRow)
	na         string
	held       callTableHeld
	fm         callTableFM
	kept       string
	keptLater  string
	heldResume callTableHeldResume
}

// callTableVerb is one verb's row: invoke runs it on r and reports whether it
// sent an action; firstAction is the call the action-failure columns fail;
// actions are its action calls, none of which a nothing-sent cell records.
// run, when set, replaces the seeded-row run for a verb that builds its own
// world and row check (runCallTableSpawn).
type callTableVerb struct {
	name        string
	invoke      func(t *testing.T, e *killEnv, r killRow) (sent bool, err error)
	firstAction tmux.Call
	actions     []tmux.Call
	cells       map[callTableOutcome]callTableCell
	run         func(t *testing.T, v callTableVerb, col callTableColumn, cell callTableCell)
}

// callTableVerbs is every verb's row; a later verb appends its adapter and
// cells here, like callTableKill, callTableReadPane and callTableSendKeys.
func callTableVerbs() []callTableVerb {
	return []callTableVerb{callTableKill(), callTableSpawn(), callTableFindMissing(), callTableReadPane(),
		callTableSendKeys(), callTableSendKeysPending(), callTablePause(), callTablePausePending(),
		callTablePauseEnded(), callTableExpire(), callTableResume(), callTableResumeHeld(), callTableKillOptIn()}
}

// TestCallTable runs every verb in every column: error name through the
// one-name helper, action sent, recorded calls, description, row unchanged
// (or the verb's own row check); a not-applicable cell is skipped with its reason.
func TestCallTable(t *testing.T) { runCallTableVerbs(t, callTableVerbs()) }

// runCallTableVerbs runs each of verbs in every column (TestCallTable;
// reuse's rows: TestCallTableReuse, lookup_calltable_reuse_test.go).
func runCallTableVerbs(t *testing.T, verbs []callTableVerb) {
	cols := callTableColumns()
	for _, v := range verbs {
		for outcome := range v.cells {
			if !slices.ContainsFunc(cols, func(c callTableColumn) bool { return c.outcome == outcome }) {
				t.Errorf("%s: cell %q names no column", v.name, outcome)
			}
		}
		for _, col := range cols {
			t.Run(v.name+"/"+string(col.outcome), func(t *testing.T) {
				cell, ok := v.cells[col.outcome]
				if !ok {
					t.Fatalf("%s has no cell for column %q", v.name, col.outcome)
				}
				runCallTableCell(t, v, col, cell)
			})
		}
	}
}

// runCallTableCell skips a not-applicable cell, else runs it through v.run
// or, by default, runCallTableRowCell.
func runCallTableCell(t *testing.T, v callTableVerb, col callTableColumn, cell callTableCell) {
	switch {
	case cell.na != "":
		t.Skipf("not applicable to %s: %s", v.name, cell.na)
	case v.run != nil:
		v.run(t, v, col, cell)
	default:
		runCallTableRowCell(t, v, col, cell)
	}
}

// runCallTableRowCell seeds col's row, builds its world, runs v and checks
// cell, with the row unchanged.
func runCallTableRowCell(t *testing.T, v callTableVerb, col callTableColumn, cell callTableCell) {
	e := newKillEnv(t)
	r := e.seedRow(t, col.spec)
	if col.world != nil {
		col.world(t, e, &r)
	}
	if col.actionFailure != 0 {
		e.rec.Script(r.Socket, tmuxfix.Script{Failure: col.actionFailure}, v.firstAction)
	}
	if cell.prepare != nil {
		cell.prepare(e, r)
	}
	rowBefore, sessionsBefore := e.columns(t, r.ID), e.rec.Sessions(r.Socket)

	sent, err := v.invoke(t, e, r)

	if cell.errName == "" && err != nil {
		t.Fatalf("err = %v; want success", err)
	}
	if cell.errName != "" {
		assertOneName(t, err, cell.errName)
		apitest.AssertDescription(t, err.Error(), cell.desc(e, r), r.Token, r.StoreID, apitest.OtherStoreID(r.StoreID))
	}
	if sent != cell.sent {
		t.Errorf("action sent = %v; want %v", sent, cell.sent)
	}
	e.assertKillCalls(t, cell.calls...)
	if !cell.sent {
		for _, c := range e.rec.SocketCalls() {
			if slices.Contains(v.actions, recordedCall(c)) {
				t.Errorf("%v sent to %q; want nothing sent", recordedCall(c), c.Target)
			}
		}
		if after := e.rec.Sessions(r.Socket); !reflect.DeepEqual(after, sessionsBefore) {
			t.Errorf("sessions on %s changed:\n got %+v\nwant %+v", r.Socket, after, sessionsBefore)
		}
	}
	if after := e.columns(t, r.ID); !reflect.DeepEqual(after, rowBefore) {
		t.Errorf("row changed:\n got %+v\nwant %+v", after, rowBefore)
	}
}

// callTableLookupRefusals are the single-row verbs' shared cells for a
// lookup that cannot decide or finds tmux unavailable: only the lookup is
// made, and the refusal says nothing was done (SR-1.4).
func callTableLookupRefusals() map[callTableOutcome]callTableCell {
	refused := func(name string, desc func(e *killEnv, r killRow) apitest.DescCase) callTableCell {
		return callTableCell{errName: name, calls: []tmux.Call{tmux.CallLookup}, desc: desc}
	}
	differentServer := refused("ErrTmuxNotAvailable", func(_ *killEnv, r killRow) apitest.DescCase {
		return apitest.DescDifferentServer(r.ID)
	})
	return map[callTableOutcome]callTableCell{
		ctDifferentRebound:   differentServer,
		ctDifferentRestarted: differentServer,
		ctDifferentNoServer:  differentServer,
		ctConflictScope: refused("ErrTmuxSessionConflict", func(_ *killEnv, r killRow) apitest.DescCase {
			return apitest.DescConflictingLabels(apitest.ConflictingLabels{InstanceID: r.ID, Scope: true, NothingWasDone: true})
		}),
		ctConflictDuplicate: refused("ErrTmuxSessionConflict", func(e *killEnv, r killRow) apitest.DescCase {
			var carrying []apitest.DescSession
			for _, s := range e.rec.Sessions(r.Socket) {
				if s.Label == r.current() {
					carrying = append(carrying, apitest.DescSession{Name: s.Name, ID: s.ID})
				}
			}
			return apitest.DescConflictingLabels(apitest.ConflictingLabels{InstanceID: r.ID, Sessions: carrying,
				NothingWasDone: true})
		}),
		ctUnreadableTimeout: refused("ErrTmuxUnresponsive", func(e *killEnv, _ killRow) apitest.DescCase {
			return apitest.DescCallTimeout(tmux.CallLookup, e.cfg.EffectiveQueryTimeout())
		}),
		ctUnreadableUnrecognised: refused("ErrTmuxUnresponsive", func(*killEnv, killRow) apitest.DescCase {
			return apitest.DescUnrecognisedReply(tmux.CallLookup, callTableFirstLine())
		}),
		ctUnreadableMalformed: refused("ErrTmuxUnresponsive", func(*killEnv, killRow) apitest.DescCase {
			return apitest.DescUnrecognisedReply(tmux.CallLookup, "")
		}),
		ctUnavailableBinary: refused("ErrTmuxNotAvailable", func(*killEnv, killRow) apitest.DescCase {
			return apitest.DescTmuxNotRun()
		}),
		ctUnavailableSocket: refused("ErrTmuxNotAvailable", func(_ *killEnv, r killRow) apitest.DescCase {
			return apitest.DescSocketPermission(r.Socket)
		}),
	}
}

// callTableKill is kill's row (SR-6.1): Ours kills the agent's pane and the
// session by id; Gone with the process dead sends nothing; every refusal
// makes only the lookup; a failed first action (the pane kill) does not stop
// the session kill, and the check decides.
func callTableKill() callTableVerb {
	lookup := []tmux.Call{tmux.CallLookup}
	gone := callTableCell{calls: lookup}
	endedBy := func(call tmux.Call) callTableCell {
		return callTableCell{sent: true,
			calls:   []tmux.Call{tmux.CallLookup, tmux.CallListPanes, tmux.CallKillPane, tmux.CallKillSession},
			prepare: func(e *killEnv, r killRow) { e.setAfterCall(call, procfix.Gone(), r.AgentPID) }}
	}
	cells := callTableLookupRefusals()
	maps.Copy(cells, map[callTableOutcome]callTableCell{
		ctOurs: endedBy(tmux.CallKillPane),
		ctLeftover: {errName: "ErrTmuxSessionConflict", calls: lookup, desc: func(_ *killEnv, r killRow) apitest.DescCase {
			return apitest.DescKillLeftover([]apitest.DescSession{{Name: r.Session.Name, ID: r.Session.ID}})
		}},
		ctGoneNoLabel:              gone,
		ctGoneOtherStoreRowToken:   gone,
		ctGoneOtherStoreOtherToken: gone,
		ctGoneForeignLabel:         gone,
		ctGoneNameUnlabelled:       gone,
		ctGoneServerRestarted:      gone,
		ctGoneEmptyServer:          gone,
		ctGoneNoServer:             gone,
		ctGoneNoSocket:             gone,
		ctActionRecognised:         endedBy(tmux.CallKillSession),
		ctActionTimeout:            endedBy(tmux.CallKillSession),
	})
	maps.Copy(cells, callTableUnusableRefused())
	return callTableVerb{
		name: "kill",
		invoke: func(_ *testing.T, e *killEnv, r killRow) (bool, error) {
			res, err := e.kill(r.ID)
			return res.KillSent, err
		},
		firstAction: tmux.CallKillPane,
		actions:     []tmux.Call{tmux.CallKillPane, tmux.CallKillSession},
		cells:       cells,
	}
}
