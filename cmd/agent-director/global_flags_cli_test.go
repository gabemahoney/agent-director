package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestGlobalFlag_StorePath_ListVerb verifies the end-to-end behaviour of
// the b.32k `--store-path` flag: the CLI must accept the global flag before
// the verb token, route it through pkg/api.Options.StorePath, and complete
// a store-opening verb successfully against the supplied path.
//
// `list` is chosen because it's the cheapest verb that still opens the store
// via setupClient (after Part D, `version` is DB-free and no longer creates
// or opens the store, so it can no longer prove flag threading to pkg/api).
// A successful `list` against a fresh temp dir confirms (a) the flag was
// parsed and stripped before dispatch, (b) setupClient applied the override,
// and (c) pkg/api.New created and opened the store at the supplied path
// (CreateIfMissing=true is the CLI default).
func TestGlobalFlag_StorePath_ListVerb(t *testing.T) {
	tmp := t.TempDir()
	storePath := filepath.Join(tmp, "custom-state.db")

	stdout, stderr, code := runCLIWithHome(t, tmp,
		"--store-path", storePath, "list",
	)
	if code != 0 {
		t.Fatalf("exit=%d want 0; stderr=%q", code, stderr)
	}
	if stdout == "" {
		t.Fatalf("stdout empty; expected JSON envelope from list")
	}

	// The store file must exist at the path we supplied — verifies the flag
	// actually threaded through to pkg/api.New rather than silently being
	// dropped (in which case the store would land at ~/.agent-director/state.db
	// inside the HOME override, not the explicit path).
	if _, err := os.Stat(storePath); err != nil {
		t.Errorf("store file not at --store-path location %q: %v", storePath, err)
	}

	// Sanity: the list envelope parses as JSON (empty store => empty result).
	var env json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("stdout not JSON-parseable: %v\nstdout=%q", err, stdout)
	}
}

// TestGlobalFlag_StorePath_EqualsForm exercises the `--store-path=value` form
// against a store-opening verb (`list`; `version` is DB-free post-Part D).
func TestGlobalFlag_StorePath_EqualsForm(t *testing.T) {
	tmp := t.TempDir()
	storePath := filepath.Join(tmp, "eq-state.db")

	_, stderr, code := runCLIWithHome(t, tmp,
		"--store-path="+storePath, "list",
	)
	if code != 0 {
		t.Fatalf("exit=%d want 0; stderr=%q", code, stderr)
	}
	if _, err := os.Stat(storePath); err != nil {
		t.Errorf("store file not at --store-path location %q: %v", storePath, err)
	}
}

// TestGlobalFlag_Home_OverridesEnv verifies --home overrides the HOME env var
// the CLI inherits, so config.Load's tilde-expansion uses the supplied path.
// The store lands inside --home rather than $HOME. Exercised with `list`
// (the cheapest store-opening verb; `version` no longer opens the store
// after Part D).
func TestGlobalFlag_Home_OverridesEnv(t *testing.T) {
	envHome := t.TempDir()
	flagHome := t.TempDir()

	// Run with HOME=<envHome> but --home <flagHome>. The store must land
	// under flagHome (where the default ~/.agent-director/state.db resolves).
	_, stderr, code := runCLIWithHome(t, envHome,
		"--home", flagHome, "list",
	)
	if code != 0 {
		t.Fatalf("exit=%d want 0; stderr=%q", code, stderr)
	}

	wantStore := filepath.Join(flagHome, ".agent-director", "state.db")
	if _, err := os.Stat(wantStore); err != nil {
		t.Errorf("store file not at --home-derived location %q: %v", wantStore, err)
	}

	// And explicitly NOT at envHome — confirms HOME override actually applied.
	envStore := filepath.Join(envHome, ".agent-director", "state.db")
	if _, err := os.Stat(envStore); err == nil {
		t.Errorf("store file unexpectedly exists at env-HOME location %q; --home override did not apply", envStore)
	}
}

// TestGlobalFlag_DBFreePath_StripsFlagsCreatesNothing pins the Part D contract
// from the flag side: global flags placed before a DB-free verb (version,
// help) are still parsed and stripped from argv on the early pre-setupClient
// dispatch path, the verb exits 0 with a valid envelope, and NOTHING is
// created anywhere — neither at the --store-path/--home flag target nor under
// the inherited HOME. This is the flag-facing complement to the side-effect
// tests: it proves the DB-free path honours (does not choke on) global flags
// while remaining store-free.
func TestGlobalFlag_DBFreePath_StripsFlagsCreatesNothing(t *testing.T) {
	// --store-path before `version`: flag accepted+stripped, version emits its
	// JSON envelope, and no store is created at the flag target or under HOME.
	t.Run("store-path before version", func(t *testing.T) {
		home := t.TempDir()
		flagStore := filepath.Join(t.TempDir(), "should-not-exist.db")

		stdout, stderr, code := runCLIWithHome(t, home,
			"--store-path", flagStore, "version",
		)
		if code != 0 {
			t.Fatalf("exit=%d want 0; stderr=%q", code, stderr)
		}

		var env struct {
			Version string `json:"version"`
			Commit  string `json:"commit"`
		}
		if err := json.Unmarshal([]byte(stdout), &env); err != nil {
			t.Fatalf("stdout not JSON-parseable: %v\nstdout=%q", err, stdout)
		}
		if env.Version == "" {
			t.Errorf("version envelope has empty .version: %q", stdout)
		}

		if _, err := os.Stat(flagStore); err == nil {
			t.Errorf("store unexpectedly created at --store-path target %q; DB-free path must not open the store", flagStore)
		}
		if _, err := os.Stat(filepath.Join(home, ".agent-director")); err == nil {
			t.Errorf("~/.agent-director unexpectedly created under HOME %q; DB-free path must not open the store", home)
		}
	})

	// --home before `help`: flag accepted+stripped, help exits 0, and no store
	// is created under the flag home or the inherited HOME.
	t.Run("home before help", func(t *testing.T) {
		envHome := t.TempDir()
		flagHome := t.TempDir()

		_, stderr, code := runCLIWithHome(t, envHome,
			"--home", flagHome, "help",
		)
		if code != 0 {
			t.Fatalf("exit=%d want 0; stderr=%q", code, stderr)
		}

		if _, err := os.Stat(filepath.Join(flagHome, ".agent-director")); err == nil {
			t.Errorf("~/.agent-director unexpectedly created under --home %q; DB-free path must not open the store", flagHome)
		}
		if _, err := os.Stat(filepath.Join(envHome, ".agent-director")); err == nil {
			t.Errorf("~/.agent-director unexpectedly created under env-HOME %q; DB-free path must not open the store", envHome)
		}
	})
}

// TestGlobalFlag_TmuxCommand_AcceptedByVersionVerb verifies the b.32k
// `--tmux-command` flag is accepted (parsed and stripped from argv) without
// erroring when it precedes a verb. `version` is used as the carrier verb: it
// exits 0 regardless of the flag value, so this case pins that a recognized
// global flag before a DB-free verb is honoured (parsed + stripped) rather
// than treated as an invalid flag. After Part D `version` no longer opens the
// store, so this no longer asserts threading through to pkg/api.New; the
// --tmux-command -> pkg/api.Options.TmuxCommand plumbing is unit-tested in
// pkg/api and verb-level tmux semantics in cmd/agent-director/spawn_test.go.
func TestGlobalFlag_TmuxCommand_AcceptedByVersionVerb(t *testing.T) {
	tmp := t.TempDir()
	tmuxStub := filepath.Join(tmp, "fake-tmux")

	_, stderr, code := runCLIWithHome(t, tmp,
		"--tmux-command", tmuxStub, "version",
	)
	if code != 0 {
		t.Fatalf("exit=%d want 0; stderr=%q", code, stderr)
	}
}

// TestGlobalFlag_InvalidFlag_TailValue covers the error envelope path:
// a recognized flag at the tail of argv with no value must yield
// ErrInvalidFlags and a non-zero exit.
func TestGlobalFlag_InvalidFlag_TailValue(t *testing.T) {
	stdout, stderr, code := runCLI(t, "--store-path")
	if code == 0 {
		t.Errorf("exit=0 want non-zero; stdout=%q stderr=%q", stdout, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout=%q want empty (error envelope goes to stderr)", stdout)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrInvalidFlags" {
		t.Errorf("err_name = %q, want %q", env.ErrName, "ErrInvalidFlags")
	}
}
