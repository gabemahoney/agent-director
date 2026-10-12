package spawn

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/codexfix"
)

// Codex composition (SR-5.0 to SR-5.3, SR-7.6, SR-9, SR-10.1): which settings
// a Codex launch carries, asserted through codexfix's splitter, never on whole
// argv strings. Placement, duplicates and resume are in
// codex_placement_test.go; hash values are in codex_hook_trust_test.go.

const (
	// codexTestExe and codexTestHelp are paths with a space, a single quote and
	// a backslash, so every hook command reads its path back through the shell
	// quoting.
	codexTestExe  = `/opt/sp ace/it's\bin/agent-director`
	codexTestHelp = `/home/op dir/it's/.config/agent-director/bin/agent-director`
	// codexTestSession is the session a resume composes for.
	codexTestSession = "019f0000-0000-7000-8000-0000000000aa"
)

// codexIn is one Codex launch's inputs, whichever composer runs it.
type codexIn struct {
	args       []string
	cwd        string
	extraEnv   map[string]string
	noPreTrust bool
}

// codexOut is what a composer returns: the create's argv and environment, the
// pre-trust outcome the argv was built for, and the argv index where the
// caller's arguments start (1 after the program, 3 after `resume <id>`).
type codexOut struct {
	argv     []string
	env      map[string]string
	preTrust PreTrustOutcome
	start    int
}

// codexComposers are the two composition entry points: ComposeLaunch (a plain
// spawn, and reuse's reset, which composes through it too) and ComposeRelaunch
// (a resume).
var codexComposers = []struct {
	name    string
	compose func(t *testing.T, in codexIn, cfg config.Config) codexOut
}{
	{"spawn", func(t *testing.T, in codexIn, cfg config.Config) codexOut {
		t.Helper()
		c, err := ComposeLaunch(codexResolved(in), cfg)
		if err != nil {
			t.Fatalf("ComposeLaunch: %v", err)
		}
		return codexOut{c.Create.Command, c.Create.Env, c.PreTrust, 1}
	}},
	{"resume", func(t *testing.T, in codexIn, cfg config.Config) codexOut {
		t.Helper()
		row := store.Spawn{InstanceID: "id-codex-1", Agent: store.AgentCodex, CWD: in.cwd, TmuxSessionName: "cd-codex-1",
			RelayMode: "off", AgentArgs: in.args, Labels: map[string]string{"role": "worker"},
			ExtraEnv: in.extraEnv, NoPreTrust: in.noPreTrust}
		c, err := ComposeRelaunch(RelaunchInput{Row: row, SessionID: codexTestSession, Socket: "/tmp/sock", Token: relaunchToken, StoreID: relaunchStoreID}, cfg)
		if err != nil {
			t.Fatalf("ComposeRelaunch: %v", err)
		}
		return codexOut{c.Create.Command, c.Create.Env, c.PreTrust, 3}
	}},
}

// codexResolved is the Resolved of a Codex spawn or reuse for in.
func codexResolved(in codexIn) Resolved {
	return Resolved{SpawnParams: SpawnParams{
		Agent: store.AgentCodex, CWD: in.cwd, InstanceID: "id-codex-1", TmuxSessionName: "cd-codex-1",
		RelayMode: "off", AgentArgs: in.args, AgentDirectorLabels: map[string]string{"role": "worker"},
		ExtraEnv: in.extraEnv, NoPreTrust: in.noPreTrust,
	}}
}

// codexPreTrusts are the three pre-trust outcomes and the inputs that give
// each one (CodexPreTrust): off, a relative CODEX_HOME, neither.
var codexPreTrusts = []struct {
	name       string
	outcome    PreTrustOutcome
	noPreTrust bool
	extraEnv   map[string]string
}{
	{"ok", PreTrustOK, false, nil},
	{"skipped", PreTrustSkipped, true, nil},
	{"failed", PreTrustFailed, false, map[string]string{"CODEX_HOME": "relative/codex"}},
}

// codexConfig is the config in force: help hook, question tool and relay window.
func codexConfig(inject, questionOff bool, relay int) config.Config {
	cfg := config.Default()
	cfg.Defaults.InjectHelpHook, cfg.Defaults.DisableAskUser, cfg.Relay.TimeoutSeconds = inject, questionOff, relay
	return cfg
}

// codexWant is what a launch's settings must carry.
type codexWant struct {
	exe, helpBin        string
	relay               int
	inject, questionOff bool
	preTrust            PreTrustOutcome
	cwd                 string
}

// codexEventLabels are the labels hooks.state keys carry, from FORMAT.md.
var codexTestLabels = map[string]string{
	"SessionStart": "session_start", "UserPromptSubmit": "user_prompt_submit", "PreToolUse": "pre_tool_use",
	"PostToolUse": "post_tool_use", "PermissionRequest": "permission_request", "Stop": "stop",
	"Interrupt": "interrupt", "SessionEnd": "session_end",
}

// wantCodexTimeout is the timeout the AD entry of ev passes (SR-5.2).
func wantCodexTimeout(ev string, relay int) (int, bool) {
	switch ev {
	case "SessionStart":
		return 600, true
	case "PreToolUse", "PermissionRequest":
		return relay, true
	case "SessionEnd", "Interrupt":
		return 3, true
	}
	return 0, false
}

// assertCodexSettings checks every per-launch setting of l against w.
func assertCodexSettings(t *testing.T, l codexfix.Launch, w codexWant) {
	t.Helper()
	if n := len(l.Of(codexfix.KindNoDaemon)); n != 1 {
		t.Errorf("--no-daemon appears %d times; want once", n)
	}
	events := sorted(keysOf(codexTestLabels))
	if got := sorted(l.Events()); !slices.Equal(got, events) {
		t.Errorf("hook events = %v; want %v", got, events)
	}
	for _, ev := range events {
		if n := len(l.HookItems(ev)); n != 1 {
			t.Errorf("%s has %d -c values; want one (a repeat replaces the whole event)", ev, n)
		}
		assertADEntry(t, l, ev, w)
	}

	assertHelpEntry(t, l, w)
	assertDenyGroup(t, l, w)

	// hooks.state names exactly the handlers the launch registers.
	var wantKeys []string
	for _, h := range l.Hooks() {
		wantKeys = append(wantKeys, fmt.Sprintf("/<session-flags>/config.toml:%s:%d:%d", codexTestLabels[h.Event], h.Group, h.Handler))
	}
	state, ok := l.State()
	if !ok || len(l.Of(codexfix.KindState)) != 1 {
		t.Fatalf("hooks.state parts = %d; want one", len(l.Of(codexfix.KindState)))
	}
	if got := sorted(keysOf(state)); !slices.Equal(got, sorted(wantKeys)) {
		t.Errorf("hooks.state keys = %v; want exactly the registered handlers %v", got, wantKeys)
	}
	for k, h := range state {
		if len(h) != len("sha256:")+64 || !strings.HasPrefix(h, "sha256:") {
			t.Errorf("hooks.state[%s] = %q; want sha256:<64 hex>", k, h)
		}
	}

	if p := l.Of(codexfix.KindPaste); len(p) != 1 || !p[0].Bool {
		t.Errorf("paste settings = %+v; want one, true", p)
	}
	if n := len(l.Enabled(codexfix.FeatureWriteStdinApproval)); n != 1 {
		t.Errorf("--enable write_stdin_approval appears %d times; want once", n)
	}

	projects, bypass := l.Of(codexfix.KindProjects), l.Of(codexfix.KindBypass)
	if w.preTrust == PreTrustOK {
		if len(projects) != 1 || !reflect.DeepEqual(projects[0].Projects, map[string]string{w.cwd: "trusted"}) {
			t.Errorf("projects parts = %+v; want one trusting %q", projects, w.cwd)
		}
		if len(bypass) != 1 {
			t.Errorf("bypass appears %d times; want once", len(bypass))
		}
	} else if len(projects) != 0 || len(bypass) != 0 {
		t.Errorf("pre-trust %s: projects %+v, bypass %+v; want neither", w.preTrust, projects, bypass)
	}

	q, last := l.Of(codexfix.KindQuestionTool), l.Items[len(l.Items)-1]
	if w.questionOff {
		if len(q) != 1 || q[0].Bool || last.Kind != codexfix.KindQuestionTool {
			t.Errorf("question-tool parts = %+v, last part %v; want one, false and last", q, last.Kind)
		}
	} else if len(q) != 0 {
		t.Errorf("question-tool parts = %+v; want none while disable_ask_user is off", q)
	}
}

// assertADEntry checks AD's one entry for ev: group 0, handler 0, no matcher,
// the exec command, its timeout and nothing else.
func assertADEntry(t *testing.T, l codexfix.Launch, ev string, w codexWant) {
	t.Helper()
	var ours []codexfix.Hook
	for _, h := range l.HooksFor(ev) {
		if h.Kind == codexfix.HookAD {
			ours = append(ours, h)
		}
	}
	if len(ours) != 1 {
		t.Errorf("%s: %d agent-director entries; want 1", ev, len(ours))
		return
	}
	h := ours[0]
	wantWords := []string{"exec", w.exe, "hook", "--agent", "codex"}
	if ev == "PermissionRequest" {
		wantWords = append(wantWords, "--timeout", strconv.Itoa(w.relay))
	}
	if !slices.Equal(h.Words, wantWords) || h.Exec != w.exe || h.Agent != "codex" {
		t.Errorf("%s: command %q reads as %q; want %q", ev, h.Command, h.Words, wantWords)
	}
	if h.Group != 0 || h.Handler != 0 || h.HasMatcher || h.Type != "command" || !slices.Equal(h.GroupKeys, []string{"hooks"}) {
		t.Errorf("%s: group %d handler %d matcher %v type %q group keys %v; want 0, 0, none, command, [hooks]", ev, h.Group, h.Handler, h.HasMatcher, h.Type, h.GroupKeys)
	}
	timeout, has := wantCodexTimeout(ev, w.relay)
	wantKeys := []string{"command", "type"}
	if has {
		wantKeys = []string{"command", "timeout", "type"}
	}
	if h.HasTimeout != has || h.Timeout != timeout || h.HasLimit || !slices.Equal(h.HandlerKeys, wantKeys) {
		t.Errorf("%s: timeout %d (given %v) keys %v; want timeout %d (given %v) keys %v", ev, h.Timeout, h.HasTimeout, h.HandlerKeys, timeout, has, wantKeys)
	}
}

// assertHelpEntry checks the help entry: present only with inject_help_hook,
// on SessionStart in its own group of the AD entry's value.
func assertHelpEntry(t *testing.T, l codexfix.Launch, w codexWant) {
	t.Helper()
	help := l.HooksOfKind(codexfix.HookHelp)
	if !w.inject {
		if len(help) != 0 {
			t.Errorf("help entries = %+v; want none with inject_help_hook off", help)
		}
		return
	}
	if len(help) != 1 {
		t.Errorf("help entries = %d; want 1", len(help))
		return
	}
	h := help[0]
	if h.Event != "SessionStart" || h.Exec != w.helpBin || !slices.Equal(h.Words, []string{"exec", w.helpBin, "help"}) {
		t.Errorf("help entry = %q on %s (path %q); want exec <installed path> help on SessionStart", h.Command, h.Event, h.Exec)
	}
	if !h.HasLimit || h.Limit != 4000 || h.HasTimeout || h.HasMatcher || !slices.Equal(h.HandlerKeys, []string{"additionalContextLimit", "command", "type"}) {
		t.Errorf("help entry limit %d (%v) timeout %v matcher %v keys %v; want limit 4000 and nothing else", h.Limit, h.HasLimit, h.HasTimeout, h.HasMatcher, h.HandlerKeys)
	}
	if n := len(l.HookItems("SessionStart")); n != 1 {
		t.Errorf("SessionStart has %d -c values; want the help entry to share the one value", n)
	}
}

// assertDenyGroup checks the question-tool deny group: present only with
// disable_ask_user, a matcher group of the PreToolUse value.
func assertDenyGroup(t *testing.T, l codexfix.Launch, w codexWant) {
	t.Helper()
	deny := l.HooksOfKind(codexfix.HookDeny)
	if !w.questionOff {
		if len(deny) != 0 {
			t.Errorf("deny groups = %+v; want none while disable_ask_user is off", deny)
		}
		return
	}
	if len(deny) != 1 {
		t.Errorf("deny groups = %d; want 1", len(deny))
		return
	}
	h := deny[0]
	if h.Event != "PreToolUse" || !h.HasMatcher || h.Matcher != "request_user_input_async" || h.Group != 1 || h.Handler != 0 {
		t.Errorf("deny group = %+v; want PreToolUse group 1 matching request_user_input_async", h)
	}
	if n := len(l.HookItems("PreToolUse")); n != 1 {
		t.Errorf("PreToolUse has %d -c values; want the deny group in the one value with AD's entry", n)
	}
}

// TestCodexComposeSettings: for a spawn and a resume, over every pre-trust
// outcome, the question-tool setting, the help hook and the relay window
// (default, an hour and the largest AD accepts), the launch carries the
// settings of SR-5.2 and the environment of SR-10.1.
func TestCodexComposeSettings(t *testing.T) {
	const cwd = "/work/proj"
	for _, c := range codexComposers {
		for _, pt := range codexPreTrusts {
			for _, questionOff := range []bool{false, true} {
				for _, inject := range []bool{false, true} {
					for _, relay := range []int{0, 3600, 2147483} {
						t.Run(fmt.Sprintf("%s/%s/questionOff=%v/help=%v/relay=%d", c.name, pt.name, questionOff, inject, relay), func(t *testing.T) {
							withStubExe(t, codexTestExe)
							withStubHelpBin(t, codexTestHelp)
							t.Setenv("CODEX_HOME", "/host/codex-home")
							out := c.compose(t, codexIn{args: []string{"--model", "o3"}, cwd: cwd, extraEnv: pt.extraEnv, noPreTrust: pt.noPreTrust},
								codexConfig(inject, questionOff, relay))
							if out.preTrust != pt.outcome {
								t.Errorf("composition carries pre-trust %q; want %q", out.preTrust, pt.outcome)
							}
							if out.argv[0] != "codex" {
								t.Errorf("program = %q; want codex", out.argv[0])
							}
							wantRelay := relay
							if relay == 0 {
								wantRelay = config.DefaultRelayTimeoutSeconds
							}
							assertCodexSettings(t, codexfix.Split(t, out.argv), codexWant{exe: codexTestExe, helpBin: codexTestHelp,
								relay: wantRelay, inject: inject, questionOff: questionOff, preTrust: pt.outcome, cwd: cwd})

							// The environment: the base keys and the labels, the Codex
							// marker, an empty BASH_ENV, the extra env; no CODEX_HOME
							// from the host.
							want := map[string]string{"AGENT_DIRECTOR_INSTANCE_ID": "id-codex-1", "AGENT_DIRECTOR_RELAY_MODE": "off",
								"AGENT_DIRECTOR_LABEL_ROLE": "worker", "AGENT_DIRECTOR_AGENT": "codex", "BASH_ENV": ""}
							maps.Copy(want, pt.extraEnv)
							if !reflect.DeepEqual(out.env, want) {
								t.Errorf("env = %v; want exactly %v", out.env, want)
							}
						})
					}
				}
			}
		}
	}
}

// TestCodexComposeTrustsProjectCwd: on ok the folder trust names the cwd as
// given, whatever it holds (a dot, a space, quotes, a backslash).
func TestCodexComposeTrustsProjectCwd(t *testing.T) {
	cwds := map[string]string{
		"plain": "/work/proj", "dot": "/work/a.b/c", "space": "/work/with space", "single quote": "/work/it's",
		"double quote": `/work/q"uote`, "backslash": `/work/back\slash`, "unicode": "/work/café",
	}
	for _, c := range codexComposers {
		for name, cwd := range cwds {
			t.Run(c.name+"/"+name, func(t *testing.T) {
				withStubExe(t, codexTestExe)
				out := c.compose(t, codexIn{cwd: cwd}, codexConfig(false, false, 0))
				p := codexfix.Split(t, out.argv).Of(codexfix.KindProjects)
				if len(p) != 1 || !reflect.DeepEqual(p[0].Projects, map[string]string{cwd: "trusted"}) {
					t.Errorf("projects = %+v; want one trusting %q", p, cwd)
				}
			})
		}
	}
}

// TestCodexHelpEntryUnderHome: unstubbed, the help entry's installed path is
// under $HOME's .config/agent-director/bin, quoted when HOME has a space. It
// sets $HOME, so it is serial.
func TestCodexHelpEntryUnderHome(t *testing.T) {
	home := t.TempDir() + "/ho me"
	t.Setenv("HOME", home)
	withStubExe(t, "/opt/ad/bin/agent-director")
	for _, c := range codexComposers {
		t.Run(c.name, func(t *testing.T) {
			out := c.compose(t, codexIn{cwd: "/work/proj"}, codexConfig(true, false, 0))
			help := codexfix.Split(t, out.argv).HooksOfKind(codexfix.HookHelp)
			want := home + "/.config/agent-director/bin/agent-director"
			if len(help) != 1 || help[0].Exec != want {
				t.Errorf("help entries = %+v; want one running %q", help, want)
			}
		})
	}
}

// keysOf returns m's keys.
func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
