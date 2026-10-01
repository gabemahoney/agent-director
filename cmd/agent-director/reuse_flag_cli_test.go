package main_test

// reuse_flag_cli_test.go covers the spawn --reuse-finished flag at the
// parameter level (SR-10.1, AC-REUSE-13): accepted on spawn, no effect without
// an explicit id, no bypass of the control-character check, no change to a
// finished row's collision when absent or false, and rejected by make-template.

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
