package spawn

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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

// TestLaunchInsertsPendingAndCreatesSession: the pending row keeps the
// launch's fields verbatim (a supplied name, SR-4.1; extra env, SR-10; label
// keys), and one labelled create on the resolved socket carries them, with
// label keys normalised into env names (SRD §7.2 step 5).
func TestLaunchInsertsPendingAndCreatesSession(t *testing.T) {
	e := newLaunchEnv(t)
	e.r.TmuxSessionName, e.r.TmuxSessionNameSupplied = "bot-claude-status", true
	e.r.ExtraEnv = map[string]string{"CLAUDE_CONFIG_DIR": "/home/bee/.claude-alt", "ANTHROPIC_API_KEY": "sk-ant-test"}
	e.r.AgentDirectorLabels = map[string]string{"role": "worker", "my-key": "v1", "x.y.z": "v2", "with spaces": "v3"}
	id := e.mustLaunch()
	if id != "id-launch-1" {
		t.Errorf("Launch returned %q; want id-launch-1", id)
	}

	row := e.row(id)
	if row.State != store.StatePending || row.CWD != e.r.CWD || row.TmuxSessionName != "bot-claude-status" {
		t.Errorf("row = {state %q, cwd %q, name %q}; want pending, %q, bot-claude-status",
			row.State, row.CWD, row.TmuxSessionName, e.r.CWD)
	}
	if !reflect.DeepEqual(row.ClaudeArgs, []string{"--model", "opus"}) || !reflect.DeepEqual(row.Labels, e.r.AgentDirectorLabels) ||
		!reflect.DeepEqual(row.ExtraEnv, e.r.ExtraEnv) {
		t.Errorf("row args/labels/extra env = %v / %v / %v", row.ClaudeArgs, row.Labels, row.ExtraEnv)
	}

	c := e.onlyCreate()
	if c.Socket != e.socket || c.Target != "bot-claude-status" || c.Cwd != e.r.CWD {
		t.Errorf("create = {socket %q, name %q, cwd %q}; want %q, bot-claude-status, %q", c.Socket, c.Target, c.Cwd, e.socket, e.r.CWD)
	}
	if c.Token != row.Identity.Token || c.InstanceID != id || c.StoreID != e.s.StoreID() {
		t.Errorf("create label args = {%q %q %q}; want {%q %q %q}", c.Token, c.InstanceID, c.StoreID,
			row.Identity.Token, id, e.s.StoreID())
	}
	n := len(c.Command)
	if n < 5 || c.Command[0] != "claude" || c.Command[1] != "--settings" || c.Command[n-2] != "--model" || c.Command[n-1] != "opus" {
		t.Errorf("create command = %v; want claude --settings <json> --model opus", c.Command)
	}
	for k, want := range map[string]string{"AGENT_DIRECTOR_RELAY_MODE": "off", "AGENT_DIRECTOR_LABEL_ROLE": "worker",
		"AGENT_DIRECTOR_LABEL_MY_KEY": "v1", "AGENT_DIRECTOR_LABEL_X_Y_Z": "v2", "AGENT_DIRECTOR_LABEL_WITH_SPACES": "v3",
		"CLAUDE_CONFIG_DIR": "/home/bee/.claude-alt", "ANTHROPIC_API_KEY": "sk-ant-test"} {
		if c.Envs[k] != want {
			t.Errorf("create env %s = %q; want %q", k, c.Envs[k], want)
		}
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

// TestLaunchPreTrust pins b.f75, SR-5.2 and SR-22.6: Launch trusts the cwd
// unless NoPreTrust and reports ok or skipped silently; a missing .claude.json
// is failed with one warning naming it. Each creates the session and records
// the choice.
func TestLaunchPreTrust(t *testing.T) {
	const seed = `{"projects":{}}`
	cases := []struct {
		name       string
		noPreTrust bool
		missing    bool
		want       PreTrustOutcome
	}{
		{"trusted", false, false, PreTrustOK},
		{"opted out", true, false, PreTrustSkipped},
		{"missing file does not block the spawn", false, true, PreTrustFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newLaunchEnv(t)
			stub := withStubClaudeJSON(t)
			if !tc.missing {
				seedFile(t, stub, seed)
			}
			warn := capturePreTrustWarn(t)
			e.r.NoPreTrust = tc.noPreTrust
			id, outcome := e.mustLaunchOutcome()
			e.onlyCreate()
			if outcome != tc.want {
				t.Errorf("Launch outcome = %q; want %q", outcome, tc.want)
			}
			if row := e.row(id); row.State != store.StatePending || row.NoPreTrust != tc.noPreTrust {
				t.Errorf("row = {state %q, NoPreTrust %v}; want pending, %v", row.State, row.NoPreTrust, tc.noPreTrust)
			}
			switch tc.want {
			case PreTrustFailed:
				assertOneFailedLine(t, warn.String(), stub)
				return
			case PreTrustSkipped:
				if got := mustReadFile(t, stub); string(got) != seed {
					t.Errorf("claude.json = %q; want untouched despite NoPreTrust", got)
				}
			default:
				if !trusts(readClaudeJSON(t, stub), e.r.CWD) {
					t.Errorf("claude.json does not trust %q", e.r.CWD)
				}
			}
			if warn.Len() != 0 {
				t.Errorf("warning = %q; want nothing printed", warn.String())
			}
		})
	}
}

// TestLaunchSecondInsertSurfacesCollision: a second Launch of the same id (a
// row the pre-check missed, a race) collides at the insert with the
// pre-check's finished-row text (b.hjs) and makes no create call.
func TestLaunchSecondInsertSurfacesCollision(t *testing.T) {
	e := newLaunchEnv(t)
	id := e.mustLaunch()
	e.rec.Reset()
	if _, _, err := e.launch(); !errors.Is(err, ErrInstanceIdCollision) || err.Error() != "ErrInstanceIdCollision: "+id {
		t.Fatalf("second Launch err = %v; want ErrInstanceIdCollision: %s", err, id)
	}
	if calls := e.rec.SocketCalls(); len(calls) != 0 {
		t.Errorf("tmux calls after collision = %+v; want none", calls)
	}
}
