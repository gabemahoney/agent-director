package store_test

// The restore after a failed reuse (SR-10.4, SR-5.3, SR-5.5, SR-5.6, SR-5.8):
// an applied restore writes the pre-reuse row back byte for byte, and a
// failed one leaves the reset row. Each case seeds through seedReuseRow and
// drives the real read and reset first. The version-guard refusals are
// row_version_reuse_test.go's.

import (
	"database/sql"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rrFailedAt is the failure time the restore is given, unlike every seeded time.
var rrFailedAt = time.Date(2030, 1, 2, 3, 9, 7, 0, time.UTC)

// rrReset is a row reuse read and reset.
type rrReset struct {
	id       string
	prior    store.RawLife          // the raw life ReadForReuse returned
	before   apitest.SpawnColumns   // raw columns before the reset
	reset    apitest.SpawnColumns   // raw columns right after the reset
	history  []apitest.HistoryEntry // every-life history right after the reset
	version  int64                  // the version the reset returned
	archived string                 // the session id the reset archived
}

// rrDoReset reads id for reuse and resets it with fresh; it must apply.
func rrDoReset(t *testing.T, f *v5Store, id string, fresh store.Spawn) rrReset {
	t.Helper()
	before, row := f.rawColumns(id), f.readForReuse(t, id)
	res, archived, v, err := f.s.ResetForReuse(id, row.Snapshot, fresh)
	if err != nil || res != store.CondApplied {
		t.Fatalf("ResetForReuse = %v, %q, %d, %v; want CondApplied", res, archived, v, err)
	}
	return rrReset{id: id, prior: row.Life, before: before, reset: f.rawColumns(id),
		history: f.historyAllLives(id), version: v, archived: archived}
}

// rrRestore restores r at version with the test's failure time.
func (f *v5Store) rrRestore(r rrReset, version int64) (store.CondResult, error) {
	return f.s.RestoreAfterFailedReuse(r.id, version, r.prior, rrFailedAt)
}

// rrNoPreTrustClass reads typeof(no_pre_trust), which tells a zero-length
// blob from NULL where the raw column reads cannot.
func rrNoPreTrustClass(t *testing.T, f *v5Store, id string) string {
	t.Helper()
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatalf("raw sql.Open(%q): %v", f.path, err)
	}
	defer func() { _ = db.Close() }()
	var class string
	if err := db.QueryRow(`SELECT typeof(no_pre_trust) FROM spawns WHERE claude_instance_id = ?`, id).Scan(&class); err != nil {
		t.Fatalf("typeof(no_pre_trust) for %q: %v", id, err)
	}
	return class
}

// TestReuseRestoreApplied checks an applied restore writes the pre-reuse row
// back byte for byte, malformed values and their storage class included, with
// no launch start, the reset version + 1, the failure time for a NULL ended_at
// and a NULL parent once the pre-reuse parent is deleted; the reset's archive
// and request deletion stay.
func TestReuseRestoreApplied(t *testing.T) {
	badToken := reuseIdentity()
	badToken.Token = "NOT-a-token!"
	cases := []struct {
		name            string
		spec            reuseSpec
		freshNoPreTrust bool // the reset's pre-trust choice, the opposite of the seed's
		deleteParent    bool
		seedClass       string // typeof(no_pre_trust) the seed must store; "" = unchecked
	}{
		{name: "ended, text a re-format would change"},
		{name: "missing, NULL ended_at gets the failure time", spec: reuseSpec{state: store.StateMissing,
			opts: []apitest.SpawnOption{apitest.WithNoEndedAt()}}},
		{name: "malformed labels, args, env, token, text no_pre_trust and timestamps", spec: reuseSpec{opts: []apitest.SpawnOption{
			apitest.WithRawLabels(`{bad`), apitest.WithRawClaudeArgs(`["unterminated`), apitest.WithRawExtraEnv(`not json`),
			apitest.WithLaunchIdentity(badToken), apitest.WithRawNoPreTrust("yes"), apitest.WithEndedAt("not a time"),
			apitest.WithStartedAt("not a start")}}},
		// The driver reads a zero-length blob as nil, like NULL; only typeof() tells them apart.
		{name: "zero-length blob no_pre_trust", spec: reuseSpec{opts: []apitest.SpawnOption{apitest.WithRawNoPreTrust([]byte{})}},
			seedClass: "blob"},
		{name: "no token, socket or identity", spec: reuseSpec{opts: []apitest.SpawnOption{apitest.WithNoLaunchToken()}}},
		{name: "pre-trust allowed, reset with the opt-out", spec: reuseSpec{preTrust: true}, freshNoPreTrust: true},
		{name: "pre-reuse parent deleted", deleteParent: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			seeded := seedReuseRow(t, f, tc.spec)
			id := seeded.id
			seededClass := rrNoPreTrustClass(t, f, id)
			if tc.seedClass != "" && seededClass != tc.seedClass {
				t.Fatalf("seeded typeof(no_pre_trust) = %q; want %q", seededClass, tc.seedClass)
			}
			r := rrDoReset(t, f, id, reuseFresh(f.seed(store.StateWaiting, ""), tc.freshNoPreTrust))
			for _, col := range []string{"State", "LifeNumber", "NoPreTrust", "ClaudeSessionID", "LaunchToken", "StartedAt", "CWD", "Labels", "ParentID"} {
				b, a := reflect.ValueOf(r.before).FieldByName(col).Interface(), reflect.ValueOf(r.reset).FieldByName(col).Interface()
				if reflect.DeepEqual(a, b) {
					t.Fatalf("the reset left %s = %s; the restore check would be vacuous", col, rsShow(b))
				}
			}
			want := r.before
			want.LaunchStartedAt, want.RowVersion = nil, r.version+1
			if want.EndedAt == nil {
				want.EndedAt = rrFailedAt.Format("2006-01-02 15:04:05")
			}
			if tc.deleteParent {
				if err := f.s.DeleteSpawn(seeded.parent); err != nil {
					t.Fatalf("DeleteSpawn(parent): %v", err)
				}
				want.ParentID = nil
			}

			if res, err := f.rrRestore(r, r.version); err != nil || res != store.CondApplied {
				t.Fatalf("RestoreAfterFailedReuse = %v, %v; want CondApplied", res, err)
			}
			rsAssertRow(t, f.rawColumns(id), want)
			if got := rrNoPreTrustClass(t, f, id); got != seededClass {
				t.Errorf("typeof(no_pre_trust) = %q; want the seeded %q", got, seededClass)
			}
			if got := f.historyAllLives(id); !reflect.DeepEqual(got, r.history) {
				t.Errorf("history %+v -> %+v; want the reset's, archive included", r.history, got)
			}
			if reqs := f.reuseRequests(t, id); len(seeded.requests) == 0 || len(reqs) != 0 {
				t.Errorf("requests %d -> %d after the restore; want the reset's deletion kept", len(seeded.requests), len(reqs))
			}
		})
	}
}

// TestReuseRestoreErrors checks an injected restore failure or a prior read
// from a live row returns an error and no CondResult, leaving the reset row.
func TestReuseRestoreErrors(t *testing.T) {
	cases := []struct {
		name  string
		prior func(t *testing.T, f *v5Store, r rrReset) store.RawLife
	}{
		{"injected reuse-restore failure", func(t *testing.T, f *v5Store, r rrReset) store.RawLife {
			storefix.InjectWriteFailure(t, f.path, storefix.WriteFailReuseRestore, r.id)
			return r.prior
		}},
		{"prior read from a live row", func(t *testing.T, f *v5Store, _ rrReset) store.RawLife {
			return f.readForReuse(t, f.seed(store.StateWaiting, "sess-rr-live")).Life
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			r := rrDoReset(t, f, seedReuseRow(t, f, reuseSpec{}).id, reuseFresh("", false))
			if res, err := f.s.RestoreAfterFailedReuse(r.id, r.version, tc.prior(t, f, r), rrFailedAt); err == nil || res != 0 {
				t.Fatalf("RestoreAfterFailedReuse = %v, %v; want an error with no CondResult", res, err)
			}
			rsAssertRow(t, f.rawColumns(r.id), r.reset)
			if got := f.historyAllLives(r.id); !reflect.DeepEqual(got, r.history) {
				t.Errorf("history %+v -> %+v; want the reset's", r.history, got)
			}
		})
	}
}
