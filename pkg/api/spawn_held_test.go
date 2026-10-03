package api_test

// spawn_held_test.go covers plain spawn after its create answers "duplicate
// session" (SR-9.4, SR-3.10, SR-13.2; AC-SPN-04, AC-SPN-06): for each
// re-lookup outcome the new row is ended before the one re-lookup, the
// classified error carries "the new row was ended", and the holder is never
// touched. It holds the shared held-name fixture (heldEnv) that the other
// spawn_held*_test.go files use.

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// heldW is the default pipe-close wait, one per tmux call in SR-13.2's
// ceilings (the Recorder never charges it).
var heldW = time.Duration(config.DefaultPipeCloseWaitMs) * time.Millisecond

// heldStoreLayout is the store's CURRENT_TIMESTAMP text layout for ended_at.
const heldStoreLayout = "2006-01-02 15:04:05"

// heldEnv is the held-name fixture: scanEnv (Recorder, clock, process
// checker, captured log, store id) with every tmux call charged its default
// timeout in virtual time.
type heldEnv struct {
	scanEnv
	start time.Time
}

// newHeldEnv builds a heldEnv; the Recorder is bound to the fixture's clock
// at the internal/config default timeouts.
func newHeldEnv(t *testing.T) heldEnv {
	t.Helper()
	e := newScanEnv(t)
	e.rec.WithVirtualTime(e.clock, tmux.Timeouts{})
	return heldEnv{scanEnv: e, start: e.clock.Now()}
}

// heldID returns a fresh caller-supplied instance id.
func heldID() string { return "held-" + uuid.NewString()[:8] }

// heldSession is a session holding name as sessionID with label (tmux.Label{}
// for none; labelSet marks a malformed value that is present but invalid).
func heldSession(name, sessionID string, label tmux.Label, labelSet bool) tmuxfix.SeedSession {
	return tmuxfix.SeedSession{ID: sessionID, Name: name, Label: label, LabelSet: labelSet}
}

// heldRun is what spawnHeld observed: the Spawn error, the clock when the
// create returned (the end write's time), the row's state when the
// re-lookup returned, and the socket's sessions when the create returned.
type heldRun struct {
	err              error
	createdAt        time.Time
	stateAtRelookup  any
	sessionsAtCreate []tmuxfix.SeedSession
}

// spawnHeld runs a plain spawn of id (caller-supplied, so the scan makes the
// first lookup) requesting name; onScan, when not nil, runs as the scan's
// lookup returns (SR-20.9: a leftover appearing between scan and create).
func (e heldEnv) spawnHeld(t *testing.T, id, name string, onScan func()) heldRun {
	t.Helper()
	var run heldRun
	lookups := 0
	e.rec.AfterCall(tmux.CallLookup, func(tmuxfix.SocketCall, error) {
		lookups++
		switch {
		case lookups == 1 && onScan != nil:
			onScan()
		case lookups == 2:
			cols, err := apitest.ReadSpawnColumns(e.dbPath, id)
			if err != nil {
				t.Errorf("ReadSpawnColumns at the re-lookup: %v", err)
			}
			run.stateAtRelookup = cols.State
		}
	})
	e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) {
		run.createdAt = e.clock.Now()
		run.sessionsAtCreate = e.rec.Sessions(e.socket)
	})
	_, run.err = e.c.Spawn(api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: id,
		TmuxSessionName: name, TmuxSessionNameSupplied: true})
	return run
}

// assertEndedRow checks id's row was ended by the applied end write at
// endedAt: ended_at in the store's layout, no launch start, row_version 1,
// the launch token kept; it returns the token.
func (e heldEnv) assertEndedRow(t *testing.T, id string, endedAt time.Time) string {
	t.Helper()
	cols, err := apitest.ReadSpawnColumns(e.dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns(%s): %v", id, err)
	}
	if cols.State != store.StateEnded || cols.RowVersion != int64(1) || cols.LaunchStartedAt != nil {
		t.Errorf("row state, row_version, launch_started_at = %v, %v, %v; want ended, 1, NULL",
			cols.State, cols.RowVersion, cols.LaunchStartedAt)
	}
	if want := endedAt.UTC().Format(heldStoreLayout); cols.EndedAt != want {
		t.Errorf("ended_at = %v; want %q (the clock at the end write)", cols.EndedAt, want)
	}
	token, _ := cols.LaunchToken.(string)
	if token == "" {
		t.Errorf("launch_token = %v; want the insert's token kept", cols.LaunchToken)
	}
	return token
}

// TestSpawnHeldRelookupOutcomes: per re-lookup outcome the row is ended
// before the re-lookup, the one classified error says so, and the holder is untouched.
func TestSpawnHeldRelookupOutcomes(t *testing.T) {
	const name = "held-name"
	other := "other-" + uuid.NewString()[:8]
	noLabel := func(_ heldEnv, _ string) []tmuxfix.SeedSession {
		return []tmuxfix.SeedSession{heldSession(name, "$4", tmux.Label{}, false)}
	}
	script := func(s tmuxfix.Script) func(e heldEnv) {
		s.Times = 1
		return func(e heldEnv) { e.rec.Script(e.socket, s, tmux.CallLookup) }
	}
	cases := []struct {
		name     string
		holders  func(e heldEnv, id string) []tmuxfix.SeedSession // seeded before the spawn
		late     func(e heldEnv, id string) []tmuxfix.SeedSession // seeded as the scan's lookup returns
		relookup func(e heldEnv)                                  // run as the scan's lookup returns
		vanish   bool                                             // the holder is removed as the create returns
		want     error
		holderID string // the holder's $N the description names
		desc     func(e heldEnv, id string, p apitest.HeldName) apitest.DescCase
		forbid   []string
	}{
		{name: "old label", late: func(e heldEnv, id string) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{e.leftover(name, "$4", id, 0)}
		}, want: api.ErrTmuxSessionConflict, holderID: "$4",
			desc: func(_ heldEnv, _ string, p apitest.HeldName) apitest.DescCase { return apitest.DescHeldLeftover(p) }},
		{name: "foreign label", holders: func(e heldEnv, _ string) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{heldSession(name, "$4", tmuxfix.Valid(tmuxfix.OtherToken, other, e.storeID), true)}
		}, want: api.ErrTmuxSessionConflict, holderID: "$4", forbid: []string{other},
			desc: func(_ heldEnv, _ string, p apitest.HeldName) apitest.DescCase { return apitest.DescHeldDifferentID(p) }},
		{name: "another store's label naming this id", holders: func(e heldEnv, id string) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{heldSession(name, "$4",
				tmuxfix.Valid(tmuxfix.OtherToken, id, apitest.OtherStoreID(e.storeID)), true)}
		}, want: api.ErrTmuxSessionConflict, holderID: "$4",
			desc: func(e heldEnv, _ string, p apitest.HeldName) apitest.DescCase {
				return apitest.DescHeldOtherStore(p, e.storeID)
			}},
		{name: "another store's label naming another id", holders: func(e heldEnv, _ string) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{heldSession(name, "$4",
				tmuxfix.Valid(tmuxfix.OtherToken, other, apitest.OtherStoreID(e.storeID)), true)}
		}, want: api.ErrTmuxSessionConflict, holderID: "$4", forbid: []string{other},
			desc: func(e heldEnv, _ string, p apitest.HeldName) apitest.DescCase {
				return apitest.DescHeldOtherStore(p, e.storeID)
			}},
		{name: "no label", holders: noLabel, want: api.ErrTmuxSessionConflict, holderID: "$4",
			desc: func(_ heldEnv, _ string, p apitest.HeldName) apitest.DescCase { return apitest.DescHeldNoValidID(p) }},
		{name: "invalid label", holders: func(heldEnv, string) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{heldSession(name, "$4", tmux.Label{}, true)}
		}, want: api.ErrTmuxSessionConflict, holderID: "$4",
			desc: func(_ heldEnv, _ string, p apitest.HeldName) apitest.DescCase { return apitest.DescHeldNoValidID(p) }},
		{name: "vanished before the re-lookup", holders: noLabel, vanish: true, want: api.ErrTmuxSessionCreate,
			desc: func(_ heldEnv, _ string, p apitest.HeldName) apitest.DescCase {
				return apitest.DescSessionCreateFailed(apitest.SessionCreateFailed{Name: p.Name, Duplicate: true}).AfterHeldName(p)
			}},
		// Plain spawn names cannot hold $ or \, so two entries with one stored name stand in.
		{name: "more than one listing entry matches", holders: func(heldEnv, string) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{heldSession(name, "$4", tmux.Label{}, false), heldSession(name, "$5", tmux.Label{}, false)}
		}, want: api.ErrTmuxUnresponsive, forbid: []string{"$4", "$5"},
			desc: func(_ heldEnv, _ string, p apitest.HeldName) apitest.DescCase { return apitest.DescHeldAmbiguous(p) }},
		{name: "provenance_conflict", holders: noLabel, relookup: func(e heldEnv) {
			e.rec.SetScope(e.socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{})
		}, want: api.ErrTmuxSessionConflict, holderID: "$4",
			desc: func(_ heldEnv, id string, p apitest.HeldName) apitest.DescCase {
				return apitest.DescConflictingLabels(apitest.ConflictingLabels{InstanceID: id, Scope: true}).AfterHeldName(p)
			}},
		{name: "unreadable: timeout", holders: noLabel, relookup: script(tmuxfix.Script{Failure: tmux.FailTimeout}),
			want: api.ErrTmuxUnresponsive, desc: func(_ heldEnv, _ string, p apitest.HeldName) apitest.DescCase {
				return apitest.DescCallTimeout(tmux.CallLookup, boundQ).AfterHeldName(p)
			}},
		{name: "unreadable: unrecognised reply", holders: noLabel, relookup: script(tmuxfix.Script{
			Failure: tmux.FailUnrecognized, FirstLine: "held: unexpected reply", ExitStatus: 1, HadStdout: true}),
			want: api.ErrTmuxUnresponsive, desc: func(_ heldEnv, _ string, p apitest.HeldName) apitest.DescCase {
				return apitest.DescUnrecognisedReply(tmux.CallLookup, "held: unexpected reply").AfterHeldName(p)
			}},
		{name: "tmux unavailable: missing binary", holders: noLabel, relookup: script(tmuxfix.Script{Failure: tmux.FailUnavailable}),
			want: api.ErrTmuxNotAvailable, desc: func(_ heldEnv, _ string, p apitest.HeldName) apitest.DescCase {
				return apitest.DescTmuxNotRun().AfterHeldName(p)
			}},
		{name: "tmux unavailable: socket permission", holders: noLabel, relookup: script(tmuxfix.Script{Failure: tmux.FailSocketDenied}),
			want: api.ErrTmuxNotAvailable, desc: func(e heldEnv, _ string, p apitest.HeldName) apitest.DescCase {
				return apitest.DescSocketPermission(e.socket).AfterHeldName(p)
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newHeldEnv(t)
			id := heldID()
			var seeded []tmuxfix.SeedSession
			if tc.holders != nil {
				seeded = tc.holders(e, id)
				e.rec.SeedSessions(e.socket, seeded...)
			}
			if tc.vanish {
				e.rec.RemoveSessionAfter(tmux.CallCreate, e.socket, seeded[0].ID)
			}

			run := e.spawnHeld(t, id, name, func() {
				if tc.late != nil {
					late := tc.late(e, id)
					seeded = append(seeded, late...)
					e.rec.SeedSessions(e.socket, late...)
				}
				if tc.relookup != nil {
					tc.relookup(e)
				}
			})

			assertOneSentinel(t, run.err, tc.want)
			token := e.assertEndedRow(t, id, run.createdAt)
			if run.stateAtRelookup != store.StateEnded {
				t.Errorf("row state when the re-lookup returned = %v; want ended (end write first)", run.stateAtRelookup)
			}
			if run.err != nil {
				_, desc := errnames.Classify(run.err)
				p := apitest.HeldName{Name: name, SessionID: tc.holderID, Row: apitest.HeldRowEnded, InstanceID: id}
				forbid := append(e.forbid(id, seeded), append(tc.forbid, token)...)
				apitest.AssertDescription(t, desc, tc.desc(e, id, p), forbid...)
			}
			if strings.Contains(e.logs.String(), "WARN") {
				t.Errorf("client log = %q; want no WARN after an applied end write", e.logs.String())
			}

			// Scan, create, re-lookup: nothing else, so nothing targets the holder.
			if got, want := callKinds(e.rec), []tmux.Call{tmux.CallLookup, tmux.CallCreate, tmux.CallLookup}; !reflect.DeepEqual(got, want) {
				t.Errorf("tmux calls = %q; want %q", got, want)
			}
			if n := len(e.rec.Calls()); n != 0 {
				t.Errorf("name-based calls = %d; want 0", n)
			}
			if len(run.sessionsAtCreate) == 0 && !tc.vanish {
				t.Fatalf("no holder on %s when the create returned", e.socket)
			}
			if after := e.rec.Sessions(e.socket); !reflect.DeepEqual(after, run.sessionsAtCreate) {
				t.Errorf("sessions changed after the create:\nat create %+v\nafter     %+v", run.sessionsAtCreate, after)
			}
		})
	}
}

// TestSpawnHeldCeiling: the held-name path charges the scan, create and
// re-lookup (minted id: create and re-lookup) and stays within SR-13.2's path (ii).
func TestSpawnHeldCeiling(t *testing.T) {
	const name = "held-ceiling"
	cases := []struct {
		name      string
		id        string
		wantCalls []tmux.Call
		want      time.Duration // SR-13.2 path (ii) at the defaults
	}{
		{"caller-supplied id", heldID(), []tmux.Call{tmux.CallLookup, tmux.CallCreate, tmux.CallLookup},
			2*boundQ + boundC + 3*heldW},
		{"minted id", "", []tmux.Call{tmux.CallCreate, tmux.CallLookup}, boundQ + boundC + 2*heldW},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newHeldEnv(t)
			e.rec.SeedSessions(e.socket, heldSession(name, "$4", tmux.Label{}, false))

			_, err := e.c.Spawn(api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: tc.id,
				TmuxSessionName: name, TmuxSessionNameSupplied: true})

			assertOneSentinel(t, err, api.ErrTmuxSessionConflict)
			calls := callKinds(e.rec)
			if !reflect.DeepEqual(calls, tc.wantCalls) {
				t.Fatalf("tmux calls = %q; want %q", calls, tc.wantCalls)
			}
			charged := e.clock.Now().Sub(e.start)
			if got := charged + time.Duration(len(calls))*heldW; got != tc.want || got > boundCap {
				t.Errorf("charged %v + %d pipe-close waits = %v; want %v, at most %v", charged, len(calls), got, tc.want, boundCap)
			}
		})
	}
}
