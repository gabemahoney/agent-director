package main_test

// kill_finished_row_cli_test.go covers kill on an ended row through the built
// CLI, bare or beside the row's own reported-in session: a no-op success with
// kill_sent false and no tmux call (SR-6.1), which CSCB relies on. Ending that
// session is agent-director-admin's kill-finished only (b.vqr), tested in
// cmd/agent-director-admin.

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The default stopping window and starting-session bound the CLI's kill uses.
var (
	cliDefWindow = time.Duration(config.DefaultStoppingWindowSeconds) * time.Second
	cliDefBound  = time.Duration(config.DefaultStartingSessionSeconds) * time.Second
)

// cliRow is a row seeded under its own HOME and socket, with its columns as
// seeded.
type cliRow struct {
	home, id, socket string
	before           apitest.SpawnColumns
}

// seedWithOwnSession seeds a row in state with a session id and its own
// labelled session, created at created, the only session on its socket.
func seedWithOwnSession(t *testing.T, state string, created time.Time, opts ...apitest.SpawnOption) cliRow {
	t.Helper()
	r := cliRow{home: t.TempDir()}
	r.socket = spawnSocket(t, r.home)
	id, err := apitest.SeedSpawn(stateDB(r.home), "", state, "", "", uuid.NewString(), true,
		append([]apitest.SpawnOption{apitest.WithTmuxSocket(r.socket)}, opts...)...)
	if err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	r.id = id
	token, _, storeID := launchIdentity(t, r.home, id)
	r.before = rowColumns(t, r.home, id)
	name, _ := r.before.TmuxSessionName.(string)
	const sessionID = "$5"
	faketmuxfix.Tables{}.Write(t, r.socket, killTable(faketmuxfix.Session{
		ID: sessionID, Created: created.Unix(), Name: name,
		Label: tmuxfix.LabelValue(token, sessionID, id, storeID),
		Panes: []faketmuxfix.Pane{{ID: apitest.TestPaneID, PID: apitest.TestPanePID,
			AdPane: tmuxfix.PaneLabelValue(token, apitest.TestPaneID)}},
	}))
	return r
}

// seedFinishedWithSession seeds an ended row (pid above the pid limit) an hour
// past the default window, with its own session created an hour past the
// default bound before ended_at, the session the admin kill-finished would end.
func seedFinishedWithSession(t *testing.T) cliRow {
	t.Helper()
	endedAt := time.Now().Add(-cliDefWindow - time.Hour).Truncate(time.Second)
	return seedWithOwnSession(t, store.StateEnded, endedAt.Add(-cliDefBound-time.Hour),
		apitest.WithPID(apitest.TestPanePID+1), apitest.WithEndedAt(endedAt))
}

// TestKillCLIEndedRowIsNoop: kill on an ended row, bare or beside its own
// reported-in session, exits 0 with kill_sent false, makes no tmux call and
// leaves the row and the session as they were (SR-6.1).
func TestKillCLIEndedRowIsNoop(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	cases := []struct {
		name         string
		seed         func(t *testing.T) cliRow
		wantSessions int
	}{
		{"bare ended row", func(t *testing.T) cliRow {
			home, id, socket := seedKillRow(t, store.StateEnded)
			return cliRow{home: home, id: id, socket: socket, before: rowColumns(t, home, id)}
		}, 0},
		{"ended row with its own reported-in session", seedFinishedWithSession, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.seed(t)

			stdout, stderr, code := runSpawnCLI(t, r.home, fakeDir, "kill", "--claude-instance-id", r.id)

			if code != 0 || stderr != "" {
				t.Fatalf("kill exit = %d, stderr = %q; want 0 and empty", code, stderr)
			}
			assertKillSent(t, stdout, false)
			assertInvocationKinds(t, r.home)
			if left := (faketmuxfix.Tables{}).Read(t, r.socket).Sessions; len(left) != tc.wantSessions {
				t.Errorf("sessions after kill = %+v; want the %d seeded, untouched", left, tc.wantSessions)
			}
			assertCLIRowUnchanged(t, r.home, r.id, r.before)
		})
	}
}
