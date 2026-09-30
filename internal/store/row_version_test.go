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
}

// rowVersionSeed sets every column no write may touch, and the launch start,
// to a non-default value so "unchanged" and "cleared" are never vacuous.
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

// stableColumns are the columns no write in this release may change (SR-5.2).
// The six identity columns (identityColumns) are stable too, except for
// RecordLaunchIdentity, which writes them.
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

// assertVersionedWrite checks the SR-5.2 rules between two reads of one row:
// stable columns kept, identity columns kept or as c.identity stores them.
func assertVersionedWrite(t *testing.T, before, after apitest.SpawnColumns, c rowVersionCase) {
	t.Helper()
	assertColumns(t, stableColumns(before), stableColumns(after), stableColumns(before))
	wantID := identityColumns(before)
	if c.identity != nil {
		wantID = wantIdentityColumns(*c.identity)
		if reflect.DeepEqual(wantID, identityColumns(before)) {
			t.Fatal("seeded identity equals the written one; the write check would be vacuous")
		}
	}
	assertColumns(t, identityColumns(before), identityColumns(after), wantID)
	bv, bok := before.RowVersion.(int64)
	av, aok := after.RowVersion.(int64)
	if !bok || !aok || av != bv+1 {
		t.Errorf("row_version %#v -> %#v, want exactly +1", before.RowVersion, after.RowVersion)
	}
	if before.LaunchStartedAt == nil {
		t.Fatal("seed left launch_started_at NULL; the launch-start check would be vacuous")
	}
	switch {
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

// hookEntries are the two exported hook-transition entry points.
var hookEntries = []struct {
	name  string
	apply func(s *store.Store, id, to string, soft bool) (store.UpsertOutcome, error)
}{
	{"ApplyHookTransition", func(s *store.Store, id, to string, soft bool) (store.UpsertOutcome, error) {
		return "", s.ApplyHookTransition(id, to, soft, "row_version_test")
	}},
	{"ApplyHookTransitionResult", func(s *store.Store, id, to string, soft bool) (store.UpsertOutcome, error) {
		return s.ApplyHookTransitionResult(id, to, soft, "row_version_test")
	}},
}

// hookCases returns one case per hook entry point for a from -> to transition.
// want is the Result entry point's outcome; the plain entry point reports none.
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
				out, err := e.apply(f.s, id, to, soft)
				if err != nil || (out != "" && out != want) {
					t.Fatalf("%s(%s, soft=%v) = %q, %v; want %q", e.name, to, soft, out, err, want)
				}
			},
		})
	}
	return cases
}

// sessionStart returns a RecordSessionStartIdentity write with the given path arguments.
func sessionStart(session, path string, present bool) func(*testing.T, *v5Store, string) {
	return func(t *testing.T, f *v5Store, id string) {
		if err := f.s.RecordSessionStartIdentity(id, session, path, present, 4242, apitest.LinuxProcStarttime); err != nil {
			t.Fatalf("RecordSessionStartIdentity: %v", err)
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
	return append(cases,
		rowVersionCase{name: "RecordLaunchIdentity/applied", state: "pending", wantState: "pending",
			identity: &created, write: recordLaunch(0, store.CondApplied)},
		rowVersionCase{name: "RecordSessionStartIdentity/path present", state: "pending",
			write: sessionStart("sess-a", "/tmp/rv/a.jsonl", true)},
		rowVersionCase{name: "RecordSessionStartIdentity/path not on disk", state: "pending",
			write: sessionStart("sess-a", "/tmp/rv/a.jsonl", false)},
		rowVersionCase{name: "RecordSessionStartIdentity/no path", state: "pending",
			write: sessionStart("sess-a", "", false)},
		rowVersionCase{name: "RecordSessionStartIdentity/rotation archives once, one bump", state: "waiting",
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
				if err := f.s.ApplyHookTransition(id, "", true, "row_version_test"); err != nil {
					t.Fatalf("ApplyHookTransition soft refresh: %v", err)
				}
			},
			write: recordLaunch(1, store.CondChanged)},
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
