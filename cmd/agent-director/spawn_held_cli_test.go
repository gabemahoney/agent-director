package main_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// A plain spawn whose create answers "duplicate session" through the built CLI
// (SR-9.4, SR-3.10): the new row is ended and the holder left alone. The
// holder's classification and ad.launch.name_held are pkg/api's
// spawn_held_*_test.go and test/envelope-diff's held spawn row; the helpers
// here also serve advice_follow_cli_test.go's I4 follow.

// heldName is the requested session name every held-name case spawns under.
const heldName = "w13-held"

// heldCreated is the holder sessions' #{session_created}.
const heldCreated = 1790549182

// heldHome bootstraps home's store and returns the launch socket and this
// store's id.
func heldHome(t *testing.T, home string) (socket, storeID string) {
	t.Helper()
	bootstrapDB(t, home)
	storeID, err := apitest.ReadStoreID(stateDB(home))
	if err != nil {
		t.Fatalf("ReadStoreID: %v", err)
	}
	return spawnSocket(t, home), storeID
}

// writeHolders makes sessions the fake's table on socket, under a running
// server.
func writeHolders(t *testing.T, socket string, sessions ...faketmuxfix.Session) {
	t.Helper()
	faketmuxfix.Tables{}.Write(t, socket, faketmuxfix.Table{
		Server:   &faketmuxfix.Server{PID: os.Getpid(), Start: heldCreated},
		Sessions: sessions,
	})
}

// holderSession is a session named heldName with id sessionID and label
// ("" = unset), whose one pane is this test process.
func holderSession(sessionID, label string) faketmuxfix.Session {
	return faketmuxfix.Session{
		ID: sessionID, Created: heldCreated, Name: heldName, Label: label,
		Panes: []faketmuxfix.Pane{{ID: "%" + strings.TrimPrefix(sessionID, "$"), PID: os.Getpid()}},
	}
}

// spawnHeld runs a minted-id plain spawn of heldName under home, requires exit
// 1 with no ErrTmuxSessionNameTaken anywhere, and returns the envelope.
func spawnHeld(t *testing.T, home, fakeDir string, extraEnv map[string]string) errorEnvelope {
	t.Helper()
	_, stderr, code := runSpawnCLIEnv(t, home, fakeDir, extraEnv,
		"spawn", "--cwd", t.TempDir(), "--tmux-session-name", heldName, "--no-pre-trust")
	if code != 1 {
		t.Fatalf("exit = %d; want 1 (stderr=%q)", code, stderr)
	}
	if strings.Contains(stderr, "ErrTmuxSessionNameTaken") {
		t.Errorf("held name surfaced ErrTmuxSessionNameTaken: %q", stderr)
	}
	return parseEnvelope(t, lastJSONLine(stderr))
}

// heldRowID returns the id of the one row under home: the held spawn's.
func heldRowID(t *testing.T, home, fakeDir string) string {
	t.Helper()
	ids := listIDs(t, home, fakeDir)
	if len(ids) != 1 {
		t.Fatalf("rows = %q; want the one row the spawn inserted", ids)
	}
	return ids[0]
}

// assertHeldRowEnded fails unless id's row was ended by the end write: status
// ended, ended_at set, no launch start, row_version 1 (Appendix F.4).
func assertHeldRowEnded(t *testing.T, home, fakeDir, id string) {
	t.Helper()
	if st := statusOf(t, home, fakeDir, id); st != string(store.StateEnded) {
		t.Errorf("status = %q; want %s", st, store.StateEnded)
	}
	cols, err := apitest.ReadSpawnColumns(stateDB(home), id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns: %v", err)
	}
	if cols.EndedAt == nil || cols.LaunchStartedAt != nil || fmt.Sprint(cols.RowVersion) != "1" {
		t.Errorf("ended_at = %v, launch_started_at = %v, row_version = %v; want set, NULL, 1",
			cols.EndedAt, cols.LaunchStartedAt, cols.RowVersion)
	}
}

// TestSpawnCLIHeldNameEndedSticks: after the held spawn ends its row, the
// leftover's hooks leave it ended, each writing one no_pane_recorded ignore.
func TestSpawnCLIHeldNameEndedSticks(t *testing.T) {
	h := gateHome{home: t.TempDir(), fakeDir: buildFakeTmux(t)}
	socket, _ := heldHome(t, h.home)
	writeHolders(t, socket, holderSession("$7", ""))
	env := spawnHeld(t, h.home, h.fakeDir, nil)
	id := heldRowID(t, h.home, h.fakeDir)
	if env.ErrName != "ErrTmuxSessionConflict" {
		t.Fatalf("err_name = %q; want ErrTmuxSessionConflict (desc=%q)", env.ErrName, env.ErrDescription)
	}
	if st := h.row(t, id).State; st != store.StateEnded {
		t.Fatalf("row state after held spawn = %q; want %s", st, store.StateEnded)
	}
	// The hook path makes no tmux call; start its log empty.
	if err := os.Remove(filepath.Join(h.home, "fake-tmux.log")); err != nil {
		t.Fatalf("clear fake-tmux log: %v", err)
	}

	transcript := h.transcript(t, "held-leftover-uuid")
	hooks := []struct{ event, payload string }{
		{"Stop", `{"hook_event_name":"Stop"}`},
		{"SessionStart", `{"hook_event_name":"SessionStart","source":"startup","transcript_path":"` + transcript + `"}`},
	}
	for i, hk := range hooks {
		if out := h.hook(t, id, "", hk.payload); out != "" {
			t.Errorf("%s stdout = %q; want empty", hk.event, out)
		}
		sp := h.row(t, id)
		if sp.State != store.StateEnded || sp.ClaudeSessionID != "" {
			t.Errorf("after %s: state = %q, session = %q; want %s and none", hk.event, sp.State, sp.ClaudeSessionID, store.StateEnded)
		}
		ign := eventsOf(readTrailLines(t, h.home), "ad.hook.ignored")
		if len(ign) != i+1 {
			t.Fatalf("after %s: ad.hook.ignored lines = %d; want %d: %v", hk.event, len(ign), i+1, ign)
		}
		got := ign[i]
		if got["claude_instance_id"] != id || got["hook_event"] != hk.event || got["reason"] != store.HookReasonNoPaneRecorded {
			t.Errorf("ad.hook.ignored = %v; want %s, %s, %s", got, id, hk.event, store.HookReasonNoPaneRecorded)
		}
	}
}
