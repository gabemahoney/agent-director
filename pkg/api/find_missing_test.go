package api_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// This file owns the shared find-missing unit-test fakes and the one sweep
// helper (runFindMissing); process answers come from procfix, tmux answers
// from a tmuxfix.Recorder.

// fakeFindMissingStore is api.FindMissingStore over a fixed live-row read. It records every write in order with
// the snapshot it was given and answers each guarded write per row from answers (unset: CondApplied).
type fakeFindMissingStore struct {
	rows    []store.LiveSpawnIdentity
	listErr error
	// answers is keyed by op ("adopt", "mark", "note", "clear") then instance id.
	answers  map[string]map[string]fmAnswer
	closeErr error
	calls    []storeCall
	// adopted holds, per row, the snapshot an applied adoption returned: the guard of the row's next write.
	adopted map[string]store.RowSnapshot
	// trailAt, when set, is read at every write and kept in storeCall.at.
	trailAt func() int

	provisional []store.ProvisionalTranscript
	healed      []healCall
}

var _ api.FindMissingStore = (*fakeFindMissingStore)(nil)

// fmAnswer is one guarded write's scripted outcome: err when set, else res.
type fmAnswer struct {
	res store.CondResult
	err error
}

// storeCall is one recorded write; op is "adopt", "mark", "note", "clear" or "close".
type storeCall struct {
	op       string
	id       string
	snap     store.RowSnapshot
	note     string
	identity store.LaunchIdentity // the identity an adoption was given
	at       int                  // trailAt() when the write was made (0 when unset)
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

// record appends c to the write log, stamped with trailAt when set.
func (f *fakeFindMissingStore) record(c storeCall) {
	if f.trailAt != nil {
		c.at = f.trailAt()
	}
	f.calls = append(f.calls, c)
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

// AdoptIdentityIfSameLife records the adoption; when it applies it returns examined one row version on and
// keeps that snapshot as the row's guard.
func (f *fakeFindMissingStore) AdoptIdentityIfSameLife(id string, examined store.RowSnapshot, li store.LaunchIdentity) (store.CondResult, store.RowSnapshot, error) {
	f.record(storeCall{op: "adopt", id: id, snap: examined, identity: li})
	res, err := f.result("adopt", id)
	if err != nil || res != store.CondApplied {
		return res, store.RowSnapshot{}, err
	}
	now := examined
	now.RowVersion++
	if f.adopted == nil {
		f.adopted = map[string]store.RowSnapshot{}
	}
	f.adopted[id] = now
	return res, now, nil
}

func (f *fakeFindMissingStore) MarkMissingIfSameLife(id string, examined store.RowSnapshot) (string, store.CondResult, error) {
	f.record(storeCall{op: "mark", id: id, snap: examined})
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
	f.record(storeCall{op: "note", id: id, snap: examined, note: note})
	return f.result("note", id)
}

func (f *fakeFindMissingStore) ClearLivenessIfSameLife(id string, examined store.RowSnapshot) (store.CondResult, error) {
	f.record(storeCall{op: "clear", id: id, snap: examined})
	return f.result("clear", id)
}

func (f *fakeFindMissingStore) CloseOrphanedPermissionRequests(id string) error {
	f.record(storeCall{op: "close", id: id})
	return f.closeErr
}

func (f *fakeFindMissingStore) ListProvisionalTranscripts() ([]store.ProvisionalTranscript, error) {
	return f.provisional, nil
}

func (f *fakeFindMissingStore) HealJsonlPath(id, sessionID, jsonlPath string) (bool, error) {
	f.healed = append(f.healed, healCall{id, sessionID, jsonlPath})
	return true, nil
}

// StoreID is the store id the fake's rows are labelled with (tmuxfix.StoreID).
func (f *fakeFindMissingStore) StoreID() string { return tmuxfix.StoreID }

// readSnap is the snapshot the live-row read returned for id.
func (f *fakeFindMissingStore) readSnap(id string) store.RowSnapshot {
	for _, r := range f.rows {
		if r.ClaudeInstanceID == id {
			return r.Snapshot
		}
	}
	return store.RowSnapshot{}
}

// guard is the snapshot id's mark, note or clear must carry: an applied adoption's, else the read one.
func (f *fakeFindMissingStore) guard(id string) store.RowSnapshot {
	if s, ok := f.adopted[id]; ok {
		return s
	}
	return f.readSnap(id)
}

// assertGuards fails unless each adoption carried its row's read snapshot and each other guarded write its guard.
func (f *fakeFindMissingStore) assertGuards(t *testing.T) {
	t.Helper()
	for _, c := range f.calls {
		want := f.guard(c.id)
		switch c.op {
		case "close":
			continue
		case "adopt":
			want = f.readSnap(c.id)
		}
		if c.snap != want {
			t.Errorf("%s on %s guarded on %+v; want %+v", c.op, c.id, c.snap, want)
		}
	}
}

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

// fmGrace and fmBudget are the default pending grace period (SR-11.2) and sweep budget (SR-13.5) every sweep
// passes unless a test sets its own.
var (
	fmGrace  = time.Duration(config.DefaultPendingGraceSeconds) * time.Second
	fmBudget = time.Duration(config.DefaultSweepBudgetSeconds) * time.Second
)

// fmBudgetSpent is a non-positive sweep budget: no tmux call is made and every row reaching the lookup is
// Skipped (not_run), so it gets the "not called" note.
const fmBudgetSpent time.Duration = -1

// fmNow is the sweep clock's start.
var fmNow = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

// fmStart is a recorded start time; fmOtherStart is another (the pid was reused).
const (
	fmStart      = procstarttimefix.LinuxProcStarttime
	fmOtherStart = procstarttimefix.DarwinProcStarttime
)

// fmSweep overrides runFindMissing's defaults: fmGrace, fmBudget, a fresh Recorder (no server: every lookup is
// Gone), a fresh recordingLogger, and, when now is nil, clock (default: one at fmNow) as both the sweep's clock
// and the Recorder's virtual time.
type fmSweep struct {
	grace  time.Duration
	budget time.Duration
	now    func() time.Time
	clock  *tmuxfix.Clock
	tmux   *tmuxfix.Recorder
	lg     api.FindMissingLogger
}

// runFindMissing runs one sweep of s judged by pc: the one place the unit tests call api.FindMissing.
// Must use: every pkg/api sweep test goes through it (or mustSweep / mustFindMissing) with a tmuxfix.Recorder
// and procfix.Checker, never a second store, tmux or process-checker fake and never real waiting; a budget or
// timeout is reached through the Recorder's virtual time on the shared tmuxfix.Clock (or fmBudgetSpent).
func runFindMissing(s api.FindMissingStore, pc api.ProcChecker, o fmSweep) (api.FindMissingResult, error) {
	if o.grace == 0 {
		o.grace = fmGrace
	}
	if o.budget == 0 {
		o.budget = fmBudget
	}
	if o.tmux == nil {
		o.tmux = tmuxfix.NewRecorder()
	}
	if o.now == nil {
		if o.clock == nil {
			o.clock = tmuxfix.NewClock(fmNow)
		}
		o.tmux.WithVirtualTime(o.clock, tmux.Timeouts{})
		o.now = o.clock.Now
	}
	if o.lg == nil {
		o.lg = &recordingLogger{}
	}
	return api.FindMissing(context.Background(), s, o.tmux, pc, o.grace, o.budget, o.now, o.lg)
}

// mustSweep is runFindMissing with o, failing the test on a sweep error or, on the fake store, a write not
// guarded on its row's guard.
func mustSweep(t *testing.T, s api.FindMissingStore, pc api.ProcChecker, o fmSweep) api.FindMissingResult {
	t.Helper()
	res, err := runFindMissing(s, pc, o)
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if f, ok := s.(*fakeFindMissingStore); ok {
		f.assertGuards(t)
	}
	return res
}

// mustFindMissing is mustSweep with every default.
func mustFindMissing(t *testing.T, s api.FindMissingStore, pc api.ProcChecker) api.FindMissingResult {
	t.Helper()
	return mustSweep(t, s, pc, fmSweep{})
}

// fmServer is the tmux server fmOurs binds on apitest.TestSocket; withServer records it on a row.
var fmServer = tmuxfix.Server{PID: 7001, Start: fmNow.Add(-time.Hour).Unix(), ProcStart: fmOtherStart}

// fmOurs returns a Recorder whose server (fmServer) holds each row's own session under its recorded name and
// current label, so the row's lookup is Ours; a row recording no pane gets one new pane carrying its token.
func fmOurs(rows ...store.LiveSpawnIdentity) *tmuxfix.Recorder {
	rec := tmuxfix.NewRecorder().StartServer(apitest.TestSocket, fmServer)
	for _, r := range rows {
		s := tmuxfix.SeedSession{Name: r.TmuxSessionName, Label: tmuxfix.Valid(r.Identity.Token, r.ClaudeInstanceID, tmuxfix.StoreID)}
		if r.Identity.PaneID == "" {
			s.Panes = []tmuxfix.SeedPane{{AdPane: r.Identity.Token}}
		}
		rec.SeedSessions(r.Identity.Socket, s)
	}
	return rec
}

// fmCantTell returns a Recorder whose lookup and pane listing fail unreadable (Can't tell) on every socket.
func fmCantTell() *tmuxfix.Recorder {
	return tmuxfix.NewRecorder().Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized},
		tmux.CallLookup, tmux.CallListPanes)
}

// fmRowOpt sets fields of a liveRow.
type fmRowOpt func(*store.LiveSpawnIdentity)

// liveRow builds a working row with a launch token on apitest.TestSocket and no process or server identity or
// note; its Snapshot is filled from the row after opts.
func liveRow(id string, opts ...fmRowOpt) store.LiveSpawnIdentity {
	r := store.LiveSpawnIdentity{ClaudeInstanceID: id, State: store.StateWorking, TmuxSessionName: "cd-" + id,
		Identity: store.LaunchIdentity{Token: tmuxfix.Token, Socket: apitest.TestSocket}}
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

// withServer records fmServer as the row's tmux server identity.
func withServer() fmRowOpt {
	return func(r *store.LiveSpawnIdentity) {
		r.Identity.ServerPID, r.Identity.ServerStart, r.Identity.ServerStarttime = fmServer.PID, fmServer.Start, fmServer.ProcStart
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

// assertMarkReason fails unless id's first tick after trail line before has reconciliation_reason reason.
func assertMarkReason(t *testing.T, before int, id, reason string) {
	t.Helper()
	ticks := ticksSince(t, before, id)
	if len(ticks) == 0 || ticks[0]["reconciliation_reason"] != reason {
		t.Errorf("ticks on %s = %v; want the first with reason %s", id, ticks, reason)
	}
}

// assertLookups fails unless rec saw exactly n socket-taking calls, all lookups.
func assertLookups(t *testing.T, rec *tmuxfix.Recorder, n int) {
	t.Helper()
	if calls := rec.SocketCalls(); len(calls) != n || len(rec.SocketCallsOf(tmux.CallLookup)) != n {
		t.Errorf("tmux calls = %+v; want %d lookup(s) and nothing else", calls, n)
	}
}
