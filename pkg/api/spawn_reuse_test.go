package api_test

// spawn_reuse_test.go covers SpawnParams.ReuseFinished (SR-10.1, AC-REUSE-13)
// at the parameter level: with no explicit id it is an ordinary fresh spawn;
// a control-character id is still ErrInvalidFlags; without the opt-in a
// finished row still collides with ErrInstanceIdCollision. It also covers
// SR-10.2's rows that need no lookup of an old row (AC-REUSE-05, AC-REUSE-14,
// AC-REUSE-18): no row (a fresh spawn after the label scan, and the insert
// race), every live state, and a failed pre-check read.

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// finishedStates are the row states the reuse opt-in is about.
var finishedStates = []string{store.StateEnded, store.StateMissing}

// reuseRowState is everything about one row a reuse could change: its
// columns, its session history over every life and its permission requests.
type reuseRowState struct {
	cols    apitest.SpawnColumns
	history []apitest.HistoryEntry
	perms   []api.PermissionRow
}

// readReuseRowState reads id's reuseRowState from the store at dbPath.
func readReuseRowState(t *testing.T, dbPath, id string) reuseRowState {
	t.Helper()
	cols, err := apitest.ReadSpawnColumns(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns(%s): %v", id, err)
	}
	history, err := apitest.ReadSessionHistoryAllLives(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSessionHistoryAllLives(%s): %v", id, err)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close() //nolint:errcheck
	perms, err := st.PermissionRequestsForSpawn(id)
	if err != nil {
		t.Fatalf("PermissionRequestsForSpawn(%s): %v", id, err)
	}
	return reuseRowState{cols: cols, history: history, perms: perms}
}

// seedFinishedRow seeds a finished row in state with a session id, one
// archived history entry and one permission request, and returns its id and
// its reuseRowState.
func seedFinishedRow(t *testing.T, dbPath, state string) (string, reuseRowState) {
	t.Helper()
	id := "reuse-" + uuid.NewString()[:8]
	if _, err := apitest.SeedSpawn(dbPath, id, state, "", "", uuid.NewString(), false,
		apitest.WithLifeNumber(1),
		apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: uuid.NewString(), JSONLPath: "/tmp/old.jsonl", Life: 1})); err != nil {
		t.Fatalf("SeedSpawn(%s): %v", state, err)
	}
	if _, err := apitest.SeedPermissionRequest(dbPath, id, "Bash"); err != nil {
		t.Fatalf("SeedPermissionRequest: %v", err)
	}
	return id, readReuseRowState(t, dbPath, id)
}

// assertRowStateUnchanged fails unless id's reuseRowState still equals before.
func assertRowStateUnchanged(t *testing.T, dbPath, id string, before reuseRowState) {
	t.Helper()
	if after := readReuseRowState(t, dbPath, id); !reflect.DeepEqual(after, before) {
		t.Errorf("row %s changed:\nbefore %+v\nafter  %+v", id, before, after)
	}
}

// TestSpawnReuseFinishedWithoutIDIsFreshSpawn: with no explicit id the opt-in
// has no effect: a minted id, one pending row, one create, and a finished row
// with another id left as it was.
func TestSpawnReuseFinishedWithoutIDIsFreshSpawn(t *testing.T) {
	for _, state := range finishedStates {
		t.Run(state, func(t *testing.T) {
			env := newSpawnEnv(t)
			other, before := seedFinishedRow(t, env.dbPath, state)

			res, err := env.c.Spawn(api.SpawnParams{CWD: t.TempDir(), ReuseFinished: true})

			if err != nil {
				t.Fatalf("Spawn: %v", err)
			}
			got := res.ClaudeInstanceID
			if _, perr := uuid.Parse(got); perr != nil {
				t.Errorf("minted id %q is not a UUID: %v", got, perr)
			}
			cols, err := apitest.ReadSpawnColumns(env.dbPath, got)
			if err != nil || cols.State != store.StatePending {
				t.Errorf("new row state = %v (err %v); want pending", cols.State, err)
			}
			if n := len(env.rec.SocketCallsOf(tmux.CallCreate)); n != 1 {
				t.Errorf("create calls = %d; want 1", n)
			}
			if ids := listIDs(t, env.c); len(ids) != 2 {
				t.Errorf("List ids = %q; want the finished row and one new row", ids)
			}
			assertRowStateUnchanged(t, env.dbPath, other, before)
		})
	}
}

// TestSpawnReuseFinishedControlCharacterID: the opt-in does not bypass the
// control-character check: ErrInvalidFlags, no row and no tmux call.
func TestSpawnReuseFinishedControlCharacterID(t *testing.T) {
	const marker = "reusemark"
	for _, tc := range []struct{ name, ctl string }{
		{"newline", "\n"},
		{"tab", "\t"},
		{"DEL", "\x7f"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			id := marker + tc.ctl + "tail"

			_, err := env.c.Spawn(api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: id, ReuseFinished: true})

			assertInvalidFlags(t, err, id, marker)
			assertNoTmuxCalls(t, env.rec)
			if ids := listIDs(t, env.c); len(ids) != 0 {
				t.Errorf("List ids = %q; want none", ids)
			}
		})
	}
}

// TestSpawnFinishedRowCollidesWithoutReuse: without the opt-in an ended or
// missing row is ErrInstanceIdCollision; the row, its history and permission
// requests are unchanged and no session is created.
func TestSpawnFinishedRowCollidesWithoutReuse(t *testing.T) {
	for _, state := range finishedStates {
		t.Run(state, func(t *testing.T) {
			env := newSpawnEnv(t)
			id, before := seedFinishedRow(t, env.dbPath, state)

			_, err := env.c.Spawn(api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: id, ReuseFinished: false})

			assertOneSentinel(t, err, spawn.ErrInstanceIdCollision)
			assertRowStateUnchanged(t, env.dbPath, id, before)
			if n := len(env.rec.SocketCallsOf(tmux.CallCreate)); n != 0 {
				t.Errorf("create calls = %d; want 0", n)
			}
		})
	}
}

// rtabParams is a reuse request for id under name, in a new cwd, pre-trusting
// in trust's config directory.
func rtabParams(t *testing.T, id, name string, trust trustConfig) api.SpawnParams {
	return api.SpawnParams{ClaudeInstanceID: id, ReuseFinished: true, TmuxSessionName: name, CWD: t.TempDir(),
		ExtraEnv: trust.extraEnv()}
}

// rtabNoNewCalls fails when the Recorder saw any call since before was taken.
func (e *killEnv) rtabNoNewCalls(t *testing.T, before writesSnapshot) {
	t.Helper()
	if n, m := len(e.rec.SocketCalls())-before.calls, len(e.rec.Calls())-before.nameCalls; n != 0 || m != 0 {
		t.Errorf("tmux calls since the snapshot: %d socket, %d name-based; want none", n, m)
	}
}

// rtabAssertInternal fails unless err is ErrInternal (it wraps no catalogued
// sentinel) worded as c.
func rtabAssertInternal(t *testing.T, err error, c apitest.DescCase) {
	t.Helper()
	if name, _ := errnames.Classify(err); err == nil || name != "ErrInternal" {
		t.Fatalf("err = %v (classified %q); want ErrInternal", err, name)
	}
	apitest.AssertDescription(t, err.Error(), c)
}

// TestSpawnReuseNoRowIsFreshSpawn: with no row, one label-scan lookup comes
// before the insert; another store's label proceeds to a plain launch (life 0,
// no archive, no ad.spawn.reused) and this store's leftover refuses, writing nothing.
func TestSpawnReuseNoRowIsFreshSpawn(t *testing.T) {
	cases := []struct {
		name    string
		label   func(e *killEnv, id string) tmux.Label // nil: no session, no server
		refused bool
	}{
		{"no session", nil, false},
		{"another store's label for the id", func(e *killEnv, id string) tmux.Label {
			return tmuxfix.Valid(tmuxfix.OtherToken, id, apitest.OtherStoreID(e.storeID))
		}, false},
		{"this store's leftover under another name", func(e *killEnv, id string) tmux.Label {
			return tmuxfix.Valid(tmuxfix.OtherToken, id, e.storeID)
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			id := "reuse-" + uuid.NewString()[:8]
			var held tmuxfix.SeedSession
			if tc.label != nil {
				e.ensureServer(&killRow{Socket: e.defaultSocket})
				e.syncServers()
				held = e.seedOther(t, e.defaultSocket, tmuxfix.SeedSession{Name: "elsewhere-" + uuid.NewString()[:8],
					Label: tc.label(e, id)})
			}
			trust := seedTrustConfig(t, t.TempDir(), trustLacksEntry)
			p := rtabParams(t, id, "", trust)
			rowAtLookup := true
			e.rec.AfterCall(tmux.CallLookup, func(tmuxfix.SocketCall, error) {
				_, err := apitest.ReadSpawnColumns(e.dbPath, id)
				rowAtLookup = !errors.Is(err, store.ErrSpawnNotFound)
			})
			mark := trailMark(t)

			res, _, err := e.reuse(t, p)

			want := []tmux.Call{tmux.CallLookup, tmux.CallCreate}
			if tc.refused {
				want = want[:1]
				assertOneSentinel(t, err, api.ErrTmuxSessionConflict)
				apitest.AssertDescription(t, err.Error(), apitest.DescScanLeftover(id,
					[]apitest.DescSession{{Name: held.Name, ID: held.ID}}))
				if _, rerr := apitest.ReadSpawnColumns(e.dbPath, id); !errors.Is(rerr, store.ErrSpawnNotFound) {
					t.Errorf("ReadSpawnColumns(%s) err = %v; want no row", id, rerr)
				}
				trust.check(t, "", false, "after the refused spawn")
			} else {
				if err != nil || res.ClaudeInstanceID != id {
					t.Fatalf("Spawn = %+v, %v; want %s", res, err, id)
				}
				cols, creates := e.columns(t, id), e.rec.SocketCallsOf(tmux.CallCreate)
				if cols.State != store.StatePending || cols.LifeNumber != int64(0) || len(creates) != 1 ||
					creates[0].Token != cols.LaunchToken {
					t.Errorf("row state %v, life %v, token %v, creates %+v; want pending at life 0, labelled by the one create",
						cols.State, cols.LifeNumber, cols.LaunchToken, creates)
				}
				if h, herr := apitest.ReadSessionHistoryAllLives(e.dbPath, id); herr != nil || len(h) != 0 {
					t.Errorf("history = %+v (%v); want none", h, herr)
				}
			}
			if got := pendCallKinds(e.rec); !slices.Equal(got, want) || rowAtLookup {
				t.Errorf("tmux calls = %v, row present at the lookup %v; want %v with no row yet", got, rowAtLookup, want)
			}
			if recs := ptRecords(t, mark, "ad.spawn.reused", id); len(recs) != 0 {
				t.Errorf("ad.spawn.reused records = %v; want none", recs)
			}
		})
	}
}

// TestSpawnReuseInsertRace: a row inserted after the pre-check read found
// none makes the insert collide: ErrInstanceIdCollision, no create, and the
// competing row is left as it was.
func TestSpawnReuseInsertRace(t *testing.T) {
	e := newKillEnv(t)
	id := "reuse-" + uuid.NewString()[:8]
	w := &hookedReuseStore{st: e.st}
	var seeded apitest.SpawnColumns
	w.afterRead(func() {
		if _, err := apitest.SeedSpawn(e.dbPath, id, store.StateWaiting, t.TempDir(), "off", "", false); err != nil {
			t.Fatalf("SeedSpawn(%s): %v", id, err)
		}
		seeded = e.columns(t, id)
	})

	_, _, err := e.reuseWith(t, w, rtabParams(t, id, "", seedTrustConfig(t, t.TempDir(), trustLacksEntry)))

	assertOneSentinel(t, err, spawn.ErrInstanceIdCollision)
	e.assertRowUnchanged(t, id, seeded)
	if n := len(e.rec.SocketCallsOf(tmux.CallCreate)); n != 0 {
		t.Errorf("create calls = %d; want 0", n)
	}
}

// rtabLive is one live row a reuse meets: how it is made, returning the row's
// id, recorded session name and socket.
type rtabLive struct {
	name string
	make func(t *testing.T, e *killEnv) (id, recorded, socket string)
}

// rtabLiveRows are the live rows of SR-10.2: every live state, a pending row
// from a fresh spawn and from a resume's move, and a row whose kill just failed.
func rtabLiveRows() []rtabLive {
	seeded := func(state string) rtabLive {
		return rtabLive{state, func(t *testing.T, e *killEnv) (string, string, string) {
			r := e.seedRow(t, killRowSpec{State: state})
			return r.ID, r.Name, r.Socket
		}}
	}
	return []rtabLive{
		{"pending from a fresh spawn", func(t *testing.T, e *killEnv) (string, string, string) {
			id := "reuse-" + uuid.NewString()[:8]
			c, _ := e.client(t)
			p := rtabParams(t, id, "", seedTrustConfig(t, t.TempDir(), trustLacksEntry))
			p.ReuseFinished = false
			if _, err := c.Spawn(p); err != nil {
				t.Fatalf("Spawn(%s): %v", id, err)
			}
			cols := e.columns(t, id)
			name, _ := cols.TmuxSessionName.(string)
			return id, name, e.defaultSocket
		}},
		{"pending from a resume's move, its session young", func(t *testing.T, e *killEnv) (string, string, string) {
			r := e.seedResumable(t, time.Hour, agentGone)
			if _, err := e.resume(r.ID); err != nil {
				t.Fatalf("resume(%s): %v", r.ID, err)
			}
			if cols := e.columns(t, r.ID); cols.State != store.StatePending || len(e.rec.SocketCallsOf(tmux.CallCreate)) != 1 {
				t.Fatalf("after the resume: state %v; want pending with one create", cols.State)
			}
			return r.ID, r.Name, r.Socket
		}},
		seeded(store.StateWaiting),
		seeded(store.StateWorking),
		seeded(store.StateAskUser),
		seeded(store.StateCheckPermission),
		{"waiting after a failed kill", func(t *testing.T, e *killEnv) (string, string, string) {
			r := e.seedRow(t, killRowSpec{})
			if _, err := e.kill(r.ID); !errors.Is(err, api.ErrTmuxKillFailed) {
				t.Fatalf("kill(%s) err = %v; want ErrTmuxKillFailed", r.ID, err)
			}
			return r.ID, r.Name, r.Socket
		}},
	}
}

// TestSpawnReuseLiveRowCollides: a live row, requested under its recorded
// name or another, is ErrInstanceIdCollision with no tmux call and nothing
// written, its launch start and the trust file included.
func TestSpawnReuseLiveRowCollides(t *testing.T) {
	for _, row := range rtabLiveRows() {
		for _, other := range []bool{false, true} {
			name := row.name + "/recorded name"
			if other {
				name = row.name + "/another name"
			}
			t.Run(name, func(t *testing.T) {
				e := newKillEnv(t)
				id, requested, socket := row.make(t, e)
				if other {
					requested = "other-" + uuid.NewString()[:8]
				}
				trust := seedTrustConfig(t, t.TempDir(), trustLacksEntry)
				before := e.snapshotWrites(t, "spawn", id, trust, socket)

				_, _, err := e.reuse(t, rtabParams(t, id, requested, trust))

				assertOneSentinel(t, err, spawn.ErrInstanceIdCollision)
				e.rtabNoNewCalls(t, before)
				e.assertWroteNothing(t, before)
			})
		}
	}
}

// TestSpawnReusePreCheckReadFails: a failed pre-check read is ErrInternal with
// the pre-check wording, no tmux call and nothing written, trust file included.
func TestSpawnReusePreCheckReadFails(t *testing.T) {
	e := newKillEnv(t)
	r := e.seedReusable(t, agentGone, reuseRowSpec{})
	w := &hookedReuseStore{st: e.st}
	w.failRead(nil)
	before := e.snapshotReuse(t, r)

	_, _, err := e.reuseWith(t, w, reuseParams(t, r, reuseRequest{}))

	rtabAssertInternal(t, err, apitest.DescPreCheckRead())
	e.rtabNoNewCalls(t, before)
	e.assertWroteNothing(t, before)
}
