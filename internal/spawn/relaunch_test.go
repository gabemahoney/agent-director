package spawn

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
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

// TestRelaunchRestoresExtraEnvVerbatim: the row's ExtraEnv reaches the create's
// environment verbatim and the argv starts `claude --resume <session id> --settings`.
func TestRelaunchRestoresExtraEnvVerbatim(t *testing.T) {
	e := newRelaunchEnv(t)
	e.row.ExtraEnv = map[string]string{
		"CLAUDE_CONFIG_DIR":       "/home/bee/.claude-alt",
		"ANTHROPIC_API_KEY":       "sk-ant-test",
		"CLAUDE_CODE_OAUTH_TOKEN": "sk-ant-oat01-test",
	}

	if out := e.relaunch("session-uuid-1"); out.Kind != CreateLabelled {
		t.Fatalf("Relaunch kind = %v (cause %v); want CreateLabelled", out.Kind, out.Cause)
	}
	c := e.onlyCreate()
	for k, want := range e.row.ExtraEnv {
		if got := c.Envs[k]; got != want {
			t.Errorf("create env[%q] = %q; want %q (ExtraEnv restored verbatim)", k, got, want)
		}
	}
	want := []string{"claude", "--resume", "session-uuid-1", "--settings"}
	if len(c.Command) < 5 || !reflect.DeepEqual(c.Command[:4], want) {
		t.Errorf("create argv = %v; want prefix %v then the settings", c.Command, want)
	}
	if got, wantArgs := c.Command[5:], e.row.ClaudeArgs; !reflect.DeepEqual(got, wantArgs) {
		t.Errorf("create argv after the settings = %v; want the row's claude_args %v", got, wantArgs)
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

// TestRelaunchLegacyEmptyExtraEnvBaseline: a nil or empty ExtraEnv (the '{}'
// column) yields exactly the base keys plus one label variable per row label.
func TestRelaunchLegacyEmptyExtraEnvBaseline(t *testing.T) {
	for _, extra := range []map[string]string{nil, {}} {
		e := newRelaunchEnv(t)
		e.row.ExtraEnv = extra
		e.relaunch("s1")
		envs := e.onlyCreate().Envs

		want := map[string]string{
			"AGENT_DIRECTOR_INSTANCE_ID": e.row.ClaudeInstanceID,
			"AGENT_DIRECTOR_RELAY_MODE":  e.row.RelayMode,
		}
		for k, v := range e.row.Labels {
			want["AGENT_DIRECTOR_LABEL_"+normalizeLabelKey(k)] = v
		}
		if got, wantKeys := sortedKeys(envs), sortedKeys(want); !reflect.DeepEqual(got, wantKeys) {
			t.Errorf("ExtraEnv=%v: env key set = %v; want %v", extra, got, wantKeys)
		}
		if !reflect.DeepEqual(envs, want) {
			t.Errorf("ExtraEnv=%v: relaunch env = %v; want exactly %v", extra, envs, want)
		}
	}
}

// TestRelaunchLabelledCreateOnPassedSocket: the create names the passed socket
// and the row's name; a plain name is labelled by the chain, a $ or \ name by id.
func TestRelaunchLabelledCreateOnPassedSocket(t *testing.T) {
	cases := []struct {
		name      string
		labelByID bool
	}{
		{"cd-relaunch-1", false},
		{`a$b`, true},
		{`a\b`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newRelaunchEnv(t)
			e.row.TmuxSessionName = tc.name
			var atCreate tmuxfix.SeedSession
			e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) { atCreate = e.rec.Sessions(e.socket)[0] })

			out := e.relaunch("session-uuid-1")
			if out.Kind != CreateLabelled {
				t.Fatalf("Relaunch kind = %v (cause %v); want CreateLabelled", out.Kind, out.Cause)
			}
			c := e.onlyCreate()
			if c.Socket != e.socket || c.Target != tc.name || c.Cwd != e.row.CWD {
				t.Errorf("create {socket %q, name %q, cwd %q}; want {%q %q %q}", c.Socket, c.Target, c.Cwd, e.socket, tc.name, e.row.CWD)
			}
			id := e.row.ClaudeInstanceID
			if c.Token != relaunchToken || c.InstanceID != id || c.StoreID != relaunchStoreID {
				t.Errorf("create label args {%q %q %q}; want {%q %q %q}", c.Token, c.InstanceID, c.StoreID, relaunchToken, id, relaunchStoreID)
			}
			if chained := atCreate.LabelSet || atCreate.Panes[0].AdPane != ""; chained == tc.labelByID {
				t.Errorf("session after the create = %+v; chained label = %v, want %v", atCreate, chained, !tc.labelByID)
			}

			labels := e.rec.SocketCallsOf(tmux.CallSetLabel)
			switch {
			case !tc.labelByID && len(labels) != 0:
				t.Errorf("labels by id = %+v; want none for a chained name", labels)
			case tc.labelByID && len(labels) != 1:
				t.Fatalf("labels by id = %+v; want exactly one", labels)
			case tc.labelByID:
				l := labels[0]
				if l.Socket != e.socket || l.Target != atCreate.ID || l.PaneID != atCreate.Panes[0].ID ||
					l.Token != relaunchToken || l.InstanceID != id || l.StoreID != relaunchStoreID {
					t.Errorf("label by id = %+v; want socket %q, session %q, pane %q, value {%q %q %q}",
						l, e.socket, atCreate.ID, atCreate.Panes[0].ID, relaunchToken, id, relaunchStoreID)
				}
			}

			after := e.rec.Sessions(e.socket)[0]
			if want := tmuxfix.Valid(relaunchToken, id, relaunchStoreID); after.Label != want || after.Panes[0].AdPane != relaunchToken {
				t.Errorf("session = %+v; want label ad1 %s %s %s %s and pane label %s",
					after, relaunchToken, after.ID, id, relaunchStoreID, relaunchToken)
			}
			if out.Reply.SessionID != after.ID || out.Reply.PaneID != after.Panes[0].ID {
				t.Errorf("outcome reply = %+v; want session %q pane %q", out.Reply, after.ID, after.Panes[0].ID)
			}
		})
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

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
