package api_test

// settle_read_test.go (b.3ie): Client.Status, Get and List with [state]
// settle_ms set, read on the Client's own clock (SetClockForTest): each
// reports the settled state, the three agree and list --state filters on it;
// no read sleeps or waits, at the largest settle time either; the permission
// requests and launch_started_at follow the raw state; with no settle time
// the settle columns are ignored.

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// settleMax is the largest settle time, the one the read tests run at.
const settleMax = int64(config.MaxStateSettleMs)

// settleReadBudget bounds one status, get or list call: no read waits for a
// state to settle (the TS client's call deadline is 30 s).
const settleReadBudget = 5 * time.Second

// settleClientFor is a Client on dbPath with [state] settle_ms = ms (0
// writes no key), tc as its tmux client, now as its clock and a sleep that
// fails t (no read may wait).
func settleClientFor(t *testing.T, dbPath string, ms int64, tc api.TmuxClient, now func() time.Time) *api.Client {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	apitest.WriteKeysConfig(t, cfgPath, apitest.ConfigKeys{StateSettleMs: ms})
	c, err := api.New(api.Options{StorePath: dbPath, ConfigPath: cfgPath, TmuxClient: tc})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	api.SetClockForTest(c, now)
	api.SetSleepForTest(c, func(d time.Duration) { t.Errorf("the Client slept %v; no read may wait", d) })
	return c
}

// clientReported returns id's state as c's Status, Get and List give it,
// failing t unless the three agree, List's state filter selects the row by
// that state, and each call returns within settleReadBudget.
func clientReported(t *testing.T, c *api.Client, id string) (api.StatusResult, api.SpawnRow, api.ListRow) {
	t.Helper()
	timed := func(what string, call func() error) {
		t.Helper()
		start := time.Now()
		if err := call(); err != nil {
			t.Fatalf("%s(%s): %v", what, id, err)
		}
		if took := time.Since(start); took > settleReadBudget {
			t.Errorf("%s took %v; want under %v", what, took, settleReadBudget)
		}
	}
	var (
		status api.StatusResult
		row    api.SpawnRow
		listed api.ListRow
	)
	listRow := func(states ...string) (api.ListRow, bool) {
		var res api.ListResult
		timed("List", func() (err error) { res, err = c.List(api.ListParams{State: states}); return err })
		for _, r := range res.Spawns {
			if r.ClaudeInstanceID == id {
				return r, true
			}
		}
		return api.ListRow{}, false
	}
	timed("Status", func() (err error) { status, err = c.Status(id); return err })
	timed("Get", func() (err error) { row, err = c.Get(id); return err })
	listed, _ = listRow()
	if row.State != status.State || listed.State != status.State {
		t.Errorf("%s: status %q, get %q, list %q; want the same state from all three", id, status.State, row.State, listed.State)
	}
	if _, ok := listRow(status.State); !ok {
		t.Errorf("%s: list --state %s does not select the row", id, status.State)
	}
	return status, row, listed
}

// TestClientSettledReads: with the largest settle time, a row raw working
// whose change from waiting is younger than it (by 1 ms, or with a stamp
// ahead of the clock) reads waiting, and from exactly the settle time
// working; a row with no settled state reads raw. With no settle time every
// row reads raw.
func TestClientSettledReads(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	nowMs := now.UnixMilli()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	cases := []struct {
		name    string
		settled string
		stampMs int64
		want    string
	}{
		{"younger than the settle time by 1 ms", store.StateWaiting, nowMs - 1, store.StateWaiting},
		{"1 ms short of the settle time", store.StateWaiting, nowMs - settleMax + 1, store.StateWaiting},
		{"held exactly the settle time", store.StateWaiting, nowMs - settleMax, store.StateWorking},
		{"held longer", store.StateWaiting, nowMs - 10*settleMax, store.StateWorking},
		{"stamped ahead of the reader's clock", store.StateWaiting, nowMs + 1000, store.StateWaiting},
		{"no settled state", "", nowMs, store.StateWorking},
	}
	ids := make([]string, len(cases))
	for i, tc := range cases {
		id, err := apitest.SeedSpawn(dbPath, "", store.StateWorking, "", "", "", true, apitest.WithSettle(tc.settled, tc.stampMs))
		if err != nil {
			t.Fatalf("SeedSpawn: %v", err)
		}
		ids[i] = id
	}
	on := settleClientFor(t, dbPath, settleMax, tmuxfix.NewRecorder(), func() time.Time { return now })
	off := settleClientFor(t, dbPath, 0, tmuxfix.NewRecorder(), func() time.Time { return now })
	for i, tc := range cases {
		if got, _, _ := clientReported(t, on, ids[i]); got.State != tc.want {
			t.Errorf("%s: reported %q; want %q", tc.name, got.State, tc.want)
		}
		if got, _, _ := clientReported(t, off, ids[i]); got.State != store.StateWorking {
			t.Errorf("%s, no settle time: reported %q; want the raw working", tc.name, got.State)
		}
	}
}

// TestClientSettledReadsKeepRawExtras: the settle time changes only state.
// A raw check_permission row reads whatever state it settled in, even ended or
// missing (b.hjj), and still shows its open request; a raw waiting row reading
// check_permission shows none (requests are never debounced); a raw waiting
// row reading pending (just after SessionStart) carries no launch_started_at,
// though its column still holds one.
func TestClientSettledReadsKeepRawExtras(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	fresh := now.UnixMilli() - 1
	dbPath := filepath.Join(t.TempDir(), "state.db")
	seed := func(raw, reported string, request bool, opts ...apitest.SpawnOption) string {
		id, err := apitest.SeedSpawn(dbPath, "", raw, "", "on", "", true,
			append([]apitest.SpawnOption{apitest.WithSettle(reported, fresh)}, opts...)...)
		if err != nil {
			t.Fatalf("SeedSpawn: %v", err)
		}
		if request {
			if _, err := apitest.SeedPermissionRequest(dbPath, id, "Bash"); err != nil {
				t.Fatalf("SeedPermissionRequest: %v", err)
			}
		}
		return id
	}
	asking := seed(store.StateCheckPermission, store.StateWaiting, true)
	stopped := seed(store.StateWaiting, store.StateCheckPermission, true)
	// Its column keeps a launch start, so only keying on the raw state hides it.
	started := seed(store.StateWaiting, store.StatePending, false, apitest.WithLaunchStartedAt(fresh))
	type read struct {
		id, state string
		requests  int
	}
	reads := []read{{asking, store.StateWaiting, 1}, {stopped, store.StateCheckPermission, 0}}
	// b.hjj: a hook's move to check_permission from any other state reads that state meanwhile.
	for _, from := range []string{store.StateWorking, store.StateAskUser, store.StatePending, store.StateEnded, store.StateMissing} {
		reads = append(reads, read{seed(store.StateCheckPermission, from, true), from, 1})
	}
	c := settleClientFor(t, dbPath, settleMax, tmuxfix.NewRecorder(), func() time.Time { return now })

	for _, tc := range reads {
		_, row, listed := clientReported(t, c, tc.id)
		if row.State != tc.state || len(row.PermissionRequests) != tc.requests || len(listed.PermissionRequests) != tc.requests {
			t.Errorf("%s: state %q, get's permission_requests %d, list's %d; want %q and %d (the raw state's)",
				tc.id, row.State, len(row.PermissionRequests), len(listed.PermissionRequests), tc.state, tc.requests)
		}
	}
	status, row, listed := clientReported(t, c, started)
	if status.State != store.StatePending || status.LaunchStartedAt != nil || row.LaunchStartedAt != nil ||
		listed.LaunchStartedAt != nil {
		t.Errorf("status %+v, get launch %v, list launch %v; want pending with no launch_started_at (raw waiting)",
			status, row.LaunchStartedAt, listed.LaunchStartedAt)
	}
	ids := []string{}
	res, err := c.List(api.ListParams{State: []string{store.StateWaiting}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, r := range res.Spawns {
		ids = append(ids, r.ClaudeInstanceID)
	}
	if !slices.Equal(ids, []string{asking}) {
		t.Errorf("list --state waiting = %v; want only the row reading waiting [%s]", ids, asking)
	}
}
