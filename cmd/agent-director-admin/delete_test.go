package main_test

// delete_test.go covers agent-director-admin delete (b.vqr): rows removed by
// id with one result per id, a failure on one never aborting the batch, no
// tmux call, and the store opened as agent-director opens it.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"testing"
	"time"

	_ "modernc.org/sqlite" // raw driver access to re-stamp the schema version

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestDeletePerRowResults: a live row with its own session, an unknown id and
// an ended row in one call give ok, ErrSpawnNotFound and ok; both rows are gone
// for agent-director too, and the live row's session is left running.
func TestDeletePerRowResults(t *testing.T) {
	home, live, socket := seedRow(t, store.StateWaiting, "")
	ownSession(t, home, live, socket, time.Now())
	ended, err := apitest.SeedSpawn(stateDB(home), "", store.StateEnded, "", "", "", false)
	if err != nil {
		t.Fatalf("seed ended row: %v", err)
	}

	stdout, stderr, code := runAdmin(t, home, "delete",
		"--claude-instance-id", live, "--claude-instance-id", "absent", "--claude-instance-id", ended)

	if code != 0 || stderr != "" {
		t.Fatalf("delete exit = %d, stderr = %q; want 0 and empty", code, stderr)
	}
	var res struct {
		Results map[string]string `json:"results"`
	}
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("parse stdout %q: %v", stdout, err)
	}
	if want := map[string]string{live: "ok", "absent": "ErrSpawnNotFound", ended: "ok"}; !maps.Equal(res.Results, want) {
		t.Errorf("results = %v; want %v", res.Results, want)
	}
	for _, id := range []string{live, ended} {
		assertRowGone(t, home, id)
		stdout, stderr, code := runMain(t, home, "get", "--claude-instance-id", id)
		assertOnlyEnvelope(t, stdout, stderr, code, "ErrSpawnNotFound")
	}
	assertInvocationKinds(t, home)
	if left := sessionsLeft(t, socket); len(left) != 1 {
		t.Errorf("sessions after delete = %+v; want the live row's session left running", left)
	}
}

// TestAdminRefusesUnmigratedStore: on a store stamped at an older schema
// version, kill-finished refuses with ErrSchemaMigrationRequired, as
// agent-director does, and kills nothing (delete opens the store through the
// same runOnClient).
func TestAdminRefusesUnmigratedStore(t *testing.T) {
	r := seedFinishedWithSession(t)
	current := stampUserVersion(t, r.home, 1)

	stdout, stderr, code := runAdmin(t, r.home, "kill-finished", "--claude-instance-id", r.id)

	assertOnlyEnvelope(t, stdout, stderr, code, "ErrSchemaMigrationRequired")
	assertInvocationKinds(t, r.home)
	if left := sessionsLeft(t, r.socket); len(left) != 1 {
		t.Errorf("sessions after kill-finished = %+v; want the row's session untouched", left)
	}
	stampUserVersion(t, r.home, current)
	assertRowUnchanged(t, r.home, r.id, r.before)
}

// stampUserVersion sets the store under home to schema version v and returns
// the version it had.
func stampUserVersion(t *testing.T, home string, v int) int {
	t.Helper()
	db, err := sql.Open("sqlite", stateDB(home))
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer db.Close()
	var old int
	if err := db.QueryRow("PRAGMA user_version").Scan(&old); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", v)); err != nil {
		t.Fatalf("stamp user_version=%d: %v", v, err)
	}
	return old
}
