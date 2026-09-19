package main_test

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	_ "modernc.org/sqlite" // raw driver access for the downgrade helper
)

// setupUnmigratedDB bootstraps a real state.db under home (by running a
// STORE-OPENING verb once so the schema is created and stamped at the
// binary's current schemaVersion) then stamps PRAGMA user_version=version
// directly, simulating an older-than-binary DB the next open must gate on.
//
// ORDER-ROBUSTNESS MANDATE (PM, cross-epic with Part D): the bootstrap verb
// MUST open the store. `list` does; `help`/`--help`/`version`/no-args must
// NEVER be used here — Part D makes those DB-free and a help-based bootstrap
// would silently stop creating the DB once D lands. This helper deliberately
// diverges from side_effects_test.go's setupTamperedDB (which bootstraps via
// `help`) for exactly that reason.
func setupUnmigratedDB(t *testing.T, home string, version int) {
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
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		t.Fatalf("stamp user_version=%d: %v", version, err)
	}
}

// TestStoreOpeningCommandRefusesUnmigratedDB verifies that a non-hook,
// store-opening command against an older-than-binary DB (user_version=1) hits
// the SR-1.4 dead-end: non-zero exit, empty stdout, ErrSchemaMigrationRequired
// envelope, admin-only message with no self-service breadcrumbs.
func TestStoreOpeningCommandRefusesUnmigratedDB(t *testing.T) {
	home := t.TempDir()
	setupUnmigratedDB(t, home, 1)

	// `list` is the asserting verb — also store-opening per the mandate.
	stdout, stderr, code := runCLIWithHome(t, home, "list")
	if code == 0 {
		t.Fatalf("exit=0 want non-zero; stdout=%q stderr=%q", stdout, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout=%q want empty on refusal", stdout)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrSchemaMigrationRequired" {
		t.Errorf("err_name=%q want %q", env.ErrName, "ErrSchemaMigrationRequired")
	}

	// Dead-end pattern (SR-1.4): names the current + required versions and
	// routes only to an administrator via the install process.
	desc := env.ErrDescription
	for _, want := range []string{
		"state.db is schema v1",
		"this binary requires v",
		"Migration must be performed by an administrator",
		"install process",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("description %q missing dead-end fragment %q", desc, want)
		}
	}

	// No self-service breadcrumbs: the operator must not be able to lift a
	// command, flag, file path, or env var name out of the message and
	// "fix it themselves". (The bare word "agent-director install process"
	// is an admin reference, not an invocable breadcrumb — assert on the
	// concrete breadcrumb SHAPES instead.)
	assertNoBreadcrumbs(t, desc)
}

// assertNoBreadcrumbs fails if desc leaks anything an operator could use to
// self-migrate: a flag (--x), a filesystem path (a "/" segment), an env var
// name (AGENT_DIRECTOR_*), or a known CLI verb token.
func assertNoBreadcrumbs(t *testing.T, desc string) {
	t.Helper()
	if strings.Contains(desc, "--") {
		t.Errorf("description leaks a flag breadcrumb (--): %q", desc)
	}
	if strings.Contains(desc, "/") {
		t.Errorf("description leaks a file-path breadcrumb (/): %q", desc)
	}
	if strings.Contains(desc, "AGENT_DIRECTOR_") {
		t.Errorf("description leaks an env-var breadcrumb: %q", desc)
	}
	// Known store-opening verbs must not appear as invocation hints. `migrate`
	// especially must not be advertised — it is not even a recognized verb.
	for _, verb := range []string{"migrate", "spawn", "list ", "resume", "expire", "delete"} {
		if strings.Contains(desc, verb) {
			t.Errorf("description leaks a command breadcrumb %q: %q", verb, desc)
		}
	}
}

// TestHookFailsOpenAgainstUnmigratedDB verifies the hook path stays fail-open
// against the same un-migrated DB the store-opening command refuses: exit 0,
// empty stdout — the agent session is unaffected (SRD §3.2).
func TestHookFailsOpenAgainstUnmigratedDB(t *testing.T) {
	home := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("AGENT_DIRECTOR_STATE_DIR", stateDir)
	setupUnmigratedDB(t, home, 1)

	stdout, stderr, code := runCLIWithEnv(t, home,
		map[string]string{
			"AGENT_DIRECTOR_INSTANCE_ID": "id-migrate-gate",
			"AGENT_DIRECTOR_STATE_DIR":   stateDir,
		},
		`{"hook_event_name":"SessionStart","transcript_path":"/x/abc.jsonl"}`, "hook")
	if code != 0 {
		t.Fatalf("hook exit=%d want 0 (fail-open); stderr=%q", code, stderr)
	}
	if stdout != "" {
		t.Errorf("hook stdout=%q want empty (state-tracking fail-open)", stdout)
	}
}

// TestMigrateIsNotARecognizedVerb asserts `migrate` is rejected as an unknown
// verb — the gate never exposes a self-service migration command.
func TestMigrateIsNotARecognizedVerb(t *testing.T) {
	home := t.TempDir()
	// Bootstrap a HEALTHY DB via a store-opening verb (NOT downgraded): on an
	// un-migrated DB setupClient fails before dispatch, so `migrate` would
	// surface ErrSchemaMigrationRequired rather than the unknown-verb path we
	// want to prove. A healthy DB lets dispatch reach the table lookup.
	if stdout, stderr, code := runCLIWithHome(t, home, "list"); code != 0 {
		t.Fatalf("bootstrap `list` exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	stdout, stderr, code := runCLIWithHome(t, home, "migrate")
	if code == 0 {
		t.Fatalf("`migrate` exit=0 want non-zero; stdout=%q stderr=%q", stdout, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout=%q want empty on unknown verb", stdout)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrUnknownVerb" {
		t.Errorf("err_name=%q want %q", env.ErrName, "ErrUnknownVerb")
	}
}
