package spawn

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Resume's launch (SR-8.1, SR-3.5, SR-10): ComposeRelaunch's environment,
// argv and settings, and Relaunch's labelled create on the passed socket,
// observed through the Recorder's recorded create and session table.

const (
	relaunchToken   = "0123456789abcdef"
	relaunchStoreID = "store-relaunch-1"
)

// relaunchEnv is one resume launch under test: a per-test TMUX_TMPDIR (TMUX
// unset), its socket, a Recorder, the config and the input's row.
type relaunchEnv struct {
	t      *testing.T
	socket string
	rec    *tmuxfix.Recorder
	cfg    config.Config
	row    store.Spawn
}

// newRelaunchEnv builds a relaunchEnv with a persisted-shaped row fed directly
// (GetSpawn's round-trip is the store's tests' concern).
func newRelaunchEnv(t *testing.T) *relaunchEnv {
	t.Helper()
	withStubExe(t, "/bin/agent-director")
	return &relaunchEnv{t: t, socket: isolateTmux(t), rec: tmuxfix.NewRecorder(), cfg: config.Default(),
		row: store.Spawn{
			ClaudeInstanceID: "id-relaunch-1",
			CWD:              "/tmp/relaunch-cwd",
			TmuxSessionName:  "cd-relaunch-1",
			RelayMode:        "off",
			ClaudeArgs:       []string{"--model", "opus"},
			Labels:           map[string]string{"role": "worker"},
		}}
}

// relaunch composes the launch of e.row resuming sessionID and runs it on the
// Recorder, failing the test on a composition error.
func (e *relaunchEnv) relaunch(sessionID string) CreateOutcome {
	e.t.Helper()
	req, err := ComposeRelaunch(RelaunchInput{Row: e.row, SessionID: sessionID, Socket: e.socket,
		Token: relaunchToken, StoreID: relaunchStoreID}, e.cfg)
	if err != nil {
		e.t.Fatalf("ComposeRelaunch: %v", err)
	}
	return Relaunch(e.rec, req)
}

// onlyCreate returns the one create Relaunch made.
func (e *relaunchEnv) onlyCreate() tmuxfix.SocketCall {
	e.t.Helper()
	calls := e.rec.SocketCallsOf(tmux.CallCreate)
	if len(calls) != 1 {
		e.t.Fatalf("creates = %+v; want exactly one", calls)
	}
	return calls[0]
}

// TestRelaunchEnvAndArgv (SR-10): the create's environment is exactly the
// base keys, one variable per row label and the row's ExtraEnv verbatim (none
// for a legacy '{}' column), and its argv is `claude --resume <session id>
// --settings <json>` then the row's claude_args.
func TestRelaunchEnvAndArgv(t *testing.T) {
	for _, extra := range []map[string]string{nil, {}, {"CLAUDE_CONFIG_DIR": "/home/bee/.claude-alt",
		"ANTHROPIC_API_KEY": "sk-ant-test", "CLAUDE_CODE_OAUTH_TOKEN": "sk-ant-oat01-test"}} {
		e := newRelaunchEnv(t)
		e.row.ExtraEnv = extra
		if out := e.relaunch("session-uuid-1"); out.Kind != CreateLabelled {
			t.Fatalf("Relaunch kind = %v (cause %v); want CreateLabelled", out.Kind, out.Cause)
		}
		c := e.onlyCreate()
		want := map[string]string{"AGENT_DIRECTOR_INSTANCE_ID": e.row.ClaudeInstanceID,
			"AGENT_DIRECTOR_RELAY_MODE": e.row.RelayMode, "AGENT_DIRECTOR_LABEL_ROLE": "worker"}
		for k, v := range extra {
			want[k] = v
		}
		if !reflect.DeepEqual(c.Envs, want) {
			t.Errorf("ExtraEnv=%v: relaunch env = %v; want exactly %v", extra, c.Envs, want)
		}
		if len(c.Command) < 5 || !reflect.DeepEqual(c.Command[:4], []string{"claude", "--resume", "session-uuid-1", "--settings"}) ||
			!reflect.DeepEqual(c.Command[5:], e.row.ClaudeArgs) {
			t.Errorf("create argv = %v; want claude --resume session-uuid-1 --settings <json> %v", c.Command, e.row.ClaudeArgs)
		}
	}
}

// TestRelaunchLeavesPermissionsNil: permissions are not persisted, so the
// settings carry only the config's AskUserQuestion deny, no per-spawn overlay.
func TestRelaunchLeavesPermissionsNil(t *testing.T) {
	e := newRelaunchEnv(t)
	e.cfg.Defaults.DisableAskUserQuestion = true

	e.relaunch("s1")
	c := e.onlyCreate()
	var got settingsShape
	if err := json.Unmarshal([]byte(c.Command[4]), &got); err != nil {
		t.Fatalf("settings %q: %v", c.Command[4], err)
	}
	want := map[string]any{"deny": []any{"AskUserQuestion"}}
	if !reflect.DeepEqual(got.Permissions, want) {
		t.Errorf("settings permissions = %v; want only %v (no per-spawn allow/ask/deny)", got.Permissions, want)
	}
	if _, has := reflect.TypeOf(store.Spawn{}).FieldByName("Permissions"); has {
		t.Error("store.Spawn grew a Permissions field; Relaunch must not reconstruct un-persisted Permissions")
	}
}

// TestRelaunchLabelledCreateOnPassedSocket: the create names the passed socket
// and the row's name and cwd, and labels the session and its pane with the
// passed token and store id. Relaunch is CreateAndLabel, whose label by id of
// a $ or \ name launch_label_test.go covers.
func TestRelaunchLabelledCreateOnPassedSocket(t *testing.T) {
	e := newRelaunchEnv(t)
	out := e.relaunch("session-uuid-1")
	if out.Kind != CreateLabelled {
		t.Fatalf("Relaunch kind = %v (cause %v); want CreateLabelled", out.Kind, out.Cause)
	}
	c, id := e.onlyCreate(), e.row.ClaudeInstanceID
	if c.Socket != e.socket || c.Target != e.row.TmuxSessionName || c.Cwd != e.row.CWD ||
		c.Token != relaunchToken || c.InstanceID != id || c.StoreID != relaunchStoreID {
		t.Errorf("create = %+v; want socket %q, name %q, cwd %q, label {%q %q %q}", c, e.socket, e.row.TmuxSessionName,
			e.row.CWD, relaunchToken, id, relaunchStoreID)
	}
	after := e.rec.Sessions(e.socket)[0]
	if want := tmuxfix.Valid(relaunchToken, id, relaunchStoreID); after.Label != want || after.Panes[0].AdPane != relaunchToken {
		t.Errorf("session = %+v; want label %+v and pane label %s", after, want, relaunchToken)
	}
	if out.Reply.SessionID != after.ID || out.Reply.PaneID != after.Panes[0].ID {
		t.Errorf("outcome reply = %+v; want session %q pane %q", out.Reply, after.ID, after.Panes[0].ID)
	}
}

// TestRelaunchWritesNoTrustEntry pins SR-8.1 step 4: resume pre-trusts before
// its move, so composing and creating writes neither .claude.json, opted out or not.
func TestRelaunchWritesNoTrustEntry(t *testing.T) {
	const seed = `{"projects":{}}`
	for _, noPreTrust := range []bool{false, true} {
		t.Run(fmt.Sprintf("NoPreTrust=%v", noPreTrust), func(t *testing.T) {
			e := newRelaunchEnv(t)
			home := withStubClaudeJSON(t)
			seedFile(t, home, seed)
			dir := t.TempDir()
			seedFile(t, filepath.Join(dir, ".claude.json"), seed)
			e.row.ExtraEnv = map[string]string{"CLAUDE_CONFIG_DIR": dir}
			e.row.NoPreTrust = noPreTrust

			if out := e.relaunch("s1"); out.Kind != CreateLabelled {
				t.Fatalf("Relaunch kind = %v (cause %v); want CreateLabelled", out.Kind, out.Cause)
			}
			for _, p := range []string{home, filepath.Join(dir, ".claude.json")} {
				if got := mustReadFile(t, p); string(got) != seed {
					t.Errorf("%s = %q; want byte-identical %q (no trust entry at create time)", p, got, seed)
				}
			}
		})
	}
}
