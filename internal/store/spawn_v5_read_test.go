package store_test

// Schema-v5 read tests (SR-5.3, SR-5.5): the v5 Spawn fields, LaunchIdentity,
// the raw ended_at text and the row snapshot, over GetSpawn and ListSpawns.
// Rows are seeded through apitest (SR-20.2), hence the external test package;
// the package's sandbox-guarded TestMain in store_test.go covers this file.

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// goodToken is a well-formed launch token: 16 lowercase hex characters.
const goodToken = "0123456789abcdef"

// v5Store is a temp store opened for reads, with its path for apitest seeding.
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

// spawnRead is one store read that returns a Spawn for an id.
type spawnRead struct {
	name string
	read func(*store.Store, string) (store.Spawn, error)
}

// spawnReads are the reads that select the v5 columns.
var spawnReads = []spawnRead{
	{"GetSpawn", (*store.Store).GetSpawn},
	{"ListSpawns", listOne},
}

// listOne returns id's row from an unfiltered ListSpawns.
func listOne(s *store.Store, id string) (store.Spawn, error) {
	rows, err := s.ListSpawns(store.ListFilters{})
	if err != nil {
		return store.Spawn{}, err
	}
	for _, sp := range rows {
		if sp.ClaudeInstanceID == id {
			return sp, nil
		}
	}
	return store.Spawn{}, errors.New("row not listed: " + id)
}

// mustRead reads id through r, failing the test on any error.
func mustRead(t *testing.T, f *v5Store, r spawnRead, id string) store.Spawn {
	t.Helper()
	sp, err := r.read(f.s, id)
	if err != nil {
		t.Fatalf("%s(%s) failed: %v; a v5 read must not fail", r.name, id, err)
	}
	return sp
}

// withToken sets the launch token (and the test socket) as stored.
func withToken(token string) apitest.SpawnOption {
	return apitest.WithLaunchIdentity(store.LaunchIdentity{Token: token, Socket: apitest.TestSocket})
}

// TestV5NarrowReadsNeverFail checks the SR-5.5 meaning of launch_started_at,
// launch_token and no_pre_trust for every stored value, with no read error.
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
		{"token well formed", withToken(goodToken), 0, goodToken, false},
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
		for _, r := range spawnReads {
			t.Run(tc.name+"/"+r.name, func(t *testing.T) {
				sp := mustRead(t, f, r, id)
				if sp.LaunchStartedAtMillis != tc.wantStart {
					t.Errorf("LaunchStartedAtMillis = %d; want %d", sp.LaunchStartedAtMillis, tc.wantStart)
				}
				if sp.Identity.Token != tc.wantToken {
					t.Errorf("Identity.Token = %q; want %q", sp.Identity.Token, tc.wantToken)
				}
				if sp.NoPreTrust != tc.wantNoPreTrust {
					t.Errorf("NoPreTrust = %v; want %v", sp.NoPreTrust, tc.wantNoPreTrust)
				}
			})
		}
	}
}

// TestV5ReadParity checks GetSpawn and ListSpawns return identical rows,
// v5 fields, identity, ended_at text and snapshot included.
func TestV5ReadParity(t *testing.T) {
	f := newV5Store(t)
	ids := []string{
		f.seed(store.StatePending, ""),
		f.seed(store.StateWaiting, "sess-parity", apitest.WithPID(4242),
			apitest.WithProcStarttime(apitest.LinuxProcStarttime), apitest.WithLifeNumber(2),
			apitest.WithNoPreTrust(), apitest.WithLaunchIdentity(fullIdentity())),
		f.seed(store.StateEnded, "sess-ended", apitest.WithEndedAt("2026-03-05T06:07:08.5Z"),
			apitest.WithStartedAt("2026-03-04T05:06:07.250+02:00")),
		f.seed(store.StateWorking, "", withToken("NOT-A-TOKEN"),
			apitest.WithRawLaunchStartedAt("soon"), apitest.WithRawNoPreTrust("x")),
		f.seed(store.StateMissing, "", apitest.WithNoLaunchToken()),
	}
	listed, err := f.s.ListSpawns(store.ListFilters{})
	if err != nil {
		t.Fatalf("ListSpawns: %v", err)
	}
	if len(listed) != len(ids) {
		t.Fatalf("ListSpawns returned %d rows; want %d", len(listed), len(ids))
	}
	for _, fromList := range listed {
		fromGet, err := f.s.GetSpawn(fromList.ClaudeInstanceID)
		if err != nil {
			t.Fatalf("GetSpawn(%s): %v", fromList.ClaudeInstanceID, err)
		}
		if !reflect.DeepEqual(fromGet, fromList) {
			t.Errorf("row %s (%s): GetSpawn and ListSpawns differ\nget:  %+v\nlist: %+v",
				fromGet.ClaudeInstanceID, fromGet.State, fromGet, fromList)
		}
	}
}

// fullIdentity is a launch identity with every field set.
func fullIdentity() store.LaunchIdentity {
	return store.LaunchIdentity{
		Token:           goodToken,
		Socket:          "/tmp/ad-v5-read-test/sock",
		ServerPID:       111,
		ServerStart:     1767225600,
		ServerStarttime: apitest.LinuxProcStarttime,
		PaneID:          "%7",
		PanePID:         222,
		PaneStarttime:   apitest.DarwinProcStarttime,
	}
}

// TestV5LaunchIdentityReadsBack checks the eight identity columns read back
// field for field, a pre-release row reads as no identity, and a NULL token
// reads as absent.
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
		{"no identity at all", apitest.WithLaunchIdentity(store.LaunchIdentity{}), store.LaunchIdentity{}},
	}
	f := newV5Store(t)
	for _, tc := range cases {
		id := f.seed(store.StateWaiting, "", tc.opt)
		for _, r := range spawnReads {
			t.Run(tc.name+"/"+r.name, func(t *testing.T) {
				if got := mustRead(t, f, r, id).Identity; got != tc.want {
					t.Errorf("Identity = %+v; want %+v", got, tc.want)
				}
			})
		}
	}
}

// TestV5EndedAtTextIsStoredText checks EndedAtText is ended_at byte for byte
// on a finished row and empty on a live one.
func TestV5EndedAtTextIsStoredText(t *testing.T) {
	cases := []struct {
		name    string
		state   string
		opts    []apitest.SpawnOption
		want    string
		fromRaw bool // want is the raw column text the store wrote
	}{
		{"finished, non-default layout", store.StateEnded,
			[]apitest.SpawnOption{apitest.WithEndedAt("2026-03-05T06:07:08.5Z")}, "2026-03-05T06:07:08.5Z", false},
		{"finished, store layout", store.StateEnded,
			[]apitest.SpawnOption{apitest.WithEndedAt("2026-03-05 06:07:08")}, "2026-03-05 06:07:08", false},
		{"finished, written by the store", store.StateEnded, nil, "", true},
		{"live row", store.StateWaiting, nil, "", false},
	}
	f := newV5Store(t)
	for _, tc := range cases {
		id := f.seed(tc.state, "", tc.opts...)
		want := tc.want
		if tc.fromRaw {
			raw, ok := f.rawColumns(id).EndedAt.(string)
			if !ok || raw == "" {
				t.Fatalf("%s: raw ended_at = %#v; want stored text", tc.name, f.rawColumns(id).EndedAt)
			}
			want = raw
		}
		for _, r := range spawnReads {
			t.Run(tc.name+"/"+r.name, func(t *testing.T) {
				sp := mustRead(t, f, r, id)
				if sp.EndedAtText != want {
					t.Errorf("EndedAtText = %q; want %q", sp.EndedAtText, want)
				}
				if (sp.EndedAt != nil) != (want != "") {
					t.Errorf("EndedAt = %v; want set only when ended_at is stored", sp.EndedAt)
				}
			})
		}
	}
}

// snapStartedAt is a started_at whose store-layout re-format would differ.
const snapStartedAt = "2026-03-04T05:06:07.250+02:00"

// snapBase seeds the six snapshot columns with fixed values.
func snapBase() []apitest.SpawnOption {
	return []apitest.SpawnOption{
		apitest.WithStartedAt(snapStartedAt),
		apitest.WithPID(4242),
		apitest.WithProcStarttime(apitest.LinuxProcStarttime),
		apitest.WithTmuxSessionName("snap-ts"),
	}
}

// TestV5RowSnapshotHoldsStoredText checks the snapshot holds the six columns
// as stored, started_at never parsed and re-formatted.
func TestV5RowSnapshotHoldsStoredText(t *testing.T) {
	f := newV5Store(t)
	id := f.seed(store.StateWaiting, "sess-a", snapBase()...)
	rawVersion, ok := f.rawColumns(id).RowVersion.(int64)
	if !ok {
		t.Fatalf("raw row_version = %#v; want an integer", f.rawColumns(id).RowVersion)
	}
	want := store.RowSnapshot{
		RowVersion:      rawVersion,
		StartedAt:       snapStartedAt,
		ClaudeSessionID: "sess-a",
		PID:             4242,
		ProcStarttime:   apitest.LinuxProcStarttime,
		TmuxSessionName: "snap-ts",
	}
	for _, r := range spawnReads {
		t.Run(r.name, func(t *testing.T) {
			sp := mustRead(t, f, r, id)
			if sp.Snapshot != want {
				t.Errorf("Snapshot = %+v; want %+v", sp.Snapshot, want)
			}
			if reformatted := sp.StartedAt.UTC().Format("2006-01-02 15:04:05"); sp.Snapshot.StartedAt == reformatted {
				t.Errorf("Snapshot.StartedAt = %q is the re-formatted time; want the stored text", reformatted)
			}
			if sp.Snapshot.RowVersion != sp.RowVersion {
				t.Errorf("Snapshot.RowVersion = %d; RowVersion = %d", sp.Snapshot.RowVersion, sp.RowVersion)
			}
		})
	}
}

// TestV5RowSnapshotComparison checks two reads of an unchanged row compare
// equal, any one of the six fields differing compares unequal, and
// launch_started_at, life_number and the launch columns are not part of it.
func TestV5RowSnapshotComparison(t *testing.T) {
	f := newV5Store(t)
	baseID := f.seed(store.StateWaiting, "sess-a", snapBase()...)
	base := mustRead(t, f, spawnReads[0], baseID).Snapshot

	t.Run("two reads of an unchanged row", func(t *testing.T) {
		for _, r := range spawnReads {
			if again := mustRead(t, f, r, baseID).Snapshot; again != base {
				t.Errorf("%s snapshot = %+v; want %+v", r.name, again, base)
			}
		}
	})
	t.Run("row_version differs", func(t *testing.T) {
		// Compares against a copy with only RowVersion bumped, independent of any store write.
		changed := base
		changed.RowVersion++
		if changed == base {
			t.Errorf("snapshots differing in RowVersion compare equal")
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
		{"launch_started_at differs", "sess-a", apitest.WithLaunchStartedAt(1767225600123), true},
		{"life_number differs", "sess-a", apitest.WithLifeNumber(4), true},
		{"launch identity differs", "sess-a", apitest.WithLaunchIdentity(fullIdentity()), true},
		{"no_pre_trust differs", "sess-a", apitest.WithNoPreTrust(), true},
	}
	for _, tc := range cases {
		opts := snapBase()
		if tc.opt != nil {
			opts = append(opts, tc.opt)
		}
		id := f.seed(store.StateWaiting, tc.sessionID, opts...)
		for _, r := range spawnReads {
			t.Run(tc.name+"/"+r.name, func(t *testing.T) {
				got := mustRead(t, f, r, id).Snapshot
				if (got == base) != tc.wantEqual {
					t.Errorf("snapshot %+v == base %+v is %v; want %v", got, base, got == base, tc.wantEqual)
				}
			})
		}
	}
}

// preV5Fields keeps the fields a verb reads today, dropping the v5 fields and
// the per-row values two seeds cannot share (id, last_seen_at).
func preV5Fields(sp store.Spawn) store.Spawn {
	sp.ClaudeInstanceID = ""
	sp.LastSeenAt = time.Time{}
	sp.RowVersion, sp.LaunchStartedAtMillis, sp.LifeNumber = 0, 0, 0
	sp.NoPreTrust = false
	sp.EndedAtText = ""
	sp.Snapshot = store.RowSnapshot{}
	sp.Identity = store.LaunchIdentity{}
	return sp
}

// TestV5OptionsLeaveVerbFieldsUnchanged checks a row seeded with v5 values,
// well formed or unusual, decodes the pre-v5 fields exactly as without them.
func TestV5OptionsLeaveVerbFieldsUnchanged(t *testing.T) {
	variants := map[string][]apitest.SpawnOption{
		"well-formed v5 values": {apitest.WithLaunchStartedAt(1767225600123), apitest.WithLifeNumber(3),
			apitest.WithNoPreTrust(), apitest.WithLaunchIdentity(fullIdentity())},
		"unusual v5 values": {apitest.WithRawLaunchStartedAt("soon"), apitest.WithRawNoPreTrust("x"),
			withToken("NOT-A-TOKEN"), apitest.WithLifeNumber(9)},
	}
	f := newV5Store(t)
	for _, state := range []string{store.StatePending, store.StateWaiting, store.StateEnded} {
		for variant, v5opts := range variants {
			common := []apitest.SpawnOption{
				apitest.WithStartedAt("2026-03-04 05:06:07"),
				apitest.WithTmuxSessionName("verb-ts"),
			}
			if state == store.StateEnded {
				common = append(common, apitest.WithEndedAt("2026-03-05 06:07:08"))
			}
			plainID := f.seed(state, "sess-v", common...)
			v5ID := f.seed(state, "sess-v", append(append([]apitest.SpawnOption{}, common...), v5opts...)...)
			for _, r := range spawnReads {
				t.Run(fmt.Sprintf("%s/%s/%s", state, variant, r.name), func(t *testing.T) {
					plain := preV5Fields(mustRead(t, f, r, plainID))
					withV5 := preV5Fields(mustRead(t, f, r, v5ID))
					if !reflect.DeepEqual(plain, withV5) {
						t.Errorf("pre-v5 fields differ\nplain: %+v\nv5:    %+v", plain, withV5)
					}
				})
			}
		}
	}
}
