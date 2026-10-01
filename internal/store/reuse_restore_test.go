package store_test

// Store tests for the restore after a failed reuse (SR-10.4, SR-5.3, SR-5.5,
// SR-5.6, SR-5.8, SR-22.9): an applied restore writes the pre-reuse row back
// byte for byte; it applies only at the reset's version; a deleted parent
// reads NULL; a store error leaves the reset row. Each test seeds through
// reuse_test.go's seedReuseRow and drives the real ReadForReuse and
// ResetForReuse (with reuseFresh) first; the reset's own columns are not
// re-tested here.

import (
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rrFailedAt is the failure time the restore is given, distinct from every
// seeded and fresh time.
var rrFailedAt = time.Date(2030, 1, 2, 3, 9, 7, 0, time.UTC)

// rrReset is a row reuse read and reset, with what the test compares against.
type rrReset struct {
	id       string
	prior    store.RawLife          // the raw life ReadForReuse returned
	before   apitest.SpawnColumns   // raw columns before the reset
	reset    apitest.SpawnColumns   // raw columns right after the reset
	history  []apitest.HistoryEntry // every-life history right after the reset
	version  int64                  // the version the reset returned
	archived string                 // the session id the reset archived
}

// rrDoReset reads id for reuse and resets it with fresh; it fails unless the reset applied.
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

// rrRestore restores r with the given reset version and the test's failure time.
func (f *v5Store) rrRestore(r rrReset, version int64) (store.CondResult, error) {
	return f.s.RestoreAfterFailedReuse(r.id, version, r.prior, rrFailedAt)
}

// rrWant is the row an applied restore of r leaves: the pre-reuse row with no
// launch start, the reset version + 1 and, for a NULL ended_at, the failure time.
func rrWant(r rrReset) apitest.SpawnColumns {
	want := r.before
	want.LaunchStartedAt = nil
	want.RowVersion = r.version + 1
	if want.EndedAt == nil {
		want.EndedAt = rrFailedAt.Format("2006-01-02 15:04:05")
	}
	return want
}

// rrAssertUnchanged fails unless id's row and history read exactly as given.
func rrAssertUnchanged(t *testing.T, f *v5Store, id string, row apitest.SpawnColumns, history []apitest.HistoryEntry) {
	t.Helper()
	rsAssertRow(t, f.rawColumns(id), row)
	if got := f.historyAllLives(id); !reflect.DeepEqual(got, history) {
		t.Errorf("history %+v -> %+v; want unchanged", history, got)
	}
}

// rrResetChanged are columns the reset must change in every seeded case, so
// "written back" is never vacuous.
func rrResetChanged(c apitest.SpawnColumns) map[string]any {
	return map[string]any{
		"state": c.State, "life_number": c.LifeNumber, "no_pre_trust": c.NoPreTrust,
		"claude_session_id": c.ClaudeSessionID, "launch_token": c.LaunchToken, "tmux_socket": c.TmuxSocket,
		"started_at": c.StartedAt, "last_seen_at": c.LastSeenAt, "cwd": c.CWD,
		"tmux_session_name": c.TmuxSessionName, "claude_args": c.ClaudeArgs, "relay_mode": c.RelayMode,
		"labels": c.Labels, "extra_env": c.ExtraEnv, "parent_id": c.ParentID, "pid": c.PID,
	}
}

// TestReuseRestoreApplied checks an applied restore writes the pre-reuse row back
// byte for byte (no launch start, version+1), keeps the archive and the deletion.
func TestReuseRestoreApplied(t *testing.T) {
	badToken := reuseIdentity()
	badToken.Token = "NOT-a-token!"
	cases := []struct {
		name            string
		spec            reuseSpec
		freshNoPreTrust bool   // the reset's pre-trust choice, the opposite of the seed's
		seedClass       string // typeof(no_pre_trust) the seed must store; "" skips the check
	}{
		{name: "ended, ended_at and started_at text a re-format would change"},
		{name: "missing, ended_at and started_at text a re-format would change", spec: reuseSpec{state: store.StateMissing}},
		{name: "missing, NULL ended_at gets the failure time", spec: reuseSpec{state: store.StateMissing, noEndedAt: true}},
		{name: "malformed labels, args, env, token, text no_pre_trust and timestamps",
			spec: reuseSpec{opts: []apitest.SpawnOption{
				apitest.WithRawLabels(`{bad`), apitest.WithRawClaudeArgs(`["unterminated`),
				apitest.WithRawExtraEnv(`not json`), apitest.WithLaunchIdentity(badToken),
				apitest.WithRawNoPreTrust("yes"), apitest.WithEndedAt("not a time"),
				apitest.WithStartedAt("not a start"),
			}}},
		{name: "real no_pre_trust", spec: reuseSpec{opts: []apitest.SpawnOption{apitest.WithRawNoPreTrust(2.5)}}},
		{name: "blob no_pre_trust", spec: reuseSpec{state: store.StateMissing,
			opts: []apitest.SpawnOption{apitest.WithRawNoPreTrust([]byte{0x01, 0x7f})}}, seedClass: "blob"},
		// The driver reads a zero-length blob as nil, the same as NULL, so only
		// typeof() tells a restore that writes it back from one that writes NULL.
		{name: "zero-length blob no_pre_trust", spec: reuseSpec{
			opts: []apitest.SpawnOption{apitest.WithRawNoPreTrust([]byte{})}}, seedClass: "blob"},
		{name: "no token, socket or identity", spec: reuseSpec{opts: []apitest.SpawnOption{apitest.WithNoLaunchToken()}}},
		{name: "pre-trust allowed, reset with the opt-out", spec: reuseSpec{preTrust: true}, freshNoPreTrust: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			seeded := seedReuseRow(t, f, tc.spec)
			id := seeded.id
			if len(seeded.requests) == 0 {
				t.Fatal("seed left no permission requests; the deletion check would be vacuous")
			}
			seededClass := rrNoPreTrustClass(t, f, id)
			if tc.seedClass != "" && seededClass != tc.seedClass {
				t.Fatalf("seeded typeof(no_pre_trust) = %q; want %q", seededClass, tc.seedClass)
			}
			r := rrDoReset(t, f, id, reuseFresh(f.seed(store.StateWaiting, ""), tc.freshNoPreTrust))
			if got := rrNoPreTrustClass(t, f, id); got != "integer" {
				t.Fatalf("typeof(no_pre_trust) after the reset = %q; want integer", got)
			}
			if (r.before.EndedAt == nil) != tc.spec.noEndedAt {
				t.Fatalf("seeded ended_at = %s; want NULL %v", rsShow(r.before.EndedAt), tc.spec.noEndedAt)
			}
			for col, was := range rrResetChanged(r.before) {
				if reflect.DeepEqual(rrResetChanged(r.reset)[col], was) {
					t.Fatalf("the reset left %s = %s; the restore check would be vacuous", col, rsShow(was))
				}
			}

			if res, err := f.rrRestore(r, r.version); err != nil || res != store.CondApplied {
				t.Fatalf("RestoreAfterFailedReuse = %v, %v; want CondApplied", res, err)
			}
			rsAssertRow(t, f.rawColumns(id), rrWant(r))
			if got := rrNoPreTrustClass(t, f, id); got != seededClass {
				t.Errorf("typeof(no_pre_trust) = %q; want the seeded %q", got, seededClass)
			}
			if got := f.historyAllLives(id); !reflect.DeepEqual(got, r.history) {
				t.Errorf("history %+v -> %+v; want the reset's, archive included", r.history, got)
			}
			if !rrHasEntry(r.history, r.archived, r.before.LifeNumber) {
				t.Errorf("history %+v lacks the archived %q in life %v", r.history, r.archived, r.before.LifeNumber)
			}
			if reqs, err := f.s.PermissionRequestsForSpawn(id); err != nil || len(reqs) != 0 {
				t.Errorf("permission requests after the restore = %+v, %v; want none", reqs, err)
			}
		})
	}
}

// rrNoPreTrustClass reads typeof(no_pre_trust) for id straight from the table.
// It tells a zero-length blob from NULL, which the raw column reads cannot.
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

// rrHasEntry reports whether history has sessionID's entry in life.
func rrHasEntry(history []apitest.HistoryEntry, sessionID string, life any) bool {
	for _, e := range history {
		if e.ClaudeSessionID == sessionID && e.LifeNumber == life {
			return true
		}
	}
	return false
}

// TestReuseRestoreParentID checks a live pre-reuse parent is written back and a
// parent deleted after the reset gives an applied restore with parent_id NULL.
func TestReuseRestoreParentID(t *testing.T) {
	for _, deleteParent := range []bool{false, true} {
		name := map[bool]string{false: "live parent written back", true: "deleted parent reads NULL"}[deleteParent]
		t.Run(name, func(t *testing.T) {
			f := newV5Store(t)
			seeded := seedReuseRow(t, f, reuseSpec{})
			r := rrDoReset(t, f, seeded.id, reuseFresh(f.seed(store.StateWaiting, ""), false))
			want := rrWant(r)
			if deleteParent {
				if err := f.s.DeleteSpawn(seeded.parent); err != nil {
					t.Fatalf("DeleteSpawn(parent %q): %v", seeded.parent, err)
				}
				want.ParentID = nil
			}
			if res, err := f.rrRestore(r, r.version); err != nil || res != store.CondApplied {
				t.Fatalf("RestoreAfterFailedReuse = %v, %v; want CondApplied", res, err)
			}
			rsAssertRow(t, f.rawColumns(seeded.id), want)
		})
	}
}

// TestReuseRestoreNotApplied checks a restore after a delete, another write, with
// a wrong version or repeated gives CondAbsent/CondChanged and writes nothing.
func TestReuseRestoreNotApplied(t *testing.T) {
	cases := []struct {
		name    string
		write   func(t *testing.T, f *v5Store, r rrReset) // a write after the reset; nil for none
		version func(r rrReset) int64                     // the reset version the restore passes
		want    store.CondResult
	}{
		{name: "row deleted after the reset", want: store.CondAbsent,
			write: func(t *testing.T, f *v5Store, r rrReset) {
				if err := f.s.DeleteSpawn(r.id); err != nil {
					t.Fatalf("DeleteSpawn: %v", err)
				}
			},
			version: func(r rrReset) int64 { return r.version }},
		{name: "SetParentID after the reset", want: store.CondChanged,
			write: func(t *testing.T, f *v5Store, r rrReset) {
				if err := f.s.SetParentID(r.id, f.seed(store.StateWaiting, "")); err != nil {
					t.Fatalf("SetParentID: %v", err)
				}
				if got := f.rawColumns(r.id); got.State != store.StatePending || got.RowVersion != r.version+1 {
					t.Fatalf("after SetParentID: state %s, row_version %s; want pending at %d",
						rsShow(got.State), rsShow(got.RowVersion), r.version+1)
				}
			},
			version: func(r rrReset) int64 { return r.version }},
		{name: "stale reset version", want: store.CondChanged, version: func(r rrReset) int64 { return r.version - 1 }},
		{name: "reset version ahead", want: store.CondChanged, version: func(r rrReset) int64 { return r.version + 1 }},
		{name: "zero reset version", want: store.CondChanged, version: func(rrReset) int64 { return 0 }},
		{name: "second restore after an applied one", want: store.CondChanged,
			write: func(t *testing.T, f *v5Store, r rrReset) {
				if res, err := f.rrRestore(r, r.version); err != nil || res != store.CondApplied {
					t.Fatalf("first RestoreAfterFailedReuse = %v, %v; want CondApplied", res, err)
				}
			},
			version: func(r rrReset) int64 { return r.version }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			r := rrDoReset(t, f, seedReuseRow(t, f, reuseSpec{}).id, reuseFresh("", false))
			if tc.write != nil {
				tc.write(t, f, r)
			}
			if tc.want == store.CondAbsent {
				if res, err := f.rrRestore(r, tc.version(r)); err != nil || res != store.CondAbsent {
					t.Fatalf("RestoreAfterFailedReuse = %v, %v; want CondAbsent", res, err)
				}
				if _, err := apitest.ReadSpawnColumns(f.path, r.id); !errors.Is(err, store.ErrSpawnNotFound) {
					t.Errorf("ReadSpawnColumns after the restore: %v; want ErrSpawnNotFound", err)
				}
				return
			}
			before, history := f.rawColumns(r.id), f.historyAllLives(r.id)
			if res, err := f.rrRestore(r, tc.version(r)); err != nil || res != tc.want {
				t.Fatalf("RestoreAfterFailedReuse = %v, %v; want %v", res, err, tc.want)
			}
			rrAssertUnchanged(t, f, r.id, before, history)
		})
	}
}

// TestReuseRestoreAfterRefusedHook checks an ordinary hook on the reset row (no
// pane) is not applied, changes nothing, and the restore then applies.
func TestReuseRestoreAfterRefusedHook(t *testing.T) {
	type fire func(t *testing.T, dbPath, id, event, sessionID string, opts ...apitest.HookOption) store.HookApplied
	senders := []struct {
		name string
		fire fire
	}{{"agent", apitest.ApplyAgentHook}, {"foreign", apitest.ApplyForeignHook}}
	for _, event := range []string{"Stop", "SessionEnd"} {
		for _, archivedSession := range []bool{true, false} {
			for _, sender := range senders {
				name := event + "/" + sender.name + map[bool]string{true: "/archived session", false: "/other session"}[archivedSession]
				t.Run(name, func(t *testing.T) {
					f := newV5Store(t)
					r := rrDoReset(t, f, seedReuseRow(t, f, reuseSpec{}).id, reuseFresh("", false))
					session := "sess-rr-other"
					if archivedSession {
						session = r.archived
					}
					got := sender.fire(t, f.path, r.id, event, session)
					if got.Applied || got.Reason != store.HookReasonNoPaneRecorded {
						t.Fatalf("%s hook = %+v; want not applied, %s", event, got, store.HookReasonNoPaneRecorded)
					}
					rrAssertUnchanged(t, f, r.id, r.reset, r.history)
					if res, err := f.rrRestore(r, r.version); err != nil || res != store.CondApplied {
						t.Fatalf("RestoreAfterFailedReuse = %v, %v; want CondApplied", res, err)
					}
					rsAssertRow(t, f.rawColumns(r.id), rrWant(r))
				})
			}
		}
	}
}

// TestReuseRestoreErrors checks an injected restore failure or a prior not read
// from a finished row returns an error, no CondResult, and leaves the reset row.
func TestReuseRestoreErrors(t *testing.T) {
	cases := []struct {
		name  string
		prior func(t *testing.T, f *v5Store, r rrReset) store.RawLife
	}{
		{name: "injected reuse-restore failure", prior: func(t *testing.T, f *v5Store, r rrReset) store.RawLife {
			storefix.InjectWriteFailure(t, f.path, storefix.WriteFailReuseRestore, r.id)
			return r.prior
		}},
		{name: "zero prior life", prior: func(*testing.T, *v5Store, rrReset) store.RawLife { return store.RawLife{} }},
		{name: "prior read from a live row", prior: func(t *testing.T, f *v5Store, _ rrReset) store.RawLife {
			row, found, err := f.s.ReadForReuse(f.seed(store.StateWaiting, "sess-rr-live"))
			if err != nil || !found {
				t.Fatalf("ReadForReuse(live) = found %v, %v; want the row", found, err)
			}
			return row.Life
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			r := rrDoReset(t, f, seedReuseRow(t, f, reuseSpec{}).id, reuseFresh("", false))
			prior := tc.prior(t, f, r)
			res, err := f.s.RestoreAfterFailedReuse(r.id, r.version, prior, rrFailedAt)
			if err == nil || res != 0 {
				t.Fatalf("RestoreAfterFailedReuse = %v, %v; want an error with no CondResult", res, err)
			}
			rrAssertUnchanged(t, f, r.id, r.reset, r.history)
			if r.reset.State != store.StatePending || r.reset.LaunchToken != reuseFresh("", false).Identity.Token {
				t.Errorf("reset row state %s, token %s; want pending with the fresh token",
					rsShow(r.reset.State), rsShow(r.reset.LaunchToken))
			}
		})
	}
}
