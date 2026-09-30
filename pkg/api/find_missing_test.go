package api_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// This file owns the shared find-missing unit-test fakes and the one sweep
// helper (runFindMissing); process answers come from procfix.

// fakeFindMissingStore is api.FindMissingStore over a fixed live-row read. It records every write in order with
// the snapshot it was given and answers each guarded write per row from answers (unset: CondApplied).
type fakeFindMissingStore struct {
	rows    []store.LiveSpawnIdentity
	listErr error
	// answers is keyed by op ("mark", "note", "clear") then instance id.
	answers  map[string]map[string]fmAnswer
	closeErr error
	calls    []storeCall

	provisional []store.ProvisionalTranscript
	healed      []healCall
}

var _ api.FindMissingStore = (*fakeFindMissingStore)(nil)

// fmAnswer is one guarded write's scripted outcome: err when set, else res.
type fmAnswer struct {
	res store.CondResult
	err error
}

// storeCall is one recorded write; op is "mark", "note", "clear" or "close".
type storeCall struct {
	op   string
	id   string
	snap store.RowSnapshot
	note string
}

// healCall records one HealJsonlPath(instanceID, sessionID, jsonlPath) call.
type healCall struct {
	id, sessionID, jsonlPath string
}

func (f *fakeFindMissingStore) ListLiveSpawnIdentities() ([]store.LiveSpawnIdentity, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return slices.Clone(f.rows), nil
}

// result is the scripted answer of op on id.
func (f *fakeFindMissingStore) result(op, id string) (store.CondResult, error) {
	a, ok := f.answers[op][id]
	switch {
	case !ok:
		return store.CondApplied, nil
	case a.err != nil:
		return 0, a.err
	}
	return a.res, nil
}

func (f *fakeFindMissingStore) MarkMissingIfSameLife(id string, examined store.RowSnapshot) (string, store.CondResult, error) {
	f.calls = append(f.calls, storeCall{op: "mark", id: id, snap: examined})
	res, err := f.result("mark", id)
	if err != nil || res != store.CondApplied {
		return "", res, err
	}
	for _, r := range f.rows {
		if r.ClaudeInstanceID == id && r.State != "" {
			return r.State, res, nil
		}
	}
	return store.StateWorking, res, nil
}

func (f *fakeFindMissingStore) SetLivenessNoteIfSameLife(id string, examined store.RowSnapshot, note string) (store.CondResult, error) {
	f.calls = append(f.calls, storeCall{op: "note", id: id, snap: examined, note: note})
	return f.result("note", id)
}

func (f *fakeFindMissingStore) ClearLivenessIfSameLife(id string, examined store.RowSnapshot) (store.CondResult, error) {
	f.calls = append(f.calls, storeCall{op: "clear", id: id, snap: examined})
	return f.result("clear", id)
}

func (f *fakeFindMissingStore) CloseOrphanedPermissionRequests(id string) error {
	f.calls = append(f.calls, storeCall{op: "close", id: id})
	return f.closeErr
}

func (f *fakeFindMissingStore) ListProvisionalTranscripts() ([]store.ProvisionalTranscript, error) {
	return f.provisional, nil
}

func (f *fakeFindMissingStore) HealJsonlPath(id, sessionID, jsonlPath string) (bool, error) {
	f.healed = append(f.healed, healCall{id, sessionID, jsonlPath})
	return true, nil
}

func (f *fakeFindMissingStore) StoreID() string { return "fake-store-id" }

// ops returns the ops of every recorded write on id, in order.
func (f *fakeFindMissingStore) ops(id string) []string {
	var out []string
	for _, c := range f.calls {
		if c.id == id {
			out = append(out, c.op)
		}
	}
	return out
}

// ids returns the instance id of every recorded op write, in order.
func (f *fakeFindMissingStore) ids(op string) []string {
	var out []string
	for _, c := range f.calls {
		if c.op == op {
			out = append(out, c.id)
		}
	}
	return out
}

// recordingLogger captures Printf lines so per-row error tests can assert the continue-and-log behaviour.
type recordingLogger struct {
	lines []string
}

func (r *recordingLogger) Printf(format string, v ...any) {
	r.lines = append(r.lines, fmt.Sprintf(format, v...))
}

// fmGrace is the default pending grace period (SR-11.2) every sweep passes unless a test sets its own.
var fmGrace = time.Duration(config.DefaultPendingGraceSeconds) * time.Second

// fmNow is the fixed sweep clock; fmClock is the now func the seam takes.
var fmNow = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

func fmClock() time.Time { return fmNow }

// fmStart is a recorded start time; fmOtherStart is another (the pid was reused).
const (
	fmStart      = procstarttimefix.LinuxProcStarttime
	fmOtherStart = procstarttimefix.DarwinProcStarttime
)

// fmSweep overrides runFindMissing's defaults; zero fields take fmGrace, fmClock and a fresh recordingLogger.
type fmSweep struct {
	grace time.Duration
	now   func() time.Time
	lg    api.FindMissingLogger
}

// runFindMissing runs one sweep of s judged by pc: the one place the unit tests call the sweep seam.
func runFindMissing(s api.FindMissingStore, pc api.ProcChecker, o fmSweep) (api.FindMissingResult, error) {
	if o.grace == 0 {
		o.grace = fmGrace
	}
	if o.now == nil {
		o.now = fmClock
	}
	if o.lg == nil {
		o.lg = &recordingLogger{}
	}
	return api.FindMissing(context.Background(), s, pc, o.grace, o.now, o.lg)
}

// mustFindMissing is runFindMissing with every default, failing the test on a sweep error.
func mustFindMissing(t *testing.T, s api.FindMissingStore, pc api.ProcChecker) api.FindMissingResult {
	t.Helper()
	res, err := runFindMissing(s, pc, fmSweep{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	return res
}

// fmRowOpt sets fields of a liveRow.
type fmRowOpt func(*store.LiveSpawnIdentity)

// liveRow builds a working row with no process identity or note; its Snapshot is filled from the row after opts.
func liveRow(id string, opts ...fmRowOpt) store.LiveSpawnIdentity {
	r := store.LiveSpawnIdentity{ClaudeInstanceID: id, State: store.StateWorking, TmuxSessionName: "cd-" + id}
	for _, o := range opts {
		o(&r)
	}
	r.Snapshot = store.RowSnapshot{
		RowVersion: 3, StartedAt: "2026-09-30 11:00:00",
		PID: r.PID, ProcStarttime: r.ProcStarttime, TmuxSessionName: r.TmuxSessionName,
	}
	return r
}

// withSessionStart records the SessionStart identity (pid, start; "" = pid-only).
func withSessionStart(pid int, start string) fmRowOpt {
	return func(r *store.LiveSpawnIdentity) { r.PID, r.ProcStarttime = pid, start }
}

// withPane records the pane identity (pid, start; "" = pid-only).
func withPane(pid int, start string) fmRowOpt {
	return func(r *store.LiveSpawnIdentity) {
		r.Identity.PaneID, r.Identity.PanePID, r.Identity.PaneStarttime = "%1", pid, start
	}
}

// withLaunch sets the row's state and launch start (ms since the epoch, 0 = none).
func withLaunch(state string, launchMs int64) fmRowOpt {
	return func(r *store.LiveSpawnIdentity) { r.State, r.LaunchStartedAtMillis = state, launchMs }
}

// withNote sets the liveness note the sweep reads on the row.
func withNote(note string) fmRowOpt {
	return func(r *store.LiveSpawnIdentity) { r.LivenessNote = note }
}

// assertLists fails unless res lists exactly ids and unverified (sorted) with matching counts.
func assertLists(t *testing.T, res api.FindMissingResult, ids, unverified []string) {
	t.Helper()
	if res.Count != len(ids) || !equalStrings(res.IDs, ids) {
		t.Errorf("count=%d ids=%v; want %d %v", res.Count, res.IDs, len(ids), ids)
	}
	if res.Unverified != len(unverified) || !equalStrings(res.UnverifiedIDs, unverified) {
		t.Errorf("unverified=%d unverified_ids=%v; want %d %v", res.Unverified, res.UnverifiedIDs, len(unverified), unverified)
	}
}

// TestFindMissingNoChangesWhenAllAlive: rows whose recorded process (SessionStart or pane) is alive with its
// recorded start time and carry no note get no write and are in neither list.
func TestFindMissingNoChangesWhenAllAlive(t *testing.T) {
	pc := procfix.New()
	pc.Set(201, procfix.Alive(fmStart))
	pc.Set(202, procfix.Alive(fmStart))
	st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{
		liveRow("a", withSessionStart(201, fmStart)),
		liveRow("b", withPane(202, fmStart)),
	}}

	res := mustFindMissing(t, st, pc)
	assertLists(t, res, nil, nil)
	if len(st.calls) != 0 {
		t.Errorf("store writes = %+v; want none", st.calls)
	}
	if got := pc.StartTimeCalls(); !slices.Equal(got, []int{201, 202}) {
		t.Errorf("StartTime calls = %v; want [201 202]", got)
	}
}

// TestFindMissingTransitionsUnprobeableRows: rows whose recorded agent process is dead (a gone SessionStart
// process, a zombie pane process) are marked; the row whose process is alive is left live.
func TestFindMissingTransitionsUnprobeableRows(t *testing.T) {
	pc := procfix.New()
	pc.Set(302, procfix.Alive(fmStart))
	pc.Set(303, procfix.Zombie())
	st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{
		liveRow("a", withSessionStart(301, fmStart)),
		liveRow("b", withPane(302, fmStart)),
		liveRow("c", withPane(303, fmStart)),
	}}

	res := mustFindMissing(t, st, pc)
	assertLists(t, res, []string{"a", "c"}, nil)
	if got := st.ids("mark"); !equalStrings(got, []string{"a", "c"}) {
		t.Errorf("marks = %v; want [a c]", got)
	}
}

// TestFindMissingNullPidFallbackGuardFree: after a reboot every recorded process is gone and every such row is
// marked, with no refusal; a row with no identity is only noted (Task 2's tmux lookup decides it).
func TestFindMissingNullPidFallbackGuardFree(t *testing.T) {
	pc := procfix.New() // empty table: every pid answers gone
	st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{
		liveRow("a", withSessionStart(401, fmStart)),
		liveRow("b", withPane(402, fmStart)),
		liveRow("c", withSessionStart(403, fmStart), withPane(403, fmStart)),
		liveRow("d"),
	}}

	res := mustFindMissing(t, st, pc)
	assertLists(t, res, []string{"a", "b", "c"}, []string{"d"})
	if got := st.ops("d"); !equalStrings(got, []string{"note"}) || st.calls[len(st.calls)-1].note != "process_not_seen_tmux_unchecked" {
		t.Errorf("writes on d = %+v; want one note process_not_seen_tmux_unchecked", st.calls)
	}
	if got := pc.StartTimeCalls(); !slices.Equal(got, []int{401, 402, 403}) {
		t.Errorf("StartTime calls = %v; want [401 402 403] (none for the row with no identity)", got)
	}
}

// TestFindMissingZeroLiveRowsIsNoopSuccess: no live rows is a no-op: no reader call, no write, no log line,
// empty non-nil lists.
func TestFindMissingZeroLiveRowsIsNoopSuccess(t *testing.T) {
	pc := procfix.New()
	st := &fakeFindMissingStore{}
	lg := &recordingLogger{}

	res, err := runFindMissing(st, pc, fmSweep{lg: lg})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	assertLists(t, res, nil, nil)
	if res.IDs == nil || res.UnverifiedIDs == nil {
		t.Errorf("ids=%#v unverified_ids=%#v; want non-nil empty slices", res.IDs, res.UnverifiedIDs)
	}
	if len(pc.StartTimeCalls()) != 0 || len(st.calls) != 0 || len(lg.lines) != 0 {
		t.Errorf("reader calls=%v writes=%+v log=%v; want none", pc.StartTimeCalls(), st.calls, lg.lines)
	}
}

// TestFindMissingPendingRowIsScanned: a pending row past its grace period whose launch pane process is gone is
// judged and marked (SR-11.2).
func TestFindMissingPendingRowIsScanned(t *testing.T) {
	launch := fmNow.Add(-fmGrace - time.Second).UnixMilli()
	st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{
		liveRow("p-1", withLaunch(store.StatePending, launch), withPane(501, fmStart)),
	}}

	res := mustFindMissing(t, st, procfix.New())
	assertLists(t, res, []string{"p-1"}, nil)
}

// TestFindMissingResultIDsSorted: ids come back sorted whatever the read order.
func TestFindMissingResultIDsSorted(t *testing.T) {
	st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{
		liveRow("z", withSessionStart(601, fmStart)),
		liveRow("a", withSessionStart(602, fmStart)),
		liveRow("m", withSessionStart(603, fmStart)),
	}}

	res := mustFindMissing(t, st, procfix.New())
	assertLists(t, res, []string{"a", "m", "z"}, nil)
}

// TestFindMissingUnverifiedIDsNeverNull: unverified_ids is non-nil when empty and sorted when populated.
func TestFindMissingUnverifiedIDsNeverNull(t *testing.T) {
	pc := procfix.New()
	pc.Set(700, procfix.Alive(fmStart))
	res := mustFindMissing(t, &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{
		liveRow("a", withSessionStart(700, fmStart)),
	}}, pc)
	if res.UnverifiedIDs == nil {
		t.Errorf("UnverifiedIDs = nil; want non-nil ([]) empty slice")
	}

	for pid := 701; pid <= 703; pid++ {
		pc.Set(pid, procfix.Unreadable())
	}
	res = mustFindMissing(t, &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{
		liveRow("z", withSessionStart(701, fmStart)),
		liveRow("a", withSessionStart(702, fmStart)),
		liveRow("m", withSessionStart(703, fmStart)),
	}}, pc)
	assertLists(t, res, nil, []string{"a", "m", "z"})
}

// TestFindMissingMarkingOrderPinned: a noted dead row gets exactly the guarded mark (liveness clear folded in)
// then the permission-request close; a separate clear, before or after the mark, fails.
func TestFindMissingMarkingOrderPinned(t *testing.T) {
	r := liveRow("ord-1", withSessionStart(7, fmStart), withNote("probe_eacces"))
	st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{r}}

	mustFindMissing(t, st, procfix.New())
	if got := st.ops("ord-1"); !equalStrings(got, []string{"mark", "close"}) {
		t.Fatalf("writes on ord-1 = %v; want [mark close] (no separate clear)", got)
	}
	if st.calls[0].snap != r.Snapshot {
		t.Errorf("mark guarded on %+v; want the read snapshot %+v", st.calls[0].snap, r.Snapshot)
	}
}

// TestFindMissingAlreadyTerminalRowMarkOnly: a mark that finds the row changed or absent writes nothing more: no
// close, not counted, in neither list.
func TestFindMissingAlreadyTerminalRowMarkOnly(t *testing.T) {
	for name, res := range map[string]store.CondResult{"changed": store.CondChanged, "absent": store.CondAbsent} {
		t.Run(name, func(t *testing.T) {
			st := &fakeFindMissingStore{
				rows:    []store.LiveSpawnIdentity{liveRow("gone", withSessionStart(3, fmStart))},
				answers: map[string]map[string]fmAnswer{"mark": {"gone": {res: res}}},
			}

			got := mustFindMissing(t, st, procfix.New())
			assertLists(t, got, nil, nil)
			if ops := st.ops("gone"); !equalStrings(ops, []string{"mark"}) {
				t.Errorf("writes on gone = %v; want [mark] only", ops)
			}
		})
	}
}

// TestFindMissingListErrorAborts: a live-row read error fails the sweep before any reader call.
func TestFindMissingListErrorAborts(t *testing.T) {
	pc := procfix.New()
	if _, err := runFindMissing(&fakeFindMissingStore{listErr: errSentinel}, pc, fmSweep{}); err == nil {
		t.Fatalf("FindMissing: nil error; want the list error to bubble up")
	}
	if got := pc.StartTimeCalls(); len(got) != 0 {
		t.Errorf("StartTime calls = %v; want none", got)
	}
}

// errSentinel (a shared package-level error, declared in sendkeys_test.go) is reused by the list-error test.
