package api_test

// expire_sweep_test.go covers expire's tmux path across sockets (SR-3.3, SR-3.15, SR-12.2, SR-13.3, SR-13.5;
// AC-EXP-05, AC-EXP-08, AC-EXP-10): a hung socket, the run's budget in virtual time, tmux unreachable, the
// wrong-server scenario and the socket rule for rows that record none. Rows come from the expire fixture.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// exsHang is the most one lookup costs at the defaults: the query timeout plus the pipe-close wait (SR-13.3).
var exsHang = config.Default().Tmux.EffectiveQueryTimeout() + config.Default().Tmux.EffectivePipeCloseWait()

// exsIDs returns n instance ids sharing a fresh prefix, sorting in index order.
func exsIDs(n int) []string {
	prefix := "exs-" + uuid.NewString()[:8]
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-%02d", prefix, i)
	}
	return ids
}

// exsSeed seeds finished row id, an hour old with no session, on socket and its agent in state a.
func exsSeed(t *testing.T, e *killEnv, id, socket string, a agentState) killRow {
	t.Helper()
	spec := e.finishedSpec(time.Hour, a, apitest.WithTmuxSocket(socket))
	spec.ID = id
	return e.seedRow(t, spec)
}

// exsLegacy seeds finished row id recording no socket and no server identity, its agent gone.
func exsLegacy(t *testing.T, e *killEnv, id string) {
	t.Helper()
	spec := e.finishedSpec(time.Hour, agentGone, apitest.WithTmuxSocket(""))
	spec.ID, spec.NoServerIdentity = id, true
	e.seedRow(t, spec)
}

// exsChargedBySocket returns a map the Recorder fills, as e's lookups return, with the virtual time each socket's
// lookups charged.
func exsChargedBySocket(e *killEnv) map[string]time.Duration {
	got, last := map[string]time.Duration{}, e.clock.Now()
	e.rec.AfterCall(tmux.CallLookup, func(c tmuxfix.SocketCall, _ error) {
		now := e.clock.Now()
		got[c.Socket] += now.Sub(last)
		last = now
	})
	return got
}

// mustExpire runs e.expire with over, failing the test on an error.
func mustExpire(t *testing.T, e *killEnv, over *time.Duration) api.ExpireResult {
	t.Helper()
	res, _, err := e.expire(over)
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	return res
}

// TestExpireSweepHungSocket: a hung socket A costs one lookup of at most Q + W; its first row is cant_tell, its
// later rows tmux_skipped, while socket B's rows, between A's in id order, are decided by B's lookup.
func TestExpireSweepHungSocket(t *testing.T) {
	e := newKillEnv(t)
	sockA, sockB := fmbSocket(1), fmbSocket(2)
	e.rec.WithVirtualTime(e.clock, tmux.Timeouts{Query: exsHang}).
		Script(sockA, tmuxfix.Script{Failure: tmux.FailTimeout}, tmux.CallLookup)
	id := exsIDs(5)
	exsSeed(t, e, id[0], sockA, agentGone)
	exsSeed(t, e, id[1], sockB, agentGone)
	exsSeed(t, e, id[2], sockA, agentUnreadable)
	ours := exsSeed(t, e, id[3], sockB, agentGone)
	e.seedSession(t, &ours)
	exsSeed(t, e, id[4], sockA, agentZombie)
	charged, mark := exsChargedBySocket(e), trailMark(t)

	res := mustExpire(t, e, olderThan(0))
	assertExpired(t, res, mark, map[string]string{
		id[0]: "cant_tell", id[1]: "", id[2]: "tmux_skipped", id[3]: "ours", id[4]: "tmux_skipped"})
	e.assertLookupsOn(t, sockA, sockB)
	if charged[sockA] > exsHang {
		t.Errorf("virtual time charged on the hung socket = %v; want at most %v", charged[sockA], exsHang)
	}
}

// TestExpireSweepBudget: Client.Expire at the default and at a configured budget, each lookup charged Q + W: no
// call starts once the budget is spent, every row from the spending call on is kept tmux_skipped, the run succeeds.
func TestExpireSweepBudget(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings []apitest.TmuxSetting
		budget   time.Duration
	}{
		{"default", nil, fmBudget},
		{"configured", []apitest.TmuxSetting{apitest.TmuxInt(config.TmuxSweepBudgetSeconds, fmbCfgBudgetSeconds)},
			fmbCfgBudgetSeconds * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			e.rec.WithVirtualTime(e.clock, tmux.Timeouts{Query: exsHang})
			calls := fmbCallsToSpend(tc.budget, exsHang)
			ids, sockets, want := exsIDs(calls+3), []string{}, map[string]string{}
			for i, id := range ids {
				exsSeed(t, e, id, fmbSocket(i), agentGone)
				sockets, want[id] = append(sockets, fmbSocket(i)), ""
				if i >= calls-1 {
					want[id] = "tmux_skipped"
				}
			}
			mark, start := trailMark(t), e.clock.Now()

			res, _, err := e.expireClient(t, olderThan(0), tc.settings...)
			if err != nil {
				t.Fatalf("Client.Expire: %v", err)
			}
			assertExpired(t, res, mark, want)
			e.assertLookupsOn(t, sockets[:calls]...)
			if spent := e.clock.Now().Sub(start); spent != time.Duration(calls)*exsHang || spent > tc.budget+exsHang {
				t.Errorf("tmux time = %v; want %v, at most %v", spent, time.Duration(calls)*exsHang, tc.budget+exsHang)
			}
		})
	}
}

// TestExpireSweepTmuxUnreachable: with every socket's lookup failing, nothing is deleted and every selected row is
// kept: each socket's first row with the failure's reason, its later rows tmux_skipped, a live agent's row
// process_alive.
func TestExpireSweepTmuxUnreachable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure tmux.Failure
		reason  string
	}{
		{"tmux not runnable", tmux.FailUnavailable, "tmux_unavailable"},
		{"socket permission denied", tmux.FailSocketDenied, "tmux_unavailable"},
		{"every socket hangs", tmux.FailTimeout, "cant_tell"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tc.failure}, tmux.CallLookup)
			s1, s2, id := fmbSocket(1), fmbSocket(2), exsIDs(5)
			exsSeed(t, e, id[0], s1, agentGone)
			exsSeed(t, e, id[1], s2, agentNotRecorded)
			exsSeed(t, e, id[2], s1, agentUnreadable)
			exsSeed(t, e, id[3], s2, agentAlive)
			exsSeed(t, e, id[4], s2, agentZombie)
			mark := trailMark(t)

			res := mustExpire(t, e, olderThan(0))
			assertExpired(t, res, mark, map[string]string{id[0]: tc.reason, id[1]: tc.reason,
				id[2]: "tmux_skipped", id[3]: "process_alive", id[4]: "tmux_skipped"})
			e.assertLookupsOn(t, s1, s2)
		})
	}
}

// TestExpireSweepWrongServer (AC-EXP-10, TLA+ F-7): a sweep on another server marks a live row missing; expire
// on the agents' server then keeps it as ours and does not delete it.
func TestExpireSweepWrongServer(t *testing.T) {
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{NoServerIdentity: true, Agent: agentUnreadable})
	other := tmuxfix.NewRecorder()

	fm, err := api.FindMissing(context.Background(), e.st, other, e.pc, e.cfg.EffectivePendingGrace(),
		e.cfg.EffectiveSweepBudget(), e.clock.Now, &recordingLogger{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	assertLists(t, fm, []string{r.ID}, nil)
	if got := e.columns(t, r.ID).State; got != store.StateMissing {
		t.Fatalf("state after the other server's sweep = %v; want %s", got, store.StateMissing)
	}
	mark := trailMark(t)

	res := mustExpire(t, e, olderThan(0))
	assertExpired(t, res, mark, map[string]string{r.ID: "ours"})
	e.assertLookupsOn(t, r.Socket)
	if got := e.columns(t, r.ID).State; got != store.StateMissing {
		t.Errorf("state after expire = %v; want the row kept %s", got, store.StateMissing)
	}
}

// TestExpireSweepNoSocketRule: recorded-socket rows are looked up there whatever the caller's environment; rows
// recording none share one lookup on the caller's socket, resolved creating nothing, or get none when refused.
func TestExpireSweepNoSocketRule(t *testing.T) {
	t.Run("caller's TMUX names another server", func(t *testing.T) {
		e := newKillEnv(t)
		caller := filepath.Join(t.TempDir(), "caller")
		t.Setenv("TMUX", caller+",4242,0")
		t.Setenv("TMUX_TMPDIR", t.TempDir())
		id := exsIDs(4)
		ours := exsSeed(t, e, id[0], apitest.TestSocket, agentGone)
		e.seedSession(t, &ours)
		exsLegacy(t, e, id[1])
		exsSeed(t, e, id[2], apitest.TestSocket, agentUnreadable)
		exsLegacy(t, e, id[3])
		mark := trailMark(t)

		res := mustExpire(t, e, olderThan(0))
		assertExpired(t, res, mark, map[string]string{id[0]: "ours", id[1]: "", id[2]: "", id[3]: ""})
		e.assertLookupsOn(t, apitest.TestSocket, caller)
	})

	t.Run("no per-user directory", func(t *testing.T) {
		e := newKillEnv(t)
		tmpdir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatalf("EvalSymlinks: %v", err)
		}
		t.Setenv("TMUX_TMPDIR", tmpdir)
		id := exsIDs(3)
		exsLegacy(t, e, id[0])
		exsSeed(t, e, id[1], apitest.TestSocket, agentGone)
		exsLegacy(t, e, id[2])
		mark := trailMark(t)

		res := mustExpire(t, e, olderThan(0))
		assertExpired(t, res, mark, map[string]string{id[0]: "", id[1]: "", id[2]: ""})
		e.assertLookupsOn(t, apitest.TestSocket, filepath.Join(userSocketDir(tmpdir), "default"))
		if _, err := os.Stat(userSocketDir(tmpdir)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("per-user directory: stat err %v; want it still missing", err)
		}
	})

	t.Run("per-user directory mode 0755", func(t *testing.T) {
		e := newKillEnv(t)
		if err := os.Chmod(filepath.Dir(e.defaultSocket), 0o755); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		id := exsIDs(4)
		exsLegacy(t, e, id[0])
		exsSeed(t, e, id[1], apitest.TestSocket, agentGone)
		exsLegacy(t, e, id[2])
		ours := exsSeed(t, e, id[3], apitest.TestSocket, agentUnreadable)
		e.seedSession(t, &ours)
		mark, start := trailMark(t), e.clock.Now()

		res := mustExpire(t, e, olderThan(0))
		assertExpired(t, res, mark, map[string]string{
			id[0]: "tmux_unavailable", id[1]: "", id[2]: "tmux_skipped", id[3]: "ours"})
		e.assertLookupsOn(t, apitest.TestSocket)
		if spent, q := e.clock.Now().Sub(start), config.Default().Tmux.EffectiveQueryTimeout(); spent != q {
			t.Errorf("virtual time spent = %v; want one query timeout (%v): a refusal charges nothing", spent, q)
		}
	})
}
