package main_test

// kill_optin_cli_test.go covers kill's operator-only --include-finished on a
// finished row through the built CLI (SR-6.5, SR-6.7): the same ended row is
// the plain no-op without the flag, its reported-in session is killed with it,
// and a row that never reported in is refused with no kill.

import (
	"encoding/json"
	"slices"
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

// finishedCLIRow is an ended row seeded with its own labelled session.
type finishedCLIRow struct {
	home, id, socket, name string
	sessionID              string // the session's tmux id
	before                 apitest.SpawnColumns
}

// seedFinishedWithSession seeds an ended row (pid above the pid limit, a session
// id) an hour past the default window, with its own session created an hour
// past the default bound before ended_at; its one pane is gone too.
func seedFinishedWithSession(t *testing.T, opts ...apitest.SpawnOption) finishedCLIRow {
	t.Helper()
	endedAt := time.Now().Add(-cliDefWindow - time.Hour).Truncate(time.Second)
	r := finishedCLIRow{home: t.TempDir(), sessionID: "$5"}
	r.socket = spawnSocket(t, r.home)
	opts = append([]apitest.SpawnOption{apitest.WithTmuxSocket(r.socket),
		apitest.WithPID(apitest.TestPanePID + 1), apitest.WithEndedAt(endedAt)}, opts...)
	id, err := apitest.SeedSpawn(stateDB(r.home), "", store.StateEnded, "", "", uuid.NewString(), true, opts...)
	if err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	r.id = id
	token, _, storeID := launchIdentity(t, r.home, id)
	r.before = rowColumns(t, r.home, id)
	r.name, _ = r.before.TmuxSessionName.(string)
	faketmuxfix.Tables{}.Write(t, r.socket, killTable(faketmuxfix.Session{
		ID: r.sessionID, Created: endedAt.Add(-cliDefBound - time.Hour).Unix(), Name: r.name,
		Label: tmuxfix.LabelValue(token, r.sessionID, id, storeID),
		Panes: []faketmuxfix.Pane{{ID: apitest.TestPaneID, PID: apitest.TestPanePID,
			AdPane: tmuxfix.PaneLabelValue(token, apitest.TestPaneID)}},
	}))
	return r
}

// sessionsLeft returns the sessions still in r's fake table.
func (r finishedCLIRow) sessionsLeft(t *testing.T) []faketmuxfix.Session {
	t.Helper()
	return faketmuxfix.Tables{}.Read(t, r.socket).Sessions
}

// assertGetState fails unless get on r's row exits 0 showing state want.
func (r finishedCLIRow) assertGetState(t *testing.T, fakeDir, want string) {
	t.Helper()
	stdout, stderr, code := runSpawnCLI(t, r.home, fakeDir, "get", "--claude-instance-id", r.id)
	var got struct {
		State string `json:"state"`
	}
	if code != 0 || json.Unmarshal([]byte(stdout), &got) != nil || got.State != want {
		t.Errorf("get exit = %d, stdout = %q (stderr %q); want state %s", code, stdout, stderr, want)
	}
}

// TestKillIncludeFinishedCLIReportedInSession: without the flag the ended row
// is the plain no-op; with it the row's own session is killed by ids.
func TestKillIncludeFinishedCLIReportedInSession(t *testing.T) {
	fakeDir := buildFakeTmux(t)

	t.Run("without the flag", func(t *testing.T) {
		r := seedFinishedWithSession(t)
		stdout, stderr, code := runSpawnCLI(t, r.home, fakeDir, "kill", "--claude-instance-id", r.id)
		if code != 0 || stderr != "" {
			t.Fatalf("kill exit = %d, stderr = %q; want 0 and empty", code, stderr)
		}
		assertKillSent(t, stdout, false)
		assertInvocationKinds(t, r.home)
		if left := r.sessionsLeft(t); len(left) != 1 {
			t.Errorf("sessions after kill = %+v; want the row's session untouched", left)
		}
		assertCLIRowUnchanged(t, r.home, r.id, r.before)
	})

	t.Run("with the flag", func(t *testing.T) {
		r := seedFinishedWithSession(t)
		stdout, stderr, code := runSpawnCLI(t, r.home, fakeDir, "kill", "--claude-instance-id", r.id, "--include-finished")
		if code != 0 || stderr != "" {
			t.Fatalf("kill exit = %d, stderr = %q; want 0 and empty", code, stderr)
		}
		assertKillSent(t, stdout, true)
		invs := assertInvocationKinds(t, r.home, "list-sessions", "list-panes", "kill-pane", "kill-session")
		wantKills := [][]string{
			{"-u", "-S", r.socket, "kill-pane", "-t", apitest.TestPaneID},
			{"-u", "-S", r.socket, "kill-session", "-t", r.sessionID},
		}
		for i, want := range wantKills {
			if got := invs[2+i]; !slices.Equal(got, want) {
				t.Errorf("kill invocation %d = %q; want %q", i, got, want)
			}
		}
		if left := r.sessionsLeft(t); len(left) != 0 {
			t.Errorf("sessions after kill = %+v; want none", left)
		}
		assertCLIRowUnchanged(t, r.home, r.id, r.before)
		r.assertGetState(t, fakeDir, store.StateEnded)
	})
}

// TestKillIncludeFinishedCLINeverReportedIn: the same row with no pid recorded
// exits 1 with only the "never reported in" envelope and sends no kill.
func TestKillIncludeFinishedCLINeverReportedIn(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	r := seedFinishedWithSession(t, apitest.WithNoPID())
	token, _, storeID := launchIdentity(t, r.home, r.id)

	stdout, stderr, code := runSpawnCLI(t, r.home, fakeDir, "kill", "--claude-instance-id", r.id, "--include-finished")
	env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrTmuxSessionConflict")
	apitest.AssertDescription(t, env.ErrDescription, apitest.DescKillOptInNeverReportedIn(r.id, r.name, cliDefBound),
		token, storeID)
	if m := cliOptInRe.FindString(stderr); m != "" {
		t.Errorf("SR-6.8: the refusal names the opt-in %q: %s", m, stderr)
	}
	assertInvocationKinds(t, r.home, "list-sessions")
	if left := r.sessionsLeft(t); len(left) != 1 {
		t.Errorf("sessions after kill = %+v; want the row's session untouched", left)
	}
	assertCLIRowUnchanged(t, r.home, r.id, r.before)
}
