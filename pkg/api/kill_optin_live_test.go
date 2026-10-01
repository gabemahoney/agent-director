package api_test

// kill_optin_live_test.go: kill with the finished-row opt-in refuses every
// live row, pending included, with ErrSpawnNotResumable before any lookup
// (SR-6.5, SR-1.4); ad.kill.called records include_finished (SR-6.4); kill
// without the opt-in never returns that name (SR-6.8). Finished rows are
// pinned only where Task 2's finished-row table keeps them.

import (
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// kolCase is a live row kill is asked to end: its seed and the state it reads.
type kolCase struct {
	name     string
	state    string
	spec     killRowSpec
	unusable bool // the recorded name is unusable (without the opt-in: ErrInternal)
}

// kolLiveCases are the live rows of SR-6.5: pending of each kind, waiting,
// working, ask_user and check_permission, each with its own session running,
// and a live row whose recorded name is unusable.
func kolLiveCases() []kolCase {
	var cases []kolCase
	for _, k := range killPendKinds() {
		cases = append(cases, kolCase{"pending, " + k.name, store.StatePending, killRowSpec{State: store.StatePending, Opts: k.opts}, false})
	}
	for _, s := range []string{store.StateWaiting, store.StateWorking, store.StateAskUser, store.StateCheckPermission} {
		cases = append(cases, kolCase{s, s, killRowSpec{State: s}, false})
	}
	return append(cases, kolCase{"waiting, unusable recorded name", store.StateWaiting,
		killRowSpec{NoSession: true, Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName("kill.name")}}, true})
}

// kolAssertCalled fails unless id has exactly one ad.kill.called record holding want's fields.
func kolAssertCalled(t *testing.T, id string, want map[string]any) {
	t.Helper()
	recs := killCalled(t, id)
	if len(recs) != 1 {
		t.Fatalf("ad.kill.called records = %d; want 1: %v", len(recs), recs)
	}
	for k, v := range want {
		if got, ok := recs[0][k]; !ok || got != v {
			t.Errorf("ad.kill.called %s = %v (present %t); want %v", k, got, ok, v)
		}
	}
}

// kolAssertRefused fails unless err is the live-row refusal of r in state, no
// tmux call was made, the process checker was not consulted, the row and its
// sessions are unchanged and the one ad.kill.called says so.
func kolAssertRefused(t *testing.T, e *killEnv, r killRow, state string, res api.KillResult, err error,
	before apitest.SpawnColumns, sessions []tmuxfix.SeedSession) {
	t.Helper()
	if !errors.Is(err, api.ErrSpawnNotResumable) || res.KillSent {
		t.Fatalf("kill = %+v, %v; want ErrSpawnNotResumable with kill_sent false", res, err)
	}
	apitest.AssertDescription(t, err.Error(), apitest.DescKillOptInLiveRow(r.ID, state), r.Token, r.StoreID)
	e.assertKillCalls(t)
	if n, m := len(e.pc.StartTimeCalls()), e.pc.EnvReads(); n != 0 || m != 0 {
		t.Errorf("process checker consulted (%d start-time calls, %d env reads); want none", n, m)
	}
	e.assertRowUnchanged(t, r.ID, before)
	if got := e.rec.Sessions(r.Socket); !reflect.DeepEqual(got, sessions) {
		t.Errorf("sessions after kill = %+v; want untouched %+v", got, sessions)
	}
	if n := len(killDisagrees(t, r.ID)); n != 0 {
		t.Errorf("ad.provenance.disagree records = %d; want 0", n)
	}
	kolAssertCalled(t, r.ID, map[string]any{"include_finished": true, "outcome": "ErrSpawnNotResumable",
		"lookup_outcome": "not_run", "followup_outcome": "not_run", "process_check": "not_run",
		"kill_sent": false, "pane_killed": false, "tmux_session_name": r.Name})
}

// TestKillIncludeFinishedRefusesLiveRow: each live row with the opt-in gets
// the live-row refusal with no lookup, no process check and no write; its own
// running session is still there.
func TestKillIncludeFinishedRefusesLiveRow(t *testing.T) {
	for _, tc := range kolLiveCases() {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, tc.spec)
			before, sessions := e.columns(t, r.ID), e.rec.Sessions(r.Socket)
			if before.State != tc.state {
				t.Fatalf("seeded state %v; want %s", before.State, tc.state)
			}
			if !tc.unusable && len(sessions) == 0 {
				t.Fatalf("no session seeded for %s", r.ID)
			}
			res, err := e.killOptIn(r.ID)
			kolAssertRefused(t, e, r, tc.state, res, err, before, sessions)
		})
	}
}

// TestKillIncludeFinishedClientRefusesLiveRow: Client.Kill with the opt-in
// refuses a working row as api.Kill does.
func TestKillIncludeFinishedClientRefusesLiveRow(t *testing.T) {
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{State: store.StateWorking})
	before, sessions := e.columns(t, r.ID), e.rec.Sessions(r.Socket)
	res, _, err := e.killOptInClient(t, r.ID)
	kolAssertRefused(t, e, r, store.StateWorking, res, err, before, sessions)
}

// TestKillIncludeFinishedUnknownID: an unknown id with the opt-in gets
// ErrSpawnNotFound, and its ad.kill.called records the opt-in.
func TestKillIncludeFinishedUnknownID(t *testing.T) {
	e := newKillEnv(t)
	id := "kill-unknown-" + uuid.NewString()[:8]
	if _, err := e.killOptIn(id); !errors.Is(err, api.ErrSpawnNotFound) {
		t.Fatalf("kill = %v; want ErrSpawnNotFound", err)
	}
	e.assertKillCalls(t)
	kolAssertCalled(t, id, map[string]any{"include_finished": true, "outcome": "ErrSpawnNotFound"})
}

// TestKillIncludeFinishedNotSetOnLiveRow: without the opt-in the same live rows
// (no session, agent gone) never get ErrSpawnNotResumable: lookup Gone,
// success with kill_sent false, include_finished false.
func TestKillIncludeFinishedNotSetOnLiveRow(t *testing.T) {
	for _, tc := range kolLiveCases() {
		if tc.unusable {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			spec := tc.spec
			spec.NoSession, spec.Agent = true, agentGone
			r := e.seedRow(t, spec)
			res, err := e.kill(r.ID)
			if err != nil || res.KillSent {
				t.Fatalf("kill = %+v, %v; want kill_sent false, nil", res, err)
			}
			e.assertKillCalls(t, tmux.CallLookup)
			kolAssertCalled(t, r.ID, map[string]any{"include_finished": false, "outcome": "ok",
				"lookup_outcome": "gone", "kill_sent": false})
		})
	}
}

// TestKillIncludeFinishedFinishedRowNoSession: an ended or missing row with
// the opt-in and no session succeeds with kill_sent false and no kill; the
// lookup outcome and call count are Task 2's and not pinned.
func TestKillIncludeFinishedFinishedRowNoSession(t *testing.T) {
	for _, state := range []string{store.StateEnded, store.StateMissing} {
		t.Run(state, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{State: state, NoSession: true, Agent: agentGone})
			res, err := e.killOptIn(r.ID)
			if err != nil || res.KillSent {
				t.Fatalf("kill = %+v, %v; want kill_sent false, nil", res, err)
			}
			if n := len(e.rec.SocketCallsOf(tmux.CallKillPane)) + len(e.rec.SocketCallsOf(tmux.CallKillSession)); n != 0 {
				t.Errorf("pane or session kills = %d; want none", n)
			}
			kolAssertCalled(t, r.ID, map[string]any{"include_finished": true, "outcome": "ok", "kill_sent": false,
				"pane_killed": false})
		})
	}
}
