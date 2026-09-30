package api_test

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// find-missing's process-only liveness (SR-3.8, SR-11.1; AC-FM-19, AC-FM-10): a row is judged by its selected
// agent process alone, never by tmux or a process environment. Fakes and runFindMissing live in
// find_missing_test.go; the start-time answers by themselves are pinned in find_missing_verdict_test.go.

// The SessionStart and pane pids the selection table records.
const (
	fpSSPID   = 1101
	fpPanePID = 1102
)

// fpOutcome is what one sweep does to a row: left live untouched, marked missing, or noted probe_eacces (the
// sweeps here get a lookup that cannot tell, so a row the process cannot decide is noted).
type fpOutcome int

const (
	fpLive fpOutcome = iota
	fpMarked
	fpNoted
)

// fpPastGrace is a launch start one second past the default pending grace period at fmNow.
var fpPastGrace = fmNow.Add(-fmGrace - time.Second).UnixMilli()

// fpSelections are the agent-process selections (SR-3.8): which recorded identities the row holds, the pid the
// sweep must read, and the other recorded pid (0 = none) it must never read.
var fpSelections = []struct {
	name            string
	opts            []fmRowOpt
	selected, other int
	pidOnly         bool
}{
	{name: "sessionstart only", opts: []fmRowOpt{withSessionStart(fpSSPID, fmStart)}, selected: fpSSPID},
	{name: "pane only", opts: []fmRowOpt{withPane(fpPanePID, fmStart)}, selected: fpPanePID},
	{name: "both agree", opts: []fmRowOpt{withSessionStart(fpSSPID, fmStart), withPane(fpSSPID, fmStart)}, selected: fpSSPID},
	{name: "different pids", opts: []fmRowOpt{withSessionStart(fpSSPID, fmStart), withPane(fpPanePID, fmStart)},
		selected: fpPanePID, other: fpSSPID},
	// The SessionStart start time is fmOtherStart, so the "other start time" answer is its own.
	{name: "equal pids, different start times", opts: []fmRowOpt{withSessionStart(fpPanePID, fmOtherStart), withPane(fpPanePID, fmStart)},
		selected: fpPanePID},
	{name: "sessionstart pid-only", opts: []fmRowOpt{withSessionStart(fpSSPID, "")}, selected: fpSSPID, pidOnly: true},
	{name: "pane pid-only", opts: []fmRowOpt{withPane(fpPanePID, "")}, selected: fpPanePID, pidOnly: true},
}

// fpAnswers are the reader's answers for the selected pid, each with a decoy for the unselected pid that would
// give the opposite verdict, and the outcome for a full identity and for a pid-only one.
var fpAnswers = []struct {
	name          string
	proc, decoy   procfix.Process
	full, pidOnly fpOutcome
}{
	{"alive", procfix.Alive(fmStart), procfix.Gone(), fpLive, fpNoted},
	{"gone", procfix.Gone(), procfix.Alive(fmStart), fpMarked, fpMarked},
	{"zombie", procfix.Zombie(), procfix.Alive(fmStart), fpMarked, fpMarked},
	{"other start time", procfix.Alive(fmOtherStart), procfix.Alive(fmStart), fpMarked, fpNoted},
	{"unreadable", procfix.Unreadable(), procfix.Alive(fmStart), fpNoted, fpNoted},
}

// fpLives are the rows past the grace period the table covers: a non-pending row and pending rows of a fresh
// spawn and of a resumed launch (a later row version, session kept).
var fpLives = []struct {
	name string
	opts []fmRowOpt
	snap func(*store.RowSnapshot)
}{
	{name: "working", snap: func(*store.RowSnapshot) {}},
	{name: "pending fresh spawn", opts: []fmRowOpt{withLaunch(store.StatePending, fpPastGrace)}, snap: func(*store.RowSnapshot) {}},
	{name: "pending resumed launch", opts: []fmRowOpt{withLaunch(store.StatePending, fpPastGrace)},
		snap: func(s *store.RowSnapshot) { s.RowVersion, s.ClaudeSessionID = 9, "sess-fp-resumed" }},
}

// fpAssertOutcome fails unless the sweep result res and the writes st recorded show outcome for row r.
func fpAssertOutcome(t *testing.T, st *fakeFindMissingStore, res api.FindMissingResult, r store.LiveSpawnIdentity, outcome fpOutcome) {
	t.Helper()
	id := r.ClaudeInstanceID
	var wantOps, ids, unver []string
	switch outcome {
	case fpMarked:
		wantOps, ids = []string{"mark", "close"}, []string{id}
	case fpNoted:
		wantOps, unver = []string{"note"}, []string{id}
	}
	assertLists(t, res, ids, unver)
	if got := st.ops(id); !equalStrings(got, wantOps) {
		t.Errorf("writes on %s = %v; want %v (a mark folds the liveness clear in)", id, got, wantOps)
	}
	for _, c := range st.calls {
		if c.op != "close" && c.snap != r.Snapshot {
			t.Errorf("%s guarded on %+v; want the read snapshot %+v", c.op, c.snap, r.Snapshot)
		}
		if c.op == "note" && c.note != "probe_eacces" {
			t.Errorf("note written = %q; want probe_eacces", c.note)
		}
	}
}

// fpAssertNoDisagree fails if any ad.provenance.disagree record for id was written after trail line mark.
func fpAssertNoDisagree(t *testing.T, mark int, id string) {
	t.Helper()
	for _, rec := range trailSince(t, mark, "ad.provenance.disagree") {
		if rec["claude_instance_id"] == id {
			t.Errorf("ad.provenance.disagree record for %s = %v; want none (pid_mismatch is retired)", id, rec)
		}
	}
}

// TestFindMissingProcessSelectionVerdict: each agent-process selection crossed with each reader answer, on a
// live row and on pending rows past grace: only the selected process is read and decides the row.
func TestFindMissingProcessSelectionVerdict(t *testing.T) {
	for _, life := range fpLives {
		for _, sel := range fpSelections {
			for _, ans := range fpAnswers {
				t.Run(life.name+"/"+sel.name+"/"+ans.name, func(t *testing.T) {
					pc := procfix.New()
					pc.Set(sel.selected, ans.proc)
					if sel.other != 0 {
						pc.Set(sel.other, ans.decoy)
					}
					r := liveRow("fp-row", append(slices.Clone(life.opts), sel.opts...)...)
					life.snap(&r.Snapshot)
					st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{r}}
					mark := trailLen(t)

					res := mustSweep(t, st, pc, fmSweep{tmux: fmCantTell()})
					want := ans.full
					if sel.pidOnly {
						want = ans.pidOnly
					}
					fpAssertOutcome(t, st, res, r, want)
					if got := pc.StartTimeCalls(); !slices.Equal(got, []int{sel.selected}) {
						t.Errorf("StartTime calls = %v; want [%d] (the selected process only)", got, sel.selected)
					}
					fpAssertNoDisagree(t, mark, r.ClaudeInstanceID)
				})
			}
		}
	}
}

// TestFindMissingChildOrLeakedIDNeverKeepsRow: a live process carrying the row's id in its environment (a child
// of the dead agent, an unrelated process) never keeps the row alive; no environment is read.
func TestFindMissingChildOrLeakedIDNeverKeepsRow(t *testing.T) {
	const agent, child, unrelated = 1201, 1202, 1203
	cases := []struct {
		name     string
		identity fmRowOpt
		dead     procfix.Process
		leaks    []int
	}{
		{"child of a gone pane agent", withPane(agent, fmStart), procfix.Gone(), []int{child}},
		{"child of a zombie sessionstart agent", withSessionStart(agent, fmStart), procfix.Zombie(), []int{child}},
		{"unrelated process, agent pid reused", withSessionStart(agent, fmStart), procfix.Alive(fmOtherStart), []int{unrelated}},
		{"child and unrelated process", withPane(agent, fmStart), procfix.Gone(), []int{child, unrelated}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := liveRow("leaked", tc.identity)
			pc := procfix.New()
			pc.Set(agent, tc.dead)
			for _, pid := range tc.leaks {
				pc.Set(pid, procfix.Alive(fmStart).WithEnv(map[string]string{probe.EnvKey: r.ClaudeInstanceID}))
			}
			st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{r}}

			res := mustFindMissing(t, st, pc)
			fpAssertOutcome(t, st, res, r, fpMarked)
			if got := pc.StartTimeCalls(); !slices.Equal(got, []int{agent}) {
				t.Errorf("StartTime calls = %v; want [%d] (never the process carrying the id)", got, agent)
			}
			if n := pc.EnvReads(); n != 0 {
				t.Errorf("environment reads = %d; want 0", n)
			}
		})
	}
}

// TestFindMissingLiveProcessWithoutIDKeepsRow: a recorded process alive with its start time keeps the row live
// whatever its environment holds; the removed environment tiebreaker would have marked it.
func TestFindMissingLiveProcessWithoutIDKeepsRow(t *testing.T) {
	const agent = 1301
	for name, env := range map[string]map[string]string{
		"no environment":             nil,
		"environment without the id": {"PATH": "/usr/bin"},
		"another row's id":           {probe.EnvKey: "someone-else"},
	} {
		t.Run(name, func(t *testing.T) {
			pc := procfix.New()
			pc.Set(agent, procfix.Alive(fmStart).WithEnv(env))
			r := liveRow("kept", withSessionStart(agent, fmStart), withPane(agent, fmStart))
			st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{r}}

			res := mustFindMissing(t, st, pc)
			fpAssertOutcome(t, st, res, r, fpLive)
			if n := pc.EnvReads(); n != 0 {
				t.Errorf("environment reads = %d; want 0", n)
			}
		})
	}
}

// TestFindMissingMixedProcessSweep: one sweep over alive, dead, unreadable, mismatched and unrecorded rows lists
// only the dead rows in ids and reads each selected process exactly once, never an unselected one; tmux can't tell.
func TestFindMissingMixedProcessSweep(t *testing.T) {
	pc := procfix.New()
	for pid, p := range map[int]procfix.Process{
		1401: procfix.Alive(fmStart), 1402: procfix.Gone(), 1403: procfix.Alive(fmOtherStart),
		1404: procfix.Unreadable(), 1405: procfix.Alive(fmStart), 1406: procfix.Gone(),
		1407: procfix.Gone(), 1408: procfix.Alive(fmStart), 1409: procfix.Alive(fmStart),
	} {
		pc.Set(pid, p)
	}
	st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{
		liveRow("m-alive", withSessionStart(1401, fmStart)),
		liveRow("z-dead-pane", withPane(1402, fmStart)),
		liveRow("b-reused", withSessionStart(1403, fmStart), withPane(1403, fmStart)),
		liveRow("q-unreadable", withSessionStart(1404, fmStart)),
		liveRow("c-pane-dead-ss-alive", withSessionStart(1405, fmStart), withPane(1406, fmStart)),
		liveRow("k-pane-alive-ss-dead", withSessionStart(1407, fmStart), withPane(1408, fmStart)),
		liveRow("a-pid-only-alive", withPane(1409, "")),
		liveRow("y-no-identity"),
	}}

	res := mustSweep(t, st, pc, fmSweep{tmux: fmCantTell()})
	assertLists(t, res, []string{"b-reused", "c-pane-dead-ss-alive", "z-dead-pane"},
		[]string{"a-pid-only-alive", "q-unreadable", "y-no-identity"})
	got := pc.StartTimeCalls()
	slices.Sort(got)
	if want := []int{1401, 1402, 1403, 1404, 1406, 1408, 1409}; !slices.Equal(got, want) {
		t.Errorf("StartTime calls (sorted) = %v; want %v (each selected pid once)", got, want)
	}
}

// TestFindMissingProcessMarkOnStore: on a real store, a dead agent's row is marked missing with its launch start
// and liveness columns cleared, while live rows, pending included, keep every column.
func TestFindMissingProcessMarkOnStore(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	seed := func(id, state, sessionID string, opts ...apitest.SpawnOption) {
		t.Helper()
		if _, err := apitest.SeedSpawn(dbPath, id, state, "", "off", sessionID, true, opts...); err != nil {
			t.Fatalf("SeedSpawn(%s): %v", id, err)
		}
	}
	pane := func(pid int) apitest.SpawnOption {
		return apitest.WithLaunchIdentity(store.LaunchIdentity{Socket: apitest.TestSocket, PaneID: apitest.TestPaneID, PanePID: pid, PaneStarttime: fmStart})
	}
	seed("fp-dead-noted", store.StateWorking, "sess-fp-1", apitest.WithNoPane(), apitest.WithPID(1501), apitest.WithProcStarttime(fmStart),
		apitest.WithLivenessNote("probe_eacces"), apitest.WithLivenessUnverifiedSince("2026-09-30 11:30:00"))
	seed("fp-resumed-dead", store.StatePending, "sess-fp-2", apitest.WithLifeNumber(1), apitest.WithLaunchStartedAt(fpPastGrace), pane(1502))
	seed("fp-alive", store.StateWorking, "sess-fp-3", apitest.WithNoPane(), apitest.WithPID(1503), apitest.WithProcStarttime(fmStart))
	seed("fp-pending-alive", store.StatePending, "", apitest.WithLaunchStartedAt(fpPastGrace), pane(1504))
	pc := procfix.New()
	pc.Set(1502, procfix.Alive(fmOtherStart))
	pc.Set(1503, procfix.Alive(fmStart))
	pc.Set(1504, procfix.Alive(fmStart))
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close() //nolint:errcheck
	mark := trailLen(t)

	res := mustFindMissing(t, st, pc)
	assertLists(t, res, []string{"fp-dead-noted", "fp-resumed-dead"}, nil)
	for id, want := range map[string][4]any{
		"fp-dead-noted":    {store.StateMissing, nil, nil, nil},
		"fp-resumed-dead":  {store.StateMissing, nil, nil, nil},
		"fp-alive":         {store.StateWorking, nil, nil, nil},
		"fp-pending-alive": {store.StatePending, fpPastGrace, nil, nil},
	} {
		c, err := apitest.ReadSpawnColumns(dbPath, id)
		if err != nil {
			t.Fatalf("ReadSpawnColumns(%s): %v", id, err)
		}
		if got := [4]any{c.State, c.LaunchStartedAt, c.LivenessNote, c.LivenessUnverifiedSince}; got != want {
			t.Errorf("%s {state, launch_started_at, liveness_note, unverified_since} = %v; want %v", id, got, want)
		}
		fpAssertNoDisagree(t, mark, id)
	}
}
