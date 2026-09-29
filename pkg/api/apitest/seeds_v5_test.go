package apitest

import (
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// wellFormedToken matches a launch token: 16 lowercase hex characters (SR-3.5).
var wellFormedToken = regexp.MustCompile(`^[0-9a-f]{16}$`)

// seedV5Row seeds id in state with opts (creating the store if needed) and
// returns the row's raw columns through ReadSpawnColumns.
func seedV5Row(t *testing.T, dbPath, id, state string, opts ...SpawnOption) SpawnColumns {
	t.Helper()
	if _, err := SeedSpawn(dbPath, id, state, "/tmp", "off", "", true, opts...); err != nil {
		t.Fatalf("SeedSpawn(%q, %s): %v", id, state, err)
	}
	cols, err := ReadSpawnColumns(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns(%q): %v", id, err)
	}
	return cols
}

// getSpawnV5 reads id through the store's own GetSpawn.
func getSpawnV5(t *testing.T, dbPath, id string) store.Spawn {
	t.Helper()
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close() //nolint:errcheck
	sp, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn(%q): %v", id, err)
	}
	return sp
}

// identityCols is the eight launch-identity columns, in LaunchIdentity order.
func identityCols(c SpawnColumns) []any {
	return []any{c.LaunchToken, c.TmuxSocket, c.TmuxServerPID, c.TmuxServerStarted,
		c.TmuxServerStarttime, c.PaneID, c.PanePID, c.PaneStarttime}
}

// TestSeedSpawn_V5Options_RoundTrip seeds each schema-v5 option and asserts the
// raw column holds exactly the seeded value, and that GetSpawn agrees where it can decode it.
func TestSeedSpawn_V5Options_RoundTrip(t *testing.T) {
	t.Parallel()
	offsetTime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.FixedZone("UTC+1", 3600))
	const offsetTimeUTC = "2020-01-02 02:04:05"
	fullIdentity := store.LaunchIdentity{
		Token: "0123456789abcdef", Socket: "/tmp/ad-v5-test/sock", ServerPID: 4242,
		ServerStart: 1700000000, ServerStarttime: LinuxProcStarttime,
		PaneID: "%7", PanePID: 4343, PaneStarttime: DarwinProcStarttime,
	}

	cases := []struct {
		name  string
		state string
		opts  []SpawnOption
		raw   func(SpawnColumns) any
		want  any
		// viaStore reads the same value through GetSpawn; nil where the
		// seeded value is one GetSpawn cannot decode.
		viaStore  func(store.Spawn) any
		wantStore any
	}{
		{
			name: "WithTmuxSessionName", state: "waiting",
			opts: []SpawnOption{WithTmuxSessionName(" odd:name.x ")},
			raw:  func(c SpawnColumns) any { return c.TmuxSessionName }, want: " odd:name.x ",
			viaStore:  func(sp store.Spawn) any { return []string{sp.TmuxSessionName, sp.Snapshot.TmuxSessionName} },
			wantStore: []string{" odd:name.x ", " odd:name.x "},
		},
		{
			name: "WithLaunchStartedAt/pending", state: "pending",
			opts: []SpawnOption{WithLaunchStartedAt(1700000000123)},
			raw:  func(c SpawnColumns) any { return c.LaunchStartedAt }, want: int64(1700000000123),
			viaStore: func(sp store.Spawn) any { return sp.LaunchStartedAtMillis }, wantStore: int64(1700000000123),
		},
		{
			name: "WithLaunchStartedAt/waiting", state: "waiting",
			opts: []SpawnOption{WithLaunchStartedAt(1700000000456)},
			raw:  func(c SpawnColumns) any { return c.LaunchStartedAt }, want: int64(1700000000456),
			viaStore: func(sp store.Spawn) any { return sp.LaunchStartedAtMillis }, wantStore: int64(1700000000456),
		},
		{
			name: "WithRawLaunchStartedAt/text", state: "pending",
			opts: []SpawnOption{WithRawLaunchStartedAt("soon")},
			raw:  func(c SpawnColumns) any { return c.LaunchStartedAt }, want: "soon",
			viaStore: func(sp store.Spawn) any { return sp.LaunchStartedAtMillis }, wantStore: int64(0),
		},
		{
			name: "WithRawLaunchStartedAt/real", state: "pending",
			opts: []SpawnOption{WithRawLaunchStartedAt(1.5)},
			raw:  func(c SpawnColumns) any { return c.LaunchStartedAt }, want: 1.5,
			viaStore: func(sp store.Spawn) any { return sp.LaunchStartedAtMillis }, wantStore: int64(0),
		},
		{
			name: "WithNoLaunchStartedAt", state: "pending",
			opts: []SpawnOption{WithNoLaunchStartedAt()},
			raw:  func(c SpawnColumns) any { return c.LaunchStartedAt }, want: nil,
			viaStore: func(sp store.Spawn) any { return sp.LaunchStartedAtMillis }, wantStore: int64(0),
		},
		{
			name: "WithLifeNumber", state: "waiting",
			opts: []SpawnOption{WithLifeNumber(3)},
			raw:  func(c SpawnColumns) any { return c.LifeNumber }, want: int64(3),
			viaStore: func(sp store.Spawn) any { return sp.LifeNumber }, wantStore: int64(3),
		},
		{
			name: "WithNoPreTrust", state: "waiting",
			opts: []SpawnOption{WithNoPreTrust()},
			raw:  func(c SpawnColumns) any { return c.NoPreTrust }, want: int64(1),
			viaStore: func(sp store.Spawn) any { return sp.NoPreTrust }, wantStore: true,
		},
		{
			name: "WithRawNoPreTrust/unusual", state: "waiting",
			opts: []SpawnOption{WithRawNoPreTrust("sometimes")},
			raw:  func(c SpawnColumns) any { return c.NoPreTrust }, want: "sometimes",
			viaStore: func(sp store.Spawn) any { return sp.NoPreTrust }, wantStore: true,
		},
		{
			name: "WithLaunchIdentity/full", state: "waiting",
			opts: []SpawnOption{WithLaunchIdentity(fullIdentity)},
			raw:  func(c SpawnColumns) any { return identityCols(c) },
			want: []any{"0123456789abcdef", "/tmp/ad-v5-test/sock", int64(4242), int64(1700000000),
				LinuxProcStarttime, "%7", int64(4343), DarwinProcStarttime},
			viaStore: func(sp store.Spawn) any { return sp.Identity }, wantStore: fullIdentity,
		},
		{
			name: "WithLaunchIdentity/empty", state: "waiting",
			opts: []SpawnOption{WithLaunchIdentity(store.LaunchIdentity{})},
			raw:  func(c SpawnColumns) any { return identityCols(c) }, want: []any{nil, nil, nil, nil, nil, nil, nil, nil},
			viaStore: func(sp store.Spawn) any { return sp.Identity }, wantStore: store.LaunchIdentity{},
		},
		{
			name: "WithLaunchIdentity/malformed token", state: "waiting",
			opts: []SpawnOption{WithLaunchIdentity(store.LaunchIdentity{Token: "0123456789ABCDEF", Socket: TestSocket})},
			raw:  func(c SpawnColumns) any { return c.LaunchToken }, want: "0123456789ABCDEF",
			viaStore: func(sp store.Spawn) any { return sp.Identity }, wantStore: store.LaunchIdentity{Socket: TestSocket},
		},
		{
			name: "WithNoLaunchToken", state: "waiting",
			opts: []SpawnOption{WithNoLaunchToken()},
			raw:  func(c SpawnColumns) any { return identityCols(c) }, want: []any{nil, nil, nil, nil, nil, nil, nil, nil},
			viaStore: func(sp store.Spawn) any { return sp.Identity }, wantStore: store.LaunchIdentity{},
		},
		{
			// A live row's default pane is nulled with the token and socket.
			name: "WithNoLaunchToken/live pending row", state: "pending",
			opts: []SpawnOption{WithNoLaunchToken()},
			raw:  func(c SpawnColumns) any { return identityCols(c) }, want: []any{nil, nil, nil, nil, nil, nil, nil, nil},
			viaStore: func(sp store.Spawn) any { return sp.Identity }, wantStore: store.LaunchIdentity{},
		},
		{
			// A later option wins, so a pre-release row nulls every identity column set before it.
			name: "WithNoLaunchToken/after full identity", state: "waiting",
			opts: []SpawnOption{WithLaunchIdentity(fullIdentity), WithNoLaunchToken()},
			raw:  func(c SpawnColumns) any { return identityCols(c) }, want: []any{nil, nil, nil, nil, nil, nil, nil, nil},
			viaStore: func(sp store.Spawn) any { return sp.Identity }, wantStore: store.LaunchIdentity{},
		},
		{
			name: "WithRawLabels", state: "waiting",
			opts: []SpawnOption{WithRawLabels(`{"a": 1,  `)},
			raw:  func(c SpawnColumns) any { return c.Labels }, want: `{"a": 1,  `,
		},
		{
			name: "WithRawClaudeArgs", state: "waiting",
			opts: []SpawnOption{WithRawClaudeArgs(` [ "--x" , `)},
			raw:  func(c SpawnColumns) any { return c.ClaudeArgs }, want: ` [ "--x" , `,
		},
		{
			name: "WithRawExtraEnv/after WithExtraEnv", state: "waiting",
			opts: []SpawnOption{WithExtraEnv(map[string]string{"A": "z"}), WithRawExtraEnv(`{ "A" : "b" }`)},
			raw:  func(c SpawnColumns) any { return c.ExtraEnv }, want: `{ "A" : "b" }`,
			viaStore: func(sp store.Spawn) any { return sp.ExtraEnv }, wantStore: map[string]string{"A": "b"},
		},
		{
			name: "WithStartedAt/time", state: "waiting",
			opts: []SpawnOption{WithStartedAt(offsetTime)},
			raw:  func(c SpawnColumns) any { return c.StartedAt }, want: offsetTimeUTC,
			viaStore: func(sp store.Spawn) any { return sp.Snapshot.StartedAt }, wantStore: offsetTimeUTC,
		},
		{
			name: "WithStartedAt/other layout text", state: "waiting",
			opts: []SpawnOption{WithStartedAt("2020-01-02T03:04:05Z")},
			raw:  func(c SpawnColumns) any { return c.StartedAt }, want: "2020-01-02T03:04:05Z",
			viaStore: func(sp store.Spawn) any { return sp.Snapshot.StartedAt }, wantStore: "2020-01-02T03:04:05Z",
		},
		{
			name: "WithStartedAt/unparseable text", state: "waiting",
			opts: []SpawnOption{WithStartedAt("not a time")},
			raw:  func(c SpawnColumns) any { return c.StartedAt }, want: "not a time",
		},
		{
			name: "WithEndedAt/time", state: "ended",
			opts: []SpawnOption{WithEndedAt(offsetTime)},
			raw:  func(c SpawnColumns) any { return c.EndedAt }, want: offsetTimeUTC,
			viaStore: func(sp store.Spawn) any { return sp.EndedAtText }, wantStore: offsetTimeUTC,
		},
		{
			name: "WithEndedAt/unparseable text", state: "ended",
			opts: []SpawnOption{WithEndedAt("whenever")},
			raw:  func(c SpawnColumns) any { return c.EndedAt }, want: "whenever",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dbPath := filepath.Join(t.TempDir(), "state.db")
			const id = "v5-option-row"
			cols := seedV5Row(t, dbPath, id, tc.state, tc.opts...)
			if got := tc.raw(cols); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("raw column = %#v; want %#v", got, tc.want)
			}
			if tc.viaStore == nil {
				return
			}
			if got := tc.viaStore(getSpawnV5(t, dbPath, id)); !reflect.DeepEqual(got, tc.wantStore) {
				t.Errorf("GetSpawn value = %#v; want %#v", got, tc.wantStore)
			}
		})
	}
}

// TestSeedSpawn_V5Defaults asserts the SR-20.3 defaults SeedSpawn writes for
// every v5 column no option names, per state, on rows sharing one store.
func TestSeedSpawn_V5Defaults(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	oldStart := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)

	// noPane and livePane are tmux_socket, the three server-identity columns and
	// the three pane columns: a terminal row has no pane, a live row the default one.
	noPane := []any{TestSocket, nil, nil, nil, nil, nil, nil}
	livePane := []any{TestSocket, nil, nil, nil, TestPaneID, int64(TestPanePID), nil}

	cases := []struct {
		name        string
		state       string
		startedAt   time.Time // zero: SeedSpawn's own started_at
		launchStart bool      // want launch_started_at == started_at in ms; else NULL
		// identity is the expected tmux_socket plus server and pane columns.
		identity []any
	}{
		{name: "pending", state: "pending", launchStart: true, identity: livePane},
		{name: "pending/old started_at", state: "pending", startedAt: oldStart, launchStart: true, identity: livePane},
		{name: "waiting", state: "waiting", identity: livePane},
		{name: "working", state: "working", identity: livePane},
		{name: "ask_user", state: "ask_user", identity: livePane},
		{name: "check_permission", state: "check_permission", identity: livePane},
		{name: "ended", state: "ended", identity: noPane},
		{name: "missing", state: "missing", identity: noPane},
	}

	tokens := map[string]string{} // token -> case name, for distinctness
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "v5-default-" + tc.name
			var opts []SpawnOption
			if !tc.startedAt.IsZero() {
				opts = append(opts, WithStartedAt(tc.startedAt))
			}
			c := seedV5Row(t, dbPath, id, tc.state, opts...)

			token, _ := c.LaunchToken.(string)
			if !wellFormedToken.MatchString(token) {
				t.Errorf("launch_token = %#v; want 16 lowercase hex", c.LaunchToken)
			}
			if prev, dup := tokens[token]; dup {
				t.Errorf("launch_token %q repeats case %q's", token, prev)
			}
			tokens[token] = tc.name

			got := identityCols(c)[1:]
			if !reflect.DeepEqual(got, tc.identity) {
				t.Errorf("socket/server/pane columns = %#v; want %#v", got, tc.identity)
			}
			if c.NoPreTrust != int64(0) || c.LifeNumber != int64(0) {
				t.Errorf("no_pre_trust, life_number = %#v, %#v; want 0, 0", c.NoPreTrust, c.LifeNumber)
			}

			var wantStart any
			if tc.launchStart {
				started, err := time.Parse(storeTimestampLayout, c.StartedAt.(string))
				if err != nil {
					t.Fatalf("started_at %#v: %v", c.StartedAt, err)
				}
				if !tc.startedAt.IsZero() && !started.Equal(tc.startedAt) {
					t.Fatalf("started_at = %v; want the seeded %v", started, tc.startedAt)
				}
				wantStart = started.UnixMilli()
			}
			if c.LaunchStartedAt != wantStart {
				t.Errorf("launch_started_at = %#v; want %#v", c.LaunchStartedAt, wantStart)
			}

			sp := getSpawnV5(t, dbPath, id)
			wantID := store.LaunchIdentity{Token: token, Socket: TestSocket}
			if pid, live := tc.identity[5].(int64); live {
				wantID.PaneID, wantID.PanePID = tc.identity[4].(string), int(pid)
			}
			if !reflect.DeepEqual(sp.Identity, wantID) {
				t.Errorf("GetSpawn Identity = %#v; want %#v", sp.Identity, wantID)
			}
			if sp.NoPreTrust || sp.LifeNumber != 0 {
				t.Errorf("GetSpawn NoPreTrust, LifeNumber = %v, %d; want false, 0", sp.NoPreTrust, sp.LifeNumber)
			}
			if want, _ := wantStart.(int64); sp.LaunchStartedAtMillis != want {
				t.Errorf("GetSpawn LaunchStartedAtMillis = %d; want %d", sp.LaunchStartedAtMillis, want)
			}
		})
	}
}

// TestSeedRowSession_MatchesSeedSpawnPane seeds rows and their Recorder sessions, then asserts the
// lookup and pane listing on the test socket name each row's pane under a label valid for its id and token.
func TestSeedRowSession_MatchesSeedSpawnPane(t *testing.T) {
	t.Parallel()
	type row struct {
		id, state string
		opts      []SpawnOption
		paneID    string
		panePID   int
	}
	live := func(state string) []row {
		return []row{{id: "v5-match-" + state, state: state, paneID: TestPaneID, panePID: TestPanePID}}
	}
	// A second row on the same socket needs its own pane (SeedRowSession refuses a shared one).
	other := store.LaunchIdentity{Token: "0123456789abcdef", Socket: TestSocket, PaneID: "%7", PanePID: 4343}

	cases := []struct {
		name string
		rows []row
	}{
		{name: "pending", rows: live("pending")},
		{name: "waiting", rows: live("waiting")},
		{name: "working", rows: live("working")},
		{name: "ask_user", rows: live("ask_user")},
		{name: "check_permission", rows: live("check_permission")},
		{name: "two rows on one socket", rows: append(live("waiting"), row{id: "v5-match-other", state: "working",
			opts: []SpawnOption{WithLaunchIdentity(other)}, paneID: other.PaneID, panePID: other.PanePID})},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dbPath := filepath.Join(t.TempDir(), "state.db")
			rec := tmuxfix.NewRecorder()
			tokens := make([]string, len(tc.rows))
			seeded := make([]tmuxfix.SeedSession, len(tc.rows))
			for i, r := range tc.rows {
				tokens[i], _ = seedV5Row(t, dbPath, r.id, r.state, r.opts...).LaunchToken.(string)
				seeded[i] = rec.SeedRowSession(t, dbPath, r.id)
			}

			ans, err := rec.Lookup(TestSocket)
			if err != nil {
				t.Fatalf("Lookup(%q): %v", TestSocket, err)
			}
			panes, err := rec.ListPanes(TestSocket)
			if err != nil {
				t.Fatalf("ListPanes(%q): %v", TestSocket, err)
			}
			if len(ans.Sessions) != len(tc.rows) || len(panes) != len(tc.rows) {
				t.Fatalf("lookup lists %d sessions, listing %d panes; want %d each", len(ans.Sessions), len(panes), len(tc.rows))
			}

			for i, r := range tc.rows {
				sid := seeded[i].ID
				wantLabel := tmux.Label{Kind: tmux.LabelValid, Token: tokens[i], InstanceID: r.id}
				var found bool
				for _, s := range ans.Sessions {
					if s.ID == sid {
						found = true
						if s.Label != wantLabel {
							t.Errorf("row %s: session %s label = %+v; want %+v", r.id, sid, s.Label, wantLabel)
						}
					}
				}
				if !found {
					t.Errorf("row %s: lookup has no session %s", r.id, sid)
				}
				var got []tmux.Pane
				for _, p := range panes {
					if p.SessionID == sid {
						got = append(got, p)
					}
				}
				if len(got) != 1 || got[0].ID != r.paneID || got[0].PID != r.panePID {
					t.Errorf("row %s: session %s panes = %+v; want one pane %s pid %d", r.id, sid, got, r.paneID, r.panePID)
				}
			}
		})
	}
}

// TestReadSessionHistoryAllLives_HookRotations archives sessions through the
// hook path and reads them back newest-first at life 0, scoped to one id.
func TestReadSessionHistoryAllLives_HookRotations(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	const id, other = "hist-row", "hist-other"
	for inst, sess := range map[string]string{id: "s1", other: "o1"} {
		if _, err := SeedSpawn(dbPath, inst, "waiting", "/tmp", "off", sess, true); err != nil {
			t.Fatalf("SeedSpawn(%q): %v", inst, err)
		}
	}

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close() //nolint:errcheck
	rotate := func(inst, sess, jsonl string) {
		t.Helper()
		if err := s.RecordSessionStartIdentity(inst, sess, jsonl, jsonl != "", 0, ""); err != nil {
			t.Fatalf("RecordSessionStartIdentity(%q, %q): %v", inst, sess, err)
		}
	}
	rotate(id, "s2", "/tmp/hist/s2.jsonl") // archives s1, which never had a transcript path
	rotate(other, "o2", "")                // archives o1 under the other id
	rotate(id, "s3", "")                   // archives s2 with its path

	got, err := ReadSessionHistoryAllLives(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSessionHistoryAllLives: %v", err)
	}
	want := []struct {
		session string
		path    any // nil = NULL
	}{{"s2", "/tmp/hist/s2.jsonl"}, {"s1", nil}}
	if len(got) != len(want) {
		t.Fatalf("got %d entries %+v; want %d (s2, s1)", len(got), got, len(want))
	}
	for i, w := range want {
		e := got[i]
		var path any
		if e.JSONLPath.Valid {
			path = e.JSONLPath.String
		}
		if e.ClaudeSessionID != w.session || path != w.path || e.LifeNumber != 0 {
			t.Errorf("entry %d = {%q, %#v, life %d}; want {%q, %#v, life 0}",
				i, e.ClaudeSessionID, path, e.LifeNumber, w.session, w.path)
		}
		if _, err := time.Parse(storeTimestampLayout, e.RecordedAt); err != nil {
			t.Errorf("entry %d recorded_at %q: %v", i, e.RecordedAt, err)
		}
	}

	none, err := ReadSessionHistoryAllLives(dbPath, "no-such-id")
	if err != nil || none == nil || len(none) != 0 {
		t.Errorf("unknown id = %#v, %v; want empty non-nil slice, nil", none, err)
	}
}
