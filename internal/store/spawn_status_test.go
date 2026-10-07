package store_test

// The narrow state reads (SR-22.2, SR-16.1, SR-5.5, SR-9.3): SpawnStatus's
// state and stored launch start in one read, never failing on the stored
// launch start or a malformed structured column, and SpawnState's collision
// pre-check. Rows are seeded through apitest (SR-20.2).

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

// TestSpawnStatus checks each row reads back its state and its stored launch
// start (0 when NULL or unreadable, SR-5.5), the pending-only gate living in
// pkg/api; a row whose labels, args or env GetSpawn cannot decode still
// answers; an unknown id is ErrSpawnNotFound, as GetSpawnState says.
func TestSpawnStatus(t *testing.T) {
	const launchMillis int64 = 1767225600123
	startedAt := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	cases := []struct {
		name, state string
		opt         apitest.SpawnOption
		want        int64
		malformed   bool // GetSpawn must fail on the row
	}{
		{"pending, integer ms with fraction", store.StatePending, apitest.WithLaunchStartedAt(launchMillis), launchMillis, false},
		{"pending, SR-20.3 default", store.StatePending, nil, startedAt.UnixMilli(), false},
		{"pending, NULL", store.StatePending, apitest.WithNoLaunchStartedAt(), 0, false},
		{"pending, text", store.StatePending, apitest.WithRawLaunchStartedAt("yesterday"), 0, false},
		{"pending, after year 9999", store.StatePending, apitest.WithLaunchStartedAt(lastInRangeLaunchMillis + 1), 0, false},
		{"working", store.StateWorking, nil, 0, false},
		{"missing", store.StateMissing, nil, 0, false},
		{"ended, stored launch start", store.StateEnded, apitest.WithLaunchStartedAt(launchMillis), launchMillis, false},
		{"pending, malformed labels", store.StatePending, apitest.WithRawLabels("{not json"), startedAt.UnixMilli(), true},
		{"pending, malformed claude_args", store.StatePending, apitest.WithRawClaudeArgs("[not json"), startedAt.UnixMilli(), true},
		{"pending, malformed extra_env", store.StatePending, apitest.WithRawExtraEnv("{not json"), startedAt.UnixMilli(), true},
	}
	f := newV5Store(t)
	for _, tc := range cases {
		opts := []apitest.SpawnOption{apitest.WithStartedAt(startedAt)}
		if tc.opt != nil {
			opts = append(opts, tc.opt)
		}
		id := f.seed(tc.state, "", opts...)
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.s.GetSpawn(id); (err != nil) != tc.malformed {
				t.Fatalf("GetSpawn err = %v; want an error %v", err, tc.malformed)
			}
			state, millis, err := f.s.SpawnStatus(id)
			if err != nil || state != tc.state || millis != tc.want {
				t.Errorf("SpawnStatus = %q, %d, %v; want %q, %d, nil", state, millis, err, tc.state, tc.want)
			}
		})
	}
	state, millis, err := f.s.SpawnStatus("no-such-id")
	if !errors.Is(err, store.ErrSpawnNotFound) || state != "" || millis != 0 {
		t.Errorf("SpawnStatus(unknown) = %q, %d, %v; want ErrSpawnNotFound", state, millis, err)
	}
	if _, gerr := f.s.GetSpawnState("no-such-id"); gerr == nil || gerr.Error() != err.Error() {
		t.Errorf("GetSpawnState err = %v; want SpawnStatus's %v", gerr, err)
	}
}

// TestSpawnState checks the collision pre-check (SR-9.3): no row is not found
// with no error, and every stored state reads back as itself.
func TestSpawnState(t *testing.T) {
	f := newV5Store(t)
	if state, found, err := f.s.SpawnState("ss-absent"); err != nil || found || state != "" {
		t.Errorf("SpawnState(no row) = %q, %v, %v; want \"\", false, nil", state, found, err)
	}
	for _, want := range []string{store.StatePending, store.StateWaiting, store.StateWorking, store.StateAskUser,
		store.StateCheckPermission, store.StateEnded, store.StateMissing} {
		if state, found, err := f.s.SpawnState(f.seed(want, "")); err != nil || !found || state != want {
			t.Errorf("SpawnState = %q, %v, %v; want %q, true, nil", state, found, err, want)
		}
	}
}
