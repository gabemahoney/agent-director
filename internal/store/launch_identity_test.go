package store_test

// Launch-identity write tests (SR-3.6, SR-5.2, SR-5.3, Appendix F.4): the
// launch columns InsertPending writes and RecordLaunchIdentity's outcomes.
// Columns are read raw through apitest.ReadSpawnColumns; the package's
// sandbox-guarded TestMain in store_test.go covers this file.

import (
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// launchStart is the launch start, in ms, the tests' inserts record.
const launchStart int64 = 1767225600123

// createdIdentity is a create reply's identity, different from fullIdentity
// in every field so a write of it is observable over a seeded identity.
func createdIdentity() store.LaunchIdentity {
	return store.LaunchIdentity{
		Token:           "ffffffffffffffff", // ignored by RecordLaunchIdentity
		Socket:          "/tmp/ad-launch-id-test/other-sock",
		ServerPID:       333,
		ServerStart:     1767225700,
		ServerStarttime: apitest.DarwinProcStarttime,
		PaneID:          "%9",
		PanePID:         444,
		PaneStarttime:   apitest.LinuxProcStarttime,
	}
}

// identityColumns returns the six server and pane identity columns of c.
func identityColumns(c apitest.SpawnColumns) map[string]any {
	return map[string]any{
		"tmux_server_pid": c.TmuxServerPID, "tmux_server_started": c.TmuxServerStarted,
		"tmux_server_starttime": c.TmuxServerStarttime, "pane_id": c.PaneID,
		"pane_pid": c.PanePID, "pane_starttime": c.PaneStarttime,
	}
}

// wantIdentityColumns is identityColumns as id should be stored: zero as NULL.
func wantIdentityColumns(id store.LaunchIdentity) map[string]any {
	orNil := func(v any, zero bool) any {
		if zero {
			return nil
		}
		return v
	}
	return map[string]any{
		"tmux_server_pid":       orNil(int64(id.ServerPID), id.ServerPID <= 0),
		"tmux_server_started":   orNil(id.ServerStart, id.ServerStart <= 0),
		"tmux_server_starttime": orNil(id.ServerStarttime, id.ServerStarttime == ""),
		"pane_id":               orNil(id.PaneID, id.PaneID == ""),
		"pane_pid":              orNil(int64(id.PanePID), id.PanePID <= 0),
		"pane_starttime":        orNil(id.PaneStarttime, id.PaneStarttime == ""),
	}
}

// insertLaunch begins a launch the way plain spawn does: a pending row at
// version 0 with the launch start, token and socket.
func (f *v5Store) insertLaunch(id string, ms int64, lid store.LaunchIdentity) store.Spawn {
	f.t.Helper()
	sp := store.Spawn{ClaudeInstanceID: id, CWD: "/tmp", TmuxSessionName: "ts-" + id, RelayMode: "off",
		LaunchStartedAtMillis: ms, Identity: lid}
	if err := f.s.InsertPending(sp); err != nil {
		f.t.Fatalf("InsertPending: %v", err)
	}
	return sp
}

// assertInsertedLaunch checks an inserted row: version 0, sp's launch start,
// token and socket (zero as NULL), and NULL server and pane identity.
func assertInsertedLaunch(t *testing.T, got apitest.SpawnColumns, sp store.Spawn) {
	t.Helper()
	want := wantIdentityColumns(store.LaunchIdentity{})
	want["row_version"], want["launch_started_at"] = int64(0), any(nil)
	want["launch_token"], want["tmux_socket"] = any(nil), any(nil)
	if sp.LaunchStartedAtMillis != 0 {
		want["launch_started_at"] = sp.LaunchStartedAtMillis
	}
	if sp.Identity.Token != "" {
		want["launch_token"] = sp.Identity.Token
	}
	if sp.Identity.Socket != "" {
		want["tmux_socket"] = sp.Identity.Socket
	}
	g := identityColumns(got)
	g["row_version"], g["launch_started_at"] = got.RowVersion, got.LaunchStartedAt
	g["launch_token"], g["tmux_socket"] = got.LaunchToken, got.TmuxSocket
	if !reflect.DeepEqual(g, want) {
		t.Errorf("inserted launch columns:\n got %#v\nwant %#v", g, want)
	}
}

// TestInsertPendingLaunchColumns checks the insert stores the launch start,
// token and socket, zero values as NULL, and never the pane or server identity.
func TestInsertPendingLaunchColumns(t *testing.T) {
	cases := []struct {
		name string
		ms   int64
		lid  store.LaunchIdentity
	}{
		{"full identity carried, only token and socket stored", launchStart, fullIdentity()},
		{"zero launch values stored as NULL", 0, store.LaunchIdentity{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newV5Store(t)
			sp := f.insertLaunch("li-insert", c.ms, c.lid)
			assertInsertedLaunch(t, f.rawColumns("li-insert"), sp)
		})
	}
}

// TestRecordLaunchIdentityOutcomes checks each outcome on a row inserted at
// version 0: Applied writes the six columns and +1 only; the others write nothing.
func TestRecordLaunchIdentityOutcomes(t *testing.T) {
	current := func(c apitest.SpawnColumns) int64 { return c.RowVersion.(int64) }
	blankStarts := createdIdentity()
	blankStarts.ServerStarttime, blankStarts.PaneStarttime = "", ""
	cases := []struct {
		name    string
		id      string                           // the id written; "" is the inserted row
		setup   func(t *testing.T, f *v5Store)   // a write landing between insert and identity
		version func(apitest.SpawnColumns) int64 // the version passed; nil is 0, the launch's
		token   string                           // "" is the row's token
		lid     store.LaunchIdentity
		want    store.CondResult
	}{
		{name: "applied", lid: createdIdentity(), want: store.CondApplied},
		{name: "applied, empty start times stored as NULL", lid: blankStarts, want: store.CondApplied},
		{name: "changed, wrong version", version: func(apitest.SpawnColumns) int64 { return 1 },
			lid: createdIdentity(), want: store.CondChanged},
		{name: "changed, wrong token", token: "0000000000000000", lid: createdIdentity(), want: store.CondChanged},
		// SR-22.9: no hook applies before the identity write (the row has no
		// pane), so the in-between writes are non-hook writes.
		{name: "changed, row no longer pending", version: current, lid: createdIdentity(), want: store.CondChanged,
			setup: func(t *testing.T, f *v5Store) { rvMark.write("", nil, store.CondApplied, "pending")(t, f, "li-row") }},
		{name: "changed, a write in between", lid: createdIdentity(), want: store.CondChanged,
			setup: func(t *testing.T, f *v5Store) {
				if err := f.s.SetParentID("li-row", ""); err != nil {
					t.Fatalf("SetParentID: %v", err)
				}
			}},
		// SR-22.9: the agent's hooks before the identity write are not applied
		// (no_pane_recorded) and write nothing, so the write then applies.
		{name: "applied after the agent's hooks before it were ignored", lid: createdIdentity(), want: store.CondApplied,
			setup: earlyAgentHooks},
		{name: "absent", id: "li-missing", lid: createdIdentity(), want: store.CondAbsent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newV5Store(t)
			f.insertLaunch("li-row", launchStart, store.LaunchIdentity{Token: goodToken, Socket: "/tmp/ad-li/sock"})
			if c.setup != nil {
				c.setup(t, f)
			}
			before := f.rawColumns("li-row")
			id, token, version := c.id, c.token, int64(0)
			if id == "" {
				id = "li-row"
			}
			if token == "" {
				token = goodToken
			}
			if c.version != nil {
				version = c.version(before)
			}
			got, err := f.s.RecordLaunchIdentity(id, version, token, c.lid)
			if err != nil || got != c.want {
				t.Fatalf("RecordLaunchIdentity = %v, %v; want %v, nil", got, err, c.want)
			}
			want := before
			if c.want == store.CondApplied {
				want = withIdentity(before, c.lid)
				want.RowVersion = before.RowVersion.(int64) + 1
			}
			if after := f.rawColumns("li-row"); !reflect.DeepEqual(after, want) {
				t.Errorf("row after the write:\n got %+v\nwant %+v", after, want)
			}
		})
	}
}

// TestRecordLaunchIdentityDriverError checks a failed statement returns an
// error and no outcome.
func TestRecordLaunchIdentityDriverError(t *testing.T) {
	f := newV5Store(t)
	f.insertLaunch("li-row", launchStart, store.LaunchIdentity{Token: goodToken})
	if err := f.s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got, err := f.s.RecordLaunchIdentity("li-row", 0, goodToken, createdIdentity()); err == nil || got != 0 {
		t.Fatalf("RecordLaunchIdentity on a closed store = %v, %v; want 0 and an error", got, err)
	}
}

// earlyAgentHooks fires the agent's SessionStart and Stop at the li-row row
// before its pane is recorded and fails unless each is ignored as
// no_pane_recorded (SR-22.9).
func earlyAgentHooks(t *testing.T, f *v5Store) {
	t.Helper()
	want := store.HookApplied{Reason: store.HookReasonNoPaneRecorded}
	for _, event := range []string{"SessionStart", "Stop"} {
		if got := storefix.ApplyAgentHook(t, f.s, "li-row", event, "sess-early"); got != want {
			t.Fatalf("%s before the identity write = %+v; want %+v", event, got, want)
		}
	}
}

// withIdentity returns c with its six identity columns as lid stores them.
func withIdentity(c apitest.SpawnColumns, lid store.LaunchIdentity) apitest.SpawnColumns {
	w := wantIdentityColumns(lid)
	c.TmuxServerPID, c.TmuxServerStarted = w["tmux_server_pid"], w["tmux_server_started"]
	c.TmuxServerStarttime, c.PaneID = w["tmux_server_starttime"], w["pane_id"]
	c.PanePID, c.PaneStarttime = w["pane_pid"], w["pane_starttime"]
	return c
}
