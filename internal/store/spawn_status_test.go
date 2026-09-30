package store_test

// SpawnStatus tests (SR-22.2, SR-16.1, SR-5.5): the narrow status read returns
// the row's state and stored launch start in one read, never failing on the
// stored launch start or on a malformed structured column. Rows are seeded
// through apitest (SR-20.2); newV5Store and seed live in spawn_v5_read_test.go.

import (
	"errors"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// *store.Store still satisfies the unchanged one-method StatusStore.
var _ api.StatusStore = (*store.Store)(nil)

// statusStartedAt is a started_at with a known SR-20.3 default launch start.
var statusStartedAt = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// launchMillis is a stored launch start with a non-zero ms fraction.
const launchMillis int64 = 1767225600123

// mustSpawnStatus calls SpawnStatus, failing the test on any error.
func mustSpawnStatus(t *testing.T, s *store.Store, id string) (string, int64) {
	t.Helper()
	state, millis, err := s.SpawnStatus(id)
	if err != nil {
		t.Fatalf("SpawnStatus(%s) failed: %v; want no error", id, err)
	}
	return state, millis
}

// TestSpawnStatusReturnsStateAndStoredLaunchStart checks each row shape reads
// back its state and its stored launch start (0 when NULL, non-integer or
// outside the years 0 to 9999; SR-5.5).
func TestSpawnStatusReturnsStateAndStoredLaunchStart(t *testing.T) {
	type statusCase struct {
		name     string
		state    string
		opt      apitest.SpawnOption
		want     int64
		rawStart *int64 // the stored launch start to confirm before reading
	}
	cases := []statusCase{
		{"pending, integer ms with fraction", store.StatePending, apitest.WithLaunchStartedAt(launchMillis), launchMillis, nil},
		{"pending, SR-20.3 default", store.StatePending, nil, statusStartedAt.UnixMilli(), nil},
		{"pending, NULL", store.StatePending, apitest.WithNoLaunchStartedAt(), 0, nil},
		{"pending, text", store.StatePending, apitest.WithRawLaunchStartedAt("yesterday"), 0, nil},
		{"pending, real", store.StatePending, apitest.WithRawLaunchStartedAt(1767225600123.5), 0, nil},
		{"pending, blob", store.StatePending, apitest.WithRawLaunchStartedAt([]byte("1767225600123")), 0, nil},
		{"waiting", store.StateWaiting, nil, 0, nil},
		{"working", store.StateWorking, nil, 0, nil},
		{"ask_user", store.StateAskUser, nil, 0, nil},
		{"check_permission", store.StateCheckPermission, nil, 0, nil},
		{"ended", store.StateEnded, nil, 0, nil},
		{"missing", store.StateMissing, nil, 0, nil},
		// The read returns what is stored; the pending-only gate lives in pkg/api.
		{"waiting, stored launch start", store.StateWaiting, apitest.WithLaunchStartedAt(launchMillis), launchMillis, nil},
		{"ended, stored launch start", store.StateEnded, apitest.WithLaunchStartedAt(launchMillis), launchMillis, nil},
	}
	for _, b := range launchStartBoundaries {
		cases = append(cases, statusCase{"pending, " + b.name, store.StatePending,
			apitest.WithLaunchStartedAt(b.stored), b.want, &b.stored})
	}
	f := newV5Store(t)
	for _, tc := range cases {
		opts := []apitest.SpawnOption{apitest.WithStartedAt(statusStartedAt)}
		if tc.opt != nil {
			opts = append(opts, tc.opt)
		}
		id := f.seed(tc.state, "", opts...)
		if tc.rawStart != nil {
			assertStoredLaunchStart(t, f, id, *tc.rawStart)
		}
		t.Run(tc.name, func(t *testing.T) {
			state, millis := mustSpawnStatus(t, f.s, id)
			if state != tc.state {
				t.Errorf("state = %q; want %q", state, tc.state)
			}
			if millis != tc.want {
				t.Errorf("launchStartedAtMillis = %d; want %d", millis, tc.want)
			}
		})
	}
}

// TestSpawnStatusIgnoresMalformedStructuredColumns checks a pending row whose
// labels, claude_args or extra_env is not JSON still answers (GetSpawn fails).
func TestSpawnStatusIgnoresMalformedStructuredColumns(t *testing.T) {
	cases := []struct {
		name string
		opt  apitest.SpawnOption
	}{
		{"labels", apitest.WithRawLabels("{not json")},
		{"claude_args", apitest.WithRawClaudeArgs("[not json")},
		{"extra_env", apitest.WithRawExtraEnv("{not json")},
	}
	f := newV5Store(t)
	for _, tc := range cases {
		id := f.seed(store.StatePending, "", apitest.WithLaunchStartedAt(launchMillis), tc.opt)
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.s.GetSpawn(id); err == nil {
				t.Fatalf("GetSpawn(%s) succeeded; want the malformed %s to fail the full read", id, tc.name)
			}
			state, millis := mustSpawnStatus(t, f.s, id)
			if state != store.StatePending || millis != launchMillis {
				t.Errorf("SpawnStatus = (%q, %d); want (%q, %d)", state, millis, store.StatePending, launchMillis)
			}
		})
	}
}

// TestSpawnStatusUnknownIDIsNotFound checks an unknown id gives ErrSpawnNotFound,
// as GetSpawnState does, with no state and no launch start.
func TestSpawnStatusUnknownIDIsNotFound(t *testing.T) {
	f := newV5Store(t)
	f.seed(store.StatePending, "")
	state, millis, err := f.s.SpawnStatus("no-such-id")
	if !errors.Is(err, store.ErrSpawnNotFound) {
		t.Fatalf("SpawnStatus err = %v; want ErrSpawnNotFound", err)
	}
	if state != "" || millis != 0 {
		t.Errorf("SpawnStatus = (%q, %d); want (\"\", 0) with the error", state, millis)
	}
	if _, gerr := f.s.GetSpawnState("no-such-id"); gerr == nil || gerr.Error() != err.Error() {
		t.Errorf("GetSpawnState err = %v; want the same error as SpawnStatus (%v)", gerr, err)
	}
}
