package store_test

// The launch identity write (SR-3.6, SR-5.2, SR-5.3, Appendix F.4):
// RecordLaunchIdentity's outcomes, and the identity column helpers the
// adoption and row_version tests share. Columns are read raw through
// apitest.ReadSpawnColumns.

import (
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// launchStart is the launch start, in ms, the tests' inserts record.
const launchStart int64 = 1767225600123

// createdIdentity is a create reply's identity, unlike fullIdentity in every
// field, so a write of it shows over a seeded identity.
func createdIdentity() store.LaunchIdentity {
	return store.LaunchIdentity{Token: "ffffffffffffffff", Socket: "/tmp/ad-launch-id-test/other-sock",
		ServerPID: 333, ServerStart: 1767225700, ServerStarttime: apitest.DarwinProcStarttime,
		PaneID: "%9", PanePID: 444, PaneStarttime: apitest.LinuxProcStarttime}
}

// identityColumns returns the six server and pane identity columns of c.
func identityColumns(c apitest.SpawnColumns) map[string]any {
	return map[string]any{"tmux_server_pid": c.TmuxServerPID, "tmux_server_started": c.TmuxServerStarted,
		"tmux_server_starttime": c.TmuxServerStarttime, "pane_id": c.PaneID, "pane_pid": c.PanePID, "pane_starttime": c.PaneStarttime}
}

// wantIdentityColumns is identityColumns as id is stored: zero as NULL.
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

// withIdentity returns c with its six identity columns as lid stores them.
func withIdentity(c apitest.SpawnColumns, lid store.LaunchIdentity) apitest.SpawnColumns {
	w := wantIdentityColumns(lid)
	c.TmuxServerPID, c.TmuxServerStarted = w["tmux_server_pid"], w["tmux_server_started"]
	c.TmuxServerStarttime, c.PaneID = w["tmux_server_starttime"], w["pane_id"]
	c.PanePID, c.PaneStarttime = w["pane_pid"], w["pane_starttime"]
	return c
}

// assertInsertedLaunch checks an inserted row: version 0, sp's launch start,
// token and socket (zero as NULL), and no server or pane identity.
func assertInsertedLaunch(t *testing.T, got apitest.SpawnColumns, sp store.Spawn) {
	t.Helper()
	orNil := func(v any, zero bool) any {
		if zero {
			return nil
		}
		return v
	}
	want := wantIdentityColumns(store.LaunchIdentity{})
	want["row_version"] = int64(0)
	want["launch_started_at"] = orNil(sp.LaunchStartedAtMillis, sp.LaunchStartedAtMillis == 0)
	want["launch_token"] = orNil(sp.Identity.Token, sp.Identity.Token == "")
	want["tmux_socket"] = orNil(sp.Identity.Socket, sp.Identity.Socket == "")
	g := identityColumns(got)
	g["row_version"], g["launch_started_at"] = got.RowVersion, got.LaunchStartedAt
	g["launch_token"], g["tmux_socket"] = got.LaunchToken, got.TmuxSocket
	if !reflect.DeepEqual(g, want) {
		t.Errorf("inserted launch columns:\n got %#v\nwant %#v", g, want)
	}
}

// TestRecordLaunchIdentityOutcomes checks each outcome on a row inserted at
// version 0: an applied write stores the six identity columns (zero as NULL)
// and +1 only; a wrong version or token, a row moved on, or an absent row
// writes nothing. The agent's hooks before it are ignored (SR-22.9).
func TestRecordLaunchIdentityOutcomes(t *testing.T) {
	blankStarts := createdIdentity()
	blankStarts.ServerStarttime, blankStarts.PaneStarttime = "", ""
	cases := []struct {
		name    string
		id      string                         // the id written; "" is the inserted row
		setup   func(t *testing.T, f *v5Store) // a write between the insert and the identity write
		version int64                          // the version passed; 0 is the insert's
		token   string                         // "" is the row's token
		lid     store.LaunchIdentity
		want    store.CondResult
	}{
		{name: "applied", lid: createdIdentity(), want: store.CondApplied},
		{name: "applied, empty start times stored as NULL", lid: blankStarts, want: store.CondApplied},
		{name: "changed, wrong version", version: 1, lid: createdIdentity(), want: store.CondChanged},
		{name: "changed, wrong token", token: "0000000000000000", lid: createdIdentity(), want: store.CondChanged},
		{name: "changed, row marked missing in between", version: 1, lid: createdIdentity(), want: store.CondChanged,
			setup: func(t *testing.T, f *v5Store) { rvMark.write("", nil, store.CondApplied, "pending")(t, f, "li-row") }},
		{name: "changed, another write in between", lid: createdIdentity(), want: store.CondChanged,
			setup: func(t *testing.T, f *v5Store) {
				if err := f.s.SetParentID("li-row", ""); err != nil {
					t.Fatalf("SetParentID: %v", err)
				}
			}},
		{name: "applied after the agent's earlier hooks were ignored", lid: createdIdentity(), want: store.CondApplied,
			setup: func(t *testing.T, f *v5Store) {
				for _, event := range []string{"SessionStart", "Stop"} {
					if got := storefix.ApplyAgentHook(t, f.s, "li-row", event, "sess-early"); got != (store.HookApplied{Reason: store.HookReasonNoPaneRecorded}) {
						t.Fatalf("%s before the identity write = %+v; want no_pane_recorded", event, got)
					}
				}
			}},
		{name: "absent", id: "li-missing", lid: createdIdentity(), want: store.CondAbsent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newV5Store(t)
			if err := f.s.InsertPending(store.Spawn{ClaudeInstanceID: "li-row", CWD: "/tmp", TmuxSessionName: "ts-li-row",
				RelayMode: "off", LaunchStartedAtMillis: launchStart, Identity: store.LaunchIdentity{Token: goodToken, Socket: "/tmp/ad-li/sock"}}); err != nil {
				t.Fatalf("InsertPending: %v", err)
			}
			if c.setup != nil {
				c.setup(t, f)
			}
			before := f.rawColumns("li-row")
			id, token := c.id, c.token
			if id == "" {
				id = "li-row"
			}
			if token == "" {
				token = goodToken
			}
			if got, err := f.s.RecordLaunchIdentity(id, c.version, token, c.lid); err != nil || got != c.want {
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
