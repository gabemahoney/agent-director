package spawn

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/codexfix"
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

// Verified parser spellings must not override the selected session or print mode.
func TestRelaunchClaudeStoredReplay(t *testing.T) {
	cases := []struct {
		name       string
		args, want []string
	}{
		{"resume short", []string{"-r", "OLD"}, nil},
		{"resume attached", []string{"-rOLD"}, nil},
		{"resume short equals", []string{"-r=OLD"}, nil},
		{"resume long", []string{"--resume", "OLD"}, nil},
		{"resume equals", []string{"--resume=OLD"}, nil},
		{"resume picker", []string{"--resume"}, nil},
		{"continue short", []string{"-c"}, nil},
		{"continue long", []string{"--continue"}, nil},
		{"fork", []string{"--fork-session"}, nil},
		{"pr detached", []string{"--from-pr", "42"}, nil},
		{"pr equals", []string{"--from-pr=42"}, nil},
		{"pr picker", []string{"--from-pr"}, nil},
		{"teleport detached", []string{"--teleport", "OLD"}, nil},
		{"teleport equals", []string{"--teleport=OLD"}, nil},
		{"teleport picker", []string{"--teleport"}, nil},
		{"resume at", []string{"--resume-session-at", "message"}, nil},
		{"resume at equals", []string{"--resume-session-at=message"}, nil},
		{"drops turn", []string{"--resume-drops-turn", "message"}, nil},
		{"drops turn equals", []string{"--resume-drops-turn=message"}, nil},
		{"rewind", []string{"--rewind-files", "message"}, nil},
		{"rewind equals", []string{"--rewind-files=message"}, nil},
		{"reply", []string{"--reply-on-resume"}, nil},
		{"cloud detached", []string{"--cloud", "OLD"}, nil},
		{"cloud equals", []string{"--cloud=OLD"}, nil},
		{"cloud picker", []string{"--cloud"}, nil},
		{"remote detached", []string{"--remote", "OLD"}, nil},
		{"remote equals", []string{"--remote=OLD"}, nil},
		{"remote picker", []string{"--remote"}, nil},
		{"attach", []string{"--attach-serve", "OLD"}, nil},
		{"attach equals", []string{"--attach-serve=OLD"}, nil},
		{"print short", []string{"-p"}, nil},
		{"print long", []string{"--print"}, nil},
		{"print continue cluster", []string{"-pc"}, nil},
		{"print preserves cluster order", []string{"-vpcd"}, []string{"-vd"}},
		{"cluster survivor", []string{"-cpd"}, []string{"-d"}},
		{"continue debug", []string{"-cd"}, []string{"-d"}},
		{"debug owns c", []string{"-dc"}, []string{"-dc"}},
		{"exact stderr option", []string{"-d2e"}, []string{"-d2e"}},
		{"name attached", []string{"-nrocket"}, []string{"-nrocket"}},
		{"model owns c", []string{"--model", "-c"}, []string{"--model", "-c"}},
		{"prompt owns dash value", []string{"--append-system-prompt", "-concise answers"}, []string{"--append-system-prompt", "-concise answers"}},
		{"prompt owns denied long", []string{"--append-system-prompt", "--resume", "foo"}, []string{"--append-system-prompt", "--resume", "foo"}},
		{"new session id survives", []string{"--session-id", "new-uuid"}, []string{"--session-id", "new-uuid"}},
		{"repeated mixed modes", []string{"-rOLD", "--model", "sonnet", "--resume=OLDER", "--print", "-pc"}, []string{"--model", "sonnet"}},
		{"orphan cloud controls", []string{"--ref", "main", "--cloud", "OLD", "--forward-home-settings", "false", "--on-branch=work"}, nil},
		{"orphan remote owned values", []string{"--remote=OLD", "--ref", "--environment", "--on-branch", "--pool", "--forward-home-settings=0"}, nil},
		{"environment preserves controls", []string{"--ref", "main", "--cloud=OLD", "--environment", "env-id", "--forward-home-settings=false", "--on-branch", "work"}, []string{"--ref", "main", "--environment", "env-id", "--forward-home-settings=false", "--on-branch", "work"}},
		{"pool preserves controls", []string{"--remote", "OLD", "--pool=pool-id", "--on-branch=work", "--forward-home-settings", "1", "--ref=main"}, []string{"--pool=pool-id", "--on-branch=work", "--forward-home-settings", "1", "--ref=main"}},
		{"owned environment is no context", []string{"--cloud=OLD", "--append-system-prompt", "--environment", "--ref", "main"}, []string{"--append-system-prompt", "--environment"}},
		{"delimiter context is prompt", []string{"--cloud=OLD", "--ref", "main", "--", "--pool", "pool-id"}, []string{"--", "--pool", "pool-id"}},
		{"no selector no cleanup", []string{"--ref", "main", "--forward-home-settings", "false", "--on-branch=work"}, []string{"--ref", "main", "--forward-home-settings", "false", "--on-branch=work"}},
		{"owned cloud no cleanup", []string{"--append-system-prompt", "--cloud", "--ref", "main"}, []string{"--append-system-prompt", "--cloud", "--ref", "main"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newRelaunchEnv(t)
			// Benign options bracket each case to expose ordering and over-removal.
			e.row.AgentArgs = slices.Concat([]string{"--model", "opus"}, tc.args, []string{"--verbose"})
			before := slices.Clone(e.row.AgentArgs)
			if out := e.relaunch("LATEST"); out.Kind != CreateLabelled {
				t.Fatalf("relaunch: %+v", out)
			}
			argv := e.onlyCreate().Command
			want := slices.Concat([]string{"--model", "opus"}, tc.want, []string{"--verbose"})
			if !slices.Equal(argv[:4], []string{"claude", "--resume", "LATEST", "--settings"}) || !slices.Equal(argv[5:], want) {
				t.Errorf("argv = %q; want latest-session prefix and survivors %q", argv, want)
			}
			if !slices.Equal(e.row.AgentArgs, before) {
				t.Errorf("stored args = %q; want unchanged %q", e.row.AgentArgs, before)
			}
		})
	}
}

// Replay repairs only argument boundaries whose removal would change parsed roles.
func TestRelaunchClaudeReplayRoles(t *testing.T) {
	cases := []struct {
		name               string
		args, want, prompt []string
		options            [][]string
	}{
		{"optional with later option", []string{"--debug", "--continue", "fix this", "--model", "sonnet"}, []string{"--debug", "--model", "sonnet", "--", "fix this"}, []string{"fix this"}, [][]string{{"--debug"}, {"--model", "sonnet"}}},
		{"list boundary", []string{"--tools", "Read", "--continue", "prompt"}, []string{"--tools", "Read", "--", "prompt"}, []string{"prompt"}, [][]string{{"--tools", "Read"}}},
		{"print optional with later option", []string{"--debug", "--print", "fix this", "--model", "sonnet"}, []string{"--debug", "--model", "sonnet", "--", "fix this"}, []string{"fix this"}, [][]string{{"--debug"}, {"--model", "sonnet"}}},
		{"print list boundary", []string{"--tools", "Read", "-p", "prompt"}, []string{"--tools", "Read", "--", "prompt"}, []string{"prompt"}, [][]string{{"--tools", "Read"}}},
		{"continue cluster exact short collision", []string{"-cd2e"}, []string{"--debug=2e"}, nil, [][]string{{"--debug", "2e"}}},
		{"print cluster exact short collision", []string{"-pd2e"}, []string{"--debug=2e"}, nil, [][]string{{"--debug", "2e"}}},
		{"safe prompt keeps position", []string{"fix this", "--continue", "--model=sonnet", "-d2e"}, []string{"fix this", "--model=sonnet", "-d2e"}, []string{"fix this"}, [][]string{{"--model", "sonnet"}, {"--debug-to-stderr"}}},
		{"owned optional value keeps spelling", []string{"-dnetwork", "--continue", "--model=sonnet"}, []string{"-dnetwork", "--model=sonnet"}, nil, [][]string{{"--debug", "network"}, {"--model", "sonnet"}}},
		{"safe list boundary keeps spelling", []string{"--tools", "Read", "--continue", "--model=sonnet", "fix this"}, []string{"--tools", "Read", "--model=sonnet", "fix this"}, []string{"fix this"}, [][]string{{"--tools", "Read"}, {"--model", "sonnet"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newRelaunchEnv(t)
			e.row.AgentArgs = slices.Clone(tc.args)
			if out := e.relaunch("LATEST"); out.Kind != CreateLabelled {
				t.Fatalf("relaunch: %+v", out)
			}
			argv := e.onlyCreate().Command
			if len(argv) < 5 || !slices.Equal(argv[:4], []string{"claude", "--resume", "LATEST", "--settings"}) || !slices.Equal(argv[5:], tc.want) {
				t.Fatalf("argv = %q; want latest interactive resume and %q", argv, tc.want)
			}
			reading := ReadClaudeArgs(argv[1:])
			if len(reading.Options) < 2 || reading.Options[0].Canonical != "--resume" || !slices.Equal(reading.Options[0].Values, []string{"LATEST"}) || reading.Options[1].Canonical != "--settings" || !slices.Equal(reading.Options[1].Values, []string{argv[4]}) || reading.HasPrint() {
				t.Fatalf("composed AD options = %+v; want latest resume/settings and no print", reading)
			}
			for _, input := range []struct {
				args     []string
				injected bool
			}{{tc.args, false}, {argv[1:], true}} {
				options, prompt := relaunchClaudeReplayRoles(input.args, input.injected)
				if !reflect.DeepEqual(options, tc.options) || !slices.Equal(prompt, tc.prompt) {
					t.Errorf("parsed roles for %q: options %q, prompt %q; want %q, %q", input.args, options, prompt, tc.options, tc.prompt)
				}
			}
			if !slices.Equal(e.row.AgentArgs, tc.args) {
				t.Errorf("stored arguments changed: %q; want %q", e.row.AgentArgs, tc.args)
			}
		})
	}
}

func TestRelaunchClaudeIncompleteRequiredValues(t *testing.T) {
	for _, tc := range []struct {
		name, missing string
		args, want    []string
	}{
		{"scalar prompt exposure", "--model", []string{"--debug", "--continue", "prompt", "--model"}, []string{"--debug", "prompt", "--model"}},
		{"orphan controls with missing environment", "--environment", []string{"--debug", "--cloud", "OLD", "prompt", "--ref", "main", "--environment"}, []string{"--debug", "prompt", "--environment"}},
		{"required variadic print exposure", "--tools", []string{"--debug", "--print", "prompt", "--tools"}, []string{"--debug", "prompt", "--tools"}},
		{"cluster remains literal", "--pool", []string{"-cd2e", "prompt", "--pool"}, []string{"-d2e", "prompt", "--pool"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRelaunchEnv(t)
			e.row.AgentArgs = slices.Clone(tc.args)
			assertClaudeMissingValue(t, tc.args, tc.missing)
			if out := e.relaunch("LATEST"); out.Kind != CreateLabelled {
				t.Fatalf("relaunch: %+v", out)
			}
			argv := e.onlyCreate().Command
			if len(argv) < 5 || !slices.Equal(argv[:4], []string{"claude", "--resume", "LATEST", "--settings"}) || !slices.Equal(argv[5:], tc.want) {
				t.Fatalf("argv = %q; want latest resume and literal survivors %q", argv, tc.want)
			}
			reading := ReadClaudeArgs(argv[1:])
			if len(reading.Options) < 2 || reading.Options[0].Canonical != "--resume" || !slices.Equal(reading.Options[0].Values, []string{"LATEST"}) || reading.Options[1].Canonical != "--settings" || !slices.Equal(reading.Options[1].Values, []string{argv[4]}) || reading.HasPrint() {
				t.Fatalf("composed options = %+v; want AD resume/settings and no print", reading)
			}
			assertClaudeMissingValue(t, argv[1:], tc.missing)
			if !slices.Equal(e.row.AgentArgs, tc.args) {
				t.Fatalf("stored arguments changed: %q; want %q", e.row.AgentArgs, tc.args)
			}
		})
	}
}

func relaunchClaudeReplayRoles(args []string, injected bool) ([][]string, []string) {
	reading := ReadClaudeArgs(args)
	var options [][]string
	var prompt []string
	for _, option := range reading.Options {
		if (injected && option.Index < 4) || option.Canonical == "--continue" || option.Canonical == "--print" {
			continue
		}
		options = append(options, slices.Concat([]string{option.Canonical}, option.Values))
	}
	for _, index := range reading.Positionals {
		prompt = append(prompt, args[index])
	}
	return options, prompt
}

func TestRelaunchCodexStoredReplay(t *testing.T) {
	cases := []struct {
		name               string
		args, want, prompt []string
	}{
		{"resume id prompt", []string{"resume", "OLD", "continue the work"}, nil, []string{"continue the work"}},
		{"fork id prompt", []string{"fork", "OLD", "--all", "--", "--last"}, nil, []string{"--last"}},
		{"last treats first positional as prompt", []string{"resume", "--last", "--all", "--include-non-interactive", "OLD"}, nil, []string{"OLD"}},
		{"fork last", []string{"fork", "--last", "--all", "keep going"}, nil, []string{"keep going"}},
		{"selector after global option", []string{"--model", "o3", "resume", "OLD", "--model", "o4", "--", "resume --last"}, []string{"--model", "o3", "--model", "o4"}, []string{"resume --last"}},
		{"owned command and delimiter", []string{"--model", "resume", "--config", "--", "fork", "OLD", "fix it"}, []string{"--model", "resume", "--config", "--"}, []string{"fix it"}},
		{"fork invalid include preserved", []string{"fork", "OLD", "--include-non-interactive"}, []string{"--include-non-interactive"}, nil},
		{"prompt commands untouched", []string{"--model", "resume", "--", "fork --last"}, []string{"--model", "resume"}, []string{"fork --last"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newRelaunchEnv(t)
			e.row.Agent = store.AgentCodex
			e.cfg.Defaults.DisableAskUser = true
			e.row.AgentArgs = slices.Clone(tc.args)
			before := slices.Clone(e.row.AgentArgs)
			if out := e.relaunch("LATEST"); out.Kind != CreateLabelled {
				t.Fatalf("relaunch: %+v", out)
			}
			argv := e.onlyCreate().Command
			l := codexfix.Split(t, argv)
			if !slices.Equal(argv[:3], []string{"codex", "resume", "LATEST"}) || !slices.Equal(l.PassThrough(), tc.want) || !slices.Equal(l.AfterDashDashArgs(), tc.prompt) {
				t.Errorf("argv = %q; want latest resume, ordered options %q, original prompt %q", argv, tc.want, tc.prompt)
			}
			if last, _ := l.Last(); last.Kind != codexfix.KindQuestionTool || last.Bool {
				t.Errorf("last setting = %+v; want question tool off", last)
			}
			if !slices.Equal(e.row.AgentArgs, before) {
				t.Errorf("stored args = %q; want unchanged %q", e.row.AgentArgs, before)
			}
		})
	}
}

// newRelaunchEnv builds a relaunchEnv with a persisted-shaped claude row fed
// directly (GetSpawn's round-trip is the store's tests' concern); a launch path
// goes through e.cfg.Launch.ClaudePath.
func newRelaunchEnv(t *testing.T) *relaunchEnv {
	t.Helper()
	withStubExe(t, "/bin/agent-director")
	return &relaunchEnv{t: t, socket: isolateTmux(t), rec: tmuxfix.NewRecorder(), cfg: config.Default(),
		row: store.Spawn{
			InstanceID:      "id-relaunch-1",
			Agent:           store.AgentClaude,
			CWD:             "/tmp/relaunch-cwd",
			TmuxSessionName: "cd-relaunch-1",
			RelayMode:       "off",
			AgentArgs:       []string{"--model", "opus"},
			Labels:          map[string]string{"role": "worker"},
		}}
}

// relaunch composes the launch of e.row resuming sessionID and runs it on the
// Recorder, failing the test on a composition error.
func (e *relaunchEnv) relaunch(sessionID string) CreateOutcome {
	e.t.Helper()
	c, err := ComposeRelaunch(RelaunchInput{Row: e.row, SessionID: sessionID, Socket: e.socket,
		Token: relaunchToken, StoreID: relaunchStoreID}, e.cfg)
	if err != nil {
		e.t.Fatalf("ComposeRelaunch: %v", err)
	}
	return Relaunch(e.rec, c.Create)
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
// --settings <json>` then the row's agent_args.
func TestRelaunchEnvAndArgv(t *testing.T) {
	for _, extra := range []map[string]string{nil, {}, {"CLAUDE_CONFIG_DIR": "/home/bee/.claude-alt",
		"ANTHROPIC_API_KEY": "sk-ant-test", "CLAUDE_CODE_OAUTH_TOKEN": "sk-ant-oat01-test"}} {
		e := newRelaunchEnv(t)
		e.row.ExtraEnv = extra
		if out := e.relaunch("session-uuid-1"); out.Kind != CreateLabelled {
			t.Fatalf("Relaunch kind = %v (cause %v); want CreateLabelled", out.Kind, out.Cause)
		}
		c := e.onlyCreate()
		want := map[string]string{"AGENT_DIRECTOR_INSTANCE_ID": e.row.InstanceID,
			"AGENT_DIRECTOR_RELAY_MODE": e.row.RelayMode, "AGENT_DIRECTOR_LABEL_ROLE": "worker"}
		for k, v := range extra {
			want[k] = v
		}
		if !reflect.DeepEqual(c.Envs, want) {
			t.Errorf("ExtraEnv=%v: relaunch env = %v; want exactly %v", extra, c.Envs, want)
		}
		if len(c.Command) < 5 || !reflect.DeepEqual(c.Command[:4], []string{"claude", "--resume", "session-uuid-1", "--settings"}) ||
			!reflect.DeepEqual(c.Command[5:], e.row.AgentArgs) {
			t.Errorf("create argv = %v; want claude --resume session-uuid-1 --settings <json> %v", c.Command, e.row.AgentArgs)
		}
	}
}

// TestRelaunchLeavesPermissionsNil: permissions are not persisted, so the
// settings carry only the config's AskUserQuestion deny, no per-spawn overlay.
func TestRelaunchLeavesPermissionsNil(t *testing.T) {
	e := newRelaunchEnv(t)
	e.cfg.Defaults.DisableAskUser = true

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
	c, id := e.onlyCreate(), e.row.InstanceID
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
