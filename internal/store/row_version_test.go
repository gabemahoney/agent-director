package store_test

// SR-5.2 versioning test: every exported spawns write advances row_version by
// exactly one and leaves the store_id unchanged (SR-5.1); later Epics append
// their new writes to rowVersionWrites.
// SeedSpawn's store calls advance the version but its options and defaults do
// not, so the seeded version depends on the seed path: assert deltas only.

import (
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rowVersionCase is one spawns write run against a freshly seeded row.
type rowVersionCase struct {
	name    string
	state   string                                    // seeded state
	session string                                    // seeded claude_session_id; "" for none
	opts    []apitest.SpawnOption                     // seed options beyond rowVersionSeed
	setup   func(t *testing.T, f *v5Store, id string) // optional, runs before the first read
	write   func(t *testing.T, f *v5Store, id string) // the write under test; checks its own return
	clears  bool                                      // the write sets a state other than pending
	// wantState is the state after the write, confirming the branch ran; "" skips it.
	wantState string
	// identity is the server and pane identity the write stores; nil keeps it.
	identity *store.LaunchIdentity
	// writesToken: the write also stores identity's launch token and socket.
	writesToken bool
	// launchStart is the launch_started_at the write sets; 0 = cleared or kept per clears.
	launchStart int64
}

// rowVersionSeed sets every column no write may touch, and the launch start,
// to a non-default value so "unchanged" and "cleared" are never vacuous. Its
// identity records the agent's pane, so the agent's hooks apply (SR-22.9).
func rowVersionSeed() []apitest.SpawnOption {
	return []apitest.SpawnOption{
		apitest.WithLifeNumber(7),
		apitest.WithNoPreTrust(),
		apitest.WithLaunchIdentity(fullIdentity()),
		apitest.WithLaunchStartedAt(1767225600123),
	}
}

// seedCase seeds c's row, runs its setup, and returns the row id.
func seedCase(t *testing.T, f *v5Store, c rowVersionCase) string {
	t.Helper()
	id := f.seed(c.state, c.session, append(rowVersionSeed(), c.opts...)...)
	if c.setup != nil {
		c.setup(t, f, id)
	}
	return id
}

// stableColumns are the columns a versioned write keeps (SR-5.2), except that
// MoveToPending and RestoreAfterFailedResume write launch_token and
// tmux_socket (c.writesToken). The six identity columns (identityColumns) are
// kept too, except by RecordLaunchIdentity, MoveToPending and
// RestoreAfterFailedResume, which write them (c.identity).
func stableColumns(c apitest.SpawnColumns) map[string]any {
	return map[string]any{
		"life_number": c.LifeNumber, "no_pre_trust": c.NoPreTrust,
		"launch_token": c.LaunchToken, "tmux_socket": c.TmuxSocket,
	}
}

// assertColumns fails unless after holds want's values; before must be non-default.
func assertColumns(t *testing.T, before, after, want map[string]any) {
	t.Helper()
	for col, v := range before {
		if v == nil || v == int64(0) {
			t.Fatalf("seed left %s at a default (%#v); the column check would be vacuous", col, v)
		}
		if !reflect.DeepEqual(after[col], want[col]) {
			t.Errorf("%s: %#v -> %#v, want %#v", col, v, after[col], want[col])
		}
	}
}

// rvNilIfZero is v as a NULLable column stores it: the zero value as NULL.
func rvNilIfZero[T comparable](v T) any {
	var zero T
	if v == zero {
		return nil
	}
	return v
}

// assertVersionedWrite checks the SR-5.2 rules between two reads of one row:
// stable columns kept, identity columns kept or as c.identity stores them,
// launch token and socket kept or as c.identity stores them (c.writesToken).
func assertVersionedWrite(t *testing.T, before, after apitest.SpawnColumns, c rowVersionCase) {
	t.Helper()
	wantStable := stableColumns(before)
	if c.writesToken {
		wantStable["launch_token"], wantStable["tmux_socket"] = rvNilIfZero(c.identity.Token), rvNilIfZero(c.identity.Socket)
		if wantStable["launch_token"] == before.LaunchToken || wantStable["tmux_socket"] == before.TmuxSocket {
			t.Fatal("token or socket before the write equals the written one; the write check would be vacuous")
		}
	}
	assertColumns(t, stableColumns(before), stableColumns(after), wantStable)
	if c.identity == nil {
		assertColumns(t, identityColumns(before), identityColumns(after), identityColumns(before))
	} else {
		// A write of the identity may start from a NULL one (the restore), so
		// the vacuity guard is that the written identity differs from before.
		wantID := wantIdentityColumns(*c.identity)
		if reflect.DeepEqual(wantID, identityColumns(before)) {
			t.Fatal("identity before the write equals the written one; the write check would be vacuous")
		}
		if got := identityColumns(after); !reflect.DeepEqual(got, wantID) {
			t.Errorf("identity %#v -> %#v, want %#v", identityColumns(before), got, wantID)
		}
	}
	bv, bok := before.RowVersion.(int64)
	av, aok := after.RowVersion.(int64)
	if !bok || !aok || av != bv+1 {
		t.Errorf("row_version %#v -> %#v, want exactly +1", before.RowVersion, after.RowVersion)
	}
	if before.LaunchStartedAt == nil {
		t.Fatal("seed left launch_started_at NULL; the launch-start check would be vacuous")
	}
	switch {
	case c.launchStart != 0:
		if before.LaunchStartedAt == c.launchStart {
			t.Fatal("launch_started_at before the write equals the written one; the check would be vacuous")
		}
		if after.LaunchStartedAt != c.launchStart {
			t.Errorf("launch_started_at %#v -> %#v, want %d", before.LaunchStartedAt, after.LaunchStartedAt, c.launchStart)
		}
	case c.clears && after.LaunchStartedAt != nil:
		t.Errorf("launch_started_at = %#v, want NULL after a non-pending state write", after.LaunchStartedAt)
	case !c.clears && !reflect.DeepEqual(after.LaunchStartedAt, before.LaunchStartedAt):
		t.Errorf("launch_started_at %#v -> %#v, want unchanged", before.LaunchStartedAt, after.LaunchStartedAt)
	}
}

// assertStoreIDKept fails unless the raw store_id still equals the id the store
// read when it opened (SR-5.1: no write changes it).
func assertStoreIDKept(t *testing.T, f *v5Store) {
	t.Helper()
	raw, err := apitest.ReadStoreID(f.path)
	if err != nil {
		t.Fatalf("ReadStoreID: %v", err)
	}
	if want := f.s.StoreID(); raw != want {
		t.Errorf("store_id %q -> %q, want unchanged", want, raw)
	}
}

// rvAgentGate is the gate of a hook from id's own agent: the row's recorded
// pane process (storefix.AgentHookParent, SR-22.9).
func rvAgentGate(t *testing.T, f *v5Store, id string) store.HookGate {
	t.Helper()
	pid, start, err := storefix.AgentHookParent(f.s, id)
	if err != nil {
		t.Fatalf("AgentHookParent: %v", err)
	}
	return store.HookGate{Event: "row_version_test", ParentPID: pid, ParentStart: start}
}

// hookEntries are the two exported hook-transition entry points.
var hookEntries = []struct {
	name  string
	apply func(s *store.Store, id string, gate store.HookGate, to string, soft bool) (store.UpsertOutcome, store.HookApplied, error)
}{
	{"ApplyHookTransition", func(s *store.Store, id string, gate store.HookGate, to string, soft bool) (store.UpsertOutcome, store.HookApplied, error) {
		applied, err := s.ApplyHookTransition(id, gate, to, soft, "row_version_test", "", false)
		return "", applied, err
	}},
	{"ApplyHookTransitionResult", func(s *store.Store, id string, gate store.HookGate, to string, soft bool) (store.UpsertOutcome, store.HookApplied, error) {
		return s.ApplyHookTransitionResult(id, gate, to, soft, "row_version_test", "", false)
	}},
}

// hookCases returns one case per hook entry point for a from -> to transition
// by the row's own agent, which the gate applies (a held working transition
// included). want is the Result entry point's outcome; the plain entry point
// reports none.
func hookCases(name, from, to string, soft, clears bool, want store.UpsertOutcome) []rowVersionCase {
	wantState := to
	if soft || want == store.UpsertNoChange {
		wantState = from
	}
	var cases []rowVersionCase
	for _, e := range hookEntries {
		cases = append(cases, rowVersionCase{
			name: e.name + "/" + name, state: from, clears: clears, wantState: wantState,
			write: func(t *testing.T, f *v5Store, id string) {
				out, applied, err := e.apply(f.s, id, rvAgentGate(t, f, id), to, soft)
				if err != nil || !applied.Applied || (out != "" && out != want) {
					t.Fatalf("%s(%s, soft=%v) = %q, %+v, %v; want %q, applied", e.name, to, soft, out, applied, err, want)
				}
			},
		})
	}
	return cases
}

// sessionStart returns the agent's SessionStart with the given session id and
// transcript path arguments; it must apply.
func sessionStart(session, path string, present bool) func(*testing.T, *v5Store, string) {
	return func(t *testing.T, f *v5Store, id string) {
		if got := storefix.ApplyAgentHook(t, f.s, id, "SessionStart", session, storefix.HookTranscript(path, present)); !got.Applied {
			t.Fatalf("SessionStart(%s) = %+v; want applied", session, got)
		}
	}
}

// foreignHook returns event from another process than id's agent; SR-22.9
// ignores it (pid_mismatch).
func foreignHook(event string) func(*testing.T, *v5Store, string) {
	return func(t *testing.T, f *v5Store, id string) {
		want := store.HookApplied{Reason: store.HookReasonPIDMismatch}
		if got := storefix.ApplyForeignHook(t, f.s, id, event, "sess-foreign"); got != want {
			t.Fatalf("foreign %s = %+v; want %+v", event, got, want)
		}
	}
}

// recordLaunch returns a RecordLaunchIdentity write of createdIdentity with the
// seeded token at the row's version minus stale, expecting want.
func recordLaunch(stale int64, want store.CondResult) func(*testing.T, *v5Store, string) {
	return func(t *testing.T, f *v5Store, id string) {
		v := f.rawColumns(id).RowVersion.(int64) - stale
		if got, err := f.s.RecordLaunchIdentity(id, v, goodToken, createdIdentity()); err != nil || got != want {
			t.Fatalf("RecordLaunchIdentity(version %d) = %v, %v; want %v, nil", v, got, err, want)
		}
	}
}

// historyLen counts id's session_history entries across every life.
func historyLen(t *testing.T, f *v5Store, id string) int {
	t.Helper()
	h, err := apitest.ReadSessionHistoryAllLives(f.path, id)
	if err != nil {
		t.Fatalf("ReadSessionHistoryAllLives: %v", err)
	}
	return len(h)
}

// seedParent seeds a second row and makes it id's parent.
func seedParent(t *testing.T, f *v5Store, id string) {
	t.Helper()
	if err := f.s.SetParentID(id, f.seed("waiting", "")); err != nil {
		t.Fatalf("SetParentID: %v", err)
	}
}

// seedRequest seeds an open permission request for id and returns its token.
func seedRequest(t *testing.T, f *v5Store, id string) string {
	t.Helper()
	req, err := apitest.SeedPermissionRequest(f.path, id, "Bash")
	if err != nil {
		t.Fatalf("SeedPermissionRequest: %v", err)
	}
	return req.RequestToken
}

// wantBool fails unless a (bool, error) write returned want with no error.
func wantBool(t *testing.T, name string, got bool, err error, want bool) {
	t.Helper()
	if err != nil || got != want {
		t.Fatalf("%s = %v, %v; want %v, nil", name, got, err, want)
	}
}

// markMissing returns a MarkSpawnMissing write expecting prior as its result.
func markMissing(prior string) func(*testing.T, *v5Store, string) {
	return func(t *testing.T, f *v5Store, id string) {
		if got, err := f.s.MarkSpawnMissing(id); err != nil || got != prior {
			t.Fatalf("MarkSpawnMissing = %q, %v; want %q", got, err, prior)
		}
	}
}

// liveness is a note stamp for rows that already carry one.
var liveness = []apitest.SpawnOption{
	apitest.WithLivenessUnverifiedSince("2026-01-01 00:00:00"), apitest.WithLivenessNote("probe_eacces"),
}

// rvMove* are resume's move arguments: a launch start, token and socket unlike the seed's.
const (
	rvMoveStart  int64 = 1767225900456
	rvMoveToken        = "fedcba9876543210"
	rvMoveSocket       = "/tmp/rv/resume-sock"
)

// rvMoveIdentity is what the move stores: its token and socket, no server or pane.
func rvMoveIdentity() store.LaunchIdentity {
	return store.LaunchIdentity{Token: rvMoveToken, Socket: rvMoveSocket}
}

// rvExamine reads id's row as resume does before the move.
func rvExamine(t *testing.T, f *v5Store, id string) store.Spawn {
	t.Helper()
	sp, err := f.s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	return sp
}

// rvMove runs the move of id from examined with no parent, expecting want and,
// when applied, the examined version plus one; it returns the moved version.
func rvMove(t *testing.T, f *v5Store, id string, examined store.Spawn, want store.CondResult) int64 {
	t.Helper()
	var wantV int64
	if want == store.CondApplied {
		wantV = examined.Snapshot.RowVersion + 1
	}
	got, v, err := f.s.MoveToPending(id, examined.Snapshot, rvMoveStart, rvMoveToken, rvMoveSocket, "")
	if err != nil || got != want || v != wantV {
		t.Fatalf("MoveToPending = %v, %d, %v; want %v, %d, nil", got, v, err, want, wantV)
	}
	return v
}

// rvHealPath is a versioned write that is not a hook: it advances id's version
// between resume's read and its write (the row must be seeded with sess-rv).
func rvHealPath(t *testing.T, f *v5Store, id string) {
	t.Helper()
	got, err := f.s.HealJsonlPath(id, "sess-rv", "/tmp/rv/healed.jsonl")
	wantBool(t, "HealJsonlPath", got, err, true)
}

// rvResume carries one resume's prior row and moved version from a case's
// setup to its write.
type rvResume struct {
	prior store.ResumePrior
	moved int64
}

// move examines id, keeps its prior values and moves it to pending.
func (r *rvResume) move(t *testing.T, f *v5Store, id string) {
	t.Helper()
	sp := rvExamine(t, f, id)
	r.prior = store.ResumePrior{State: sp.State, EndedAtText: sp.EndedAtText, PID: sp.PID,
		ProcStarttime: sp.ProcStarttime, LivenessUnverifiedSince: sp.LivenessUnverifiedSince,
		LivenessNote: sp.LivenessNote, Identity: sp.Identity}
	r.moved = rvMove(t, f, id, sp, store.CondApplied)
}

// restore returns the restore of r's prior row at r's moved version, expecting want.
func (r *rvResume) restore(want store.CondResult) func(*testing.T, *v5Store, string) {
	return func(t *testing.T, f *v5Store, id string) {
		if got, err := f.s.RestoreAfterFailedResume(id, r.moved, r.prior); err != nil || got != want {
			t.Fatalf("RestoreAfterFailedResume(version %d) = %v, %v; want %v, nil", r.moved, got, err, want)
		}
	}
}

// rvResumeWrites are the applied move and restore for each finished state: the
// move sets the launch start, token and socket and clears the identity; the
// restore clears the launch start and writes the pre-move token, socket and
// identity back (SR-5.2).
func rvResumeWrites() []rowVersionCase {
	var cases []rowVersionCase
	for _, st := range []string{"ended", "missing"} {
		moved, full, r := rvMoveIdentity(), fullIdentity(), &rvResume{}
		cases = append(cases,
			rowVersionCase{name: "MoveToPending/applied, " + st + " row", state: st, wantState: "pending",
				identity: &moved, writesToken: true, launchStart: rvMoveStart,
				write: func(t *testing.T, f *v5Store, id string) { rvMove(t, f, id, rvExamine(t, f, id), store.CondApplied) }},
			rowVersionCase{name: "RestoreAfterFailedResume/applied, " + st + " row", state: st, wantState: st,
				clears: true, identity: &full, writesToken: true, setup: r.move, write: r.restore(store.CondApplied)},
		)
	}
	return cases
}

// rvResumeNoOps are a move and a restore that do not apply: each writes nothing.
func rvResumeNoOps() []rowVersionCase {
	staleMove := func() rowVersionCase {
		var examined store.Spawn
		return rowVersionCase{name: "MoveToPending/stale snapshot, another write first", state: "ended", session: "sess-rv",
			setup: func(t *testing.T, f *v5Store, id string) { examined = rvExamine(t, f, id); rvHealPath(t, f, id) },
			write: func(t *testing.T, f *v5Store, id string) { rvMove(t, f, id, examined, store.CondChanged) }}
	}
	moveNow := func(want store.CondResult) func(*testing.T, *v5Store, string) {
		return func(t *testing.T, f *v5Store, id string) { rvMove(t, f, id, rvExamine(t, f, id), want) }
	}
	afterWrite, neverMoved, badPrior := &rvResume{}, &rvResume{}, &rvResume{}
	return []rowVersionCase{
		staleMove(),
		{name: "MoveToPending/live row", state: "waiting", write: moveNow(store.CondChanged)},
		{name: "MoveToPending/pending row", state: "pending", write: moveNow(store.CondChanged)},
		{name: "MoveToPending/parent id names no row", state: "ended",
			write: func(t *testing.T, f *v5Store, id string) {
				sp := rvExamine(t, f, id)
				got, v, err := f.s.MoveToPending(id, sp.Snapshot, rvMoveStart, rvMoveToken, rvMoveSocket, "rv-no-such-parent")
				if err == nil || got != 0 || v != 0 {
					t.Fatalf("MoveToPending(bad parent) = %v, %d, %v; want 0, 0, an error", got, v, err)
				}
			}},
		{name: "MoveToPending/absent row", state: "ended",
			write: func(t *testing.T, f *v5Store, id string) {
				got, v, err := f.s.MoveToPending("rv-absent", rvExamine(t, f, id).Snapshot, rvMoveStart, rvMoveToken, rvMoveSocket, "")
				if err != nil || got != store.CondAbsent || v != 0 {
					t.Fatalf("MoveToPending(absent) = %v, %d, %v; want CondAbsent, 0, nil", got, v, err)
				}
			}},
		{name: "RestoreAfterFailedResume/another write after the move", state: "ended", session: "sess-rv",
			setup: func(t *testing.T, f *v5Store, id string) { afterWrite.move(t, f, id); rvHealPath(t, f, id) },
			write: afterWrite.restore(store.CondChanged)},
		{name: "RestoreAfterFailedResume/finished row never moved", state: "ended",
			setup: func(t *testing.T, f *v5Store, id string) {
				sp := rvExamine(t, f, id)
				neverMoved.prior, neverMoved.moved = store.ResumePrior{State: sp.State, Identity: sp.Identity}, sp.RowVersion
			},
			write: neverMoved.restore(store.CondChanged)},
		{name: "RestoreAfterFailedResume/prior state not finished", state: "ended", setup: badPrior.move,
			write: func(t *testing.T, f *v5Store, id string) {
				prior := badPrior.prior
				prior.State = "waiting"
				if got, err := f.s.RestoreAfterFailedResume(id, badPrior.moved, prior); err == nil || got != 0 {
					t.Fatalf("RestoreAfterFailedResume(prior waiting) = %v, %v; want 0, an error", got, err)
				}
			}},
		{name: "RestoreAfterFailedResume/absent row", state: "pending",
			write: func(t *testing.T, f *v5Store, id string) {
				got, err := f.s.RestoreAfterFailedResume("rv-absent", 1, store.ResumePrior{State: "ended"})
				if err != nil || got != store.CondAbsent {
					t.Fatalf("RestoreAfterFailedResume(absent) = %v, %v; want CondAbsent, nil", got, err)
				}
			}},
	}
}

// rowVersionWrites is every exported write that updates a spawns row, one case
// per branch. Later Epics append a case for each new spawns write.
func rowVersionWrites() []rowVersionCase {
	var cases []rowVersionCase
	for _, h := range []struct {
		name, from, to string
		soft, clears   bool
	}{
		{"pending to waiting", "pending", "waiting", false, true},
		{"waiting to working, no open request", "waiting", "working", false, true},
		{"working to ask_user", "working", "ask_user", false, true},
		{"working to check_permission", "working", "check_permission", false, true},
		{"ended transition of pending row", "pending", "ended", false, true},
		{"resurrection ended to waiting", "ended", "waiting", false, true},
		{"same state waiting", "waiting", "waiting", false, true},
		{"waiting to pending keeps launch start", "waiting", "pending", false, false},
		{"soft refresh of pending row", "pending", "", true, false},
	} {
		cases = append(cases, hookCases(h.name, h.from, h.to, h.soft, h.clears, store.UpsertUpdated)...)
	}
	created := createdIdentity()
	cases = append(cases, rvResumeWrites()...)
	return append(cases,
		rowVersionCase{name: "RecordLaunchIdentity/applied", state: "pending", wantState: "pending",
			identity: &created, write: recordLaunch(0, store.CondApplied)},
		// SR-22.9: SessionStart is one write, from the row's own agent: it sets
		// waiting and clears the launch start in the same statement.
		rowVersionCase{name: "RecordSessionStartIdentity/path present", state: "pending", clears: true, wantState: "waiting",
			write: sessionStart("sess-a", "/tmp/rv/a.jsonl", true)},
		rowVersionCase{name: "RecordSessionStartIdentity/path not on disk", state: "pending", clears: true, wantState: "waiting",
			write: sessionStart("sess-a", "/tmp/rv/a.jsonl", false)},
		rowVersionCase{name: "RecordSessionStartIdentity/no path", state: "pending", clears: true, wantState: "waiting",
			write: sessionStart("sess-a", "", false)},
		rowVersionCase{name: "RecordSessionStartIdentity/rotation archives once, one bump", state: "waiting",
			clears: true, wantState: "waiting",
			session: "sess-old", opts: []apitest.SpawnOption{apitest.WithJsonlPath("/tmp/rv/old.jsonl")},
			write: func(t *testing.T, f *v5Store, id string) {
				n := historyLen(t, f, id)
				sessionStart("sess-new", "", false)(t, f, id)
				if got := historyLen(t, f, id); got != n+1 {
					t.Fatalf("history entries %d -> %d, want one archived entry", n, got)
				}
			}},
		rowVersionCase{name: "SetLivenessUnverified/first set on pending row", state: "pending",
			write: func(t *testing.T, f *v5Store, id string) {
				got, err := f.s.SetLivenessUnverified(id, "probe_eacces")
				wantBool(t, "SetLivenessUnverified", got, err, true)
			}},
		rowVersionCase{name: "ClearLivenessUnverified/note set on pending row", state: "pending", opts: liveness,
			write: func(t *testing.T, f *v5Store, id string) {
				if err := f.s.ClearLivenessUnverified(id); err != nil {
					t.Fatalf("ClearLivenessUnverified: %v", err)
				}
			}},
		rowVersionCase{name: "ClearLivenessUnverified/no note", state: "waiting",
			write: func(t *testing.T, f *v5Store, id string) {
				if err := f.s.ClearLivenessUnverified(id); err != nil {
					t.Fatalf("ClearLivenessUnverified: %v", err)
				}
			}},
		rowVersionCase{name: "MarkSpawnMissing/live row", state: "waiting", clears: true, wantState: "missing",
			write: markMissing("waiting")},
		rowVersionCase{name: "MarkSpawnMissing/pending row", state: "pending", clears: true, wantState: "missing",
			write: markMissing("pending")},
		rowVersionCase{name: "HealJsonlPath/path NULL", state: "waiting", session: "sess-heal",
			write: func(t *testing.T, f *v5Store, id string) {
				got, err := f.s.HealJsonlPath(id, "sess-heal", "/tmp/rv/healed.jsonl")
				wantBool(t, "HealJsonlPath", got, err, true)
			}},
		rowVersionCase{name: "SetParentID/set", state: "waiting", write: seedParent},
		rowVersionCase{name: "SetParentID/clear", state: "waiting", setup: seedParent,
			write: func(t *testing.T, f *v5Store, id string) {
				if err := f.s.SetParentID(id, ""); err != nil {
					t.Fatalf("SetParentID clear: %v", err)
				}
			}},
	)
}

// TestRowVersionEveryWriteAdvancesByOne runs each spawns write once and checks
// the SR-5.2 rules: +1, stable columns unchanged, launch start cleared or kept.
func TestRowVersionEveryWriteAdvancesByOne(t *testing.T) {
	for _, c := range rowVersionWrites() {
		t.Run(c.name, func(t *testing.T) {
			f := newV5Store(t)
			id := seedCase(t, f, c)
			before := f.rawColumns(id)
			c.write(t, f, id)
			after := f.rawColumns(id)
			assertVersionedWrite(t, before, after, c)
			if c.wantState != "" && after.State != c.wantState {
				t.Errorf("state = %#v, want %q (wrong branch?)", after.State, c.wantState)
			}
			assertStoreIDKept(t, f)
		})
	}
}

// TestRowVersionInsertStartsAtZero checks a new row starts at version 0 with
// the launch start, token and socket given, no identity, life 0, no_pre_trust 0.
func TestRowVersionInsertStartsAtZero(t *testing.T) {
	f := newV5Store(t)
	sp := f.insertLaunch("rv-insert", launchStart, store.LaunchIdentity{Token: goodToken, Socket: "/tmp/rv/sock"})
	c := f.rawColumns("rv-insert")
	assertInsertedLaunch(t, c, sp)
	if got, want := []any{c.LifeNumber, c.NoPreTrust}, []any{int64(0), int64(0)}; !reflect.DeepEqual(got, want) {
		t.Errorf("life_number, no_pre_trust = %#v, want %#v", got, want)
	}
	assertStoreIDKept(t, f)
}

// TestRowVersionNoOpWritesChangeNothing checks the hold path, zero-row writes
// and other-table writes leave the whole spawns row, version included, as it was.
func TestRowVersionNoOpWritesChangeNothing(t *testing.T) {
	cases := []rowVersionCase{
		{name: "SetLivenessUnverified/already set", state: "waiting", opts: liveness,
			write: func(t *testing.T, f *v5Store, id string) {
				got, err := f.s.SetLivenessUnverified(id, "probe_eacces")
				wantBool(t, "SetLivenessUnverified", got, err, false)
			}},
		{name: "SetLivenessUnverified/finished row", state: "ended",
			write: func(t *testing.T, f *v5Store, id string) {
				got, err := f.s.SetLivenessUnverified(id, "probe_eacces")
				wantBool(t, "SetLivenessUnverified", got, err, false)
			}},
		{name: "MarkSpawnMissing/finished row", state: "ended", write: markMissing("")},
		{name: "RecordLaunchIdentity/stale version, hook wrote first", state: "pending",
			setup: func(t *testing.T, f *v5Store, id string) {
				if applied, err := f.s.ApplyHookTransition(id, rvAgentGate(t, f, id), "", true, "row_version_test", "", false); err != nil || !applied.Applied {
					t.Fatalf("ApplyHookTransition soft refresh = %+v, %v; want applied", applied, err)
				}
			},
			write: recordLaunch(1, store.CondChanged)},
		// SR-22.9: a hook from another parent is ignored and writes nothing.
		{name: "ApplyHookTransition/another parent, ignored", state: "waiting", write: foreignHook("Stop")},
		{name: "RecordSessionStartIdentity/another parent, ignored", state: "pending", write: foreignHook("SessionStart")},
		{name: "HealJsonlPath/path already set", state: "waiting", session: "sess-heal",
			opts: []apitest.SpawnOption{apitest.WithJsonlPath("/tmp/rv/have.jsonl")},
			write: func(t *testing.T, f *v5Store, id string) {
				got, err := f.s.HealJsonlPath(id, "sess-heal", "/tmp/rv/healed.jsonl")
				wantBool(t, "HealJsonlPath", got, err, false)
			}},
		{name: "HealJsonlPath/session differs", state: "waiting", session: "sess-heal",
			write: func(t *testing.T, f *v5Store, id string) {
				got, err := f.s.HealJsonlPath(id, "sess-other", "/tmp/rv/healed.jsonl")
				wantBool(t, "HealJsonlPath", got, err, false)
			}},
		{name: "permission request seeded", state: "check_permission",
			write: func(t *testing.T, f *v5Store, id string) { seedRequest(t, f, id) }},
		{name: "permission request seeded and decided", state: "check_permission",
			write: func(t *testing.T, f *v5Store, id string) {
				got, err := f.s.DecidePermissionRequest(id, seedRequest(t, f, id), "allow", "", "row_version_test")
				wantBool(t, "DecidePermissionRequest", got, err, true)
			}},
		{name: "CloseOrphanedPermissionRequests", state: "check_permission",
			write: func(t *testing.T, f *v5Store, id string) {
				seedRequest(t, f, id)
				if err := f.s.CloseOrphanedPermissionRequests(id); err != nil {
					t.Fatalf("CloseOrphanedPermissionRequests: %v", err)
				}
			}},
	}
	for _, hold := range hookCases("working hold path, open request", "check_permission", "working", false, false, store.UpsertNoChange) {
		hold.setup = func(t *testing.T, f *v5Store, id string) { seedRequest(t, f, id) }
		cases = append(cases, hold)
	}
	cases = append(cases, rvResumeNoOps()...)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newV5Store(t)
			id := seedCase(t, f, c)
			before := f.rawColumns(id)
			c.write(t, f, id)
			if after := f.rawColumns(id); !reflect.DeepEqual(after, before) {
				t.Errorf("row changed:\nbefore %+v\nafter  %+v", before, after)
			}
			assertStoreIDKept(t, f)
		})
	}
}
