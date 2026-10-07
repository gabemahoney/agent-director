package api_test

// kill_optin_table_test.go: kill with the finished-row opt-in on an ended and
// a missing row walks SR-6.5's finished-row table in resume's order (Gone,
// Leftover, Can't tell, tmux unavailable, Ours inside the stopping window,
// Ours younger than the bound, Ours past both), at SR-4.2's default
// boundaries (SR-20.6). Every refusal sends nothing and every row stays as it
// was. The reported-in boundaries are kill_optin_reported_test.go's; the
// configured values kill_optin_config_test.go's.

import (
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// kftStates are the finished states every case runs on.
var kftStates = []string{store.StateEnded, store.StateMissing}

// kftOutcome is what one opt-in kill must produce: the error name ("" is
// success) with its description case and extra forbidden values, kill_sent,
// the recorded calls in order and ad.kill.called's lookup_outcome.
type kftOutcome struct {
	errName string
	desc    apitest.DescCase
	forbid  []string
	sent    bool
	calls   []tmux.Call
	lookup  string
}

// kftWant is an Ours row's expected answer.
type kftWant int

const (
	kftKilled          kftWant = iota // the kill sequence, the agent exiting at its pane kill
	kftStopping                       // "appears to still be stopping"
	kftStarting                       // "appears to still be starting"
	kftNeverReportedIn                // "never reported in", no kill
)

// kftRefusal is a refusal of name with desc after the lookup alone, whose token is lookup.
func kftRefusal(name string, desc apitest.DescCase, lookup string) kftOutcome {
	return kftOutcome{errName: name, desc: desc, calls: []tmux.Call{tmux.CallLookup}, lookup: lookup}
}

// kftOursOutcome is w's outcome for s's Ours row r under bound and window.
func kftOursOutcome(w kftWant, s startingRow, r resumeRow, bound, window time.Duration) kftOutcome {
	switch w {
	case kftStopping:
		return kftRefusal("ErrTmuxUnresponsive", apitest.DescStillStopping(s.startingCase(r, bound, window)), "ours")
	case kftStarting:
		return kftRefusal("ErrTmuxUnresponsive", apitest.DescStillStarting(s.startingCase(r, bound, window)), "ours")
	case kftNeverReportedIn:
		return kftRefusal("ErrTmuxSessionConflict", apitest.DescKillOptInNeverReportedIn(r.ID, r.Name, bound), "ours")
	}
	return kftOutcome{sent: true, calls: killOursCalls, lookup: "ours"}
}

// kftScrub replaces r's TMUX_TMPDIR in s: that per-test directory carries the
// test's name, which spells the opt-in the description check forbids.
func kftScrub(r resumeRow, s string) string {
	return strings.ReplaceAll(s, filepath.Dir(filepath.Dir(r.Socket)), "$TMUX_TMPDIR")
}

// kftBefore is what an opt-in kill must leave as it was: the row and its socket's sessions.
type kftBefore struct {
	cols     apitest.SpawnColumns
	sessions []tmuxfix.SeedSession
}

// kftSnap takes r's kftBefore; take it just before the kill.
func (e *killEnv) kftSnap(t *testing.T, r resumeRow) kftBefore {
	t.Helper()
	return kftBefore{cols: e.columns(t, r.ID), sessions: e.rec.Sessions(r.Socket)}
}

// kftCheck fails unless res and err are o's, the calls are o.calls, the row is
// unchanged, the sessions are untouched unless o.sent (else the kills hit r's
// pane and session by id) and the one ad.kill.called records the opt-in.
func (e *killEnv) kftCheck(t *testing.T, r resumeRow, before kftBefore, res api.KillResult, err error, o kftOutcome) {
	t.Helper()
	outcome := "ok"
	if o.errName == "" {
		if err != nil {
			t.Fatalf("kill: %v; want success", err)
		}
		if res.KillSent != o.sent {
			t.Errorf("kill_sent = %v; want %v", res.KillSent, o.sent)
		}
	} else {
		assertOneName(t, err, o.errName)
		apitest.AssertDescription(t, kftScrub(r, err.Error()), o.desc, append([]string{r.Token, r.StoreID}, o.forbid...)...)
		outcome = o.errName
	}
	e.assertKillCalls(t, o.calls...)
	e.assertRowUnchanged(t, r.ID, before.cols)
	if !o.sent {
		if got := e.rec.Sessions(r.Socket); !reflect.DeepEqual(got, before.sessions) {
			t.Errorf("sessions on %s changed:\n got %+v\nwant %+v", r.Socket, got, before.sessions)
		}
	} else if got := seqTarget(e, tmux.CallKillPane); got != r.Spawn.Identity.PaneID {
		t.Errorf("pane kill targets %q; want the agent's pane %q", got, r.Spawn.Identity.PaneID)
	}
	if o.sent && slices.Contains(o.calls, tmux.CallKillSession) {
		if got := seqTarget(e, tmux.CallKillSession); got != r.Session.ID || seqHas(e, r.Socket, r.Session.ID) {
			t.Errorf("session kill targets %q (still listed %t); want the row's session %q gone",
				got, seqHas(e, r.Socket, r.Session.ID), r.Session.ID)
		}
	}
	kolAssertCalled(t, r.ID, map[string]any{"include_finished": true, "outcome": outcome, "kill_sent": o.sent,
		"lookup_outcome": o.lookup})
}

// kftOurs seeds s's row on a new fixture (its agent exits at its pane kill),
// kills it through call and checks w's outcome under bound and window.
func kftOurs(t *testing.T, s startingRow, w kftWant, bound, window time.Duration,
	call func(e *killEnv, r resumeRow) (api.KillResult, error)) {
	t.Helper()
	e := newKillEnv(t)
	r := e.seedStarting(t, s)
	e.setAfterCall(tmux.CallKillPane, procfix.Gone(), r.AgentPID)
	before := e.kftSnap(t, r)
	res, err := call(e, r)
	e.kftCheck(t, r, before, res, err, kftOursOutcome(w, s, r, bound, window))
}

// kftKill is kftOurs' call through api.Kill at e.cfg's durations.
func kftKill(e *killEnv, r resumeRow) (api.KillResult, error) { return e.killOptIn(r.ID) }

// TestKillIncludeFinishedTableBoundaries: Ours with ended_at window-1 s or the
// window ago and a reported-in session bound-1 s or the bound old; the window decides first.
func TestKillIncludeFinishedTableBoundaries(t *testing.T) {
	t.Parallel()
	ended := []struct {
		name   string
		ago    time.Duration
		inside bool
	}{{"ended window-1 s ago", defWindow - time.Second, true}, {"ended the window ago", defWindow, false}}
	sessions := []struct {
		name  string
		age   time.Duration
		young bool
	}{{"session bound-1 s old", defBound - time.Second, true}, {"session the bound old", defBound, false}}
	for _, state := range kftStates {
		for _, en := range ended {
			for _, s := range sessions {
				want := kftKilled
				switch {
				case en.inside:
					want = kftStopping
				case s.young:
					want = kftStarting
				}
				t.Run(state+", "+en.name+", "+s.name, func(t *testing.T) {
					kftOurs(t, startingRow{state: state, endedAgo: en.ago, age: s.age}, want, defBound, defWindow, kftKill)
				})
			}
		}
		t.Run(state+", ended the window ago, session the bound old, no pid", func(t *testing.T) {
			kftOurs(t, startingRow{state: state, endedAgo: defWindow, age: defBound, noPID: true},
				kftNeverReportedIn, defBound, defWindow, kftKill)
		})
	}
}

// kftCase is one non-Ours row of the table: its finished row (state set per
// run), the world built around it (returning other rows' ids to forbid), the
// agent's exit at its pane kill, and the outcome.
type kftCase struct {
	name     string
	row      startingRow
	world    func(t *testing.T, e *killEnv, r *resumeRow) []string
	paneEnds bool
	want     func(e *killEnv, r resumeRow) kftOutcome
}

// kftScript scripts s on every lookup of r's socket.
func kftScript(s tmuxfix.Script) func(*testing.T, *killEnv, *resumeRow) []string {
	return func(_ *testing.T, e *killEnv, r *resumeRow) []string {
		e.rec.Script(r.Socket, s, tmux.CallLookup)
		return nil
	}
}

// kftHolder seeds k's holder of r's name.
func kftHolder(k holderKind) func(*testing.T, *killEnv, *resumeRow) []string {
	return func(t *testing.T, e *killEnv, r *resumeRow) []string {
		h := e.seedHolder(t, r.killRow, k)
		return []string{h.Label.InstanceID}
	}
}

// kftFixed is a want that does not depend on the seeded row.
func kftFixed(o kftOutcome) func(*killEnv, resumeRow) kftOutcome {
	return func(*killEnv, resumeRow) kftOutcome { return o }
}

// kftNoPane is ErrTmuxKillFailed with no pane of the running agent found: the lookup and one listing, nothing sent.
func kftNoPane(_ *killEnv, r resumeRow) kftOutcome {
	return kftOutcome{errName: "ErrTmuxKillFailed", desc: apitest.DescKillNoPane(r.ID, r.Name, r.AgentPID),
		calls: []tmux.Call{tmux.CallLookup, tmux.CallListPanes}, lookup: "gone"}
}

// kftCases are the table's Gone, Leftover, Can't tell and tmux-unavailable
// rows. Unless a row says otherwise its own session is past both and reported
// in, so only the row itself stops a kill.
func kftCases() []kftCase {
	pastBoth := startingRow{endedAgo: defWindow, age: defWindow + defBound}
	goneRow := func(a agentState, endedAgo time.Duration) startingRow {
		return startingRow{endedAgo: endedAgo, agent: a, noSession: true}
	}
	gone := kftFixed(kftOutcome{calls: []tmux.Call{tmux.CallLookup}, lookup: "gone"})
	differentServer := func(t *testing.T, e *killEnv, r *resumeRow) []string {
		e.rec.RebindServer(r.Socket, tmuxfix.Server{})
		e.syncServers()
		e.seedBystander(t, r.Socket)
		return nil
	}
	return []kftCase{
		{name: "Gone, agent process gone", row: goneRow(agentGone, defWindow), want: gone},
		{name: "Gone, no agent process recorded", row: goneRow(agentNotRecorded, defWindow), want: gone},
		// AC-KILL-12: a holder of the name is not the row's session, so with
		// the agent known dead the kill succeeds and the holder still runs.
		{name: "Gone, agent process gone, name held by an unlabelled session", row: goneRow(agentGone, defWindow),
			world: kftHolder(holderNone), want: gone},
		{name: "Gone, agent process gone, name held by another row's session", row: goneRow(agentGone, defWindow),
			world: kftHolder(holderForeign), want: gone},
		{name: "Gone, agent pane shown in a viewer session, inside the window", row: goneRow(agentAlive, defWindow-time.Second),
			paneEnds: true,
			world: func(t *testing.T, e *killEnv, r *resumeRow) []string {
				e.seedViewer(t, r.killRow)
				return nil
			}, want: kftFixed(kftOutcome{sent: true, calls: seqGonePane, lookup: "gone"})},
		{name: "Gone, agent running with no pane found", row: goneRow(agentAlive, defWindow), want: kftNoPane},
		{name: "Gone, agent running, name held by an unlabelled session", row: goneRow(agentAlive, defWindow),
			world: kftHolder(holderNone), want: kftNoPane},
		{name: "Gone, agent running, name held by another row's session", row: goneRow(agentAlive, defWindow),
			world: kftHolder(holderForeign), want: kftNoPane},
		{name: "Leftover", row: goneRow(agentAlive, defWindow),
			world: func(t *testing.T, e *killEnv, r *resumeRow) []string {
				e.seedSession(t, &r.killRow, tmuxfix.WithRowSessionLabel(r.old(), true), e.createdBefore(defWindow+defBound))
				return nil
			}, want: func(_ *killEnv, r resumeRow) kftOutcome {
				return kftRefusal("ErrTmuxSessionConflict", apitest.DescKillOptInNeverReportedInLeftover(r.ID,
					[]apitest.DescSession{{Name: r.Session.Name, ID: r.Session.ID}}), "leftover")
			}},
		{name: "Can't tell, different server", row: pastBoth, world: differentServer,
			want: func(_ *killEnv, r resumeRow) kftOutcome {
				return kftRefusal("ErrTmuxNotAvailable", apitest.DescDifferentServer(r.ID), "different_server")
			}},
		{name: "Can't tell, scope value", row: pastBoth,
			world: func(_ *testing.T, e *killEnv, r *resumeRow) []string {
				e.rec.SetScope(r.Socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{})
				return nil
			}, want: func(_ *killEnv, r resumeRow) kftOutcome {
				return kftRefusal("ErrTmuxSessionConflict", apitest.DescConflictingLabels(apitest.ConflictingLabels{
					InstanceID: r.ID, Scope: true, NothingWasDone: true}), "provenance_conflict")
			}},
		{name: "Can't tell, duplicate label", row: pastBoth,
			world: func(t *testing.T, e *killEnv, r *resumeRow) []string {
				e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "dup-" + r.ID, Label: r.current()})
				return nil
			}, want: func(e *killEnv, r resumeRow) kftOutcome {
				var carrying []apitest.DescSession
				for _, s := range e.rec.Sessions(r.Socket) {
					if s.Label == r.current() {
						carrying = append(carrying, apitest.DescSession{Name: s.Name, ID: s.ID})
					}
				}
				return kftRefusal("ErrTmuxSessionConflict", apitest.DescConflictingLabels(apitest.ConflictingLabels{
					InstanceID: r.ID, Sessions: carrying, NothingWasDone: true}), "provenance_conflict")
			}},
		{name: "Can't tell, lookup timeout", row: pastBoth, world: kftScript(tmuxfix.Script{Failure: tmux.FailTimeout}),
			want: func(e *killEnv, _ resumeRow) kftOutcome {
				return kftRefusal("ErrTmuxUnresponsive", apitest.DescCallTimeout(tmux.CallLookup, e.cfg.EffectiveQueryTimeout()),
					"cant_tell")
			}},
		{name: "Can't tell, unrecognised reply", row: pastBoth,
			world: kftScript(tmuxfix.Script{Failure: tmux.FailUnrecognized, FirstLine: callTableFirstLine(), ExitStatus: 1}),
			want: kftFixed(kftRefusal("ErrTmuxUnresponsive", apitest.DescUnrecognisedReply(tmux.CallLookup, callTableFirstLine()),
				"cant_tell"))},
		{name: "Can't tell, own young session's creation time unreadable",
			row:   startingRow{endedAgo: defWindow + defBound, age: defBound - time.Second},
			world: kftScript(tmuxfix.Script{Failure: tmux.FailUnrecognized, HadStdout: true}),
			want: func(_ *killEnv, r resumeRow) kftOutcome {
				desc := apitest.DescUnrecognisedReply(tmux.CallLookup, "")
				young := startingRow{endedAgo: defWindow + defBound, age: defBound - time.Second}
				for _, p := range apitest.DescStillStarting(young.startingCase(r, defBound, defWindow)).Require {
					if p != strconv.Quote(r.Name) {
						desc.MustNot = append(desc.MustNot, p)
					}
				}
				return kftRefusal("ErrTmuxUnresponsive", desc, "cant_tell")
			}},
		{name: "tmux unavailable, missing binary", row: pastBoth, world: kftScript(tmuxfix.Script{Failure: tmux.FailUnavailable}),
			want: kftFixed(kftRefusal("ErrTmuxNotAvailable", apitest.DescTmuxNotRun(), "tmux_unavailable"))},
		{name: "tmux unavailable, socket permission", row: pastBoth, world: kftScript(tmuxfix.Script{Failure: tmux.FailSocketDenied}),
			want: func(_ *killEnv, r resumeRow) kftOutcome {
				return kftRefusal("ErrTmuxNotAvailable", apitest.DescSocketPermission(kftScrub(r, r.Socket)), "tmux_unavailable")
			}},
	}
}

// TestKillIncludeFinishedTable: each Gone, Leftover, Can't tell and
// tmux-unavailable row on an ended and a missing row; only Ours reaches the
// window, the bound and the reported-in rule.
func TestKillIncludeFinishedTable(t *testing.T) {
	t.Parallel()
	for _, state := range kftStates {
		for _, tc := range kftCases() {
			t.Run(state+", "+tc.name, func(t *testing.T) {
				t.Parallel() // each case has its own fixture and random ids
				e := newKillEnv(t)
				row := tc.row
				row.state = state
				r := e.seedStarting(t, row)
				var forbid []string
				if tc.world != nil {
					forbid = tc.world(t, e, &r)
				}
				if tc.paneEnds {
					e.setAfterCall(tmux.CallKillPane, procfix.Gone(), r.AgentPID)
				}
				before := e.kftSnap(t, r)
				res, err := e.killOptIn(r.ID)
				o := tc.want(e, r)
				o.forbid = append(o.forbid, forbid...)
				e.kftCheck(t, r, before, res, err, o)
			})
		}
	}
}
