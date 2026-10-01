package api_test

// resume_held_fixture_test.go extends the kill fixture's resume helpers
// (resume_lookup_fixture_test.go) for resume's "duplicate session" path
// (SR-8.5, SR-13.2 path (ii), SR-20.2, SR-20.3): one arrangeHeld call makes
// the pre-launch lookup read Gone with the name free, the create answer
// "duplicate session", and the one re-lookup meet a chosen holder, server
// and typed answer; plus the restored-exactly and holder-untouched checks.
// It holds no tests.

import (
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// resumeHeldAt is the virtual time from a resume's call to its re-lookup's
// return (SR-13.2 path (ii)): Q + C + Q at the defaults the Recorder charges.
var resumeHeldAt = 2*resumeLookupQ + config.Tmux{}.EffectiveCreateTimeout()

// heldInstant is the clock reading after the re-lookup if resume runs next,
// advancing e.clock (under a second) to make it whole; a later ruleInstant
// call (resumableSpec, seedResumable, createdBefore) moves the clock again.
func (e *killEnv) heldInstant() time.Time {
	at := e.clock.Now().Add(resumeHeldAt)
	if frac := at.Sub(at.Truncate(time.Second)); frac > 0 {
		e.clock.Advance(time.Second - frac)
		at = at.Add(time.Second - frac)
	}
	return at
}

// heldResumableSpec is resumableSpec's row with ended_at age before
// heldInstant, the rule's reading at the re-lookup; opts still go last.
func (e *killEnv) heldResumableSpec(age time.Duration, a agentState, opts ...apitest.SpawnOption) killRowSpec {
	spec := e.resumableSpec(age, a)
	spec.Opts = append(append(spec.Opts, apitest.WithEndedAt(e.heldInstant().Add(-age))), opts...)
	return spec
}

// seedHeldResumable seeds heldResumableSpec's row through seedResumableRow.
func (e *killEnv) seedHeldResumable(t *testing.T, age time.Duration, a agentState, opts ...apitest.SpawnOption) resumeRow {
	t.Helper()
	return e.seedResumableRow(t, e.heldResumableSpec(age, a, opts...))
}

// heldServer is the tmux server on the row's socket at each lookup.
type heldServer int

const (
	heldServerRecorded  heldServer = iota // the recorded server answers both lookups
	heldServerRebound                     // re-bound as the create returns, the recorded one still running: differs at the re-lookup only
	heldServerRestarted                   // restarted before the call, the recorded one gone: restarted at both lookups
)

// heldSpec is one "duplicate session" arrangement: the holder of the
// recorded name and what else the re-lookup meets.
type heldSpec struct {
	Holder holderKind // holderSessions' kind; holderVanished: nothing holds the name
	// Created is every placed session's age at heldInstant (negative: in the
	// future); zero leaves the clock's second when the create returns.
	Created time.Duration
	// OursRenamed also places the row's own (current-label) session under
	// this stored name: name_changed, or with holderCurrent duplicate_label.
	OursRenamed string
	// Scope sets the row's current label at this level, embedding the first
	// holder's session id (scope_value).
	Scope tmuxfix.ScopeLevel
	// Relookup is the re-lookup's typed answer (Times forced to 1); a zero
	// Failure answers from the table.
	Relookup tmuxfix.Script
	Server   heldServer
}

// heldScene is what arrangeHeld set up; Holders, Ours and Moved are filled
// when the create returns.
type heldScene struct {
	r       resumeRow
	before  resumeSnapshot        // r just before the resume, taken by arrangeHeld
	Placed  bool                  // the create answered "duplicate session" and the holders were placed
	Holders []tmuxfix.SeedSession // the holders as stored (none for holderVanished)
	Ours    tmuxfix.SeedSession   // the row's renamed own session (OursRenamed), as stored
	Moved   apitest.SpawnColumns  // r's row when the create returned: the move's
}

// Holder is the first holder as stored (the zero session when none).
func (sc *heldScene) Holder() tmuxfix.SeedSession {
	if len(sc.Holders) == 0 {
		return tmuxfix.SeedSession{}
	}
	return sc.Holders[0]
}

// arrangeHeld makes r's next create answer tmux.FailDuplicate and, as it
// returns, places spec's server, sessions, scope and re-lookup answer; call it
// last before the resume (it aligns the clock and takes r's snapshot).
func (e *killEnv) arrangeHeld(t *testing.T, r resumeRow, spec heldSpec) *heldScene {
	t.Helper()
	sc := &heldScene{r: r}
	if spec.Server == heldServerRestarted {
		e.rec.RestartServer(r.Socket, tmuxfix.Server{})
		e.syncServers()
		e.seedBystander(t, r.Socket)
	}
	holders := e.holderSessions(t, r.killRow, spec.Holder)
	var ours []tmuxfix.SeedSession
	if spec.OursRenamed != "" {
		ours = e.holderSessions(t, r.killRow, holderCurrent)
		ours[0].Name = spec.OursRenamed
	}
	if at := e.heldInstant(); spec.Created != 0 {
		for i := range holders {
			holders[i].Created = at.Add(-spec.Created).Unix()
		}
		for i := range ours {
			ours[i].Created = at.Add(-spec.Created).Unix()
		}
	}
	e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailDuplicate, Times: 1}, tmux.CallCreate)
	e.rec.AfterCall(tmux.CallCreate, func(c tmuxfix.SocketCall, _ error) {
		if sc.Placed || c.Socket != r.Socket {
			return
		}
		sc.Placed = true
		var err error
		if sc.Moved, err = apitest.ReadSpawnColumns(e.dbPath, r.ID); err != nil {
			t.Errorf("ReadSpawnColumns(%s) as the create returned: %v", r.ID, err)
		}
		if spec.Server == heldServerRebound {
			e.rec.RebindServer(r.Socket, tmuxfix.Server{})
			e.syncServers()
		}
		sc.Holders = e.placeHolder(t, r.killRow, spec.Holder, holders)
		if placed := e.placeHolder(t, r.killRow, holderCurrent, ours); len(placed) > 0 {
			sc.Ours = placed[0]
		}
		if spec.Scope != 0 {
			e.rec.SetScope(r.Socket, spec.Scope, tmuxfix.ScopeValue{SessionID: sc.Holder().ID, Label: r.current()})
		}
		if spec.Relookup.Failure != 0 {
			s := spec.Relookup
			s.Times = 1
			e.rec.Script(r.Socket, s, tmux.CallLookup)
		}
	})
	sc.before = e.snapshotResume(t, r)
	return sc
}

// assertHeldRestored fails unless sc's row is every column as before the move,
// with the move's parent id and row_version two past (rstRestored).
func (e *killEnv) assertHeldRestored(t *testing.T, sc *heldScene) {
	t.Helper()
	if !sc.Placed {
		t.Fatalf("the create on %s never answered %q", sc.r.Socket, "duplicate session")
	}
	e.assertRowUnchanged(t, sc.r.ID, rstRestored(resumableRow{Before: sc.before.cols}, sc.Moved.ParentID))
}

// assertHolderUntouched fails unless every placed session is still stored as
// placed and, since arrangeHeld, no call but the lookups and create reached it.
func (e *killEnv) assertHolderUntouched(t *testing.T, sc *heldScene) {
	t.Helper()
	placed := append([]tmuxfix.SeedSession(nil), sc.Holders...)
	if sc.Ours.ID != "" {
		placed = append(placed, sc.Ours)
	}
	ids := map[string]bool{}
	for _, want := range placed {
		ids[want.ID] = true
		for _, p := range want.Panes {
			ids[p.ID] = true
		}
		var found bool
		for _, got := range e.rec.Sessions(sc.r.Socket) {
			if got.ID == want.ID {
				found = true
				if !reflect.DeepEqual(got, want) {
					t.Errorf("holder %s changed:\n got %+v\nwant %+v", want.ID, got, want)
				}
			}
		}
		if !found {
			t.Errorf("holder %s (%q) is gone from %s; want it untouched", want.ID, want.Name, sc.r.Socket)
		}
	}
	for _, c := range e.rec.SocketCalls()[sc.before.calls:] {
		if c.Socket != sc.r.Socket || c.Call == tmux.CallLookup || c.Call == tmux.CallCreate {
			continue
		}
		if c.Call == tmux.CallListPanes || ids[c.Target] || ids[c.PaneID] {
			t.Errorf("tmux call %v on %s (target %q, pane %q) reaches a holder; want none", c.Call, c.Socket, c.Target, c.PaneID)
		}
	}
	if n := len(e.rec.Calls()) - sc.before.nameCalls; n != 0 {
		t.Errorf("%d name-based tmux calls; want none", n)
	}
}

// removeHolders removes every placed session through Recorder.KillSessionID
// (a recorded call charged A), so a re-issued resume finds the name free.
func (e *killEnv) removeHolders(t *testing.T, sc *heldScene) {
	t.Helper()
	for _, s := range append(append([]tmuxfix.SeedSession(nil), sc.Holders...), sc.Ours) {
		if s.ID == "" {
			continue
		}
		if err := e.rec.KillSessionID(sc.r.Socket, s.ID); err != nil {
			t.Fatalf("KillSessionID(%s, %s): %v", sc.r.Socket, s.ID, err)
		}
	}
}
