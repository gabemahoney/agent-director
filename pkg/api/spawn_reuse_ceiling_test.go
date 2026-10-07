package api_test

// spawn_reuse_ceiling_test.go proves the reuse row of SR-13.2 in virtual time
// on the reuse fixture (AC-REUSE-16). The Recorder charges every call its full
// class timeout and no pipe-close wait W (SR-20.6 proves W per call
// elsewhere), so path (i), lookup, create, label by id and kill of the
// unlabelled session, is asserted as Q + C + 2A (SR-13.2's 10.9 s less its
// four W), and path (ii), lookup, create answering "duplicate session" and
// re-lookup, as 2Q + C (SR-13.2's 8.3 s less its three W).

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rclLimit is AC-REUSE-16's bound on an opted-in spawn's tmux time at the defaults.
const rclLimit = 15 * time.Second

// rclAssertRestored fails unless r's row is again every column as before
// (row_version aside) and the reuse wrote one ad.spawn.reuse_restored since.
func (e *killEnv) rclAssertRestored(t *testing.T, r reuseRow, before writesSnapshot) {
	t.Helper()
	got := e.columns(t, r.ID)
	if got.State != store.StateEnded || got.RowVersion == before.cols.RowVersion {
		t.Errorf("row {state %v, row_version %v}; want ended again, past %v", got.State, got.RowVersion, before.cols.RowVersion)
	}
	want := before.cols
	want.RowVersion = got.RowVersion
	if !reflect.DeepEqual(got, want) {
		t.Errorf("restored row =\n  %+v\nwant as before\n  %+v", got, want)
	}
	if n := len(before.since(t, "ad.spawn.reuse_restored")); n != 1 {
		t.Errorf("ad.spawn.reuse_restored lines = %d; want 1", n)
	}
}

// TestSpawnReuseCeilingPaths: path (i) (a create whose chained label fails,
// then the label and the kill by id fail) charges Q + C + 2A, 10.5 s at the
// defaults; path (ii) charges 2Q + C, 8 s, and is the longer once Q is
// raised above 2A. Each restores the row.
func TestSpawnReuseCeilingPaths(t *testing.T) {
	t.Parallel()
	q, a, _ := ceilDefaults()
	c := config.Default().Tmux.EffectiveCreateTimeout()
	if got := q + c + 2*a; got != 10500*time.Millisecond || got > rclLimit {
		t.Fatalf("Q + C + 2A at the defaults = %v; the Epic says 10.5 s, within %v", got, rclLimit)
	}
	if got := 2*q + c; got != 8*time.Second || got > rclLimit {
		t.Fatalf("2Q + C at the defaults = %v; the Epic says 8 s, within %v", got, rclLimit)
	}
	raised := config.Tmux{QueryTimeoutMs: 2*config.DefaultActionTimeoutMs + config.DefaultQueryTimeoutMs}
	rq, written := raised.EffectiveQueryTimeout(), []apitest.TmuxSetting{
		apitest.TmuxInt(config.TmuxQueryTimeoutMs, raised.QueryTimeoutMs)}
	if rq <= 2*a || 2*rq+c <= rq+c+2*a {
		t.Fatalf("raised Q = %v; want above 2A = %v, making path (ii) the longer", rq, 2*a)
	}
	cases := []struct {
		name   string
		q      time.Duration
		config []apitest.TmuxSetting // also written for the Client
		held   bool                  // path (ii); false: path (i)
	}{
		{"default Q/path i", q, nil, false},
		{"default Q/path ii", q, nil, true},
		{"Q above 2A/path i", rq, written, false},
		{"Q above 2A/path ii", rq, written, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			e.rec.WithVirtualTime(e.clock, tmux.Timeouts{Query: tc.q})
			r := e.seedReusable(t, agentGone, reuseRowSpec{Age: rceSettled(e), Held: tc.held})
			want, wantErr := tc.q+c+2*a, api.ErrTmuxSessionCreate
			calls := []tmux.Call{tmux.CallLookup, tmux.CallCreate, tmux.CallSetLabel, tmux.CallKillSession}
			if tc.held {
				want, wantErr, calls = 2*tc.q+c, api.ErrTmuxSessionConflict, []tmux.Call{tmux.CallLookup, tmux.CallCreate, tmux.CallLookup}
				e.arrangeHeld(t, r.resumeRow, heldSpec{Holder: holderForeign})
			} else {
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailLabel, Times: 1}, tmux.CallCreate).
					Script(r.Socket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}, tmux.CallSetLabel, tmux.CallKillSession)
			}
			p, before := reuseParams(t, r, reuseRequest{}), e.snapshotReuse(t, r)
			start := e.clock.Now()

			_, _, err := e.reuse(t, p, tc.config...)

			if elapsed := e.clock.Now().Sub(start); elapsed != want {
				t.Errorf("virtual time = %v; want %v", elapsed, want)
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("err = %v; want %v", err, wantErr)
			}
			e.assertKillCalls(t, calls...)
			e.rclAssertRestored(t, r, before)
		})
	}
}

// TestSpawnReuseCeilingRefusalChargesOnlyLookup: a refusal at the lookup
// charges Q and makes no further call; a live row's refusal charges nothing
// and makes no call.
func TestSpawnReuseCeilingRefusalChargesOnlyLookup(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	q, _, _ := ceilDefaults()
	cases := []struct {
		name   string
		live   bool       // a pending row a reuse launched; else a finished row
		agent  agentState // a finished row's old agent
		setup  func(*testing.T, *killEnv, *reuseRow, *reuseRequest)
		charge time.Duration
		want   error
	}{
		{name: "leftover", agent: agentGone, charge: q, want: api.ErrTmuxSessionConflict,
			setup: func(t *testing.T, e *killEnv, r *reuseRow, _ *reuseRequest) { e.seedHolder(t, r.killRow, holderOld) }},
		{name: "requested name held by another row", agent: agentGone, charge: q, want: api.ErrTmuxSessionConflict,
			setup: func(t *testing.T, e *killEnv, r *reuseRow, req *reuseRequest) {
				req.Name = r.ID + "-new"
				e.seedHolder(t, r.withName(req.Name), holderForeign)
			}},
		{name: "own session past the bound", agent: agentGone, charge: q, want: api.ErrTmuxSessionConflict,
			setup: func(t *testing.T, e *killEnv, r *reuseRow, _ *reuseRequest) {
				e.seedSession(t, &r.killRow, e.createdBefore(rceSettled(e)))
			}},
		{name: "agent still stopping", agent: agentAlive, charge: q, want: api.ErrTmuxUnresponsive},
		{name: "live row", live: true, want: spawn.ErrInstanceIdCollision},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			var r reuseRow
			var req reuseRequest
			var before writesSnapshot
			if tc.live {
				r = e.reusePending(t, agentAlive, reuseRowSpec{Age: rceSettled(e)}, req)
				before = e.snapshotWrites(t, "spawn", r.ID, trustConfig{}, r.Socket)
			} else {
				age := rceSettled(e)
				if tc.agent == agentAlive {
					age = 0
				}
				r = e.seedReusable(t, tc.agent, reuseRowSpec{Age: age})
				if tc.setup != nil {
					tc.setup(t, e, &r, &req)
				}
				before = e.snapshotReuse(t, r)
			}
			p := reuseParams(t, r, req)
			start := e.clock.Now()

			_, _, err := e.reuse(t, p)

			if elapsed := e.clock.Now().Sub(start); elapsed != tc.charge {
				t.Errorf("virtual time = %v; want %v", elapsed, tc.charge)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v; want %v", err, tc.want)
			}
			wantCalls := 1
			if tc.live {
				wantCalls = 0
			}
			if got := e.rec.SocketCalls()[before.calls:]; len(got) != wantCalls {
				t.Errorf("tmux calls = %+v; want %d (a lookup)", got, wantCalls)
			}
			e.assertWroteNothing(t, before)
		})
	}
}
