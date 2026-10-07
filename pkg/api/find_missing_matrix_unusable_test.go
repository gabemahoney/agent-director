package api_test

// find_missing_matrix_unusable_test.go: the pending matrix (find_missing_matrix_test.go) and working and waiting
// rows with an unusable recorded name (SR-3.2, SR-11.1 to SR-11.3, SR-22.8): the process decides first, the
// unusable-name note only when it cannot, with no tmux call; the usable control in the same cell is looked up.

import (
	"fmt"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
)

// mxuState is a row kind the unusable dimension runs on: a pending kind of the matrix, or (opts) a working or
// waiting row; ss records a SessionStart identity agreeing with the recorded pane.
type mxuState struct {
	name string
	k    mxKind
	opts []fmRowOpt
	ss   bool
}

// mxuStates: the matrix's fresh-spawn, resumed and seeded-reuse pending rows, and working and waiting rows.
var mxuStates = []mxuState{
	{name: mxKinds[0].name, k: mxKinds[0]},
	{name: mxKinds[1].name, k: mxKinds[1]},
	{name: mxReuse.name, k: mxReuse},
	{name: "working", k: mxKinds[0], opts: []fmRowOpt{withLaunch(store.StateWorking, 0)}, ss: true},
	{name: "waiting", k: mxKinds[0], opts: []fmRowOpt{withLaunch(store.StateWaiting, 0)}, ss: true},
}

// rowOpts is s's row options for pane p, then name.
func (s mxuState) rowOpts(p mxPane, name string) []fmRowOpt {
	o := append([]fmRowOpt{}, s.opts...)
	if s.ss && !p.none {
		o = append(o, withSessionStart(mxPanePID, fmStart))
	}
	return append(o, fmuNamed(name))
}

// mxuControl is the cell of the usable control name: the matrix's own outcome (one adopted listing when Ours
// and no pane is recorded).
func mxuControl(id string, k mxKind, p mxPane, lk mxLookup, opts []fmRowOpt) mxCell {
	if lk.ours && p.none {
		return mxOursNoPaneCell(id, k, lk, mxListings[0], opts...)
	}
	return mxPlainCell(id, k, p, lk, opts...)
}

// mxuUnusable is the same cell with unusable name f: alive and dead keep the matrix's process outcome; unknown
// and none get f's note and entry tick with no lookup or listing, whatever lk's Recorder would answer.
func mxuUnusable(id string, k mxKind, p mxPane, lk mxLookup, f unusableNameFixture, opts []fmRowOpt) mxCell {
	c := mxPlainCell(id, k, p, lk, opts...)
	if p.fixed == nil {
		c.want = mxWant{ops: []string{"note"}, note: f.note, reason: f.note}
	}
	return c
}

// TestFindMissingUnusableNameMatrix: every row kind x pane evidence x lookup cell with an unusable name (the
// fixtures in turn) and with the usable control: only the name switches an unknown or absent row off the lookup.
// Each cell sweeps its own fake store and reads only its own id's trail records, so the cells run in parallel;
// a cell's two sweeps share its id and stay in order.
func TestFindMissingUnusableNameMatrix(t *testing.T) {
	t.Parallel()
	fixtures, usable := unusableNameFixtures(), usableNameFixture()
	for si, s := range mxuStates {
		for pi, p := range mxPanes {
			for li, lk := range mxLookups {
				if lk.lostReply && !p.none {
					continue // a lost create reply records no pane
				}
				f := fixtures[(li+pi)%len(fixtures)]
				t.Run(s.name+"/"+p.name+"/"+lk.name, func(t *testing.T) {
					t.Parallel()
					k, id := s.k.made(t), fmt.Sprintf("mxu-%d-%d-%d", si, pi, li)
					t.Run(f.label, func(t *testing.T) { runMxCell(t, mxuUnusable(id, k, p, lk, f, s.rowOpts(p, f.raw))) })
					t.Run(usable.label, func(t *testing.T) { runMxCell(t, mxuControl(id, k, p, lk, s.rowOpts(p, usable.raw))) })
				})
			}
		}
	}
}

// TestFindMissingUnusableNameMatrixGrace: a pending row with an unusable name inside its grace period is not
// judged (no reader or tmux call, no write); one with no launch start is past grace and gets its note.
func TestFindMissingUnusableNameMatrixGrace(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		launch int64
		judged bool
	}{
		{"inside grace", fmNow.Add(-(fmGrace - time.Second)).UnixMilli(), false},
		{"no launch start", 0, true},
	}
	for si, s := range mxuStates[:3] {
		for _, f := range fmuReps() {
			for ci, tc := range cases {
				t.Run(s.name+"/"+f.label+"/"+tc.name, func(t *testing.T) { // in order: fixtures share an id
					r := mxRow(fmt.Sprintf("mxu-grace-%d-%d", si, ci), s.k.made(t), true, true,
						withLaunch(store.StatePending, tc.launch), fmuNamed(f.raw))
					want := mxWant{reads: []int{}}
					if tc.judged {
						want = mxWant{ops: []string{"note"}, note: f.note, reason: f.note, reads: []int{mxPanePID}}
					}
					runMxCell(t, mxCell{row: r, pc: mxChecker(mxPanePID, procfix.Unreadable()),
						rec: mxHeldBy(mxRecorded, mxNoLabel)(r, nil), want: want})
				})
			}
		}
	}
}
