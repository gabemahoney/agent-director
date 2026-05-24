package spawn

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
)

// captureTmux is a TmuxClient test double — records the argv that
// Launch would have handed tmux, and returns a programmable error.
// failOnSessionName injects a NewSession failure only when the
// supplied name matches — used by the live-collision case so the
// "tmux already has this name" path can be exercised without a real
// tmux.
type captureTmux struct {
	got struct {
		name    string
		cwd     string
		envs    map[string]string
		command []string
		called  bool
	}
	err               error
	failOnSessionName string
}

func (c *captureTmux) NewSession(name, cwd string, envs map[string]string, command []string) error {
	c.got.called = true
	c.got.name = name
	c.got.cwd = cwd
	c.got.envs = envs
	c.got.command = command
	if c.failOnSessionName != "" && name == c.failOnSessionName {
		return errors.New("tmux: duplicate session: can't create session")
	}
	return c.err
}

// newStoreAndLaunchInputs builds a Resolved that has already passed
// validation + defaults. Centralizing the boilerplate keeps each test
// focused on the behavior it pins.
func newStoreAndLaunchInputs(t *testing.T) (*store.Store, Resolved, config.Config) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	s, err := store.OpenOrInit(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	cwd := t.TempDir()
	r := Resolved{SpawnParams: SpawnParams{
		CWD:              cwd,
		ClaudeInstanceID: "id-launch-1",
		TmuxSessionName:  "cd-launch-1",
		RelayMode:        "off",
		ClaudeArgs:       []string{"--model", "opus"},
		AgentDirectorLabels: map[string]string{
			"role": "worker",
		},
	}}
	return s, r, config.Default()
}

func TestLaunchInsertsPendingAndCallsTmux(t *testing.T) {
	withStubExe(t, "/bin/agent-director")
	t.Setenv(envInstanceID, "") // ensure no parent leakage from the host shell
	s, r, cfg := newStoreAndLaunchInputs(t)
	tmux := &captureTmux{}

	id, err := Launch(s, tmux, r, cfg)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if id != "id-launch-1" {
		t.Errorf("Launch returned %q; want id-launch-1", id)
	}

	// Pending row written.
	row, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	if row.State != store.StatePending {
		t.Errorf("State = %q; want pending", row.State)
	}
	if row.CWD != r.CWD {
		t.Errorf("CWD = %q; want %q", row.CWD, r.CWD)
	}
	if row.TmuxSessionName != "cd-launch-1" {
		t.Errorf("TmuxSessionName = %q", row.TmuxSessionName)
	}
	if !reflect.DeepEqual(row.ClaudeArgs, []string{"--model", "opus"}) {
		t.Errorf("ClaudeArgs = %v", row.ClaudeArgs)
	}
	if row.Labels["role"] != "worker" {
		t.Errorf("Labels = %v", row.Labels)
	}

	// tmux call observed.
	if !tmux.got.called {
		t.Fatal("tmux.NewSession not called")
	}
	if tmux.got.name != "cd-launch-1" {
		t.Errorf("session name = %q", tmux.got.name)
	}
	if tmux.got.cwd != r.CWD {
		t.Errorf("cwd = %q; want %q", tmux.got.cwd, r.CWD)
	}
	if len(tmux.got.command) < 3 || tmux.got.command[0] != "claude" || tmux.got.command[1] != "--settings" {
		t.Errorf("command argv prefix = %v", tmux.got.command[:3])
	}
	if tmux.got.command[len(tmux.got.command)-2] != "--model" || tmux.got.command[len(tmux.got.command)-1] != "opus" {
		t.Errorf("user claude_args missing from command tail: %v", tmux.got.command)
	}

	// Env vars composed correctly.
	if tmux.got.envs["AGENT_DIRECTOR_INSTANCE_ID"] != "id-launch-1" {
		t.Errorf("env AGENT_DIRECTOR_INSTANCE_ID = %q", tmux.got.envs["AGENT_DIRECTOR_INSTANCE_ID"])
	}
	if tmux.got.envs["AGENT_DIRECTOR_RELAY_MODE"] != "off" {
		t.Errorf("env AGENT_DIRECTOR_RELAY_MODE = %q", tmux.got.envs["AGENT_DIRECTOR_RELAY_MODE"])
	}
	if tmux.got.envs["AGENT_DIRECTOR_LABEL_ROLE"] != "worker" {
		t.Errorf("env AGENT_DIRECTOR_LABEL_ROLE = %q", tmux.got.envs["AGENT_DIRECTOR_LABEL_ROLE"])
	}
}

func TestLaunchTmuxFailureLeavesRowPending(t *testing.T) {
	withStubExe(t, "/bin/agent-director")
	t.Setenv(envInstanceID, "") // ensure no parent leakage from the host shell
	s, r, cfg := newStoreAndLaunchInputs(t)
	tmux := &captureTmux{err: errors.New("tmux: name collision")}

	_, err := Launch(s, tmux, r, cfg)
	if err == nil {
		t.Fatal("expected error from Launch when tmux fails")
	}

	row, getErr := s.GetSpawn("id-launch-1")
	if getErr != nil {
		t.Fatalf("GetSpawn: %v", getErr)
	}
	if row.State != store.StatePending {
		t.Errorf("row should remain pending after tmux failure; got %q", row.State)
	}
}

// TestLaunchSerializesLabelsAsJSON pins SRD §4.2: the labels column is a
// JSON object. The deserialized round-trip is already covered by the
// happy-path test; this one reads the raw column directly so a future
// change to the column format (e.g. base64, msgpack) breaks the test.
func TestLaunchSerializesLabelsAsJSON(t *testing.T) {
	withStubExe(t, "/bin/agent-director")
	t.Setenv(envInstanceID, "") // ensure no parent leakage from the host shell

	dbPath := filepath.Join(t.TempDir(), "state.db")
	s, err := store.OpenOrInit(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	r := Resolved{SpawnParams: SpawnParams{
		CWD:              t.TempDir(),
		ClaudeInstanceID: "id-labels-json",
		TmuxSessionName:  "cd-labels-json",
		RelayMode:        "off",
		AgentDirectorLabels: map[string]string{
			"project": "agent-director",
			"env":     "dev",
		},
	}}
	if _, err := Launch(s, &captureTmux{}, r, config.Default()); err != nil {
		t.Fatalf("Launch: %v", err)
	}

	// Read the raw labels column. The store does not export a "raw JSON"
	// accessor — opening a parallel sql.DB is the narrowest way to assert
	// on the stored byte shape without leaking SQL into internal/spawn's
	// production code.
	raw := openRawForRead(t, dbPath)
	defer raw.Close()

	var labelsCol string
	if err := raw.QueryRow(`SELECT labels FROM spawns WHERE claude_instance_id = ?`,
		"id-labels-json").Scan(&labelsCol); err != nil {
		t.Fatalf("raw read labels: %v", err)
	}
	// JSON object representations differ only in key order; both keys
	// must be present with their literal values. A substring check is
	// resilient to encoder-determined key ordering.
	for _, want := range []string{`"project":"agent-director"`, `"env":"dev"`} {
		if !contains(labelsCol, want) {
			t.Errorf("labels column %q missing %q", labelsCol, want)
		}
	}
}

// TestLaunchEmitsEnvForNonAlphanumericLabelKey pins SRD §7.2 step 5:
// label keys are uppercased with non-alphanumerics replaced by `_` for
// the env-var name. The DB column keeps the original key verbatim
// (SRD §19 Q12) — the test asserts both.
func TestLaunchEmitsEnvForNonAlphanumericLabelKey(t *testing.T) {
	withStubExe(t, "/bin/agent-director")
	t.Setenv(envInstanceID, "")

	s, r, cfg := newStoreAndLaunchInputs(t)
	r.ClaudeInstanceID = "id-label-norm"
	r.TmuxSessionName = "cd-label-norm"
	r.AgentDirectorLabels = map[string]string{
		"my-key":      "v1",
		"x.y.z":       "v2",
		"already_ok":  "v3",
		"with spaces": "v4",
	}
	tmux := &captureTmux{}

	if _, err := Launch(s, tmux, r, cfg); err != nil {
		t.Fatalf("Launch: %v", err)
	}

	wantEnv := map[string]string{
		"AGENT_DIRECTOR_LABEL_MY_KEY":      "v1",
		"AGENT_DIRECTOR_LABEL_X_Y_Z":       "v2",
		"AGENT_DIRECTOR_LABEL_ALREADY_OK":  "v3",
		"AGENT_DIRECTOR_LABEL_WITH_SPACES": "v4",
	}
	for k, v := range wantEnv {
		if tmux.got.envs[k] != v {
			t.Errorf("env %s = %q; want %q", k, tmux.got.envs[k], v)
		}
	}

	// DB column preserves the verbatim keys, not the normalized ones.
	row, err := s.GetSpawn("id-label-norm")
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	for k, v := range r.AgentDirectorLabels {
		if row.Labels[k] != v {
			t.Errorf("labels[%q] = %q; want %q", k, row.Labels[k], v)
		}
	}
}

// TestLaunchParentIDNullWhenEnvUnset pins SRD §7.5: a Spawn launched
// from a plain shell (with no AGENT_DIRECTOR_INSTANCE_ID set) has a
// NULL parent_id. The store materializes NULL as the empty string in
// the Go struct; the test asserts that contract.
func TestLaunchParentIDNullWhenEnvUnset(t *testing.T) {
	withStubExe(t, "/bin/agent-director")
	t.Setenv(envInstanceID, "")

	s, r, cfg := newStoreAndLaunchInputs(t)
	r.ClaudeInstanceID = "id-no-parent"
	r.TmuxSessionName = "cd-no-parent"
	if _, err := Launch(s, &captureTmux{}, r, cfg); err != nil {
		t.Fatalf("Launch: %v", err)
	}

	row, err := s.GetSpawn("id-no-parent")
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	if row.ParentID != "" {
		t.Errorf("ParentID = %q; want \"\" (NULL in DB)", row.ParentID)
	}
}

// TestLaunchParentIDInheritsCallerEnv pins SRD §7.5: when the spawning
// process has AGENT_DIRECTOR_INSTANCE_ID set, that value lands in the
// new row's parent_id.
func TestLaunchParentIDInheritsCallerEnv(t *testing.T) {
	withStubExe(t, "/bin/agent-director")
	t.Setenv(envInstanceID, "id-the-parent")

	s, r, cfg := newStoreAndLaunchInputs(t)
	r.ClaudeInstanceID = "id-the-child"
	r.TmuxSessionName = "cd-the-child"

	// Pre-seed the parent row so the FK constraint is satisfied —
	// production code relies on parent already existing when its env
	// var is set.
	parent := store.Spawn{
		ClaudeInstanceID: "id-the-parent",
		CWD:              "/tmp",
		TmuxSessionName:  "cd-the-parent",
		RelayMode:        "off",
	}
	if err := s.InsertPending(parent); err != nil {
		t.Fatalf("seed parent: %v", err)
	}

	if _, err := Launch(s, &captureTmux{}, r, cfg); err != nil {
		t.Fatalf("Launch: %v", err)
	}

	row, err := s.GetSpawn("id-the-child")
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	if row.ParentID != "id-the-parent" {
		t.Errorf("ParentID = %q; want id-the-parent", row.ParentID)
	}
}

// TestLaunchParentDeleteCascadesToChild pins the schema's
// `parent_id ... ON DELETE SET NULL` clause. When the parent row is
// removed (Epic 8's delete verb will be the production trigger), the
// child's parent_id flips to NULL — orphans are not surfaced as a
// foreign-key constraint failure to callers.
func TestLaunchParentDeleteCascadesToChild(t *testing.T) {
	withStubExe(t, "/bin/agent-director")
	t.Setenv(envInstanceID, "id-cascade-parent")

	dbPath := filepath.Join(t.TempDir(), "state.db")
	s, err := store.OpenOrInit(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Pre-seed parent so FK on child INSERT is satisfied.
	parent := store.Spawn{
		ClaudeInstanceID: "id-cascade-parent",
		CWD:              "/tmp",
		TmuxSessionName:  "cd-cp",
		RelayMode:        "off",
	}
	if err := s.InsertPending(parent); err != nil {
		t.Fatalf("seed parent: %v", err)
	}

	r := Resolved{SpawnParams: SpawnParams{
		CWD:              t.TempDir(),
		ClaudeInstanceID: "id-cascade-child",
		TmuxSessionName:  "cd-cc",
		RelayMode:        "off",
	}}
	if _, err := Launch(s, &captureTmux{}, r, config.Default()); err != nil {
		t.Fatalf("Launch child: %v", err)
	}

	// Sanity check the precondition: child's parent_id is the parent.
	row, err := s.GetSpawn("id-cascade-child")
	if err != nil {
		t.Fatalf("GetSpawn child: %v", err)
	}
	if row.ParentID != "id-cascade-parent" {
		t.Fatalf("precondition: ParentID = %q; want id-cascade-parent", row.ParentID)
	}

	// Delete the parent via a parallel sql.DB connection — no DeleteSpawn
	// primitive exists yet (Epic 8). PRAGMA foreign_keys = ON must be set
	// on the new connection too; the store does it on open, but a fresh
	// raw conn defaults to off.
	raw := openRawForRead(t, dbPath)
	defer raw.Close()
	if _, err := raw.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("enable FK: %v", err)
	}
	if _, err := raw.Exec(`DELETE FROM spawns WHERE claude_instance_id = ?`,
		"id-cascade-parent"); err != nil {
		t.Fatalf("delete parent: %v", err)
	}

	// Re-read child via the store. ParentID should now be empty (NULL).
	row, err = s.GetSpawn("id-cascade-child")
	if err != nil {
		t.Fatalf("GetSpawn child after parent delete: %v", err)
	}
	if row.ParentID != "" {
		t.Errorf("ParentID after parent delete = %q; want \"\" (NULL via ON DELETE SET NULL)",
			row.ParentID)
	}
}

// openRawForRead returns a *sql.DB pointed at the same SQLite file the
// store uses, with foreign-key enforcement enabled. Used by the small
// number of tests that need to drop into raw SQL (e.g. verifying the
// labels column byte shape or driving a DELETE that no store primitive
// exposes yet).
func openRawForRead(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	return db
}

// contains is a tiny strings.Contains alias to keep the JSON-shape
// asserts readable.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestLaunchPreTrustsCwdByDefault pins the b.f75 fix at the Launch
// integration boundary: with NoPreTrust=false (the default), Launch
// writes hasTrustDialogAccepted=true into ~/.claude.json for the
// resolved cwd before exec'ing tmux.
func TestLaunchPreTrustsCwdByDefault(t *testing.T) {
	withStubExe(t, "/bin/agent-director")
	t.Setenv(envInstanceID, "")
	stub := withStubClaudeJSON(t)
	// Seed an empty projects map so preTrustCwd has a file to read+rewrite.
	if err := os.WriteFile(stub, []byte(`{"projects":{}}`), 0o600); err != nil {
		t.Fatalf("seed claude.json: %v", err)
	}

	s, r, cfg := newStoreAndLaunchInputs(t)
	if _, err := Launch(s, &captureTmux{}, r, cfg); err != nil {
		t.Fatalf("Launch: %v", err)
	}

	got := readClaudeJSON(t, stub)
	projects, ok := got["projects"].(map[string]any)
	if !ok {
		t.Fatalf("projects missing: %T", got["projects"])
	}
	entry, ok := projects[r.CWD].(map[string]any)
	if !ok {
		t.Fatalf("projects[%q] missing after Launch", r.CWD)
	}
	if b, _ := entry["hasTrustDialogAccepted"].(bool); !b {
		t.Errorf("hasTrustDialogAccepted = %v; want true", entry["hasTrustDialogAccepted"])
	}
}

// TestLaunchNoPreTrustFlagSkipsWrite pins AC #2: --no-pre-trust
// (NoPreTrust=true) opts out of the ~/.claude.json write entirely.
func TestLaunchNoPreTrustFlagSkipsWrite(t *testing.T) {
	withStubExe(t, "/bin/agent-director")
	t.Setenv(envInstanceID, "")
	stub := withStubClaudeJSON(t)
	if err := os.WriteFile(stub, []byte(`{"projects":{}}`), 0o600); err != nil {
		t.Fatalf("seed claude.json: %v", err)
	}

	s, r, cfg := newStoreAndLaunchInputs(t)
	r.NoPreTrust = true
	if _, err := Launch(s, &captureTmux{}, r, cfg); err != nil {
		t.Fatalf("Launch: %v", err)
	}

	got := readClaudeJSON(t, stub)
	projects, _ := got["projects"].(map[string]any)
	if _, present := projects[r.CWD]; present {
		t.Errorf("projects[%q] was written despite NoPreTrust=true", r.CWD)
	}
}

// TestLaunchMissingClaudeJSONDoesNotBlockSpawn pins AC #5: when
// ~/.claude.json doesn't exist (truly-fresh-machine case), Launch
// still proceeds to insert the row and start the tmux session — the
// soft warning lands in preTrustWarn but the spawn is not blocked.
func TestLaunchMissingClaudeJSONDoesNotBlockSpawn(t *testing.T) {
	withStubExe(t, "/bin/agent-director")
	t.Setenv(envInstanceID, "")
	// Stub points at a path we never create — preTrustCwd will surface
	// ErrClaudeJSONMissing, and Launch should swallow it.
	withStubClaudeJSON(t)

	// Redirect the warn writer so the test's stderr stays clean and we
	// can assert the warning text.
	var warn captureWriter
	saved := preTrustWarn
	preTrustWarn = &warn
	t.Cleanup(func() { preTrustWarn = saved })

	s, r, cfg := newStoreAndLaunchInputs(t)
	tmux := &captureTmux{}
	if _, err := Launch(s, tmux, r, cfg); err != nil {
		t.Fatalf("Launch should succeed despite missing claude.json: %v", err)
	}
	if !tmux.got.called {
		t.Errorf("tmux.NewSession not called; pre-trust failure should not block the spawn")
	}
	if warn.buf == "" {
		t.Errorf("expected a warning written to preTrustWarn; got empty")
	}
}

// captureWriter is an io.Writer that records writes as a string.
type captureWriter struct{ buf string }

func (c *captureWriter) Write(p []byte) (int, error) {
	c.buf += string(p)
	return len(p), nil
}

// TestLaunchPassesUserSuppliedTmuxSessionName pins SR-4.1 + SR-3.1: a
// caller-supplied TmuxSessionName flows verbatim into
// TmuxClient.NewSession and into the persisted spawns row — no
// sanitization, no suffix.
func TestLaunchPassesUserSuppliedTmuxSessionName(t *testing.T) {
	withStubExe(t, "/bin/agent-director")
	t.Setenv(envInstanceID, "")
	s, r, cfg := newStoreAndLaunchInputs(t)
	r.ClaudeInstanceID = "id-user-name"
	r.TmuxSessionName = "bot-claude-status"
	r.TmuxSessionNameSupplied = true
	tmux := &captureTmux{}

	if _, err := Launch(s, tmux, r, cfg); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if tmux.got.name != "bot-claude-status" {
		t.Errorf("tmux.NewSession name = %q; want bot-claude-status (verbatim, no decoration)", tmux.got.name)
	}
	row, err := s.GetSpawn("id-user-name")
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	if row.TmuxSessionName != "bot-claude-status" {
		t.Errorf("row.TmuxSessionName = %q; want bot-claude-status", row.TmuxSessionName)
	}
}

// TestLaunchSurfacesTmuxNewSessionFailureUnchanged pins SR-2.4 + SR-4.1:
// a NewSession failure (incl. tmux's own live-name-collision refusal)
// wraps through unchanged — no ErrTmuxSessionNameTaken, no new spawn
// state. The pending row is left for find-missing.
func TestLaunchSurfacesTmuxNewSessionFailureUnchanged(t *testing.T) {
	withStubExe(t, "/bin/agent-director")
	t.Setenv(envInstanceID, "")
	s, r, cfg := newStoreAndLaunchInputs(t)
	r.ClaudeInstanceID = "id-collide"
	r.TmuxSessionName = "bot-claude-status"
	r.TmuxSessionNameSupplied = true
	tmux := &captureTmux{failOnSessionName: "bot-claude-status"}

	_, err := Launch(s, tmux, r, cfg)
	if err == nil {
		t.Fatal("expected Launch to surface NewSession failure")
	}
	// No typed sentinel: the wrapped tmux error must surface as-is.
	if errors.Is(err, ErrTmuxSessionNameEmpty) ||
		errors.Is(err, ErrTmuxSessionNameInvalid) ||
		errors.Is(err, ErrTmuxSessionNameTooLong) {
		t.Errorf("collision must not be classified as a name-validation sentinel: %v", err)
	}
	row, getErr := s.GetSpawn("id-collide")
	if getErr != nil {
		t.Fatalf("GetSpawn: %v", getErr)
	}
	if row.State != store.StatePending {
		t.Errorf("State after NewSession failure = %q; want pending (find-missing reconciles)", row.State)
	}
}

func TestLaunchSecondInsertSurfacesCollision(t *testing.T) {
	withStubExe(t, "/bin/agent-director")
	t.Setenv(envInstanceID, "") // ensure no parent leakage from the host shell
	s, r, cfg := newStoreAndLaunchInputs(t)
	tmux := &captureTmux{}
	if _, err := Launch(s, tmux, r, cfg); err != nil {
		t.Fatalf("first Launch: %v", err)
	}
	tmux2 := &captureTmux{}
	_, err := Launch(s, tmux2, r, cfg)
	if !errors.Is(err, ErrInstanceIdCollision) {
		t.Fatalf("second Launch err = %v; want ErrInstanceIdCollision", err)
	}
	if tmux2.got.called {
		t.Error("tmux.NewSession should not be called on collision")
	}
}
