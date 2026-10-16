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
	res, archived, v, err := f.s.ResetForReuse(id, row.Snapshot, row.Agent, fresh)
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

// rrStorageClass distinguishes an empty blob from NULL for restoration checks.
func rrStorageClass(t *testing.T, f *v5Store, id, column string) string {
	t.Helper()
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatalf("raw sql.Open(%q): %v", f.path, err)
	}
	defer func() { _ = db.Close() }()
	var class string
	if err := db.QueryRow(`SELECT typeof(`+column+`) FROM spawns WHERE instance_id = ?`, id).Scan(&class); err != nil {
		t.Fatalf("typeof(%s) for %q: %v", column, id, err)
	}
	return class
}

// TestReuseRestoreApplied checks an applied restore writes the pre-reuse row
// back byte for byte, malformed values and their storage class included, with
// no launch start, the reset version + 1, the failure time for a NULL ended_at
// and a NULL parent once the pre-reuse parent is deleted, the agent and end
// report and subagent_tracking (the reset set 1) included; the reset's archive
// and request deletion stay; the restore closes and reopens no subagent entry:
// what the reset closed (SR-8.1, SR-10.3) stays closed and no claimable launch comes back
// (SR-8.4).
func TestReuseRestoreApplied(t *testing.T) {
	badToken := reuseIdentity()
	badToken.Token = "NOT-a-token!"
	cases := []struct {
		name            string
		spec            reuseSpec
		freshNoPreTrust bool // the reset's pre-trust choice, the opposite of the seed's
		deleteParent    bool
		reopen          bool
		seedClass       string // typeof(no_pre_trust) the seed must store; "" = unchecked
	}{
		{name: "ended, text a re-format would change"},
		{name: "missing, NULL ended_at gets the failure time", spec: reuseSpec{state: store.StateMissing,
			opts: []apitest.SpawnOption{apitest.WithNoEndedAt()}}},
		{name: "malformed labels, args, env, token, text no_pre_trust and timestamps", spec: reuseSpec{opts: []apitest.SpawnOption{
			apitest.WithRawLabels(`{bad`), apitest.WithRawAgentArgs(`["unterminated`), apitest.WithRawExtraEnv(`not json`),
			apitest.WithLaunchIdentity(badToken), apitest.WithRawNoPreTrust("yes"), apitest.WithEndedAt("not a time"),
			apitest.WithStartedAt("not a start")}}},
		// The driver reads a zero-length blob as nil, like NULL; only typeof() tells them apart.
		{name: "zero-length blob no_pre_trust", spec: reuseSpec{opts: []apitest.SpawnOption{apitest.WithRawNoPreTrust([]byte{})}},
			seedClass: "blob"},
		{name: "no token, socket or identity", spec: reuseSpec{opts: []apitest.SpawnOption{apitest.WithNoLaunchToken()}}},
		{name: "pre-trust allowed, reset with the opt-out", spec: reuseSpec{preTrust: true}, freshNoPreTrust: true},
		{name: "pre-reuse parent deleted", deleteParent: true},
		{name: "same-agent claude", spec: reuseSpec{tracked: true, opts: []apitest.SpawnOption{apitest.WithAgent(store.AgentClaude)}}},
		{name: "Cursor switch restores unsupported tracking after reopen", reopen: true, spec: reuseSpec{opts: []apitest.SpawnOption{apitest.WithAgent(store.AgentCursor)}}},
		{name: "malformed agent text", spec: reuseSpec{handAgent: true, rawAgent: " Claude\n"}},
		{name: "NULL agent", spec: reuseSpec{handAgent: true}},
		{name: "blob agent", spec: reuseSpec{handAgent: true, rawAgent: []byte("claude")}},
		{name: "empty blob agent", spec: reuseSpec{handAgent: true, rawAgent: []byte{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			seeded := seedReuseRow(t, f, tc.spec)
			id := seeded.id
			agentClass := rrStorageClass(t, f, id, "agent")
			seededClass := rrStorageClass(t, f, id, "no_pre_trust")
			if tc.seedClass != "" && seededClass != tc.seedClass {
				t.Fatalf("seeded typeof(no_pre_trust) = %q; want %q", seededClass, tc.seedClass)
			}
			mix := f.seedCloseMix(id)
			fresh := reuseFresh(f.seed(store.StateWaiting, ""), tc.freshNoPreTrust)
			r := rrDoReset(t, f, id, fresh)
			reset := f.subagentRowsOf(id)
			wantResetVisible := 0
			if r.before.LifeNumber == r.reset.LifeNumber {
				for _, e := range reset.Entries {
					if e.Confirmed == int64(1) && e.LifeNumber == r.reset.LifeNumber {
						wantResetVisible++
					}
				}
			}
			if _, rows, err := f.s.ListSubagentRecords(id); err != nil || len(rows) != wantResetVisible {
				t.Fatalf("reset still reveals previous records: %+v, %v", rows, err)
			}
			if reflect.DeepEqual(reset, mix.rows) || !reflect.DeepEqual(reset, cmClosed(mix.rows, fresh.LaunchStartedAtMillis)) {
				t.Fatalf("the reset left the entries and launches %+v from %+v; want the close mix closed at the launch start, or the restore check is vacuous", reset, mix.rows)
			}
			for _, col := range []string{"State", "LifeNumber", "NoPreTrust", "SessionID", "LaunchToken", "StartedAt", "CWD", "Labels", "ParentID", "Agent", "EndReportedAt", "SubagentTracking"} {
				b, a := reflect.ValueOf(r.before).FieldByName(col).Interface(), reflect.ValueOf(r.reset).FieldByName(col).Interface()
				if (col == "LifeNumber" || col == "Agent" || col == "SubagentTracking") && r.before.Agent == store.AgentClaude {
					continue
				}
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
			if tc.reopen {
				if err := f.s.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := store.Open(f.path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = reopened.Close() })
				f.s = reopened
			}
			rsAssertRow(t, f.rawColumns(id), want)
			if got := rrStorageClass(t, f, id, "agent"); got != agentClass {
				t.Errorf("agent storage class = %q; want %q", got, agentClass)
			}
			if got := rrStorageClass(t, f, id, "no_pre_trust"); got != seededClass {
				t.Errorf("typeof(no_pre_trust) = %q; want the seeded %q", got, seededClass)
			}
			if got := f.historyAllLives(id); !reflect.DeepEqual(got, r.history) {
				t.Errorf("history %+v -> %+v; want the reset's, archive included", r.history, got)
			}
			if got := f.subagentRowsOf(id); !reflect.DeepEqual(got, reset) {
				t.Errorf("subagent rows %+v -> %+v; want unchanged by the restore", reset, got)
			}
			tracked, visible, err := f.s.ListSubagentRecords(id)
			if err != nil {
				t.Fatal(err)
			}
			wantVisible := 0
			for _, e := range reset.Entries {
				if e.Confirmed == int64(1) && e.LifeNumber == r.before.LifeNumber {
					wantVisible++
				}
			}
			if r.before.Agent == store.AgentCursor {
				if tracked || visible == nil {
					t.Fatalf("Cursor tracking projection: %v %+v", tracked, visible)
				}
				wantVisible = 0
			} else if wantVisible == 0 {
				t.Fatal("seeded retained subagent records missing")
			}
			if len(visible) != wantVisible {
				t.Errorf("restore shows %d records; want %d previous-life records", len(visible), wantVisible)
			}
			f.assertBystanderKept(mix)
			if reqs := f.reuseRequests(t, id); len(seeded.requests) == 0 || len(reqs) != 0 {
				t.Errorf("requests %d -> %d after the restore; want the reset's deletion kept", len(seeded.requests), len(reqs))
			}
		})
	}
}

// TestReuseRestoreErrors checks an injected restore failure or a prior read
// from a live row returns an error and no CondResult, leaving the reset row
// (the reset's agent and no end report).
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
	for _, agent := range []string{store.AgentCodex, store.AgentCursor} {
		for _, tc := range cases {
			t.Run(agent+"/"+tc.name, func(t *testing.T) {
				f := newV5Store(t)
				id := seedReuseRow(t, f, reuseSpec{opts: []apitest.SpawnOption{apitest.WithAgent(agent)}}).id
				mix := f.seedCloseMix(id)
				fresh := reuseFresh("", false)
				r := rrDoReset(t, f, id, fresh)
				if res, err := f.s.RestoreAfterFailedReuse(r.id, r.version, tc.prior(t, f, r), rrFailedAt); err == nil || res != 0 {
					t.Fatalf("RestoreAfterFailedReuse = %v, %v; want an error with no CondResult", res, err)
				}
				if r.reset.Agent == r.before.Agent || r.reset.EndReportedAt != nil {
					t.Fatalf("the reset wrote agent %v (was %v), end_reported_at %v; want a new agent and none, or the check is vacuous",
						r.reset.Agent, r.before.Agent, r.reset.EndReportedAt)
				}
				rsAssertRow(t, f.rawColumns(r.id), r.reset)
				if got := f.historyAllLives(r.id); !reflect.DeepEqual(got, r.history) {
					t.Errorf("history %+v -> %+v; want the reset's", r.history, got)
				}
				f.assertMixClosedAt(mix, fresh.LaunchStartedAtMillis)
			})
		}
	}
}
