package api_test

// settle_guards_test.go (b.3ie): with [state] settle_ms at its largest and a
// raw state that changed just now, so status reports the last settled state,
// every guard acts on the raw state: send-keys' state and relay guards (their
// err_details' state included), decide (ErrRelayFallenBack's err_details
// state included), find-missing, resume, pause, kill (its finished-row opt-in
// too), expire and spawn's collision pre-checks (plain and reuse). One test
// per guard, each through a Client whose status shows the other state. The
// store's guard reads and guarded writes are internal/store's
// settle_guards_test.go. Last, only the read surfaces read the settled state.

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// settleClient is e.clientFor with [state] settle_ms at its largest.
func (e *killEnv) settleClient(t *testing.T) *api.Client {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	apitest.WriteKeysConfig(t, cfgPath, apitest.ConfigKeys{StateSettleMs: settleMax})
	c, _, err := e.clientFor(t, cfgPath)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	return c
}

// unsettle makes id's raw state a hook's change at `at` from reported, which
// c reports until it has held the settle time, and checks that c's status
// shows reported. Call it once c's clock is set.
func unsettle(t *testing.T, c *api.Client, dbPath, id, reported string, at time.Time) {
	t.Helper()
	if err := apitest.SetSettle(dbPath, id, reported, at.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Status(id); err != nil || got.State != reported {
		t.Fatalf("Status(%s) = %+v, %v; want %s while the raw change settles", id, got, err, reported)
	}
}

// TestSettleGuardSendKeysState: a row raw waiting just after its SessionStart
// reads pending, yet send-keys, which refuses a pending row without
// allow_pending, delivers to it.
func TestSettleGuardSendKeysState(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{})
	c := e.settleClient(t)
	unsettle(t, c, e.dbPath, r.ID, store.StatePending, e.clock.Now())
	if _, err := c.SendKeys(api.SendKeysParams{InstanceID: r.ID, Text: "1"}); err != nil {
		t.Fatalf("SendKeys on a raw waiting row reading pending: %v; want delivered", err)
	}
	e.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, "1")
}

// TestSettleGuardSendKeysRelay: on a relay-on row raw check_permission that
// reads waiting, plain send-keys refuses with ErrRelayFallenBack while its
// request has fallen back, and ErrDialogMaybeOpen once it is decided but not
// proven gone, each err_details naming the raw state check_permission.
func TestSettleGuardSendKeysRelay(t *testing.T) {
	t.Parallel()
	settled := sendKeysWindow() + api.RelayKillSafetyMargin + api.CreatedAtResolution
	for _, decided := range []bool{false, true} {
		t.Run(map[bool]string{false: "fallen back", true: "decided, unproven"}[decided], func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := seedRelayRow(t, e, storefix.TestRequestTokenA)
			now := time.Now()
			if decided {
				if ok, err := e.st.DecidePermissionRequest(r.ID, storefix.TestRequestTokenA, "allow", "", store.WriterProcessDecide); err != nil || !ok {
					t.Fatalf("DecidePermissionRequest = %v, %v", ok, err)
				}
				now = advRequestCreatedAt(t, e, r).Add(settled)
			} else {
				storefix.SeedUndeliverablePermissionRequest(t, e.st, e.dbPath, r.ID, storefix.TestRequestTokenA, 2*sendKeysWindow())
			}
			c := e.settleClient(t)
			api.SetClockForTest(c, func() time.Time { return now }) // the requests are stored at wall-clock time
			unsettle(t, c, e.dbPath, r.ID, store.StateWaiting, now)

			_, err := c.SendKeys(api.SendKeysParams{InstanceID: r.ID, Text: "2"})

			if decided {
				assertDialogMaybeOpen(t, err, storefix.TestRequestTokenA, store.StateCheckPermission)
			} else {
				assertOneSentinel(t, err, api.ErrRelayFallenBack)
				if d, ok := api.ErrDetails(err).(api.RelayFallenBackDetails); !ok || d.State != store.StateCheckPermission {
					t.Errorf("err_details = %#v; want RelayFallenBackDetails with state %s", api.ErrDetails(err), store.StateCheckPermission)
				}
			}
			e.assertNoTmuxCall(t)
		})
	}
}

// TestSettleGuardDecide: decide judges a fallen-back request by the raw
// state: on a row raw check_permission reading waiting it is
// ErrRelayFallenBack with err_details state check_permission; on a row raw
// waiting (the agent's Stop) reading check_permission, ErrNoOpenPermissionRequest
// naming the state waiting.
func TestSettleGuardDecide(t *testing.T) {
	t.Parallel()
	for _, stopped := range []bool{false, true} {
		t.Run(map[bool]string{false: "raw check_permission reading waiting", true: "raw waiting reading check_permission"}[stopped], func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := seedRelayRow(t, e, storefix.TestRequestTokenA)
			storefix.SeedUndeliverablePermissionRequest(t, e.st, e.dbPath, r.ID, storefix.TestRequestTokenA, 2*sendKeysWindow())
			reported := store.StateWaiting
			if stopped {
				if err := seedAgentState(e.st, e.dbPath, r.ID, store.StateWaiting); err != nil {
					t.Fatalf("the agent's Stop: %v", err)
				}
				reported = store.StateCheckPermission
			}
			c := e.settleClient(t)
			now := time.Now()
			api.SetClockForTest(c, func() time.Time { return now })
			unsettle(t, c, e.dbPath, r.ID, reported, now)

			_, err := c.Decide(api.DecideParams{InstanceID: r.ID, RequestToken: storefix.TestRequestTokenA, Decision: "allow"})

			if stopped {
				assertOneSentinel(t, err, store.ErrNoOpenPermissionRequest)
				adviceAssertPhrase(t, err, "the spawn is in state "+store.StateWaiting)
				return
			}
			assertOneSentinel(t, err, api.ErrRelayFallenBack)
			if d, ok := api.ErrDetails(err).(api.RelayFallenBackDetails); !ok || d.State != store.StateCheckPermission {
				t.Errorf("err_details = %#v; want RelayFallenBackDetails with state %s", api.ErrDetails(err), store.StateCheckPermission)
			}
		})
	}
}

// TestSettleGuardFindMissing: a row raw waiting whose agent is gone, reading
// pending, is marked missing from waiting (proc_absent), and reads missing at
// once.
func TestSettleGuardFindMissing(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{Agent: agentGone, NoSession: true})
	c := e.settleClient(t)
	unsettle(t, c, e.dbPath, r.ID, store.StatePending, e.clock.Now())

	res, err := c.FindMissing(context.Background())

	if err != nil || !slices.Contains(res.IDs, r.ID) {
		t.Fatalf("FindMissing = %+v, %v; want %s marked", res, err, r.ID)
	}
	ticks := pendTrail(t, "ad.find_missing.tick", r.ID)
	if len(ticks) != 1 || ticks[0]["prior_state"] != store.StateWaiting {
		t.Errorf("ticks = %v; want one from the raw state %s", ticks, store.StateWaiting)
	}
	if got, err := c.Status(r.ID); err != nil || got.State != store.StateMissing {
		t.Errorf("Status after the mark = %+v, %v; want missing at once", got, err)
	}
}

// TestSettleGuardResume: a row raw waiting (its agent reported in out of
// missing) reading missing is not resumable, and resume writes nothing.
func TestSettleGuardResume(t *testing.T) {
	t.Parallel()
	e := newResumeEnv(t)
	r := e.seedRow(t, resumableSpec{State: store.StateWaiting})
	c := settleClientFor(t, e.dbPath, settleMax, e.rec, e.clock.Now)
	unsettle(t, c, e.dbPath, r.ID, store.StateMissing, e.clock.Now())
	before := e.columns(t, r.ID)

	_, err := c.Resume(api.ResumeParams{InstanceID: r.ID})

	assertOneSentinel(t, err, api.ErrSpawnNotResumable)
	adviceAssertPhrase(t, err, "state="+store.StateWaiting)
	if after := e.columns(t, r.ID); !reflect.DeepEqual(after, before) {
		t.Errorf("row changed:\nbefore %+v\nafter  %+v", before, after)
	}
}

// TestSettleGuardPause: a row raw working reading waiting is not pausable:
// ErrSpawnNotPausable, no /exit typed.
func TestSettleGuardPause(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{State: store.StateWorking})
	c := e.settleClient(t)
	unsettle(t, c, e.dbPath, r.ID, store.StateWaiting, e.clock.Now())
	e.rec.Reset() // unsettle's status read sampled the raw working row's own pane

	_, err := c.Pause(context.Background(), api.PauseParams{InstanceID: r.ID})

	assertOneSentinel(t, err, api.ErrSpawnNotPausable)
	e.assertNoTmuxCall(t)
}

// TestSettleGuardKill: a row raw waiting reading missing is a live row to
// kill: kill does not take it as finished (its unusable name is ErrInternal,
// where a finished row is a no-op), and the finished-row opt-in refuses it as
// live, naming the state waiting.
func TestSettleGuardKill(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	unusable := e.seedRow(t, killRowSpec{NoSession: true, Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName("a:b")}})
	live := e.seedRow(t, killRowSpec{})
	c := e.settleClient(t)
	for _, id := range []string{unusable.ID, live.ID} {
		unsettle(t, c, e.dbPath, id, store.StateMissing, e.clock.Now())
	}

	res, err := c.Kill(api.KillParams{InstanceID: unusable.ID})
	assertOneName(t, err, "ErrInternal") // the live row's unusable name; a finished row is a no-op
	if res.KillSent {
		t.Errorf("Kill = %+v; want no kill sent", res)
	}
	_, err = api.KillFinished(e.store, e.rec, e.pc, e.cfg.EffectiveStartingSession(), e.cfg.EffectiveStoppingWindow(),
		e.cfg.EffectiveKillExitWait(), e.clock.Now, e.sleep, live.ID)
	assertOneSentinel(t, err, api.ErrSpawnNotResumable)
	adviceAssertPhrase(t, err, "the row is live (state "+store.StateWaiting+")")
	e.assertNoTmuxCall(t)
}

// TestSettleGuardExpire: a row raw waiting with an old ended_at, reading
// ended (resurrected by its agent's hook), is no expire candidate; a raw
// ended one is expired.
func TestSettleGuardExpire(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	live := e.seedRow(t, killRowSpec{Agent: agentGone, NoSession: true,
		Opts: []apitest.SpawnOption{apitest.WithEndedAt(e.clock.Now().Add(-48 * time.Hour))}})
	gone := e.seedFinished(t, 48*time.Hour, agentGone)
	c := e.settleClient(t)
	unsettle(t, c, e.dbPath, live.ID, store.StateEnded, e.clock.Now())

	res, err := c.Expire(olderThan(time.Hour))

	if err != nil || !slices.Equal(res.IDs, []string{gone.ID}) || len(res.KeptIDs) != 0 {
		t.Fatalf("Expire = %+v, %v; want only the raw ended row %s deleted", res, err, gone.ID)
	}
	if _, err := e.st.GetSpawn(live.ID); err != nil {
		t.Errorf("the raw waiting row: %v; want it kept", err)
	}
}

// TestSettleGuardSpawnCollision: spawn's collision pre-checks read a row raw
// waiting, reading missing, as live: a plain spawn and a reuse of its id are
// ErrInstanceIdCollision "already live", with no create.
func TestSettleGuardSpawnCollision(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{NoSession: true})
	c := e.settleClient(t)
	unsettle(t, c, e.dbPath, r.ID, store.StateMissing, e.clock.Now())
	for _, reuse := range []bool{false, true} {
		_, err := c.Spawn(api.SpawnParams{Agent: "claude", InstanceID: r.ID, ReuseFinished: reuse, CWD: t.TempDir(),
			ExtraEnv: seedTrustConfig(t, t.TempDir(), trustLacksEntry).extraEnv()})
		assertOneSentinel(t, err, spawn.ErrInstanceIdCollision)
		adviceAssertPhrase(t, err, r.ID+" already live")
	}
	if n := len(e.rec.SocketCallsOf(tmux.CallCreate)); n != 0 {
		t.Errorf("create calls = %d; want 0", n)
	}
}

// settledReadNames are the identifiers of the settled read: the reported
// state, the store's settled reads and the reader's settle time and clock.
var settledReadNames = map[string]bool{"ReportedState": true, "GetSpawnSettled": true, "SpawnStatusSettled": true,
	"SettleRead": true, "settleRead": true, "Settle": true}

// settledReadFiles are the only production files that may name them: the
// derivation and the store reads, and status, get and list.
var settledReadFiles = []string{"internal/store/settle.go", "internal/store/family.go", "internal/store/spawns.go", "internal/store/spawn_status.go", "internal/store/list.go", "internal/store/cursor_missing_read.go", "internal/store/display.go", "pkg/api/display.go",
	"pkg/api/client.go", "pkg/api/status.go", "pkg/api/get.go", "pkg/api/list.go"}

// TestOnlyTheReadSurfacesReadTheSettledState: no production file outside
// settledReadFiles names the settled read, so no guard can act on the
// reported state (b.3ie requirement 8).
func TestOnlyTheReadSurfacesReadTheSettledState(t *testing.T) {
	t.Parallel()
	skip := map[string]bool{"apitest": true, "testsupport": true, "testdata": true, "test": true, "vendor": true}
	root := filepath.Join("..", "..")
	found := map[string]bool{}
	for _, dir := range []string{"internal", "pkg", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case d.IsDir() && (skip[d.Name()] || strings.HasPrefix(d.Name(), ".")):
				return filepath.SkipDir
			case d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go"):
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && settledReadNames[id.Name] {
					found[rel] = true
					if !slices.Contains(settledReadFiles, rel) {
						t.Errorf("%s names %s; only %v may read the settled state", rel, id.Name, settledReadFiles)
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	for _, f := range []string{"internal/store/settle.go", "pkg/api/status.go", "pkg/api/get.go", "pkg/api/list.go"} {
		if !found[f] {
			t.Errorf("%s names no settled read; the walk missed it", f)
		}
	}
}
