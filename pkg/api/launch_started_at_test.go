package api_test

// launch_started_at_test.go covers launch_started_at on Status, Get and List
// (SR-22.2, SR-16.1, SR-5.5): present, as an RFC3339 UTC instant at
// millisecond precision, only on a pending row with a readable launch start;
// absent (never null) on every other row, and no verb fails on an unreadable
// value, including an integer outside the years 0 to 9999. It also covers Status's optional narrow read and a real spawn.

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The seeded launch starts: one with a non-zero millisecond fraction and one
// on a whole second, with the strings Go's time.Time JSON encoding gives them.
const (
	launchFracMillis  = int64(1790000000123)
	launchFracJSON    = "2026-09-21T14:13:20.123Z"
	launchWholeMillis = int64(1790000000000)
	launchWholeJSON   = "2026-09-21T14:13:20Z"
)

// launchShape is one seeded row: its state, seed options, and the launch
// start the verbs must show (wantMillis 0 and wantJSON "" = absent).
type launchShape struct {
	name       string
	state      string
	opts       []apitest.SpawnOption
	wantMillis int64
	wantJSON   string
}

// id is the shape's instance id in the shared store.
func (s launchShape) id() string { return "launch-" + strings.ReplaceAll(s.name, " ", "-") }

// launchShapes returns the readable pending rows (including the first and
// last millisecond of the range, years 0 to 9999 UTC), the unreadable pending
// rows (including an integer just outside the range on each side), and one
// row in every other state seeded with a readable launch start.
func launchShapes() []launchShape {
	// The range's inclusive ends in Unix milliseconds, and one past each end.
	firstMillis := time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	lastMillis := time.Date(9999, 12, 31, 23, 59, 59, 999_000_000, time.UTC).UnixMilli()
	const (
		firstJSON = "0000-01-01T00:00:00Z"
		lastJSON  = "9999-12-31T23:59:59.999Z"
	)
	year10000Millis := lastMillis + 1
	beforeYear0Millis := firstMillis - 1
	shapes := []launchShape{
		{"pending ms fraction", store.StatePending, []apitest.SpawnOption{apitest.WithLaunchStartedAt(launchFracMillis)}, launchFracMillis, launchFracJSON},
		{"pending whole second", store.StatePending, []apitest.SpawnOption{apitest.WithLaunchStartedAt(launchWholeMillis)}, launchWholeMillis, launchWholeJSON},
		{"pending first in range", store.StatePending, []apitest.SpawnOption{apitest.WithLaunchStartedAt(firstMillis)}, firstMillis, firstJSON},
		{"pending last in range", store.StatePending, []apitest.SpawnOption{apitest.WithLaunchStartedAt(lastMillis)}, lastMillis, lastJSON},
		{"pending year 10000", store.StatePending, []apitest.SpawnOption{apitest.WithLaunchStartedAt(year10000Millis)}, 0, ""},
		{"pending before year 0", store.StatePending, []apitest.SpawnOption{apitest.WithLaunchStartedAt(beforeYear0Millis)}, 0, ""},
		{"pending NULL", store.StatePending, []apitest.SpawnOption{apitest.WithNoLaunchStartedAt()}, 0, ""},
		{"pending text", store.StatePending, []apitest.SpawnOption{apitest.WithRawLaunchStartedAt("soon")}, 0, ""},
		{"pending real", store.StatePending, []apitest.SpawnOption{apitest.WithRawLaunchStartedAt(float64(launchFracMillis) + 0.5)}, 0, ""},
		{"pending blob", store.StatePending, []apitest.SpawnOption{apitest.WithRawLaunchStartedAt([]byte{0x01, 0x02})}, 0, ""},
	}
	for _, st := range []string{store.StateWaiting, store.StateWorking, store.StateAskUser,
		store.StateCheckPermission, store.StateEnded, store.StateMissing} {
		shapes = append(shapes, launchShape{st, st, []apitest.SpawnOption{apitest.WithLaunchStartedAt(launchFracMillis)}, 0, ""})
	}
	return shapes
}

// newLaunchClient returns a Client over a store seeded with every launchShape.
func newLaunchClient(t *testing.T, shapes []launchShape) *api.Client {
	t.Helper()
	c, _ := newTestClientWithRows(t, func(dbPath string) {
		for _, s := range shapes {
			if _, err := apitest.SeedSpawn(dbPath, s.id(), s.state, "", "", "", true, s.opts...); err != nil {
				t.Fatalf("SeedSpawn %s: %v", s.name, err)
			}
		}
	})
	return c
}

// launchVerb reads one row through a verb: its typed launch start and the
// value whose JSON encoding carries it (the result, or list's row).
type launchVerb struct {
	name string
	read func(t *testing.T, c *api.Client, id string) (*time.Time, any)
}

var launchVerbs = []launchVerb{
	{"status", func(t *testing.T, c *api.Client, id string) (*time.Time, any) {
		t.Helper()
		r, err := c.Status(id)
		if err != nil {
			t.Fatalf("Status(%s): %v", id, err)
		}
		return r.LaunchStartedAt, r
	}},
	{"get", func(t *testing.T, c *api.Client, id string) (*time.Time, any) {
		t.Helper()
		r, err := c.Get(id)
		if err != nil {
			t.Fatalf("Get(%s): %v", id, err)
		}
		return r.LaunchStartedAt, r
	}},
	{"list", func(t *testing.T, c *api.Client, id string) (*time.Time, any) {
		t.Helper()
		r, err := c.List(api.ListParams{})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, row := range r.Spawns {
			if row.ClaudeInstanceID == id {
				return row.LaunchStartedAt, row
			}
		}
		t.Fatalf("List has no row %s", id)
		return nil, nil
	}},
}

// assertLaunchTime checks the typed field: nil when wantMillis is 0, else
// exactly that instant with location UTC.
func assertLaunchTime(t *testing.T, got *time.Time, wantMillis int64) {
	t.Helper()
	if wantMillis == 0 {
		if got != nil {
			t.Errorf("LaunchStartedAt = %v; want nil", *got)
		}
		return
	}
	if got == nil {
		t.Fatalf("LaunchStartedAt = nil; want %v", time.UnixMilli(wantMillis).UTC())
	}
	if got.Location() != time.UTC {
		t.Errorf("LaunchStartedAt location = %v; want UTC", got.Location())
	}
	if !got.Equal(time.UnixMilli(wantMillis)) {
		t.Errorf("LaunchStartedAt = %v; want %v", *got, time.UnixMilli(wantMillis).UTC())
	}
}

// assertLaunchJSON checks v's JSON object: launch_started_at is the string
// want (ending in Z), or absent when want is "" (never null).
func assertLaunchJSON(t *testing.T, v any, want string) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	raw, ok := m["launch_started_at"]
	switch {
	case want == "" && ok:
		t.Errorf("launch_started_at = %s; want the key absent", raw)
	case want == "":
	case !ok:
		t.Errorf("launch_started_at absent; want %q in %s", want, b)
	case string(raw) != strconv.Quote(want) || !strings.HasSuffix(want, "Z"):
		t.Errorf("launch_started_at = %s; want %q", raw, want)
	}
}

// TestLaunchStartedAtByVerbAndRow: each verb shows the launch start only on a
// readable pending row, exactly to the millisecond in UTC, and never errors.
func TestLaunchStartedAtByVerbAndRow(t *testing.T) {
	shapes := launchShapes()
	c := newLaunchClient(t, shapes)
	for _, v := range launchVerbs {
		for _, s := range shapes {
			t.Run(v.name+"/"+s.name, func(t *testing.T) {
				got, res := v.read(t, c, s.id())
				assertLaunchTime(t, got, s.wantMillis)
				assertLaunchJSON(t, res, s.wantJSON)
			})
		}
	}
}

// TestLaunchStartedAtListMixedRows: List returns every row and the whole
// result encodes as JSON, even with out-of-range rows among them, and only the
// readable pending rows carry the field, each with its own value.
func TestLaunchStartedAtListMixedRows(t *testing.T) {
	shapes := launchShapes()
	c := newLaunchClient(t, shapes)
	res, err := c.List(api.ListParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := json.Marshal(res); err != nil {
		t.Fatalf("json.Marshal(List result): %v", err)
	}
	rows := map[string]api.ListRow{}
	for _, r := range res.Spawns {
		rows[r.ClaudeInstanceID] = r
	}
	if len(res.Spawns) != len(shapes) {
		t.Errorf("List rows = %d; want %d", len(res.Spawns), len(shapes))
	}
	for _, s := range shapes {
		r, ok := rows[s.id()]
		if !ok {
			t.Errorf("List has no row %s", s.id())
			continue
		}
		assertLaunchTime(t, r.LaunchStartedAt, s.wantMillis)
		assertLaunchJSON(t, r, s.wantJSON)
	}
}

// TestLaunchStartedAtStatusMalformedLabels: Status decodes no structured
// column, so a pending row with unparsable labels still shows its launch start.
func TestLaunchStartedAtStatusMalformedLabels(t *testing.T) {
	shape := launchShape{"pending bad labels", store.StatePending, []apitest.SpawnOption{
		apitest.WithRawLabels("{not json"), apitest.WithLaunchStartedAt(launchFracMillis)}, launchFracMillis, launchFracJSON}
	c := newLaunchClient(t, []launchShape{shape})
	got, res := launchVerbs[0].read(t, c, shape.id())
	assertLaunchTime(t, got, shape.wantMillis)
	assertLaunchJSON(t, res, shape.wantJSON)
}

// stateOnlyStore is a StatusStore with GetSpawnState only (no narrow read).
type stateOnlyStore struct{ state string }

func (s stateOnlyStore) GetSpawnState(string) (string, error) { return s.state, nil }

// The exported StatusStore and Status keep their signatures.
var (
	_ api.StatusStore                                         = stateOnlyStore{}
	_ func(api.StatusStore, string) (api.StatusResult, error) = api.Status
)

// TestLaunchStartedAtStatusNarrowRead: api.Status shows the launch start with
// the real store, none with a GetSpawnState-only store, and keeps not-found.
func TestLaunchStartedAtStatusNarrowRead(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	id, err := apitest.SeedSpawn(dbPath, "narrow-pending", store.StatePending, "", "", "", true,
		apitest.WithLaunchStartedAt(launchFracMillis))
	if err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	t.Run("real store", func(t *testing.T) {
		res, err := api.Status(s, id)
		if err != nil || res.State != store.StatePending {
			t.Fatalf("Status = %+v, %v; want pending, nil", res, err)
		}
		assertLaunchTime(t, res.LaunchStartedAt, launchFracMillis)
		assertLaunchJSON(t, res, launchFracJSON)
	})
	t.Run("real store unknown id", func(t *testing.T) {
		if _, err := api.Status(s, "narrow-absent"); !errors.Is(err, store.ErrSpawnNotFound) {
			t.Errorf("Status err = %v; want ErrSpawnNotFound", err)
		}
	})
	t.Run("GetSpawnState only", func(t *testing.T) {
		res, err := api.Status(stateOnlyStore{state: store.StatePending}, id)
		if err != nil || res.State != store.StatePending {
			t.Fatalf("Status = %+v, %v; want pending, nil", res, err)
		}
		assertLaunchTime(t, res.LaunchStartedAt, 0)
		assertLaunchJSON(t, res, "")
	})
}

// TestLaunchStartedAtAfterSpawn: a spawn at a fixed clock with a millisecond
// fraction shows that instant through Status, Get and List --state pending.
func TestLaunchStartedAtAfterSpawn(t *testing.T) {
	env := newSpawnEnv(t)
	want := env.clock.Now()
	const wantJSON = "2026-09-29T12:00:00.123Z"
	res, err := env.c.Spawn(api.SpawnParams{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	id := res.ClaudeInstanceID
	for _, v := range launchVerbs[:2] {
		t.Run(v.name, func(t *testing.T) {
			got, out := v.read(t, env.c, id)
			assertLaunchTime(t, got, want.UnixMilli())
			assertLaunchJSON(t, out, wantJSON)
		})
	}
	t.Run("list state pending", func(t *testing.T) {
		lr, err := env.c.List(api.ListParams{State: []string{store.StatePending}})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(lr.Spawns) != 1 || lr.Spawns[0].ClaudeInstanceID != id {
			t.Fatalf("List --state pending = %+v; want the one spawned row %s", lr.Spawns, id)
		}
		assertLaunchTime(t, lr.Spawns[0].LaunchStartedAt, want.UnixMilli())
		assertLaunchJSON(t, lr.Spawns[0], wantJSON)
	})
}
