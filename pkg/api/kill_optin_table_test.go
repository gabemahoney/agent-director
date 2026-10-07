package api_test

// kill_optin_table_test.go: kill with the finished-row opt-in refuses every
// live row, pending included, with ErrSpawnNotResumable before any lookup
// (SR-6.5, SR-1.4), refuses a finished row's unusable recorded name next
// (SR-3.2), then walks SR-6.5's finished-row table in resume's order (Gone,
// Leftover, Can't tell, tmux unavailable, Ours inside the stopping window,
// Ours younger than the bound, Ours past both; SR-20.6), with the window and
// the bound as configured through api.New or passed to killFinished
// (AC-CFG-02, SR-4.2). Every refusal sends nothing and every row stays as it
// was; ad.kill.called records include_finished (SR-6.4). It holds the runner
// the reported-in boundaries (kill_optin_reported_test.go) use. The opt-in
// never branches on ended against missing, so these rows are ended. Without
// the opt-in a live row is looked up as kill always does (SR-6.8):
// TestKillTrailCalledPerReturnPath and the call table's kill row.

import (
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// kftStates are the finished states the reported-in boundaries run on.
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
		if res.KillSent {
			t.Error("kill_sent = true; want false")
		}
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

// kftCase is one non-Ours row of the table: its finished row, the world built
// around it (returning other rows' ids to forbid), the agent's exit at its
// pane kill, and the outcome.
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
		// AC-KILL-07's "agent running, name held" half: no kill sent, the holder still running.
		{name: "Gone, agent running, name held by an unlabelled session (AC-KILL-07)", row: goneRow(agentAlive, defWindow),
			world: kftHolder(holderNone), want: kftNoPane},
		{name: "Gone, agent running, name held by another row's session (AC-KILL-07)", row: goneRow(agentAlive, defWindow),
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
// tmux-unavailable row; only Ours reaches the window, the bound and the
// reported-in rule.
func TestKillIncludeFinishedTable(t *testing.T) {
	t.Parallel()
	for _, tc := range kftCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel() // each case has its own fixture and random ids
			e := newKillEnv(t)
			row := tc.row
			row.state = store.StateEnded
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

// TestKillIncludeFinishedUnusableName (SR-3.2, SR-6.5): each unusable name on
// a finished row gets its ErrInternal with no tmux call, no process check and
// no disagree record, nothing sent or changed.
func TestKillIncludeFinishedUnusableName(t *testing.T) {
	t.Parallel()
	for _, f := range unusableNameFixtures() {
		t.Run(f.label, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := resumeRow{killRow: e.seedRow(t, killRowSpec{State: store.StateEnded, NoSession: true,
				Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName(f.raw)}})}
			before := e.kftSnap(t, r)

			res, err := e.killOptIn(r.ID)

			e.kftCheck(t, r, before, res, err, kftOutcome{errName: "ErrInternal", desc: f.desc, lookup: tmux.TokenNotRun})
			if n, m := len(e.pc.StartTimeCalls()), e.pc.EnvReads(); n != 0 || m != 0 {
				t.Errorf("process checker consulted (%d start-time calls, %d env reads); want none", n, m)
			}
			if d := killDisagrees(t, r.ID); len(d) != 0 {
				t.Errorf("ad.provenance.disagree records = %v; want none", d)
			}
		})
	}
}

// kolAssertCalled fails unless id has exactly one ad.kill.called record holding want's fields.
func kolAssertCalled(t *testing.T, id string, want map[string]any) {
	t.Helper()
	recs := killCalled(t, id)
	if len(recs) != 1 {
		t.Fatalf("ad.kill.called records = %d; want 1: %v", len(recs), recs)
	}
	for k, v := range want {
		if got, ok := recs[0][k]; !ok || got != v {
			t.Errorf("ad.kill.called %s = %v (present %t); want %v", k, got, ok, v)
		}
	}
}

// kolAssertRefused fails unless err is the live-row refusal of r in state, no
// tmux call was made, the process checker was not consulted, the row and its
// sessions are unchanged and the one ad.kill.called says so.
func kolAssertRefused(t *testing.T, e *killEnv, r killRow, state string, res api.KillResult, err error,
	before apitest.SpawnColumns, sessions []tmuxfix.SeedSession) {
	t.Helper()
	if !errors.Is(err, api.ErrSpawnNotResumable) || res.KillSent {
		t.Fatalf("kill = %+v, %v; want ErrSpawnNotResumable with kill_sent false", res, err)
	}
	apitest.AssertDescription(t, err.Error(), apitest.DescKillOptInLiveRow(r.ID, state), r.Token, r.StoreID)
	e.assertKillCalls(t)
	if n, m := len(e.pc.StartTimeCalls()), e.pc.EnvReads(); n != 0 || m != 0 {
		t.Errorf("process checker consulted (%d start-time calls, %d env reads); want none", n, m)
	}
	e.assertRowUnchanged(t, r.ID, before)
	if got := e.rec.Sessions(r.Socket); !reflect.DeepEqual(got, sessions) {
		t.Errorf("sessions after kill = %+v; want untouched %+v", got, sessions)
	}
	if n := len(killDisagrees(t, r.ID)); n != 0 {
		t.Errorf("ad.provenance.disagree records = %d; want 0", n)
	}
	kolAssertCalled(t, r.ID, map[string]any{"include_finished": true, "outcome": "ErrSpawnNotResumable",
		"lookup_outcome": "not_run", "followup_outcome": "not_run", "process_check": "not_run",
		"kill_sent": false, "pane_killed": false, "tmux_session_name": r.Name})
}

// TestKillIncludeFinishedRefusesLiveRow: each live row with the opt-in gets
// the live-row refusal with no lookup, no process check and no write; its own
// running session is still there. A live row's unusable recorded name keeps
// the live-row refusal.
func TestKillIncludeFinishedRefusesLiveRow(t *testing.T) {
	t.Parallel()
	type liveCase struct {
		name, state string
		spec        killRowSpec
		unusable    bool // the recorded name is unusable, no session seeded
	}
	var cases []liveCase
	for _, s := range []string{store.StatePending, store.StateWaiting, store.StateWorking, store.StateAskUser,
		store.StateCheckPermission} {
		cases = append(cases, liveCase{s, s, killRowSpec{State: s}, false})
	}
	cases = append(cases, liveCase{"waiting, unusable recorded name", store.StateWaiting,
		killRowSpec{NoSession: true, Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName("kill.name")}}, true})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedRow(t, tc.spec)
			before, sessions := e.columns(t, r.ID), e.rec.Sessions(r.Socket)
			if before.State != tc.state {
				t.Fatalf("seeded state %v; want %s", before.State, tc.state)
			}
			if !tc.unusable && len(sessions) == 0 {
				t.Fatalf("no session seeded for %s", r.ID)
			}
			res, err := e.killOptIn(r.ID)
			kolAssertRefused(t, e, r, tc.state, res, err, before, sessions)
		})
	}
}

// kocProbe is one row probing bound and window.
type kocProbe struct {
	name string
	row  startingRow
	want kftWant
}

// kocProbes are bound's and window's probes on an ended row whose session
// reported in: ended window-1 s ago with an old session (stopping), or with a
// session bound-1 s old (the window decides first); ended the window ago with
// a session bound-1 s old (starting), or the bound old (killed when created
// before ended_at, else never reported in).
func kocProbes(bound, window time.Duration) []kocProbe {
	atBound := kftKilled
	if bound <= window {
		atBound = kftNeverReportedIn
	}
	return []kocProbe{
		{"ended window-1 s ago, old session",
			startingRow{state: store.StateEnded, endedAgo: window - time.Second, age: window + bound}, kftStopping},
		{"ended window-1 s ago, session bound-1 s old: the window first",
			startingRow{state: store.StateEnded, endedAgo: window - time.Second, age: bound - time.Second}, kftStopping},
		{"ended the window ago, session bound-1 s old",
			startingRow{state: store.StateEnded, endedAgo: window, age: bound - time.Second}, kftStarting},
		{"ended the window ago, session the bound old",
			startingRow{state: store.StateEnded, endedAgo: window, age: bound}, atBound},
	}
}

// TestKillIncludeFinishedClientSettings: through api.New, the window and the
// bound at their safe minimums decide at value-1 s and value, each leaving the other at its default.
func TestKillIncludeFinishedClientSettings(t *testing.T) {
	t.Parallel()
	minB, minW := int64(config.MinStartingSessionSeconds), int64(config.MinStoppingWindowSeconds)
	cases := []struct {
		name          string
		settings      []apitest.TmuxSetting
		bound, window time.Duration
	}{
		{"window at its safe minimum", []apitest.TmuxSetting{apitest.TmuxInt(config.TmuxStoppingWindowSeconds, minW)},
			defBound, secs(minW)},
		{"bound at its safe minimum", []apitest.TmuxSetting{apitest.TmuxInt(config.TmuxStartingSessionSeconds, minB)},
			secs(minB), defWindow},
		{"both at their safe minimums", []apitest.TmuxSetting{apitest.TmuxInt(config.TmuxStartingSessionSeconds, minB),
			apitest.TmuxInt(config.TmuxStoppingWindowSeconds, minW)}, secs(minB), secs(minW)},
	}
	for _, tc := range cases {
		for _, p := range kocProbes(tc.bound, tc.window) {
			t.Run(tc.name+"/"+p.name, func(t *testing.T) {
				kftOurs(t, p.row, p.want, tc.bound, tc.window, func(e *killEnv, r resumeRow) (api.KillResult, error) {
					res, logs, err := e.killOptInClient(t, r.ID, tc.settings...)
					if logs != "" {
						t.Errorf("Client log = %q; want none", logs)
					}
					return res, err
				})
			})
		}
	}
}

// TestKillIncludeFinishedPassedDurations: killFinished applies the bound and
// window it is given, not the configured defaults: the defaults themselves,
// and below and above them.
func TestKillIncludeFinishedPassedDurations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		bound, window time.Duration
	}{
		{"the defaults", defBound, defWindow},
		{"safe minimums", secs(config.MinStartingSessionSeconds), secs(config.MinStoppingWindowSeconds)},
		{"above the defaults", defWindow + defBound, defBound},
	}
	for _, tc := range cases {
		for _, p := range kocProbes(tc.bound, tc.window) {
			t.Run(tc.name+"/"+p.name, func(t *testing.T) {
				kftOurs(t, p.row, p.want, tc.bound, tc.window, func(e *killEnv, r resumeRow) (api.KillResult, error) {
					return e.killOptInWith(r.ID, tc.bound, tc.window)
				})
			})
		}
	}
}
