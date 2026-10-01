package api_test

// expire_reasons_test.go pins expire's per-row outcome and kept reason
// (SR-12.2) for every process state and the outcomes beyond the call table's
// (lookup_calltable_expire_test.go has each lookup outcome, with a later row
// on the same socket), and its call discipline (SR-12.5, SR-3.15): lookups
// only, at most one per socket, no adoption, no session touched. The
// unusable-name reasons are expire_reasons_unusable_test.go's.

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rsnAge is how long ago a seeded row ended; rsnOtherSocket is a second socket.
const (
	rsnAge         = 48 * time.Hour
	rsnOtherSocket = apitest.TestSocket + "-other"
)

// rsnID is a fresh row id starting with prefix, so rows sort by prefix.
func rsnID(prefix string) string { return prefix + "-" + uuid.NewString()[:8] }

// rsnFinish makes spec a row with id that ended rsnAge ago, its agent gone.
func (e *killEnv) rsnFinish(spec killRowSpec, id string) killRowSpec {
	f := e.finishedSpec(rsnAge, agentGone)
	spec.ID, spec.State, spec.Agent = id, f.State, f.Agent
	spec.Opts = append(f.Opts, spec.Opts...)
	return spec
}

// rsnExpect runs expire over every finished row (window) and checks want
// (id -> kept reason, "" deleted) through rsnExpectWith.
func (e *killEnv) rsnExpect(t *testing.T, window time.Duration, want map[string]string, sockets ...string) {
	t.Helper()
	e.rsnExpectWith(t, func() (api.ExpireResult, *recordingLogger, error) { return e.expire(olderThan(window)) },
		want, sockets...)
}

// rsnExpectWith runs run and checks want, exactly one lookup per socket and
// no other call, kept rows and every session unchanged, deleted rows gone.
func (e *killEnv) rsnExpectWith(t *testing.T, run func() (api.ExpireResult, *recordingLogger, error),
	want map[string]string, sockets ...string) {
	t.Helper()
	before := map[string]apitest.SpawnColumns{}
	for id := range want {
		before[id] = e.columns(t, id)
	}
	sessions := map[string][]tmuxfix.SeedSession{}
	for _, s := range []string{apitest.TestSocket, rsnOtherSocket} {
		sessions[s] = e.rec.Sessions(s)
	}
	mark := trailMark(t)

	res, lg, err := run()

	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if len(lg.lines) != 0 {
		t.Errorf("log lines = %q; want none", lg.lines)
	}
	assertExpired(t, res, mark, want)
	e.assertLookupsOn(t, sockets...)
	for id, reason := range want {
		after, readErr := apitest.ReadSpawnColumns(e.dbPath, id)
		switch {
		case reason == "" && readErr == nil:
			t.Errorf("row %s still in the store; want deleted", id)
		case reason != "" && !reflect.DeepEqual(after, before[id]):
			t.Errorf("kept row %s changed:\n got %+v\nwant %+v", id, after, before[id])
		}
	}
	for s, was := range sessions {
		if now := e.rec.Sessions(s); !reflect.DeepEqual(now, was) {
			t.Errorf("sessions on %s changed:\n got %+v\nwant %+v", s, now, was)
		}
	}
}

// rsnSS is how a process case records the SessionStart identity beside the pane's.
type rsnSS int

const (
	ssAgrees        rsnSS = iota // SessionStart equals the pane identity (the fixture default)
	ssPaneOnly                   // only the pane identity is recorded
	ssOnly                       // only SessionStart is recorded, with a start time
	ssPIDOnly                    // only a SessionStart pid is recorded, no start time
	ssDisagreesDead              // another SessionStart pid, gone
	ssDisagreesLive              // another SessionStart pid, alive
)

// TestExpireReasons_Process pins the agent-process step: alive by the selected
// identity keeps process_alive with no tmux call; anything else is looked up.
func TestExpireReasons_Process(t *testing.T) {
	cases := []struct {
		name  string
		ss    rsnSS
		agent agentState // the selected identity's process
		want  string
	}{
		{"session start alive", ssAgrees, agentAlive, "process_alive"},
		{"only session start recorded, alive", ssOnly, agentAlive, "process_alive"},
		{"only pane identity recorded, alive", ssPaneOnly, agentAlive, "process_alive"},
		{"identities disagree, session start dead, pane alive", ssDisagreesDead, agentAlive, "process_alive"},
		{"identities disagree, session start alive, pane dead", ssDisagreesLive, agentGone, ""},
		{"dead", ssAgrees, agentGone, ""},
		{"zombie", ssAgrees, agentZombie, ""},
		{"unreadable", ssAgrees, agentUnreadable, ""},
		{"pid-only identity alive", ssPIDOnly, agentAlive, ""},
		{"no process recorded", ssAgrees, agentNotRecorded, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newKillEnv(t)
			spec := e.finishedSpec(rsnAge, c.agent)
			ssPID := e.newPID()
			switch c.ss {
			case ssPaneOnly:
				spec.Opts = append(spec.Opts, apitest.WithPID(0), apitest.WithProcStarttime(""))
			case ssOnly, ssPIDOnly:
				spec.NoPane = true
				spec.Opts = append(spec.Opts, apitest.WithPID(ssPID))
				if c.ss == ssOnly {
					spec.Opts = append(spec.Opts, apitest.WithProcStarttime(apitest.LinuxProcStarttime))
				}
			case ssDisagreesDead, ssDisagreesLive:
				spec.Opts = append(spec.Opts, apitest.WithPID(ssPID), apitest.WithProcStarttime(apitest.LinuxProcStarttime))
			}
			r := e.seedRow(t, spec)
			switch c.ss {
			case ssDisagreesDead:
				e.pc.Set(ssPID, procfix.Gone())
			case ssDisagreesLive:
				e.pc.Set(ssPID, procfix.Alive(apitest.LinuxProcStarttime))
			}
			var sockets []string
			if c.want == "" {
				sockets = []string{apitest.TestSocket}
			}
			e.rsnExpect(t, 0, map[string]string{r.ID: c.want}, sockets...)
		})
	}
}

// TestExpireReasons_Rows pins the outcomes beyond the call table's single
// row: the stopping window, another store's sessions, no adoption and cost.
func TestExpireReasons_Rows(t *testing.T) {
	cases := []struct {
		name   string
		window time.Duration
		setup  func(t *testing.T, e *killEnv) (want map[string]string, sockets []string)
	}{
		{name: "ours, row ended within the stopping window", setup: func(t *testing.T, e *killEnv) (map[string]string, []string) {
			r := e.seedFinished(t, e.cfg.EffectiveStoppingWindow()/3, agentGone)
			e.seedSession(t, &r)
			return map[string]string{r.ID: "ours"}, []string{apitest.TestSocket}
		}},
		{name: "ours, row ended long ago", setup: func(t *testing.T, e *killEnv) (map[string]string, []string) {
			r := e.seedFinished(t, 30*24*time.Hour, agentGone)
			e.seedSession(t, &r)
			return map[string]string{r.ID: "ours"}, []string{apitest.TestSocket}
		}},
		{name: "gone, server restarted and answering with no session", setup: func(t *testing.T, e *killEnv) (map[string]string, []string) {
			r := e.seedFinished(t, rsnAge, agentGone)
			e.seedSession(t, &r)
			e.rec.RestartServer(r.Socket, tmuxfix.Server{})
			e.syncServers()
			return map[string]string{r.ID: ""}, []string{apitest.TestSocket}
		}},
		{name: "gone, another store's session with the row's token under another name", setup: rsnOtherStoreElsewhere(func(r killRow) string { return r.Token })},
		{name: "gone, another store's session with another token under another name", setup: rsnOtherStoreElsewhere(func(killRow) string { return tmuxfix.OtherToken })},
		{name: "another store's session beside an ours row, one lookup", setup: func(t *testing.T, e *killEnv) (map[string]string, []string) {
			other := e.seedFinished(t, rsnAge, agentGone)
			e.seedSession(t, &other, tmuxfix.WithRowSessionLabel(other.otherStore(other.Token), true))
			ours := e.seedFinished(t, rsnAge, agentGone)
			e.seedSession(t, &ours)
			return map[string]string{other.ID: "", ours.ID: "ours"}, []string{apitest.TestSocket}
		}},
		{name: "no adoption, ours row with no server or pane recorded", setup: func(t *testing.T, e *killEnv) (map[string]string, []string) {
			spec := e.finishedSpec(rsnAge, agentGone)
			spec.NoServerIdentity, spec.NoPane, spec.NoSession = true, true, false
			r := e.seedRow(t, spec)
			return map[string]string{r.ID: "ours"}, []string{apitest.TestSocket}
		}},
		{name: "many rows on one socket, one lookup", setup: func(t *testing.T, e *killEnv) (map[string]string, []string) {
			want := map[string]string{}
			for range 3 {
				want[e.seedFinished(t, rsnAge, agentGone).ID] = ""
				r := e.seedFinished(t, rsnAge, agentGone)
				e.seedSession(t, &r)
				want[r.ID] = "ours"
			}
			left := e.seedFinished(t, rsnAge, agentZombie)
			e.seedLeftover(t, left, tmuxfix.OtherToken)
			want[left.ID] = "leftover_running"
			want[e.seedFinished(t, rsnAge, agentAlive).ID] = "process_alive"
			return want, []string{apitest.TestSocket}
		}},
		{name: "rows on two sockets, one lookup each", setup: func(t *testing.T, e *killEnv) (map[string]string, []string) {
			r := e.seedFinished(t, rsnAge, agentGone)
			e.seedSession(t, &r)
			want := map[string]string{r.ID: "ours"}
			for range 2 {
				spec := e.finishedSpec(rsnAge, agentGone, apitest.WithTmuxSocket(rsnOtherSocket))
				spec.NoServerIdentity = true
				want[e.seedRow(t, spec).ID] = ""
			}
			return want, []string{apitest.TestSocket, rsnOtherSocket}
		}},
		{name: "every selected row process_alive, no lookup", setup: func(t *testing.T, e *killEnv) (map[string]string, []string) {
			want := map[string]string{}
			for range 3 {
				want[e.seedFinished(t, rsnAge, agentAlive).ID] = "process_alive"
			}
			return want, nil
		}},
		{name: "nothing selected, no lookup", window: time.Hour, setup: func(t *testing.T, e *killEnv) (map[string]string, []string) {
			e.seedFinished(t, 10*time.Minute, agentGone)
			return map[string]string{}, nil
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newKillEnv(t)
			want, sockets := c.setup(t, e)
			e.rsnExpect(t, c.window, want, sockets...)
		})
	}
}

// rsnOtherStoreElsewhere seeds a row whose only session is another store's,
// labelled with token(row) and not holding the recorded name: deleted.
func rsnOtherStoreElsewhere(token func(killRow) string) func(*testing.T, *killEnv) (map[string]string, []string) {
	return func(t *testing.T, e *killEnv) (map[string]string, []string) {
		r := e.seedFinished(t, rsnAge, agentGone)
		e.seedSession(t, &r, tmuxfix.WithRowSessionLabel(r.otherStore(token(r)), true),
			tmuxfix.WithRowSessionName("elsewhere-"+r.ID))
		return map[string]string{r.ID: ""}, []string{apitest.TestSocket}
	}
}

// TestExpireReasons_BudgetSpentIsSkipped pins not_run -> tmux_skipped: with no
// budget, no call is made and only process_alive differs.
func TestExpireReasons_BudgetSpentIsSkipped(t *testing.T) {
	e := newKillEnv(t)
	gone := e.seedFinished(t, rsnAge, agentGone)
	ours := e.seedFinished(t, rsnAge, agentGone)
	e.seedSession(t, &ours)
	alive := e.seedFinished(t, rsnAge, agentAlive)
	run := func() (api.ExpireResult, *recordingLogger, error) {
		lg := &recordingLogger{}
		res, err := api.Expire(e.st, e.rec, e.pc, config.Default().Defaults.ExpireRetentionDays, olderThan(0),
			fmBudgetSpent, e.clock.Now, lg)
		return res, lg, err
	}
	e.rsnExpectWith(t, run, map[string]string{gone.ID: "tmux_skipped", ours.ID: "tmux_skipped",
		alive.ID: "process_alive"})
}
