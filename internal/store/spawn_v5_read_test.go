package store_test

// Schema-v5 reads (SR-5.3, SR-5.5): the v5 Spawn fields, LaunchIdentity, the
// raw ended_at text and the row snapshot, over GetSpawn and ListSpawns. Rows
// are seeded through apitest (SR-20.2), hence the external test package; the
// package's sandbox-guarded TestMain in store_test.go covers it. The v5Store
// fixture here is shared by every external test file.

import (
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// goodToken is a well-formed launch token: 16 lowercase hex characters.
const goodToken = "0123456789abcdef"

// v5Store is a temp store opened for the test, with its path for apitest seeding.
type v5Store struct {
	t    *testing.T
	path string
	s    *store.Store
}

// newV5Store creates a fresh store in a temp dir and opens it for the test.
func newV5Store(t *testing.T) *v5Store {
	t.Helper()
	path, err := apitest.InitStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return &v5Store{t: t, path: path, s: s}
}

// seed seeds one row through apitest.SeedSpawn and returns its id.
func (f *v5Store) seed(state, sessionID string, opts ...apitest.SpawnOption) string {
	f.t.Helper()
	id, err := apitest.SeedSpawn(f.path, "", state, "/tmp", "", sessionID, false, opts...)
	if err != nil {
		f.t.Fatalf("SeedSpawn(%s): %v", state, err)
	}
	return id
}

// rawColumns reads id's row straight from the table, undecoded.
func (f *v5Store) rawColumns(id string) apitest.SpawnColumns {
	f.t.Helper()
	c, err := apitest.ReadSpawnColumns(f.path, id)
	if err != nil {
		f.t.Fatalf("ReadSpawnColumns: %v", err)
	}
	return c
}

// spawnReads are the two reads that return a Spawn: GetSpawn and the row's
// entry in an unfiltered ListSpawns.
var spawnReads = []struct {
	name string
	read func(*store.Store, string) (store.Spawn, error)
}{
	{"GetSpawn", (*store.Store).GetSpawn},
	{"ListSpawns", func(s *store.Store, id string) (store.Spawn, error) {
		rows, err := s.ListSpawns(store.ListFilters{})
		for _, sp := range rows {
			if sp.ClaudeInstanceID == id {
				return sp, err
			}
		}
		return store.Spawn{}, errors.Join(err, errors.New("row not listed: "+id))
	}},
}

// forEachRead runs check on id's row from each of spawnReads, as subtests.
func forEachRead(t *testing.T, f *v5Store, name, id string, check func(t *testing.T, sp store.Spawn)) {
	t.Helper()
	for _, r := range spawnReads {
		t.Run(name+"/"+r.name, func(t *testing.T) {
			sp, err := r.read(f.s, id)
			if err != nil {
				t.Fatalf("%s(%s): %v; a v5 read must not fail", r.name, id, err)
			}
			check(t, sp)
		})
	}
}

// withToken sets the launch token (and the test socket) as stored.
func withToken(token string) apitest.SpawnOption {
	return apitest.WithLaunchIdentity(store.LaunchIdentity{Token: token, Socket: apitest.TestSocket})
}

// fullIdentity is a launch identity with every field set.
func fullIdentity() store.LaunchIdentity {
	return store.LaunchIdentity{Token: goodToken, Socket: "/tmp/ad-v5-read-test/sock", ServerPID: 111,
		ServerStart: 1767225600, ServerStarttime: apitest.LinuxProcStarttime,
		PaneID: "%7", PanePID: 222, PaneStarttime: apitest.DarwinProcStarttime}
}

// The SR-5.5 range of a launch start in Unix ms: a time outside the years 0
// to 9999 reads as absent.
const (
	firstInRangeLaunchMillis int64 = -62167219200000 // 0000-01-01T00:00:00.000Z
	lastInRangeLaunchMillis  int64 = 253402300799999 // 9999-12-31T23:59:59.999Z
)

// TestV5NarrowReadsNeverFail checks the SR-5.5 meaning of every stored
// launch_started_at, launch_token and no_pre_trust, with no read error.
func TestV5NarrowReadsNeverFail(t *testing.T) {
	cases := []struct {
		name           string
		opt            apitest.SpawnOption
		wantStart      int64
		wantToken      string
		wantNoPreTrust bool
	}{
		{"launch start integer", apitest.WithLaunchStartedAt(1767225600123), 1767225600123, goodToken, false},
		{"launch start text", apitest.WithRawLaunchStartedAt("yesterday"), 0, goodToken, false},
		{"launch start real", apitest.WithRawLaunchStartedAt(1767225600123.5), 0, goodToken, false},
		{"launch start blob", apitest.WithRawLaunchStartedAt([]byte("1767225600123")), 0, goodToken, false},
		{"launch start NULL", apitest.WithNoLaunchStartedAt(), 0, goodToken, false},
		{"launch start first ms of year 0", apitest.WithLaunchStartedAt(firstInRangeLaunchMillis), firstInRangeLaunchMillis, goodToken, false},
		{"launch start last ms of year 9999", apitest.WithLaunchStartedAt(lastInRangeLaunchMillis), lastInRangeLaunchMillis, goodToken, false},
		{"launch start one ms before year 0", apitest.WithLaunchStartedAt(firstInRangeLaunchMillis - 1), 0, goodToken, false},
		{"launch start first ms of year 10000", apitest.WithLaunchStartedAt(lastInRangeLaunchMillis + 1), 0, goodToken, false},
		{"launch start int64 min", apitest.WithLaunchStartedAt(math.MinInt64), 0, goodToken, false},
		{"launch start int64 max", apitest.WithLaunchStartedAt(math.MaxInt64), 0, goodToken, false},
		{"token too short", withToken(goodToken[:15]), 0, "", false},
		{"token too long", withToken(goodToken + "0"), 0, "", false},
		{"token upper case", withToken("0123456789ABCDEF"), 0, "", false},
		{"token non-hex", withToken("0123456789abcdeg"), 0, "", false},
		{"token NULL", apitest.WithNoLaunchToken(), 0, "", false},
		{"no_pre_trust 0", apitest.WithRawNoPreTrust(int64(0)), 0, goodToken, false},
		{"no_pre_trust 1", apitest.WithNoPreTrust(), 0, goodToken, true},
		{"no_pre_trust 7", apitest.WithRawNoPreTrust(int64(7)), 0, goodToken, true},
		{"no_pre_trust -1", apitest.WithRawNoPreTrust(int64(-1)), 0, goodToken, true},
		{"no_pre_trust text", apitest.WithRawNoPreTrust("weird"), 0, goodToken, true},
		{"no_pre_trust real", apitest.WithRawNoPreTrust(0.5), 0, goodToken, true},
	}
	f := newV5Store(t)
	for _, tc := range cases {
		// A waiting row has no default launch start; the case's option wins over withToken.
		id := f.seed(store.StateWaiting, "", withToken(goodToken), tc.opt)
		forEachRead(t, f, tc.name, id, func(t *testing.T, sp store.Spawn) {
			if sp.LaunchStartedAtMillis != tc.wantStart || sp.Identity.Token != tc.wantToken || sp.NoPreTrust != tc.wantNoPreTrust {
				t.Errorf("launch start, token, no_pre_trust = %d, %q, %v; want %d, %q, %v",
					sp.LaunchStartedAtMillis, sp.Identity.Token, sp.NoPreTrust, tc.wantStart, tc.wantToken, tc.wantNoPreTrust)
			}
		})
	}
}

// TestV5ReadParity checks GetSpawn and ListSpawns return identical rows, v5
// fields, identity, ended_at text and snapshot included, unusual values too.
func TestV5ReadParity(t *testing.T) {
	f := newV5Store(t)
	ids := []string{
		f.seed(store.StatePending, ""),
		f.seed(store.StateWaiting, "sess-parity", apitest.WithPID(4242), apitest.WithProcStarttime(apitest.LinuxProcStarttime),
			apitest.WithLifeNumber(2), apitest.WithNoPreTrust(), apitest.WithLaunchIdentity(fullIdentity())),
		f.seed(store.StateEnded, "sess-ended", apitest.WithEndedAt("2026-03-05T06:07:08.5Z"),
			apitest.WithStartedAt("2026-03-04T05:06:07.250+02:00")),
		f.seed(store.StateWorking, "", withToken("NOT-A-TOKEN"), apitest.WithRawLaunchStartedAt("soon"), apitest.WithRawNoPreTrust("x")),
		f.seed(store.StateMissing, "", apitest.WithNoLaunchToken()),
	}
	listed, err := f.s.ListSpawns(store.ListFilters{})
	if err != nil || len(listed) != len(ids) {
		t.Fatalf("ListSpawns = %d rows, %v; want %d", len(listed), err, len(ids))
	}
	for _, fromList := range listed {
		if fromGet, err := f.s.GetSpawn(fromList.ClaudeInstanceID); err != nil || !reflect.DeepEqual(fromGet, fromList) {
			t.Errorf("row %s: GetSpawn (%v) and ListSpawns differ\nget:  %+v\nlist: %+v", fromList.ClaudeInstanceID, err, fromGet, fromList)
		}
	}
}

// TestV5LaunchIdentityReadsBack checks the eight identity columns read back
// field for field, and a row with no token reads as no identity.
func TestV5LaunchIdentityReadsBack(t *testing.T) {
	cases := []struct {
		name string
		opt  apitest.SpawnOption
		want store.LaunchIdentity
	}{
		{"every field set", apitest.WithLaunchIdentity(fullIdentity()), fullIdentity()},
		{"pre-release row", apitest.WithNoLaunchToken(), store.LaunchIdentity{}},
		{"no token, test socket", apitest.WithLaunchIdentity(store.LaunchIdentity{Socket: apitest.TestSocket}),
			store.LaunchIdentity{Socket: apitest.TestSocket}},
	}
	f := newV5Store(t)
	for _, tc := range cases {
		forEachRead(t, f, tc.name, f.seed(store.StateWaiting, "", tc.opt), func(t *testing.T, sp store.Spawn) {
			if sp.Identity != tc.want {
				t.Errorf("Identity = %+v; want %+v", sp.Identity, tc.want)
			}
		})
	}
}

// TestV5EndedAtTextIsStoredText checks EndedAtText is ended_at byte for byte
// on a finished row (EndedAt set) and empty on a live one.
func TestV5EndedAtTextIsStoredText(t *testing.T) {
	f := newV5Store(t)
	stored := f.seed(store.StateEnded, "")
	written, _ := f.rawColumns(stored).EndedAt.(string)
	cases := []struct{ name, id, want string }{
		{"non-default layout", f.seed(store.StateEnded, "", apitest.WithEndedAt("2026-03-05T06:07:08.5Z")), "2026-03-05T06:07:08.5Z"},
		{"store layout", f.seed(store.StateEnded, "", apitest.WithEndedAt("2026-03-05 06:07:08")), "2026-03-05 06:07:08"},
		{"written by the store", stored, written},
		{"live row", f.seed(store.StateWaiting, ""), ""},
	}
	if written == "" {
		t.Fatalf("raw ended_at of a finished row = %#v; want stored text", f.rawColumns(stored).EndedAt)
	}
	for _, tc := range cases {
		forEachRead(t, f, tc.name, tc.id, func(t *testing.T, sp store.Spawn) {
			if sp.EndedAtText != tc.want || (sp.EndedAt != nil) != (tc.want != "") {
				t.Errorf("EndedAtText, EndedAt = %q, %v; want %q, set only when stored", sp.EndedAtText, sp.EndedAt, tc.want)
			}
		})
	}
}

// snapStartedAt is a started_at whose store-layout re-format would differ.
const snapStartedAt = "2026-03-04T05:06:07.250+02:00"

// TestV5RowSnapshot checks the snapshot holds its six columns as stored
// (started_at never re-formatted) and compares unequal exactly when one of
// them differs, never for the launch start, life or launch identity.
func TestV5RowSnapshot(t *testing.T) {
	f := newV5Store(t)
	base := []apitest.SpawnOption{apitest.WithStartedAt(snapStartedAt), apitest.WithPID(4242),
		apitest.WithProcStarttime(apitest.LinuxProcStarttime), apitest.WithTmuxSessionName("snap-ts")}
	baseID := f.seed(store.StateWaiting, "sess-a", base...)
	want := store.RowSnapshot{RowVersion: f.rawColumns(baseID).RowVersion.(int64), StartedAt: snapStartedAt,
		ClaudeSessionID: "sess-a", PID: 4242, ProcStarttime: apitest.LinuxProcStarttime, TmuxSessionName: "snap-ts"}
	forEachRead(t, f, "stored values", baseID, func(t *testing.T, sp store.Spawn) {
		if sp.Snapshot != want || sp.Snapshot.RowVersion != sp.RowVersion {
			t.Errorf("Snapshot = %+v (RowVersion %d); want %+v", sp.Snapshot, sp.RowVersion, want)
		}
	})

	cases := []struct {
		name      string
		sessionID string
		opt       apitest.SpawnOption
		wantEqual bool
	}{
		{"identical column values", "sess-a", nil, true},
		{"started_at differs", "sess-a", apitest.WithStartedAt("2026-03-04T05:06:07.251+02:00"), false},
		{"claude_session_id differs", "sess-b", nil, false},
		{"pid differs", "sess-a", apitest.WithPID(4243), false},
		{"proc_starttime differs", "sess-a", apitest.WithProcStarttime(apitest.DarwinProcStarttime), false},
		{"tmux_session_name differs", "sess-a", apitest.WithTmuxSessionName("snap-ts-2"), false},
		{"row_version differs", "sess-a", apitest.WithRowVersion(want.RowVersion + 1), false},
		{"launch_started_at differs", "sess-a", apitest.WithLaunchStartedAt(1767225600123), true},
		{"life_number differs", "sess-a", apitest.WithLifeNumber(4), true},
		{"launch identity differs", "sess-a", apitest.WithLaunchIdentity(fullIdentity()), true},
		{"no_pre_trust differs", "sess-a", apitest.WithNoPreTrust(), true},
	}
	for _, tc := range cases {
		opts := append([]apitest.SpawnOption{}, base...)
		if tc.opt != nil {
			opts = append(opts, tc.opt)
		}
		forEachRead(t, f, tc.name, f.seed(store.StateWaiting, tc.sessionID, opts...), func(t *testing.T, sp store.Spawn) {
			if (sp.Snapshot == want) != tc.wantEqual {
				t.Errorf("snapshot %+v == %+v is %v; want %v", sp.Snapshot, want, sp.Snapshot == want, tc.wantEqual)
			}
		})
	}
}
