package api_test

// find_missing_launch_owner_test.go: find-missing and a pending row's launch owner (b.kdf, b.146 rules 11 and
// 14): the sweep does not judge a row whose owner is provably alive, on the fake store and during each real
// launch's create (plain spawn, resume, reuse), and the launch releases its hold when it ends.

import (
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// withOwner records o as the row's launch owner.
func withOwner(o store.LaunchOwner) fmRowOpt {
	return func(r *store.LiveSpawnIdentity) { r.LaunchOwner = o }
}

// selfPIDNamespace is this process's pid namespace, skipping t where it cannot be read.
func selfPIDNamespace(t *testing.T) string {
	t.Helper()
	ns, known := probe.SelfPIDNamespace()
	if !known {
		t.Skip("this process's pid namespace cannot be read here")
	}
	return ns
}

// TestFindMissingLaunchOwnerHoldsPendingRow: a pending row past its grace period whose launch owner is alive in
// this process's pid namespace is not judged: no write, no tmux call, only the owner's start time read. An owner
// gone leaves the row judged as before (noted unreported, or marked); a live row's recorded owner is never read.
// The owner's other verdicts are internal/spawn's TestLaunchOwnerAlive.
func TestFindMissingLaunchOwnerHoldsPendingRow(t *testing.T) {
	t.Parallel()
	ns := selfPIDNamespace(t)
	const ownerPID, panePID = 1701, 1702
	owner := store.LaunchOwner{PID: ownerPID, Starttime: fmStart, PIDNamespace: ns}
	pending := withLaunch(store.StatePending, fpPastGrace)
	rows := []struct {
		name string
		opts []fmRowOpt
		pane procfix.Process
		ops  []string // the writes when judged
	}{
		{"pending, agent alive", []fmRowOpt{pending, withPane(panePID, fmStart)}, procfix.Alive(fmStart), []string{"unreported"}},
		{"pending, agent gone", []fmRowOpt{pending, withPane(panePID, fmStart)}, procfix.Gone(), []string{"mark"}},
		{"pending, no pane, session gone", []fmRowOpt{pending}, procfix.Gone(), []string{"mark"}},
		{"working, agent gone", []fmRowOpt{withPane(panePID, fmStart)}, procfix.Gone(), []string{"mark"}},
	}
	for _, r := range rows {
		for _, alive := range []bool{true, false} {
			t.Run(r.name+"/"+map[bool]string{true: "owner alive", false: "owner gone"}[alive], func(t *testing.T) {
				t.Parallel() // its own fake store; no trail check
				pc := procfix.New()
				pc.Set(ownerPID, map[bool]procfix.Process{true: procfix.Alive(fmStart), false: procfix.Gone()}[alive])
				pc.Set(panePID, r.pane)
				row := liveRow("lo-row", append(slices.Clone(r.opts), withOwner(owner))...)
				st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{row}}
				rec := tmuxfix.NewRecorder()

				mustSweep(t, st, pc, fmSweep{tmux: rec})

				held := alive && row.State == store.StatePending
				wantOps := r.ops
				if held {
					wantOps = nil
				}
				if got := st.ops("lo-row"); !equalStrings(got, wantOps) {
					t.Errorf("writes = %v; want %v", got, wantOps)
				}
				if held && (len(rec.SocketCalls()) != 0 || !slices.Equal(pc.StartTimeCalls(), []int{ownerPID})) {
					t.Errorf("tmux calls %+v, start-time reads %v; want none, and only the owner's", rec.SocketCalls(), pc.StartTimeCalls())
				}
				if row.State != store.StatePending && slices.Contains(pc.StartTimeCalls(), ownerPID) {
					t.Errorf("start-time reads = %v; want the live row's owner never read", pc.StartTimeCalls())
				}
			})
		}
	}
}

// TestFindMissingSparesALaunchInProgress: during each launch's create (a plain spawn's, a resume's, a reuse's), a
// sweep past the pending grace period leaves the row as it is while its launch owner, this process, is alive,
// whether its agent runs (it would be noted unreported) or its create made nothing (it would be marked missing),
// and the launch releases its hold when it ends. With the owner gone the same sweep judges the row.
func TestFindMissingSparesALaunchInProgress(t *testing.T) {
	t.Parallel()
	ns := selfPIDNamespace(t)
	paths := []struct {
		name   string
		launch func(t *testing.T, e *killEnv) (id string, run func() error)
	}{
		{"spawn", func(t *testing.T, e *killEnv) (string, func() error) {
			id := "own-spawn-" + uuid.NewString()[:8]
			c, _ := e.client(t)
			return id, func() error { _, err := c.Spawn(fgcSpawnParams(t, id, false)); return err }
		}},
		{"resume", func(t *testing.T, e *killEnv) (string, func() error) {
			r := e.seedResumable(t, rlkSettled(e), agentGone)
			return r.ID, func() error { _, err := e.resume(r.ID); return err }
		}},
		{"reuse", func(t *testing.T, e *killEnv) (string, func() error) {
			r := e.seedReusable(t, agentGone, reuseRowSpec{Age: rlkSettled(e)})
			return r.ID, func() error { _, _, err := e.reuse(t, reuseParams(t, r, reuseRequest{})); return err }
		}},
	}
	creates := []struct {
		name       string
		script     tmuxfix.Script
		judged     string // the row's state after the sweep when the owner is gone
		judgedNote any    // and its liveness note (nil: none)
		wantErr    error
	}{
		{"lost reply, agent running", tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 0, Applied: true, Times: 1},
			store.StatePending, "unreported", nil},
		{"create timed out", tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}, store.StateMissing, nil, api.ErrTmuxUnresponsive},
	}
	for _, p := range paths {
		for _, c := range creates {
			for _, ownerAlive := range []bool{true, false} {
				name := p.name + "/" + c.name + "/" + map[bool]string{true: "owner alive", false: "owner gone"}[ownerAlive]
				t.Run(name, func(t *testing.T) {
					t.Parallel() // its own store, Recorder and ids
					e := newKillEnv(t)
					e.pc.Set(os.Getpid(), procfix.Alive(fmStart))
					id, run := p.launch(t, e)
					e.rec.Script(tmuxfix.AnySocket, c.script, tmux.CallCreate)
					var (
						during, swept apitest.SpawnColumns
						res           api.FindMissingResult
						sweepErr      error
						ran           bool
					)
					e.rec.AfterCall(tmux.CallCreate, func(sc tmuxfix.SocketCall, _ error) {
						if ran || sc.InstanceID != id {
							return
						}
						ran = true
						for _, s := range e.rec.Sessions(sc.Socket) {
							if s.Label.Token == sc.Token && len(s.Panes) > 0 {
								e.pc.Set(s.Panes[0].PID, procfix.Alive(fmStart))
							}
						}
						if !ownerAlive {
							e.pc.Set(os.Getpid(), procfix.Gone())
						}
						during = e.columns(t, id)
						e.clock.Advance(fmGrace + 5*time.Second)
						res, sweepErr = api.FindMissing(context.Background(), e.st, e.rec, e.pc, fmGrace, fmBudget, e.clock.Now,
							&recordingLogger{})
						swept = e.columns(t, id)
					})

					err := run()

					if !ran || sweepErr != nil {
						t.Fatalf("sweep during the create ran %v, err %v; want it run", ran, sweepErr)
					}
					if want := []any{int64(os.Getpid()), fmStart, ns}; !reflect.DeepEqual(
						[]any{during.LaunchOwnerPID, during.LaunchOwnerStarttime, during.LaunchOwnerPIDNS}, want) || during.State != store.StatePending {
						t.Fatalf("row during the create = state %v, owner %v %v %v; want pending, owned by %v",
							during.State, during.LaunchOwnerPID, during.LaunchOwnerStarttime, during.LaunchOwnerPIDNS, want)
					}
					if !errors.Is(err, c.wantErr) {
						t.Errorf("%s = %v; want %v", p.name, err, c.wantErr)
					}
					after := e.columns(t, id)
					if !ownerAlive {
						if swept.State != c.judged || after.State != c.judged || swept.LivenessNote != c.judgedNote {
							t.Errorf("state after the sweep, after the launch, note = %v, %v, %v; want %s, note %v judged",
								swept.State, after.State, swept.LivenessNote, c.judged, c.judgedNote)
						}
						return
					}
					if !reflect.DeepEqual(swept, during) || len(res.IDs) != 0 || len(res.UnverifiedIDs) != 0 {
						t.Errorf("the sweep touched the held row (listed %v %v):\n before %+v\n after  %+v",
							res.IDs, res.UnverifiedIDs, during, swept)
					}
					if after.State != store.StatePending || after.LaunchOwnerPID != nil || after.LaunchOwnerStarttime != nil ||
						after.LaunchOwnerPIDNS != nil {
						t.Errorf("row after the launch = state %v, owner %v %v %v; want pending, its hold released (NULL)",
							after.State, after.LaunchOwnerPID, after.LaunchOwnerStarttime, after.LaunchOwnerPIDNS)
					}
				})
			}
		}
	}
}
