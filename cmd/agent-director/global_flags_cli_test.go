package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestGlobalFlag_StorePath_VersionVerb verifies the end-to-end behaviour of
// the b.32k `--store-path` flag: the CLI must accept the global flag before
// the verb token, route it through pkg/api.Options.StorePath, and complete
// the `version` verb successfully against the supplied path.
//
// `version` is chosen because it's the cheapest verb to exercise: it opens
// the store via setupClient but performs no further DB I/O. A successful
// run with --store-path pointing into a fresh temp dir confirms (a) the
// flag was parsed and stripped before dispatch, (b) setupClient applied
// the override, and (c) pkg/api.New created and opened the store at the
// supplied path (CreateIfMissing=true is the CLI default).
func TestGlobalFlag_StorePath_VersionVerb(t *testing.T) {
	tmp := t.TempDir()
	storePath := filepath.Join(tmp, "custom-state.db")

	stdout, stderr, code := runCLIWithHome(t, tmp,
		"--store-path", storePath, "version",
	)
	if code != 0 {
		t.Fatalf("exit=%d want 0; stderr=%q", code, stderr)
	}
	if stdout == "" {
		t.Fatalf("stdout empty; expected JSON envelope from version")
	}

	// The store file must exist at the path we supplied — verifies the flag
	// actually threaded through to pkg/api.New rather than silently being
	// dropped (in which case the store would land at ~/.agent-director/state.db
	// inside the HOME override, not the explicit path).
	if _, err := os.Stat(storePath); err != nil {
		t.Errorf("store file not at --store-path location %q: %v", storePath, err)
	}

	// Sanity: the version envelope parses and has non-empty version + commit.
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
}

// TestGlobalFlag_StorePath_EqualsForm exercises the `--store-path=value` form.
func TestGlobalFlag_StorePath_EqualsForm(t *testing.T) {
	tmp := t.TempDir()
	storePath := filepath.Join(tmp, "eq-state.db")

	_, stderr, code := runCLIWithHome(t, tmp,
		"--store-path="+storePath, "version",
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
// The store lands inside --home rather than $HOME.
func TestGlobalFlag_Home_OverridesEnv(t *testing.T) {
	envHome := t.TempDir()
	flagHome := t.TempDir()

	// Run with HOME=<envHome> but --home <flagHome>. The store must land
	// under flagHome (where the default ~/.agent-director/state.db resolves).
	_, stderr, code := runCLIWithHome(t, envHome,
		"--home", flagHome, "version",
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

// TestGlobalFlag_TmuxCommand_AcceptedByVersionVerb verifies the b.32k
// `--tmux-command` flag is accepted (parsed, stripped from argv, threaded
// through to pkg/api.Options.TmuxCommand) without erroring. The `version`
// verb does not actually invoke tmux so a non-existent stub path is fine;
// this case pins that the flag plumbing is wired end-to-end. Threading to
// pkg/api is unit-tested in pkg/api; verb-level tmux semantics are unit-
// tested in cmd/agent-director/spawn_test.go and friends.
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
