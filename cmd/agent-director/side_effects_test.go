package main_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite" // raw driver access for the tamper helper
)

// directorDir returns the path to the .agent-director directory under home.
func directorDir(home string) string {
	return filepath.Join(home, ".agent-director")
}

// stateDB returns the path to state.db under home.
func stateDB(home string) string {
	return filepath.Join(directorDir(home), "state.db")
}

// statMode returns the permission bits of path or fails the test.
func statMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Mode().Perm()
}

// assertDirectorAbsent fails if ~/.agent-director exists under home. Part D
// (SR-4) makes the static-data verbs DB-free: they must neither open nor
// create the store, so after a fresh-HOME run the directory must not exist.
func assertDirectorAbsent(t *testing.T, home string) {
	t.Helper()
	if _, err := os.Stat(directorDir(home)); err == nil {
		t.Errorf("~/.agent-director exists under %s; DB-free verb must not create it", home)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", directorDir(home), err)
	}
}

// dbFreeShapes enumerates the argv shapes that Part D dispatches before
// setupClient. Each must be DB-free: no ~/.agent-director side effect. The
// two "version" rows exercise the bare verb and the version --json shape
// (the extra flag is ignored by versionHandler and must not change dispatch).
var dbFreeShapes = []struct {
	name string
	argv []string
}{
	{name: "help", argv: []string{"help"}},
	{name: "--help", argv: []string{"--help"}},
	{name: "no-args", argv: nil},
	{name: "version", argv: []string{"version"}},
	{name: "version --json", argv: []string{"version", "--json"}},
}

// TestDBFreeVerbsProduceNoStore drives each DB-free argv shape in an isolated
// HOME with no pre-existing ~/.agent-director and asserts the Part D contract:
// exit 0, a valid JSON envelope on stdout, empty stderr, and — critically —
// ~/.agent-director absent after the run (the verb never opened or created the
// store). This is the inverted successor to the old
// TestHelpCreatesDirAndDBOnFirstRun, which asserted the pre-Part-D side effect.
func TestDBFreeVerbsProduceNoStore(t *testing.T) {
	for _, shape := range dbFreeShapes {
		shape := shape
		t.Run(shape.name, func(t *testing.T) {
			home := t.TempDir()
			stdout, stderr, code := runCLIWithHome(t, home, shape.argv...)
			if code != 0 {
				t.Fatalf("exit=%d want 0; stderr=%q", code, stderr)
			}
			if stderr != "" {
				t.Errorf("stderr=%q want empty on success", stderr)
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
				t.Errorf("stdout is not a JSON envelope: %v\nstdout=%q", err, stdout)
			}
			assertDirectorAbsent(t, home)
		})
	}
}

// TestDBFreeVerbsSurviveMalformedConfig pins the blessed behavior delta
// (SR-4.1): the static-data verbs run their pre-setupClient path, which never
// calls config.Load, so a malformed ~/.agent-director/config.toml must not
// break them — they still exit 0 with a JSON envelope. (Writing the bad config
// forces the directory to exist, so this case does NOT assert absence; it pins
// the config-free early path, not the no-side-effect property.)
func TestDBFreeVerbsSurviveMalformedConfig(t *testing.T) {
	for _, shape := range dbFreeShapes {
		shape := shape
		t.Run(shape.name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.MkdirAll(directorDir(home), 0o700); err != nil {
				t.Fatalf("mkdir .agent-director: %v", err)
			}
			cfgPath := filepath.Join(directorDir(home), "config.toml")
			// Deliberately-broken TOML: unterminated string / stray bracket.
			if err := os.WriteFile(cfgPath, []byte("this is = not [valid toml\n"), 0o600); err != nil {
				t.Fatalf("write malformed config: %v", err)
			}

			stdout, stderr, code := runCLIWithHome(t, home, shape.argv...)
			if code != 0 {
				t.Fatalf("exit=%d want 0 despite malformed config; stderr=%q", code, stderr)
			}
			if stderr != "" {
				t.Errorf("stderr=%q want empty on success", stderr)
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
				t.Errorf("stdout is not a JSON envelope: %v\nstdout=%q", err, stdout)
			}
		})
	}
}

// TestStoreOpeningVerbCreatesDirAndDBOnFirstRun replaces the old
// TestHelpCreatesDirAndDBOnFirstRun: post-Part-D only store-opening verbs
// create the store, so first-run creation and its 0700/0600 mode bits are
// driven by `list`, not `help`.
func TestStoreOpeningVerbCreatesDirAndDBOnFirstRun(t *testing.T) {
	home := t.TempDir()
	stdout, stderr, code := runCLIWithHome(t, home, "list")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if stdout == "" {
		t.Errorf("stdout empty; want list JSON")
	}

	dir := directorDir(home)
	if mode := statMode(t, dir); mode != 0o700 {
		t.Errorf(".agent-director mode = %o, want 0700", mode)
	}
	db := stateDB(home)
	if mode := statMode(t, db); mode != 0o600 {
		t.Errorf("state.db mode = %o, want 0600", mode)
	}
}

// TestStoreOpeningVerbIdempotentAcrossInvocations replaces the old
// TestHelpIdempotentAcrossInvocations: idempotency of store creation is now
// observed through a store-opening verb (`list`).
func TestStoreOpeningVerbIdempotentAcrossInvocations(t *testing.T) {
	home := t.TempDir()

	// First run creates dir + db.
	if _, _, code := runCLIWithHome(t, home, "list"); code != 0 {
		t.Fatalf("first invocation exit=%d want 0", code)
	}
	firstDirMode := statMode(t, directorDir(home))
	firstDBMode := statMode(t, stateDB(home))

	// Second run: must succeed and not change modes.
	stdout2, stderr2, code2 := runCLIWithHome(t, home, "list")
	if code2 != 0 {
		t.Fatalf("second invocation exit=%d stderr=%q", code2, stderr2)
	}
	if stdout2 == "" {
		t.Errorf("second stdout empty")
	}
	if got := statMode(t, directorDir(home)); got != firstDirMode {
		t.Errorf(".agent-director mode changed: %o -> %o", firstDirMode, got)
	}
	if got := statMode(t, stateDB(home)); got != firstDBMode {
		t.Errorf("state.db mode changed: %o -> %o", firstDBMode, got)
	}
}

// setupTamperedDB creates a real state.db under home by running a STORE-OPENING
// verb once (`list`), then stamps PRAGMA user_version=99 directly so the next
// store-opening invocation must surface ErrSchemaMismatch.
//
// PART-D MANDATE: the bootstrap verb MUST open the store. A `help`/`version`
// bootstrap creates NO DB post-Part-D and would silently break this helper, so
// `list` is used deliberately (mirroring migration_gate_test.go's
// setupUnmigratedDB). user_version=99 is NEWER-than-binary: its error is
// ErrSchemaMismatch both before and after Part A per SR-11 — assert that
// literal name with no runtime conditionals. The older-than-binary
// (un-migrated) case is covered in migration_gate_test.go, not here.
func setupTamperedDB(t *testing.T, home string) {
	t.Helper()
	// `list` is a store-opening verb: it creates + stamps state.db on first run.
	if stdout, stderr, code := runCLIWithHome(t, home, "list"); code != 0 {
		t.Fatalf("bootstrap `list` exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	db, err := sql.Open("sqlite", stateDB(home))
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close raw db: %v", cerr)
		}
	}()
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", 99)); err != nil {
		t.Fatalf("stamp user_version: %v", err)
	}
}

// TestStoreOpeningVerbEmitsSchemaMismatchWhenDBTampered replaces the old
// TestHelpEmitsSchemaMismatchEnvelopeWhenDBTampered. Post-Part-D `help` is
// DB-free and never surfaces the mismatch, so a store-opening verb (`list`)
// both bootstraps and asserts. user_version=99 is newer-than-binary, whose
// error is the literal "ErrSchemaMismatch" (unchanged by Part A per SR-11).
func TestStoreOpeningVerbEmitsSchemaMismatchWhenDBTampered(t *testing.T) {
	home := t.TempDir()
	setupTamperedDB(t, home)

	stdout, stderr, code := runCLIWithHome(t, home, "list")
	if code == 0 {
		t.Fatalf("exit=0 want non-zero; stdout=%q stderr=%q", stdout, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout=%q want empty on error", stdout)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrSchemaMismatch" {
		t.Errorf("err_name=%q want %q", env.ErrName, "ErrSchemaMismatch")
	}
	if env.ErrDescription == "" {
		t.Errorf("err_description empty in %q", stderr)
	}
}
