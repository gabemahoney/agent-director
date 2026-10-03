package spawn

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// launchEnv is one Launch under test: a temp store, a per-test TMUX_TMPDIR,
// the Recorder, the start-time reader fake, a test clock and a captured log.
type launchEnv struct {
	t      *testing.T
	dbPath string
	s      *store.Store
	rec    *tmuxfix.Recorder
	pc     *procfix.Checker
	clock  *tmuxfix.Clock
	logs   bytes.Buffer
	socket string // the socket Launch resolves in this environment
	r      Resolved
	minted bool // Launch's minted: whether the pre-check minted r's id (default: caller-supplied)
	cfg    config.Config
}

// newLaunchEnv builds a launchEnv whose Resolved has passed validation and
// defaults; tests override only the fields they pin.
func newLaunchEnv(t *testing.T) *launchEnv {
	t.Helper()
	withStubExe(t, "/bin/agent-director")
	t.Setenv(envInstanceID, "") // no parent leakage from the host shell
	e := &launchEnv{t: t, socket: isolateTmux(t), rec: tmuxfix.NewRecorder(), pc: procfix.New(),
		clock: tmuxfix.NewClock(time.Date(2026, 9, 29, 12, 0, 0, 123_000_000, time.UTC)), cfg: config.Default()}
	e.dbPath = filepath.Join(t.TempDir(), "state.db")
	s, err := store.OpenOrInit(e.dbPath)
	if err != nil {
		t.Fatalf("store.OpenOrInit: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	e.s = s
	e.r = Resolved{SpawnParams: SpawnParams{
		CWD:                 t.TempDir(),
		ClaudeInstanceID:    "id-launch-1",
		TmuxSessionName:     "cd-launch-1",
		RelayMode:           "off",
		ClaudeArgs:          []string{"--model", "opus"},
		AgentDirectorLabels: map[string]string{"role": "worker"},
	}}
	return e
}

// isolateTmux points TMUX_TMPDIR at a fresh directory, unsets TMUX and
// returns the socket tmux would resolve there.
func isolateTmux(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	t.Setenv("TMUX_TMPDIR", dir)
	t.Setenv("TMUX", "")
	os.Unsetenv("TMUX")
	return filepath.Join(dir, fmt.Sprintf("tmux-%d", os.Getuid()), "default")
}

// launch runs Launch on e's store, Recorder, reader, minted, config and clock.
func (e *launchEnv) launch() (string, PreTrustOutcome, error) {
	return Launch(e.s, e.rec, e.pc, e.r, e.minted, e.cfg, e.clock.Now, log.New(&e.logs, "", 0))
}

// mustLaunch runs launch, fails the test on an error and returns the id.
func (e *launchEnv) mustLaunch() string {
	e.t.Helper()
	id, _ := e.mustLaunchOutcome()
	return id
}

// mustLaunchOutcome runs launch, fails the test on an error and returns the
// id and the pre-trust outcome Launch reported.
func (e *launchEnv) mustLaunchOutcome() (string, PreTrustOutcome) {
	e.t.Helper()
	id, outcome, err := e.launch()
	if err != nil {
		e.t.Fatalf("Launch: %v", err)
	}
	return id, outcome
}

// onlyCreate returns the one recorded tmux call, which must be the create.
func (e *launchEnv) onlyCreate() tmuxfix.SocketCall {
	e.t.Helper()
	calls := e.rec.SocketCalls()
	if len(calls) != 1 || calls[0].Call != tmux.CallCreate {
		e.t.Fatalf("tmux calls = %+v; want exactly one create", calls)
	}
	return calls[0]
}

// row reads id's row through the store.
func (e *launchEnv) row(id string) store.Spawn {
	e.t.Helper()
	row, err := e.s.GetSpawn(id)
	if err != nil {
		e.t.Fatalf("GetSpawn(%s): %v", id, err)
	}
	return row
}

// TestLaunchInsertsPendingAndCreatesSession: the pending row's fields, and
// one labelled create on the resolved socket carrying the launch's inputs.
func TestLaunchInsertsPendingAndCreatesSession(t *testing.T) {
	e := newLaunchEnv(t)
	id := e.mustLaunch()
	if id != "id-launch-1" {
		t.Errorf("Launch returned %q; want id-launch-1", id)
	}

	row := e.row(id)
	if row.State != store.StatePending || row.CWD != e.r.CWD || row.TmuxSessionName != "cd-launch-1" {
		t.Errorf("row = {state %q, cwd %q, name %q}; want pending, %q, cd-launch-1",
			row.State, row.CWD, row.TmuxSessionName, e.r.CWD)
	}
	if !reflect.DeepEqual(row.ClaudeArgs, []string{"--model", "opus"}) || row.Labels["role"] != "worker" {
		t.Errorf("row args/labels = %v / %v", row.ClaudeArgs, row.Labels)
	}

	c := e.onlyCreate()
	if c.Socket != e.socket || c.Target != "cd-launch-1" || c.Cwd != e.r.CWD {
		t.Errorf("create = {socket %q, name %q, cwd %q}; want %q, cd-launch-1, %q", c.Socket, c.Target, c.Cwd, e.socket, e.r.CWD)
	}
	if c.Token != row.Identity.Token || c.InstanceID != id || c.StoreID != e.s.StoreID() {
		t.Errorf("create label args = {%q %q %q}; want {%q %q %q}", c.Token, c.InstanceID, c.StoreID,
			row.Identity.Token, id, e.s.StoreID())
	}
	n := len(c.Command)
	if n < 5 || c.Command[0] != "claude" || c.Command[1] != "--settings" || c.Command[n-2] != "--model" || c.Command[n-1] != "opus" {
		t.Errorf("create command = %v; want claude --settings <json> --model opus", c.Command)
	}
	for k, want := range map[string]string{"AGENT_DIRECTOR_RELAY_MODE": "off", "AGENT_DIRECTOR_LABEL_ROLE": "worker"} {
		if c.Envs[k] != want {
			t.Errorf("create env %s = %q; want %q", k, c.Envs[k], want)
		}
	}
}

// TestLaunchPersistsExtraEnv pins SR-10 write-side: a resolved ExtraEnv
// reaches the create's env and round-trips through the row verbatim.
func TestLaunchPersistsExtraEnv(t *testing.T) {
	e := newLaunchEnv(t)
	e.r.ExtraEnv = map[string]string{
		"CLAUDE_CONFIG_DIR": "/home/bee/.claude-alt",
		"ANTHROPIC_API_KEY": "sk-ant-test",
	}
	id := e.mustLaunch()
	if got := e.onlyCreate().Envs["CLAUDE_CONFIG_DIR"]; got != "/home/bee/.claude-alt" {
		t.Errorf("create env CLAUDE_CONFIG_DIR = %q; want /home/bee/.claude-alt", got)
	}
	if got := e.row(id).ExtraEnv; !reflect.DeepEqual(got, e.r.ExtraEnv) {
		t.Errorf("row.ExtraEnv = %v; want %v (persisted verbatim)", got, e.r.ExtraEnv)
	}
}

// TestLaunchCreateFailureLeavesRowPending: a failed create other than
// "duplicate session" maps to its tmux sentinel; the row stays pending with
// its launch fields and no identity.
func TestLaunchCreateFailureLeavesRowPending(t *testing.T) {
	cases := []struct {
		name    string
		failure tmux.Failure
		want    error
	}{
		{"no server", tmux.FailNoServer, tmux.ErrTmuxSessionCreate},
		{"timed out", tmux.FailTimeout, tmux.ErrTmuxUnresponsive},
		{"binary unavailable", tmux.FailUnavailable, tmux.ErrTmuxNotAvailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newLaunchEnv(t)
			e.r.TmuxSessionName, e.r.TmuxSessionNameSupplied = "bot-claude-status", true
			e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tc.failure}, tmux.CallCreate)

			_, outcome, err := e.launch()
			if !errors.Is(err, tc.want) {
				t.Fatalf("Launch err = %v; want %v", err, tc.want)
			}
			if outcome != "" {
				t.Errorf("outcome = %q; want none on an error (the error, not a result)", outcome)
			}
			if errors.Is(err, ErrTmuxSessionNameInvalid) || errors.Is(err, ErrInstanceIdCollision) {
				t.Errorf("err = %v; must not read as a validation or collision sentinel", err)
			}
			e.onlyCreate()
			row := e.row("id-launch-1")
			if row.State != store.StatePending || row.Identity.Token == "" || row.Identity.Socket != e.socket {
				t.Errorf("row = {state %q, token %q, socket %q}; want pending with token and %q",
					row.State, row.Identity.Token, row.Identity.Socket, e.socket)
			}
			if row.Identity.ServerPID != 0 || row.Identity.PaneID != "" {
				t.Errorf("identity = %+v; want none recorded after a failed create", row.Identity)
			}
		})
	}
}

// catalogued is every exported sentinel a Launch error could wrap.
var catalogued = []error{
	tmux.ErrTmuxNotAvailable, tmux.ErrTmuxSessionCreate, tmux.ErrTmuxUnresponsive, tmux.ErrTmuxSessionConflict,
	tmux.ErrTmuxKillFailed, tmux.ErrTmuxListPanesFailed, tmux.ErrTmuxSendKeys, tmux.ErrTmuxCaptureFailed,
	ErrCwdMissing, ErrCwdNotAPath, ErrCwdNotFound, ErrCwdNotADirectory, ErrRelayModeInvalid, ErrSpawnDeniedFlag,
	ErrReservedEnvKey, ErrInstanceIdCollision, ErrTmuxSessionNameEmpty, ErrTmuxSessionNameInvalid,
	ErrTmuxSessionNameTooLong, ErrClaudeJSONMissing, store.ErrSpawnNotFound, store.ErrPrimaryKeyCollision,
}

// TestLaunchDuplicateSessionReturnsHeldName pins SR-9.4: "duplicate session"
// returns a *HeldNameError with the insert's facts and nothing after the create.
func TestLaunchDuplicateSessionReturnsHeldName(t *testing.T) {
	e := newLaunchEnv(t)
	e.r.TmuxSessionName, e.r.TmuxSessionNameSupplied = "bot-claude-status", true
	e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailDuplicate}, tmux.CallCreate)

	id, outcome, err := e.launch()
	var held *HeldNameError
	if !errors.As(err, &held) {
		t.Fatalf("Launch err = %v; want a *HeldNameError", err)
	}
	if id != "" || outcome != "" {
		t.Errorf("Launch = (%q, %q); want no id and no outcome with the error", id, outcome)
	}
	for _, s := range catalogued {
		if errors.Is(err, s) {
			t.Errorf("err = %v matches catalogued sentinel %v; want none", err, s)
		}
	}

	e.onlyCreate() // no tmux call after the "duplicate session" answer
	row := e.row("id-launch-1")
	want := HeldNameError{InstanceID: "id-launch-1", Name: "bot-claude-status", Socket: e.socket,
		Token: row.Identity.Token, LaunchStartedAtMillis: e.clock.Now().UnixMilli()}
	if *held != want || want.Token == "" {
		t.Errorf("held = %+v; want %+v (with the row's token)", *held, want)
	}
	if row.State != store.StatePending || row.RowVersion != 0 || row.LaunchStartedAtMillis != want.LaunchStartedAtMillis ||
		row.EndedAtText != "" || row.Identity.Socket != e.socket {
		t.Errorf("row = {state %q, version %d, launch start %d, ended_at %q, socket %q}; want pending at version 0 as inserted",
			row.State, row.RowVersion, row.LaunchStartedAtMillis, row.EndedAtText, row.Identity.Socket)
	}
	if e.logs.Len() != 0 {
		t.Errorf("log = %q; want no line from Launch", e.logs.String())
	}
	if strings.Contains(err.Error(), want.Token) {
		t.Errorf("err text %q carries the launch token", err.Error())
	}
}

// TestLaunchSerializesLabelsAsJSON pins SRD §4.2: the raw labels column is a
// JSON object with the verbatim keys.
func TestLaunchSerializesLabelsAsJSON(t *testing.T) {
	e := newLaunchEnv(t)
	e.r.AgentDirectorLabels = map[string]string{"project": "agent-director", "env": "dev"}
	id := e.mustLaunch()

	raw := openRawForRead(t, e.dbPath)
	defer raw.Close()
	var labelsCol string
	if err := raw.QueryRow(`SELECT labels FROM spawns WHERE claude_instance_id = ?`, id).Scan(&labelsCol); err != nil {
		t.Fatalf("raw read labels: %v", err)
	}
	for _, want := range []string{`"project":"agent-director"`, `"env":"dev"`} {
		if !strings.Contains(labelsCol, want) {
			t.Errorf("labels column %q missing %q", labelsCol, want)
		}
	}
}

// TestLaunchEmitsEnvForNonAlphanumericLabelKey pins SRD §7.2 step 5: env
// names are normalised while the row keeps the verbatim keys.
func TestLaunchEmitsEnvForNonAlphanumericLabelKey(t *testing.T) {
	e := newLaunchEnv(t)
	e.r.AgentDirectorLabels = map[string]string{"my-key": "v1", "x.y.z": "v2", "already_ok": "v3", "with spaces": "v4"}
	id := e.mustLaunch()

	envs := e.onlyCreate().Envs
	wantEnv := map[string]string{
		"AGENT_DIRECTOR_LABEL_MY_KEY":      "v1",
		"AGENT_DIRECTOR_LABEL_X_Y_Z":       "v2",
		"AGENT_DIRECTOR_LABEL_ALREADY_OK":  "v3",
		"AGENT_DIRECTOR_LABEL_WITH_SPACES": "v4",
	}
	for k, v := range wantEnv {
		if envs[k] != v {
			t.Errorf("env %s = %q; want %q", k, envs[k], v)
		}
	}
	if got := e.row(id).Labels; !reflect.DeepEqual(got, e.r.AgentDirectorLabels) {
		t.Errorf("row labels = %v; want %v", got, e.r.AgentDirectorLabels)
	}
}

// TestLaunchParentID pins SRD §7.5: parent_id is NULL with no caller
// AGENT_DIRECTOR_INSTANCE_ID and the caller's id when set.
func TestLaunchParentID(t *testing.T) {
	for _, parent := range []string{"", "id-the-parent"} {
		t.Run("parent="+parent, func(t *testing.T) {
			e := newLaunchEnv(t)
			if parent != "" {
				seedParent(t, e.s, parent)
				t.Setenv(envInstanceID, parent)
			}
			if got := e.row(e.mustLaunch()).ParentID; got != parent {
				t.Errorf("ParentID = %q; want %q", got, parent)
			}
		})
	}
}

// seedParent inserts a parent row so a child's parent_id FK is satisfied.
func seedParent(t *testing.T, s *store.Store, id string) {
	t.Helper()
	if err := s.InsertPending(store.Spawn{ClaudeInstanceID: id, CWD: "/tmp", TmuxSessionName: "cd-" + id, RelayMode: "off"}); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
}

// TestLaunchParentDeleteCascadesToChild pins `parent_id ... ON DELETE SET
// NULL`: deleting the parent leaves the child with a NULL parent_id.
func TestLaunchParentDeleteCascadesToChild(t *testing.T) {
	e := newLaunchEnv(t)
	seedParent(t, e.s, "id-cascade-parent")
	t.Setenv(envInstanceID, "id-cascade-parent")
	id := e.mustLaunch()
	if got := e.row(id).ParentID; got != "id-cascade-parent" {
		t.Fatalf("precondition: ParentID = %q; want id-cascade-parent", got)
	}

	// No store primitive deletes a row, so a raw connection (foreign keys on) does.
	raw := openRawForRead(t, e.dbPath)
	defer raw.Close()
	if _, err := raw.Exec(`DELETE FROM spawns WHERE claude_instance_id = ?`, "id-cascade-parent"); err != nil {
		t.Fatalf("delete parent: %v", err)
	}
	if got := e.row(id).ParentID; got != "" {
		t.Errorf("ParentID after parent delete = %q; want \"\" (ON DELETE SET NULL)", got)
	}
}

// openRawForRead opens the store's SQLite file with foreign keys enforced,
// for the byte-shape and delete checks no store primitive exposes.
func openRawForRead(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	return db
}

// TestLaunchPreTrust pins b.f75, SR-5.2 and SR-22.6: Launch trusts the cwd
// unless NoPreTrust, reports ok or skipped, prints nothing, creates the
// session and records the choice.
func TestLaunchPreTrust(t *testing.T) {
	cases := []struct {
		noPreTrust bool
		want       PreTrustOutcome
	}{{false, PreTrustOK}, {true, PreTrustSkipped}}
	for _, tc := range cases {
		noPreTrust, want := tc.noPreTrust, tc.want
		t.Run(fmt.Sprintf("NoPreTrust=%v", noPreTrust), func(t *testing.T) {
			e := newLaunchEnv(t)
			stub := withStubClaudeJSON(t)
			seedFile(t, stub, `{"projects":{}}`)
			warn := capturePreTrustWarn(t)
			e.r.NoPreTrust = noPreTrust
			id, outcome := e.mustLaunchOutcome()
			e.onlyCreate()

			if outcome != want {
				t.Errorf("Launch outcome = %q; want %q", outcome, want)
			}

			if got := e.row(id).NoPreTrust; got != noPreTrust {
				t.Errorf("row.NoPreTrust = %v; want %v (the spawn's choice recorded)", got, noPreTrust)
			}
			if warn.Len() != 0 {
				t.Errorf("warning = %q; want nothing printed", warn.String())
			}
			if noPreTrust {
				if got := mustReadFile(t, stub); string(got) != `{"projects":{}}` {
					t.Errorf("claude.json = %q; want untouched despite NoPreTrust", got)
				}
				return
			}
			projects, _ := readClaudeJSON(t, stub)["projects"].(map[string]any)
			entry, _ := projects[e.r.CWD].(map[string]any)
			if b, _ := entry["hasTrustDialogAccepted"].(bool); !b {
				t.Errorf("projects[%q] = %v; want hasTrustDialogAccepted true", e.r.CWD, entry)
			}
		})
	}
}

// TestLaunchMissingClaudeJSONDoesNotBlockSpawn: with no .claude.json the
// pre-trust warns once naming the file, Launch reports failed, and the spawn
// still launches.
func TestLaunchMissingClaudeJSONDoesNotBlockSpawn(t *testing.T) {
	e := newLaunchEnv(t)
	stub := withStubClaudeJSON(t) // a path that is never created
	warn := capturePreTrustWarn(t)

	id, outcome := e.mustLaunchOutcome()
	e.onlyCreate()
	if outcome != PreTrustFailed {
		t.Errorf("Launch outcome = %q; want %q", outcome, PreTrustFailed)
	}
	if row := e.row(id); row.State != store.StatePending || row.NoPreTrust {
		t.Errorf("row = {state %q, NoPreTrust %v}; want pending with pre-trust allowed", row.State, row.NoPreTrust)
	}
	assertOneFailedLine(t, warn.String(), stub)
}

// TestLaunchPassesUserSuppliedTmuxSessionName pins SR-4.1/SR-3.1: a
// caller-supplied name reaches the create and the row verbatim.
func TestLaunchPassesUserSuppliedTmuxSessionName(t *testing.T) {
	e := newLaunchEnv(t)
	e.r.TmuxSessionName, e.r.TmuxSessionNameSupplied = "bot-claude-status", true
	id := e.mustLaunch()
	if got := e.onlyCreate().Target; got != "bot-claude-status" {
		t.Errorf("create name = %q; want bot-claude-status (verbatim)", got)
	}
	if got := e.row(id).TmuxSessionName; got != "bot-claude-status" {
		t.Errorf("row.TmuxSessionName = %q; want bot-claude-status", got)
	}
}

// TestLaunchSecondInsertSurfacesCollision: a second Launch of the same id
// collides at the insert and makes no create call.
func TestLaunchSecondInsertSurfacesCollision(t *testing.T) {
	e := newLaunchEnv(t)
	e.mustLaunch()
	e.rec.Reset()
	if _, _, err := e.launch(); !errors.Is(err, ErrInstanceIdCollision) {
		t.Fatalf("second Launch err = %v; want ErrInstanceIdCollision", err)
	}
	if calls := e.rec.SocketCalls(); len(calls) != 0 {
		t.Errorf("tmux calls after collision = %+v; want none", calls)
	}
}
