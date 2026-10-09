package api_test

// launch_owner_release_test.go: a resume or reuse whose launch ends without its identity write applied and without
// its restore applied leaves its row pending, and ends its hold on it (b.kdf, b.146 rule 11), so find-missing can
// judge the row by its grace period while the launching process lives on. The other release paths (a lost reply, a
// timeout) are find_missing_launch_owner_test.go's; plain spawn's are internal/spawn's, and its held-name path's
// spawn_held_release_test.go's.

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestLaunchReleasesItsHold: tmux unavailable at the create with the restore failing in the store, or a labelled
// create whose identity write fails in the store (one WARN line, the launch succeeding), leave the row pending with
// no launch owner recorded; it was this process during the launch.
func TestLaunchReleasesItsHold(t *testing.T) {
	t.Parallel()
	selfPIDNamespace(t)
	// launch runs the verb on its seeded row; restoreFails makes the restore fail, else the identity write fails.
	type launch func(t *testing.T, e *killEnv, restoreFails bool) (id, logs string, err error)
	paths := []struct {
		name string
		run  launch
	}{
		{"resume", func(t *testing.T, e *killEnv, restoreFails bool) (string, string, error) {
			r := e.seedResumable(t, rlkSettled(e), agentGone)
			if restoreFails {
				s := &hookedResumeStore{st: e.st}
				s.failRestore(nil)
				_, err := e.resumeWith(s, r.ID)
				return r.ID, "", err
			}
			storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailLaunchIdentity, r.ID)
			_, logs, err := e.resumeClient(t, r.ID)
			return r.ID, logs, err
		}},
		{"reuse", func(t *testing.T, e *killEnv, restoreFails bool) (string, string, error) {
			r := e.seedReusable(t, agentGone, reuseRowSpec{Age: rlkSettled(e)})
			kind := storefix.WriteFailLaunchIdentity
			if restoreFails {
				kind = storefix.WriteFailReuseRestore
			}
			storefix.InjectWriteFailure(t, e.dbPath, kind, r.ID)
			_, logs, err := e.reuse(t, reuseParams(t, r, reuseRequest{}))
			return r.ID, logs, err
		}},
	}
	ends := []struct {
		name         string
		restoreFails bool
		wantErr      error
	}{
		{"create failed, restore failed", true, api.ErrTmuxNotAvailable},
		{"identity write failed", false, nil},
	}
	for _, p := range paths {
		for _, end := range ends {
			t.Run(p.name+"/"+end.name, func(t *testing.T) {
				t.Parallel() // its own store and Recorder
				e := newKillEnv(t)
				e.pc.Set(os.Getpid(), procfix.Alive(fmStart))
				if end.restoreFails {
					e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnavailable, Times: 1}, tmux.CallCreate)
				}
				var during any
				adviceOnceAfter(e.rec, tmux.CallCreate, func() { during = e.columns(t, e.lastCreated(t)).LaunchOwnerPID })

				id, logs, err := p.run(t, e, end.restoreFails)

				if !errors.Is(err, end.wantErr) {
					t.Fatalf("%s = %v; want %v", p.name, err, end.wantErr)
				}
				if during != int64(os.Getpid()) {
					t.Errorf("owner during the create = %v; want this process, %d", during, os.Getpid())
				}
				c := e.columns(t, id)
				if c.State != store.StatePending || c.LaunchOwnerPID != nil || c.LaunchOwnerStarttime != nil || c.LaunchOwnerPIDNS != nil {
					t.Errorf("row = state %v, owner %v %v %v; want pending, its hold released (NULL)", c.State,
						c.LaunchOwnerPID, c.LaunchOwnerStarttime, c.LaunchOwnerPIDNS)
				}
				if end.restoreFails {
					return
				}
				if c.PanePID != nil {
					t.Errorf("pane_pid = %v; want none recorded (the identity write failed)", c.PanePID)
				}
				lines := strings.Split(strings.TrimSpace(logs), "\n")
				if len(lines) != 1 {
					t.Fatalf("client log lines = %q; want exactly one", lines)
				}
				apitest.AssertDescription(t, lines[0], apitest.DescIdentityWriteWarn(id))
			})
		}
	}
}

// lastCreated is the instance id the last create labelled.
func (e *killEnv) lastCreated(t *testing.T) string {
	t.Helper()
	creates := e.rec.SocketCallsOf(tmux.CallCreate)
	if len(creates) == 0 {
		t.Fatal("no create made")
	}
	return creates[len(creates)-1].InstanceID
}
