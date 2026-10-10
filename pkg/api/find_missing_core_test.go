package api_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
)

// find-missing's core sweep behaviour on the fake store (SR-11.1, SR-11.3, SR-20.6); fakes and runFindMissing live
// in find_missing_test.go.

// TestFindMissingNullPidFallbackGuardFree: after a reboot every recorded process is gone and every row is marked
// (no degraded-mode refusal); a row with no identity, or a start time with no pid, takes no reader call and is
// marked tmux_absent when its lookup is Gone.
func TestFindMissingNullPidFallbackGuardFree(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	pc := procfix.New() // empty table: every pid answers gone
	st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{
		liveRow("a", withSessionStart(401, fmStart)),
		liveRow("b", withPane(402, fmStart)),
		liveRow("c", withSessionStart(403, fmStart), withPane(403, fmStart)),
		liveRow("d"),
		liveRow("e", withSessionStart(0, fmStart)),
		liveRow("f", withPane(0, fmStart)),
	}}
	before := len(readAPITrailLines(t))

	res := mustFindMissing(t, st, pc)
	assertLists(t, res, []string{"a", "b", "c", "d", "e", "f"}, nil)
	for id, reason := range map[string]string{"a": "proc_absent", "b": "proc_absent", "d": "tmux_absent", "e": "tmux_absent", "f": "tmux_absent"} {
		assertMarkReason(t, before, id, reason)
	}
	if got := pc.StartTimeCalls(); !slices.Equal(got, []int{401, 402, 403}) {
		t.Errorf("StartTime calls = %v; want [401 402 403] (none for the rows recording no pid)", got)
	}
}

// TestFindMissingZeroLiveRowsIsNoopSuccess: no live rows is a no-op: no reader or tmux call, no write, no log
// line, empty non-nil lists.
func TestFindMissingZeroLiveRowsIsNoopSuccess(t *testing.T) {
	t.Parallel()
	pc := procfix.New()
	st := &fakeFindMissingStore{}
	lg := &recordingLogger{}
	rec := tmuxfix.NewRecorder()

	res := mustSweep(t, st, pc, fmSweep{lg: lg, tmux: rec})
	assertLists(t, res, nil, nil)
	if res.IDs == nil || res.UnverifiedIDs == nil {
		t.Errorf("ids=%#v unverified_ids=%#v; want non-nil empty slices", res.IDs, res.UnverifiedIDs)
	}
	if len(pc.StartTimeCalls()) != 0 || len(st.calls) != 0 || len(lg.lines) != 0 {
		t.Errorf("reader calls=%v writes=%+v log=%v; want none", pc.StartTimeCalls(), st.calls, lg.lines)
	}
	assertLookups(t, rec, 0)
}

// TestFindMissingListErrorAborts: a live-row read error fails the sweep before any reader or tmux call, with
// empty non-nil lists (b.hbt).
func TestFindMissingListErrorAborts(t *testing.T) {
	t.Parallel()
	pc := procfix.New()
	rec := tmuxfix.NewRecorder()
	res, err := runFindMissing(&fakeFindMissingStore{listErr: errSentinel}, pc, fmSweep{tmux: rec})
	if !errors.Is(err, errSentinel) {
		t.Fatalf("FindMissing err = %v; want the list error %v to bubble up", err, errSentinel)
	}
	if got, want := jsonOf(t, res), `{"count":0,"ids":[],"unverified":0,"unverified_ids":[]}`; got != want {
		t.Errorf("FindMissing = %s; want %s", got, want)
	}
	if got := pc.StartTimeCalls(); len(got) != 0 {
		t.Errorf("StartTime calls = %v; want none", got)
	}
	assertLookups(t, rec, 0)
}
