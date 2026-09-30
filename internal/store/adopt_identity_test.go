package store_test

// Adoption write tests (SR-3.6, SR-5.2, SR-5.3; Appendix F.3/F.4): the one
// conditional write kill, send-keys, pause and find-missing make after a lost
// create reply, AdoptIdentityIfUnchanged. Rows are seeded only through
// apitest.SeedSpawn options and read raw through apitest.ReadSpawnColumns;
// the package's sandbox-guarded TestMain in store_test.go covers this file.

import (
	"errors"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// adoptSocket is the seeded rows' recorded tmux socket.
const adoptSocket = "/tmp/ad-adopt-test/sock"

// adoptNoIdentity is a lost create reply: the launch's token and socket, no
// server or pane identity.
func adoptNoIdentity() store.LaunchIdentity {
	return store.LaunchIdentity{Token: goodToken, Socket: adoptSocket}
}

// adoptServerNoPane records the server identity but no pane.
func adoptServerNoPane() store.LaunchIdentity {
	id := adoptNoIdentity()
	id.ServerPID, id.ServerStart, id.ServerStarttime = 111, 1767225600, apitest.LinuxProcStarttime
	return id
}

// adoptPaneNoServer records the agent's pane but no server identity, so the
// agent's hooks apply (SR-22.9) and can land before the adoption.
func adoptPaneNoServer() store.LaunchIdentity {
	id := adoptNoIdentity()
	id.PaneID, id.PanePID, id.PaneStarttime = "%7", 222, apitest.DarwinProcStarttime
	return id
}

// adoptSeed is the seed of every case: seeded carries the launch identity,
// and every column the write must keep holds a non-default value so the
// unchanged check is never vacuous (ended_at included, on any state: the
// write never looks at it).
func adoptSeed(seeded store.LaunchIdentity) []apitest.SpawnOption {
	return []apitest.SpawnOption{
		apitest.WithLaunchIdentity(seeded),
		apitest.WithPID(4242),
		apitest.WithProcStarttime(apitest.LinuxProcStarttime),
		apitest.WithLaunchStartedAt(launchStart),
		apitest.WithEndedAt("2026-01-02 03:04:05"),
		apitest.WithLivenessUnverifiedSince("2026-01-01 00:00:00"),
		apitest.WithLivenessNote("probe_eacces"),
		apitest.WithJsonlPath("/tmp/ad-adopt-test/a.jsonl"),
		apitest.WithLifeNumber(3),
		apitest.WithNoPreTrust(),
	}
}

// adoptKeptColumns are the columns adoptSeed sets that the write must keep;
// assertAdoptSeeded fails if any is NULL.
func adoptKeptColumns(c apitest.SpawnColumns) map[string]any {
	return map[string]any{
		"state": c.State, "launch_token": c.LaunchToken, "tmux_socket": c.TmuxSocket,
		"pid": c.PID, "proc_starttime": c.ProcStarttime, "launch_started_at": c.LaunchStartedAt,
		"ended_at": c.EndedAt, "liveness_unverified_since": c.LivenessUnverifiedSince,
		"liveness_note": c.LivenessNote, "jsonl_path": c.JSONLPath, "started_at": c.StartedAt,
		"life_number": c.LifeNumber, "no_pre_trust": c.NoPreTrust, "tmux_session_name": c.TmuxSessionName,
	}
}

// assertAdoptSeeded fails when a kept column is NULL after seeding, which
// would make "unchanged" vacuous.
func assertAdoptSeeded(t *testing.T, c apitest.SpawnColumns) {
	t.Helper()
	for col, v := range adoptKeptColumns(c) {
		if v == nil {
			t.Fatalf("seed left %s NULL; the unchanged check would be vacuous", col)
		}
	}
}

// adoptNoTrail fails unless no trail line was written since mark: the write
// emits no ad.spawn.state_transition and no other event (SR-14).
func adoptNoTrail(t *testing.T, mark int, id string) {
	t.Helper()
	if n := len(store.TrailEventsSince(t, mark, "ad.spawn.state_transition", id)); n != 0 {
		t.Errorf("ad.spawn.state_transition lines = %d, want 0", n)
	}
	if got := store.TrailMark(t); got != mark {
		t.Errorf("trail lines %d -> %d, want no line written", mark, got)
	}
}

// TestAdoptIdentityIfUnchangedApplied checks an applied adoption writes the
// six identity columns (zero as NULL) and row_version +1, and nothing else.
func TestAdoptIdentityIfUnchangedApplied(t *testing.T) {
	serverOnly := createdIdentity()
	serverOnly.PaneID, serverOnly.PanePID, serverOnly.PaneStarttime = "", 0, ""
	blankStarts := createdIdentity()
	blankStarts.ServerStarttime, blankStarts.PaneStarttime = "", ""
	paneOnSeededServer := adoptServerNoPane()
	created := createdIdentity()
	paneOnSeededServer.PaneID, paneOnSeededServer.PanePID, paneOnSeededServer.PaneStarttime =
		created.PaneID, created.PanePID, created.PaneStarttime
	cases := []struct {
		name    string
		state   string
		session string
		seeded  store.LaunchIdentity
		adopt   store.LaunchIdentity // Token and Socket differ from the seed's and must be ignored
	}{
		{"live row, no identity, server and pane adopted", "waiting", "sess-adopt", adoptNoIdentity(), createdIdentity()},
		{"pending row, no identity, server and pane adopted", "pending", "", adoptNoIdentity(), createdIdentity()},
		{"row with server and no pane, pane adopted", "waiting", "sess-adopt", adoptServerNoPane(), paneOnSeededServer},
		{"server-only adoption leaves pane columns NULL", "waiting", "sess-adopt", adoptNoIdentity(), serverOnly},
		{"empty start times stored as NULL", "pending", "", adoptNoIdentity(), blankStarts},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newV5Store(t)
			id := f.seed(c.state, c.session, adoptSeed(c.seeded)...)
			before := f.rawColumns(id)
			assertAdoptSeeded(t, before)
			if reflect.DeepEqual(identityColumns(before), wantIdentityColumns(c.adopt)) {
				t.Fatal("identity before the write equals the adopted one; the write check would be vacuous")
			}
			examined, mark := rvExamine(t, f, id).Snapshot, store.TrailMark(t)

			got, err := f.s.AdoptIdentityIfUnchanged(id, examined, c.adopt)
			if err != nil || got != store.CondApplied {
				t.Fatalf("AdoptIdentityIfUnchanged = %v, %v; want CondApplied, nil", got, err)
			}
			want := withIdentity(before, c.adopt)
			want.RowVersion = before.RowVersion.(int64) + 1
			if after := f.rawColumns(id); !reflect.DeepEqual(after, want) {
				t.Errorf("row after the adoption:\n got %+v\nwant %+v", after, want)
			}
			adoptNoTrail(t, mark, id)

			// The same examined snapshot again: the first write moved the
			// version, so the second sees a changed row and writes nothing.
			applied := f.rawColumns(id)
			if got, err := f.s.AdoptIdentityIfUnchanged(id, examined, createdIdentity()); err != nil || got != store.CondChanged {
				t.Fatalf("second AdoptIdentityIfUnchanged = %v, %v; want CondChanged, nil", got, err)
			}
			if after := f.rawColumns(id); !reflect.DeepEqual(after, applied) {
				t.Errorf("second call changed the row:\nbefore %+v\nafter  %+v", applied, after)
			}
		})
	}
}

// TestAdoptIdentityIfUnchangedNotApplied checks a stale snapshot gives
// CondChanged and a deleted row CondAbsent, each writing nothing.
func TestAdoptIdentityIfUnchangedNotApplied(t *testing.T) {
	type staleFn func(t *testing.T, f *v5Store, id string, s *store.RowSnapshot)
	// between: a write landing after the verb examined the row.
	between := func(write func(t *testing.T, f *v5Store, id string)) staleFn {
		return func(t *testing.T, f *v5Store, id string, _ *store.RowSnapshot) { write(t, f, id) }
	}
	// examinedDiffers: the examined snapshot differs from the stored one in one field.
	examinedDiffers := func(edit func(*store.RowSnapshot)) staleFn {
		return func(_ *testing.T, _ *v5Store, _ string, s *store.RowSnapshot) { edit(s) }
	}
	hook := func(event string) func(*testing.T, *v5Store, string) {
		return func(t *testing.T, f *v5Store, id string) {
			if got := storefix.ApplyAgentHook(t, f.s, id, event, "sess-adopt"); !got.Applied {
				t.Fatalf("%s = %+v; want applied", event, got)
			}
		}
	}
	cases := []struct {
		name    string
		state   string
		session string
		seeded  store.LaunchIdentity
		stale   staleFn
		want    store.CondResult
	}{
		{name: "hook write first", state: "waiting", session: "sess-adopt", seeded: adoptPaneNoServer(),
			stale: between(hook("Stop")), want: store.CondChanged},
		{name: "SessionStart first", state: "pending", seeded: adoptPaneNoServer(),
			stale: between(hook("SessionStart")), want: store.CondChanged},
		{name: "non-hook write first", state: "waiting", session: "sess-adopt", seeded: adoptNoIdentity(),
			stale: between(markMissing("waiting")), want: store.CondChanged},
		{name: "examined version differs", state: "waiting", session: "sess-adopt", seeded: adoptNoIdentity(),
			stale: examinedDiffers(func(s *store.RowSnapshot) { s.RowVersion++ }), want: store.CondChanged},
		{name: "examined started_at differs", state: "waiting", session: "sess-adopt", seeded: adoptNoIdentity(),
			stale: examinedDiffers(func(s *store.RowSnapshot) { s.StartedAt += "x" }), want: store.CondChanged},
		{name: "examined session id differs", state: "waiting", session: "sess-adopt", seeded: adoptNoIdentity(),
			stale: examinedDiffers(func(s *store.RowSnapshot) { s.ClaudeSessionID = "sess-other" }), want: store.CondChanged},
		{name: "examined pid differs", state: "waiting", session: "sess-adopt", seeded: adoptNoIdentity(),
			stale: examinedDiffers(func(s *store.RowSnapshot) { s.PID++ }), want: store.CondChanged},
		{name: "examined process start time differs", state: "waiting", session: "sess-adopt", seeded: adoptNoIdentity(),
			stale: examinedDiffers(func(s *store.RowSnapshot) { s.ProcStarttime = apitest.DarwinProcStarttime }), want: store.CondChanged},
		{name: "examined session name differs", state: "waiting", session: "sess-adopt", seeded: adoptNoIdentity(),
			stale: examinedDiffers(func(s *store.RowSnapshot) { s.TmuxSessionName += "x" }), want: store.CondChanged},
		{name: "row deleted", state: "waiting", session: "sess-adopt", seeded: adoptNoIdentity(),
			stale: between(func(t *testing.T, f *v5Store, id string) {
				if err := f.s.DeleteSpawn(id); err != nil {
					t.Fatalf("DeleteSpawn: %v", err)
				}
			}), want: store.CondAbsent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newV5Store(t)
			id := f.seed(c.state, c.session, adoptSeed(c.seeded)...)
			examined := rvExamine(t, f, id).Snapshot
			c.stale(t, f, id, &examined)
			var before apitest.SpawnColumns
			if c.want != store.CondAbsent {
				before = f.rawColumns(id)
			}
			mark := store.TrailMark(t)

			got, err := f.s.AdoptIdentityIfUnchanged(id, examined, createdIdentity())
			if err != nil || got != c.want {
				t.Fatalf("AdoptIdentityIfUnchanged = %v, %v; want %v, nil", got, err, c.want)
			}
			if c.want == store.CondAbsent {
				if _, err := apitest.ReadSpawnColumns(f.path, id); !errors.Is(err, store.ErrSpawnNotFound) {
					t.Errorf("ReadSpawnColumns after an absent adoption: %v; want ErrSpawnNotFound", err)
				}
			} else if after := f.rawColumns(id); !reflect.DeepEqual(after, before) {
				t.Errorf("row changed:\nbefore %+v\nafter  %+v", before, after)
			}
			adoptNoTrail(t, mark, id)
		})
	}
}

// TestAdoptIdentityIfUnchangedDriverError checks a failed statement returns
// an error and a zero CondResult, never a Cond value.
func TestAdoptIdentityIfUnchangedDriverError(t *testing.T) {
	f := newV5Store(t)
	id := f.seed("waiting", "sess-adopt", adoptSeed(adoptNoIdentity())...)
	examined := rvExamine(t, f, id).Snapshot
	if err := f.s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got, err := f.s.AdoptIdentityIfUnchanged(id, examined, createdIdentity()); err == nil || got != 0 {
		t.Fatalf("AdoptIdentityIfUnchanged on a closed store = %v, %v; want 0 and an error", got, err)
	}
}
