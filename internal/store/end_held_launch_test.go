package store_test

// EndHeldLaunch tests (SR-9.4, SR-5.3, SR-5.8, Appendix F.4): a plain spawn's
// end write after "duplicate session" applies only to the pending row at
// version 0 with the insert's launch start, and writes only its four columns.
// Rows are seeded through apitest and read raw through apitest.ReadSpawnColumns.

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// heldStart is the launch start, in ms, the held launch's insert recorded.
const heldStart int64 = 1767225600789

// heldEndedAt is the end time passed, off UTC and mid-second; the store keeps
// it as heldEndedAtText, the CURRENT_TIMESTAMP layout (UTC, whole seconds).
var heldEndedAt = time.Date(2026, 9, 30, 14, 5, 6, 789_000_000, time.FixedZone("UTC+5", 5*3600))

const heldEndedAtText = "2026-09-30 09:05:06"

// heldGuards names the three guard columns EndHeldLaunch compares.
const (
	guardState       = "state"
	guardVersion     = "row_version"
	guardLaunchStart = "launch_started_at"
)

// seedHeldRow seeds a pending row at version 0 with heldStart and every column
// the write must keep set to a non-default value, plus opts.
func seedHeldRow(f *v5Store, opts ...apitest.SpawnOption) string {
	f.t.Helper()
	all := []apitest.SpawnOption{
		apitest.WithLaunchStartedAt(heldStart),
		apitest.WithLaunchIdentity(fullIdentity()),
		apitest.WithLifeNumber(7),
		apitest.WithNoPreTrust(),
		apitest.WithPID(5151),
		apitest.WithProcStarttime(apitest.LinuxProcStarttime),
		apitest.WithJsonlPath("/tmp/ad-held-test/cur.jsonl"),
		apitest.WithLivenessUnverifiedSince("2026-09-28 10:00:00"),
		apitest.WithLivenessNote("held-test note"),
		apitest.WithExtraEnv(map[string]string{"HELD": "1"}),
		apitest.WithRawLabels(`{"team":"held"}`),
		apitest.WithRawClaudeArgs(`["--held"]`),
		apitest.WithStartedAt(time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)),
		// The session id seed advanced the version; the insert's is 0.
		apitest.WithRowVersion(0),
	}
	return f.seed(store.StatePending, "sess-held", append(all, opts...)...)
}

// heldGuardMismatches lists the guard columns of c that differ from the
// held launch's insert (pending, version 0, heldStart).
func heldGuardMismatches(c apitest.SpawnColumns) []string {
	var out []string
	if c.State != store.StatePending {
		out = append(out, guardState)
	}
	if c.RowVersion != int64(0) {
		out = append(out, guardVersion)
	}
	if c.LaunchStartedAt != heldStart {
		out = append(out, guardLaunchStart)
	}
	return out
}

// TestEndHeldLaunchApplied checks the applied write sets only state, ended_at
// (raw store-layout text), launch_started_at and row_version.
func TestEndHeldLaunchApplied(t *testing.T) {
	f := newV5Store(t)
	id := seedHeldRow(f)
	before := f.rawColumns(id)
	if m := heldGuardMismatches(before); len(m) != 0 {
		t.Fatalf("seeded row differs from the insert on %v", m)
	}
	v := reflect.ValueOf(before)
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		if name != "ParentID" && name != "EndedAt" && v.Field(i).IsNil() {
			t.Fatalf("seed left %s NULL; the kept-column check would be vacuous", name)
		}
	}

	got, err := f.s.EndHeldLaunch(id, heldStart, heldEndedAt)
	if err != nil || got != store.CondApplied {
		t.Fatalf("EndHeldLaunch = %v, %v; want CondApplied, nil", got, err)
	}
	want := before
	want.State, want.EndedAt = store.StateEnded, heldEndedAtText
	want.LaunchStartedAt, want.RowVersion = nil, int64(1)
	if after := f.rawColumns(id); !reflect.DeepEqual(after, want) {
		t.Errorf("row after the end write:\n got %+v\nwant %+v", after, want)
	}
}

// TestEndHeldLaunchNotApplied checks each row the insert no longer describes
// gives CondChanged and is left exactly as it was.
func TestEndHeldLaunchNotApplied(t *testing.T) {
	cases := []struct {
		name  string
		seed  func(t *testing.T, f *v5Store) string
		guard string // the one guard column that differs; "" when several do
	}{
		{name: "deleted and inserted again with another launch start", guard: guardLaunchStart,
			seed: func(t *testing.T, f *v5Store) string {
				id := f.seed(store.StatePending, "", apitest.WithLaunchStartedAt(heldStart))
				if err := f.s.DeleteSpawn(id); err != nil {
					t.Fatalf("DeleteSpawn: %v", err)
				}
				if _, err := apitest.SeedSpawn(f.path, id, store.StatePending, "/tmp", "", "", false,
					apitest.WithLaunchStartedAt(heldStart+1000)); err != nil {
					t.Fatalf("SeedSpawn again: %v", err)
				}
				return id
			}},
		{name: "another versioned write came first", guard: guardVersion,
			seed: func(t *testing.T, f *v5Store) string {
				id := f.seed(store.StatePending, "", apitest.WithLaunchStartedAt(heldStart))
				apitest.SeedSessionID(t, f.path, id, "sess-competing")
				return id
			}},
		{name: "off pending at version 0", guard: guardState,
			seed: func(t *testing.T, f *v5Store) string {
				return f.seed(store.StateEnded, "", apitest.WithLaunchStartedAt(heldStart), apitest.WithRowVersion(0))
			}},
		{name: "no longer pending after find-missing's mark",
			seed: func(t *testing.T, f *v5Store) string {
				id := f.seed(store.StatePending, "", apitest.WithLaunchStartedAt(heldStart))
				prior, res, err := f.s.MarkMissingIfSameLife(id, rvExamine(t, f, id).Snapshot)
				if err != nil || res != store.CondApplied || prior != store.StatePending {
					t.Fatalf("MarkMissingIfSameLife = %q, %v, %v; want pending, CondApplied, nil", prior, res, err)
				}
				return id
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newV5Store(t)
			id := c.seed(t, f)
			before := f.rawColumns(id)
			m := heldGuardMismatches(before)
			if len(m) == 0 || (c.guard != "" && !slices.Equal(m, []string{c.guard})) {
				t.Fatalf("guards differing from the insert = %v; want only %q", m, c.guard)
			}
			got, err := f.s.EndHeldLaunch(id, heldStart, heldEndedAt)
			if err != nil || got != store.CondChanged {
				t.Fatalf("EndHeldLaunch = %v, %v; want CondChanged, nil", got, err)
			}
			if after := f.rawColumns(id); !reflect.DeepEqual(after, before) {
				t.Errorf("row changed:\n got %+v\nwant %+v", after, before)
			}
		})
	}
}

// TestEndHeldLaunchAbsent checks an id with no row gives CondAbsent and
// creates none.
func TestEndHeldLaunchAbsent(t *testing.T) {
	f := newV5Store(t)
	got, err := f.s.EndHeldLaunch("held-absent", heldStart, heldEndedAt)
	if err != nil || got != store.CondAbsent {
		t.Fatalf("EndHeldLaunch = %v, %v; want CondAbsent, nil", got, err)
	}
	if _, err := apitest.ReadSpawnColumns(f.path, "held-absent"); !errors.Is(err, store.ErrSpawnNotFound) {
		t.Errorf("ReadSpawnColumns after the write: %v; want ErrSpawnNotFound", err)
	}
}

// TestEndHeldLaunchStoreError checks a failing write returns an error, no
// result, and leaves the row unchanged (SR-5.8).
func TestEndHeldLaunchStoreError(t *testing.T) {
	f := newV5Store(t)
	id := seedHeldRow(f)
	storefix.InjectWriteFailure(t, f.path, storefix.WriteFailReuseRestore, id)
	before := f.rawColumns(id)
	got, err := f.s.EndHeldLaunch(id, heldStart, heldEndedAt)
	if err == nil || got != 0 {
		t.Fatalf("EndHeldLaunch = %v, %v; want no result and an error", got, err)
	}
	if after := f.rawColumns(id); !reflect.DeepEqual(after, before) {
		t.Errorf("row changed:\n got %+v\nwant %+v", after, before)
	}
}

// TestEndHeldLaunchExpireSelection checks expire's candidate read selects the
// stored ended_at once older than the cutoff, and not before.
func TestEndHeldLaunchExpireSelection(t *testing.T) {
	const window = time.Hour
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		endedAt  time.Time // off UTC, so an unconverted time compares wrongly
		selected bool
	}{
		{"older than the cutoff", now.Add(-2 * window).In(time.FixedZone("UTC+5", 5*3600)), true},
		{"not yet older than the cutoff", now.Add(-window / 2).In(time.FixedZone("UTC-5", -5*3600)), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newV5Store(t)
			id := seedHeldRow(f)
			if got, err := f.s.EndHeldLaunch(id, heldStart, c.endedAt); err != nil || got != store.CondApplied {
				t.Fatalf("EndHeldLaunch = %v, %v; want CondApplied, nil", got, err)
			}
			var want []string
			if c.selected {
				want = []string{id}
			}
			if got := expCandidateIDs(t, f, now.Add(-window)); !slices.Equal(got, want) {
				t.Errorf("ListExpireCandidates = %v; want %v", got, want)
			}
		})
	}
}
