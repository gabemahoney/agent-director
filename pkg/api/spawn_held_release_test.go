package api_test

// spawn_held_release_test.go: plain spawn's held-name path ends its launch's hold on the row (b.kdf, b.146 rule
// 11) when its end write after "duplicate session" does not apply or fails, so a row left pending is not held for
// the life of the caller. The path's other outcomes are spawn_held_test.go's.

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestSpawnHeldNameReleasesTheHold: after "duplicate session", an end write that another versioned write kept from
// applying, or that failed in the store, leaves the row pending with no launch owner recorded (one version more
// than the end write left); it was this process during the create. The release logs nothing.
func TestSpawnHeldNameReleasesTheHold(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	cases := []struct {
		name     string
		atCreate func(t *testing.T, e heldEnv, id string) // as the create returns, before the end write
		version  int64                                    // row_version after the spawn
	}{
		{"end write not applied", func(t *testing.T, e heldEnv, id string) {
			apitest.SeedSessionID(t, e.dbPath, id, uuid.NewString())
		}, 2},
		{"end write failed", func(t *testing.T, e heldEnv, id string) {
			storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailReuseRestore, id)
		}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newHeldEnv(t)
			e.pc.Set(os.Getpid(), procfix.Alive(fmStart))
			e.rec.SeedSessions(e.socket, heldHolder("$4", tmux.Label{}, false))
			id := heldID()
			var during any
			e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) {
				during = e.readRow(t, id).LaunchOwnerPID
				tc.atCreate(t, e, id)
			})

			run := e.spawnHeld(t, id, heldName, nil)

			if !errors.Is(run.err, api.ErrTmuxSessionConflict) {
				t.Fatalf("Spawn = %v; want ErrTmuxSessionConflict", run.err)
			}
			if during != int64(os.Getpid()) {
				t.Errorf("owner during the create = %v; want this process, %d", during, os.Getpid())
			}
			c := e.readRow(t, id)
			if c.State != store.StatePending || c.RowVersion != tc.version || c.LaunchOwnerPID != nil ||
				c.LaunchOwnerStarttime != nil || c.LaunchOwnerPIDNS != nil {
				t.Errorf("row = state %v, row_version %v, owner %v %v %v; want pending, %d, its hold released (NULL)",
					c.State, c.RowVersion, c.LaunchOwnerPID, c.LaunchOwnerStarttime, c.LaunchOwnerPIDNS, tc.version)
			}
			if strings.Contains(e.logs.String(), "launch hold") {
				t.Errorf("log = %q; want no release failure", e.logs.String())
			}
		})
	}
}
