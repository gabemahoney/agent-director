package api_test

import (
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// find-missing's process-only liveness (SR-3.8, SR-11.1; AC-FM-19, AC-FM-10): a row is judged by its selected
// agent process alone, never by tmux or a process environment (a process carrying the row's id is the matrix's,
// find_missing_matrix_test.go). Fakes and runFindMissing live in find_missing_test.go.

// The SessionStart and pane pids the selection table records.
const (
	fpSSPID   = 1101
	fpPanePID = 1102
)

// fpOutcome is what one sweep does to a row: left live untouched, marked missing, noted probe_eacces (the
// sweeps here get a lookup that cannot tell, so a row the process cannot decide is noted), or, for a pending row
// whose recorded pane process is alive, noted unreported and left pending, in neither list (b.kdf).
type fpOutcome int

const (
	fpLive fpOutcome = iota
	fpMarked
	fpNoted
	fpUnreported
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

// fpLives are the rows past the grace period the table covers: a non-pending row and a resumed launch's pending
// row (a later row version, session kept).
var fpLives = []struct {
	name string
	opts []fmRowOpt
	snap func(*store.RowSnapshot)
}{
	{name: "working", snap: func(*store.RowSnapshot) {}},
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
		wantOps, ids = []string{"mark"}, []string{id}
	case fpNoted:
		wantOps, unver = []string{"note"}, []string{id}
	case fpUnreported:
		wantOps = []string{"unreported"}
	}
	assertLists(t, res, ids, unver)
	if got := st.ops(id); !equalStrings(got, wantOps) {
		t.Errorf("writes on %s = %v; want %v (a mark folds the liveness clear in)", id, got, wantOps)
	}
	for _, c := range st.calls {
		if c.snap != r.Snapshot {
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
// live row and on pending rows past grace: only the selected process is read and decides the row; a pending row
// it finds alive is noted unreported when it records its pane (b.kdf). The cells sweep their own fake stores and
// run in parallel: the one trail check, that no cell writes a disagree record for the shared id fp-row, holds
// whatever the others write.
func TestFindMissingProcessSelectionVerdict(t *testing.T) {
	t.Parallel()
	for _, life := range fpLives {
		for _, sel := range fpSelections {
			for _, ans := range fpAnswers {
				t.Run(life.name+"/"+sel.name+"/"+ans.name, func(t *testing.T) {
					t.Parallel()
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
					if want == fpLive && r.State == store.StatePending && r.Identity.PanePID > 0 {
						want = fpUnreported
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

// TestFindMissingMixedProcessSweep: one sweep over alive, dead, unreadable, mismatched and unrecorded rows lists
// only the dead rows in ids (sorted) and reads each selected process exactly once, never an unselected one; a
// noted dead row gets the mark alone, its liveness clear folded in; tmux can't tell. The process verdict comes
// before the unusable-name note (a dead row with an unusable name is marked, an alive one's note cleared), and a
// pending row inside grace, read first, is skipped alone: every later row is still judged.
func TestFindMissingMixedProcessSweep(t *testing.T) {
	t.Parallel()
	pc := procfix.New()
	for pid, p := range map[int]procfix.Process{
		1401: procfix.Alive(fmStart), 1402: procfix.Gone(), 1403: procfix.Alive(fmOtherStart),
		1404: procfix.Unreadable(), 1405: procfix.Alive(fmStart), 1406: procfix.Gone(),
		1407: procfix.Gone(), 1408: procfix.Alive(fmStart), 1409: procfix.Alive(fmStart),
		1411: procfix.Gone(), 1412: procfix.Alive(fmStart),
	} {
		pc.Set(pid, p)
	}
	boot := liveRow("0-boot", withLaunch(store.StatePending, startedAgo(fmGrace-time.Second)), withPane(1410, fmStart))
	u := fmuReps()[0]
	st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{
		boot,
		liveRow("d-dead-unusable", withPane(1411, fmStart), fmuNamed(u.raw)),
		liveRow("e-alive-unusable", withSessionStart(1412, fmStart), fmuNamed(u.raw), withNote(u.note)),
		liveRow("m-alive", withSessionStart(1401, fmStart)),
		liveRow("z-dead-pane", withPane(1402, fmStart), withNote("probe_eacces")),
		liveRow("b-reused", withSessionStart(1403, fmStart), withPane(1403, fmStart)),
		liveRow("q-unreadable", withSessionStart(1404, fmStart)),
		liveRow("c-pane-dead-ss-alive", withSessionStart(1405, fmStart), withPane(1406, fmStart)),
		liveRow("k-pane-alive-ss-dead", withSessionStart(1407, fmStart), withPane(1408, fmStart)),
		liveRow("a-pid-only-alive", withPane(1409, "")),
		liveRow("y-no-identity"),
	}}

	res := mustSweep(t, st, pc, fmSweep{tmux: fmCantTell()})
	assertLists(t, res, []string{"b-reused", "c-pane-dead-ss-alive", "d-dead-unusable", "z-dead-pane"},
		[]string{"a-pid-only-alive", "q-unreadable", "y-no-identity"})
	if ops := st.ops("z-dead-pane"); !equalStrings(ops, []string{"mark"}) {
		t.Errorf("writes on z-dead-pane = %v; want [mark] (no separate clear or close)", ops)
	}
	if ops := st.ops("e-alive-unusable"); !equalStrings(ops, []string{"clear"}) {
		t.Errorf("writes on e-alive-unusable = %v; want [clear] (alive wins over the unusable name)", ops)
	}
	assertUntouched(t, boot, st, pc, res)
	got := pc.StartTimeCalls()
	slices.Sort(got)
	if want := []int{1401, 1402, 1403, 1404, 1406, 1408, 1409, 1411, 1412}; !slices.Equal(got, want) {
		t.Errorf("StartTime calls (sorted) = %v; want %v (each selected pid once)", got, want)
	}
}
