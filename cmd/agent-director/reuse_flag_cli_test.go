package main_test

// reuse_flag_cli_test.go covers the spawn --reuse-finished flag through the
// built CLI (SR-10.1, AC-REUSE-13): accepted on spawn, no effect without an
// explicit id, no bypass of the control-character check, no change to a
// finished row's collision when absent or false, and rejected by
// make-template; with an explicit id, a finished row is reused, a live row
// collides and a leftover session refuses (SR-10.2).

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// seedFinishedCLIRow seeds a row in state under home's store and returns its
// id and columns.
func seedFinishedCLIRow(t *testing.T, home, state string) (string, apitest.SpawnColumns) {
	t.Helper()
	bootstrapDB(t, home)
	id := "id-reuse-" + uuid.NewString()[:8]
	if _, err := apitest.SeedSpawn(stateDB(home), id, state, "", "", uuid.NewString(), false); err != nil {
		t.Fatalf("SeedSpawn(%s): %v", state, err)
	}
	cols, err := apitest.ReadSpawnColumns(stateDB(home), id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns: %v", err)
	}
	return id, cols
}

// assertCLIRowUnchanged fails unless id's columns under home still equal before.
func assertCLIRowUnchanged(t *testing.T, home, id string, before apitest.SpawnColumns) {
	t.Helper()
	after, err := apitest.ReadSpawnColumns(stateDB(home), id)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Errorf("row %s = %+v (err %v); want unchanged %+v", id, after, err, before)
	}
}

// TestSpawnCLIReuseFinishedWithoutID: --reuse-finished with no id is an
// ordinary spawn: a minted id, one new pending row and one create, while a
// finished row with another id is unchanged.
func TestSpawnCLIReuseFinishedWithoutID(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	other, before := seedFinishedCLIRow(t, home, store.StateEnded)

	stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "spawn", "--cwd", t.TempDir(), "--reuse-finished")

	if code != 0 {
		t.Fatalf("exit = %d; stderr=%s", code, stderr)
	}
	var res spawnResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("parse stdout %q: %v", stdout, err)
	}
	if _, err := uuid.Parse(res.ClaudeInstanceID); err != nil {
		t.Errorf("minted id %q is not a UUID: %v", res.ClaudeInstanceID, err)
	}
	assertInvocationKinds(t, home, "new-session")
	if st := statusOf(t, home, fakeDir, res.ClaudeInstanceID); st != "pending" {
		t.Errorf("status = %q; want pending", st)
	}
	if ids := spawnRowIDs(t, home, fakeDir); len(ids) != 2 || !slices.Contains(ids, other) {
		t.Errorf("rows = %q; want %s and one new row", ids, other)
	}
	assertCLIRowUnchanged(t, home, other, before)
}

// TestSpawnCLIReuseFinishedControlCharacterID: with --reuse-finished a
// control-character id is still ErrInvalidFlags with the control-character
// description, and no row or tmux call results.
func TestSpawnCLIReuseFinishedControlCharacterID(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	const marker = "reusemark"
	for _, tc := range []struct{ name, ctl string }{
		{"newline", "\n"},
		{"tab", "\t"},
		{"DEL", "\x7f"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			id := marker + tc.ctl + "tail"

			_, stderr, code := runSpawnCLI(t, home, fakeDir,
				"spawn", "--cwd", t.TempDir(), "--claude-instance-id", id, "--reuse-finished")

			if code == 0 {
				t.Fatalf("exit = 0; want non-zero (stderr=%q)", stderr)
			}
			env := parseEnvelope(t, stderr)
			if env.ErrName != "ErrInvalidFlags" {
				t.Errorf("err_name = %q; want ErrInvalidFlags (stderr=%q)", env.ErrName, stderr)
			}
			if strings.Contains(env.ErrDescription, "not defined") {
				t.Errorf("err_description = %q; want the control-character text, not a flag-parsing error", env.ErrDescription)
			}
			apitest.AssertDescription(t, env.ErrDescription, apitest.DescInstanceIDControlChar(id), marker)
			assertInvocationKinds(t, home)
			if ids := spawnRowIDs(t, home, fakeDir); len(ids) != 0 {
				t.Errorf("rows = %q; want none", ids)
			}
		})
	}
}

// TestSpawnCLIFinishedRowCollidesWithoutReuse: with the flag absent or
// false, an ended or missing row is ErrInstanceIdCollision; the row is
// unchanged and no session is created.
func TestSpawnCLIFinishedRowCollidesWithoutReuse(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for _, state := range []string{store.StateEnded, store.StateMissing} {
		for _, form := range []struct{ name, flag string }{{"absent", ""}, {"false", "--reuse-finished=false"}} {
			t.Run(state+"/"+form.name, func(t *testing.T) {
				home := t.TempDir()
				id, before := seedFinishedCLIRow(t, home, state)
				args := []string{"spawn", "--cwd", t.TempDir(), "--claude-instance-id", id}
				if form.flag != "" {
					args = append(args, form.flag)
				}

				_, stderr, code := runSpawnCLI(t, home, fakeDir, args...)

				if code == 0 {
					t.Fatalf("exit = 0; want non-zero (stderr=%q)", stderr)
				}
				if env := parseEnvelope(t, lastJSONLine(stderr)); env.ErrName != "ErrInstanceIdCollision" {
					t.Errorf("err_name = %q; want ErrInstanceIdCollision (stderr=%q)", env.ErrName, stderr)
				}
				assertCLIRowUnchanged(t, home, id, before)
				for _, argv := range fakeTmuxInvocations(t, home) {
					if slices.Contains(argv, "new-session") {
						t.Errorf("fake-tmux logged a create: %q", argv)
					}
				}
			})
		}
	}
}

// reuseGet is the part of `get` the reuse cases read.
type reuseGet struct {
	State           string            `json:"state"`
	LaunchStartedAt *string           `json:"launch_started_at"`
	PriorSessions   []json.RawMessage `json:"prior_sessions"`
}

// getCLI runs `get` for id under home and returns its reuse fields.
func getCLI(t *testing.T, home, fakeDir, id string) reuseGet {
	t.Helper()
	stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "get", "--claude-instance-id", id)
	if code != 0 {
		t.Fatalf("get exit = %d; stderr=%q", code, stderr)
	}
	var g reuseGet
	if err := json.Unmarshal([]byte(stdout), &g); err != nil {
		t.Fatalf("parse get %q: %v", stdout, err)
	}
	return g
}

// seedReuseRow seeds a row in state with a session id on home's spawn socket,
// plus opts, and returns its id, columns, the socket and this store's id.
func seedReuseRow(t *testing.T, home, state string, opts ...apitest.SpawnOption) (id string, cols apitest.SpawnColumns, socket, storeID string) {
	t.Helper()
	bootstrapDB(t, home)
	socket = spawnSocket(t, home)
	id = "id-reuse-" + uuid.NewString()[:8]
	opts = append([]apitest.SpawnOption{apitest.WithTmuxSocket(socket)}, opts...)
	if _, err := apitest.SeedSpawn(stateDB(home), id, state, "", "", uuid.NewString(), false, opts...); err != nil {
		t.Fatalf("SeedSpawn(%s): %v", state, err)
	}
	cols, err := apitest.ReadSpawnColumns(stateDB(home), id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns: %v", err)
	}
	if storeID, err = apitest.ReadStoreID(stateDB(home)); err != nil {
		t.Fatalf("ReadStoreID: %v", err)
	}
	return id, cols, socket, storeID
}

// reuseSpawn runs `spawn --claude-instance-id id --reuse-finished` under home.
func reuseSpawn(t *testing.T, home, fakeDir, id string) (string, string, int) {
	t.Helper()
	return runSpawnCLI(t, home, fakeDir, "spawn", "--cwd", t.TempDir(), "--claude-instance-id", id, "--reuse-finished")
}

// TestSpawnReuseCLIFinishedRow: --reuse-finished over an ended or missing row
// with no session left succeeds; get shows pending, a launch start and no
// prior sessions (the user demo's step 4).
func TestSpawnReuseCLIFinishedRow(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for _, state := range []string{store.StateEnded, store.StateMissing} {
		t.Run(state, func(t *testing.T) {
			home := t.TempDir()
			id, _, _, _ := seedReuseRow(t, home, state, apitest.WithLifeNumber(1),
				apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: uuid.NewString(), Life: 1}))
			if g := getCLI(t, home, fakeDir, id); g.LaunchStartedAt != nil || len(g.PriorSessions) != 1 {
				t.Fatalf("seeded get = %+v; want no launch start and one prior session", g)
			}

			stdout, stderr, code := reuseSpawn(t, home, fakeDir, id)

			if code != 0 {
				t.Fatalf("exit = %d; stderr=%s", code, stderr)
			}
			var res spawnResult
			if err := json.Unmarshal([]byte(stdout), &res); err != nil || res.ClaudeInstanceID != id {
				t.Errorf("stdout = %q (%v); want a success JSON for %s", stdout, err, id)
			}
			g := getCLI(t, home, fakeDir, id)
			if g.State != store.StatePending || g.LaunchStartedAt == nil || len(g.PriorSessions) != 0 {
				t.Errorf("get = %+v; want pending, a launch start and no prior sessions", g)
			}
			assertInvocationKinds(t, home, "list-sessions", "new-session")
		})
	}
}

// TestSpawnReuseCLILiveRow: --reuse-finished over a live row (pending
// included) is ErrInstanceIdCollision with no tmux call; the row is unchanged.
func TestSpawnReuseCLILiveRow(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for _, state := range []string{store.StatePending, store.StateWaiting, store.StateWorking} {
		t.Run(state, func(t *testing.T) {
			home := t.TempDir()
			id, before, _, _ := seedReuseRow(t, home, state)

			_, stderr, code := reuseSpawn(t, home, fakeDir, id)

			if code == 0 {
				t.Fatalf("exit = 0; want non-zero (stderr=%q)", stderr)
			}
			if env := parseEnvelope(t, lastJSONLine(stderr)); env.ErrName != "ErrInstanceIdCollision" {
				t.Errorf("err_name = %q; want ErrInstanceIdCollision (stderr=%q)", env.ErrName, stderr)
			}
			assertInvocationKinds(t, home)
			assertCLIRowUnchanged(t, home, id, before)
		})
	}
}

// TestSpawnReuseCLILeftover: a session under the old name labelled with an
// earlier token of this row is ErrTmuxSessionConflict; nothing changes.
func TestSpawnReuseCLILeftover(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	id, before, socket, storeID := seedReuseRow(t, home, store.StateEnded)
	oldName, _ := before.TmuxSessionName.(string)
	writeHolders(t, socket, faketmuxfix.Session{
		ID: "$5", Created: heldCreated, Name: oldName, Label: tmuxfix.LabelValue(tmuxfix.OtherToken, "$5", id, storeID),
		Panes: []faketmuxfix.Pane{{ID: "%5", PID: os.Getpid()}},
	})
	sessions := faketmuxfix.Tables{}.Read(t, socket).Sessions

	_, stderr, code := reuseSpawn(t, home, fakeDir, id)

	if code == 0 {
		t.Fatalf("exit = 0; want non-zero (stderr=%q)", stderr)
	}
	env := parseEnvelope(t, lastJSONLine(stderr))
	if env.ErrName != "ErrTmuxSessionConflict" {
		t.Errorf("err_name = %q; want ErrTmuxSessionConflict (desc=%q)", env.ErrName, env.ErrDescription)
	}
	token, _ := before.LaunchToken.(string)
	apitest.AssertDescription(t, env.ErrDescription,
		apitest.DescPreLaunchLeftover(id, []apitest.DescSession{{Name: oldName, ID: "$5"}}), token, storeID, tmuxfix.OtherToken)
	assertCLIRowUnchanged(t, home, id, before)
	assertInvocationKinds(t, home, "list-sessions")
	if after := (faketmuxfix.Tables{}).Read(t, socket).Sessions; !reflect.DeepEqual(after, sessions) {
		t.Errorf("fake sessions changed:\nbefore=%+v\nafter =%+v", sessions, after)
	}
}

// TestMakeTemplateCLIRejectsReuseFinished: make-template has no
// --reuse-finished, so it is ErrInvalidFlags and no template is written; the
// same call without it writes one.
func TestMakeTemplateCLIRejectsReuseFinished(t *testing.T) {
	for _, tc := range []struct {
		name   string
		extra  []string
		reject bool
	}{
		{"with --reuse-finished", []string{"--reuse-finished"}, true},
		{"without it", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			name := "reuse-tmpl-" + uuid.NewString()[:8]

			_, stderr, code := runCLIWithHome(t, home, append([]string{"make-template", "--name", name}, tc.extra...)...)

			_, statErr := os.Stat(filepath.Join(directorDir(home), "templates", name+".toml"))
			if !tc.reject {
				if code != 0 || statErr != nil {
					t.Fatalf("exit = %d, template stat err = %v; want 0 and a template (stderr=%q)", code, statErr, stderr)
				}
				return
			}
			if code == 0 {
				t.Fatalf("exit = 0; want non-zero (stderr=%q)", stderr)
			}
			env := parseEnvelope(t, stderr)
			if env.ErrName != "ErrInvalidFlags" || !strings.Contains(env.ErrDescription, "reuse-finished") {
				t.Errorf("envelope = %+v; want ErrInvalidFlags naming reuse-finished", env)
			}
			if !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("template stat err = %v; want no template written", statErr)
			}
		})
	}
}
