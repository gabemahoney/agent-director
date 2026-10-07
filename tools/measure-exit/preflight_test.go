package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// stubVersions is a versionReader double.
type stubVersions struct{ claude string }

func (v stubVersions) readVersions(string) (string, string, string, error) {
	return "0.0.0-test", v.claude, "/stub/bin/claude", nil
}

// pfSetup is one preflight input: a config, an environment and the dirs it
// lives in.
type pfSetup struct {
	cfg      config
	vars     map[string]string
	home     string
	passwd   string
	tmuxBase string
	cases    []string
}

// gatewayOnlyRefused are the variables that would bill real mode somewhere
// other than the gateway.
var gatewayOnlyRefused = []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "AWS_ACCESS_KEY_ID",
	"AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_BEARER_TOKEN_BEDROCK", "AWS_REGION", "AWS_PROFILE", "AWS_DEFAULT_REGION"}

// newPF builds a passing preflight setup for m; tests override fields.
func newPF(t *testing.T, m mode) *pfSetup {
	t.Helper()
	dir := t.TempDir()
	p := &pfSetup{home: filepath.Join(dir, "home"), passwd: filepath.Join(dir, "passwd"), tmuxBase: filepath.Join(dir, "tmuxbase")}
	for _, d := range []string{p.home, p.passwd, p.tmuxBase} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	p.cfg = config{mode: m, samples: 2, outDir: filepath.Join(dir, "out"), tmuxBase: p.tmuxBase, runID: "mx-test-run"}
	p.vars = map[string]string{"HOME": p.home, "PATH": "/usr/bin:/bin"}
	p.cases = []string{"rn6.idle"}
	switch m {
	case modeReal:
		p.cfg.samples = 20
		p.vars[containerMarkerEnv] = "1"
		p.vars["ANTHROPIC_AUTH_TOKEN"] = "gateway-token-value"
		p.vars["ANTHROPIC_MODEL"] = "gateway-opus-id"
	case modeDry:
		p.vars[sandboxMarkerEnv] = "1"
	case modeProbe:
		p.vars[containerMarkerEnv] = "1"
		p.vars["ANTHROPIC_AUTH_TOKEN"] = "mx-probe-dummy-token-not-a-secret"
		p.cases = []string{"probe.exec-form"}
	}
	return p
}

// run runs the preflight with claude reporting claudeVersion.
func (p *pfSetup) run(t *testing.T, claudeVersion string) (isolation, error) {
	t.Helper()
	sel, err := selectCases(caseRegistry, p.cases, p.cfg.mode)
	if err != nil {
		t.Fatal(err)
	}
	return preflight(p.cfg, fakeEnv(p.vars, p.passwd), stubVersions{claude: claudeVersion}, anySampled(sel))
}

// assertRefused checks err is a refusal naming rule.
func assertRefused(t *testing.T, err error, rule string) {
	t.Helper()
	var r *refusal
	if !errors.As(err, &r) {
		t.Fatalf("want refusal %s, got %v", rule, err)
	}
	if r.rule != rule || !strings.Contains(err.Error(), "("+rule+")") {
		t.Fatalf("want refusal %s, got %v", rule, err)
	}
}

func TestPreflightRules(t *testing.T) {
	// realCC is the deployed version, real mode's floor.
	const realCC, stubCC = "2.1.280 (Claude Code)", "2.1.285 (measure-exit dry-run stub)"
	// layer writes a layer file holding body and returns its path.
	layer := func(body string) string {
		path := filepath.Join(t.TempDir(), "layer.json")
		writeFile(t, path, body)
		return path
	}
	const offending = `{"env": {"AWS_REGION": "us-east-1"}}`
	// creds puts a Claude Code login at $HOME/.claude/.credentials.json (b.tba).
	creds := func(p *pfSetup) {
		writeFile(t, filepath.Join(p.home, ".claude", ".credentials.json"), `{"claudeAiOauth": {"accessToken": "`+layerSecret+`"}}`)
	}
	// envRules refuse before the private TMUX_TMPDIR is made: nothing is written.
	envRules := map[string]bool{ruleContainerOnly: true, ruleTmuxUnset: true, ruleHomeSet: true, ruleHomeHasStore: true,
		ruleSampleFloor: true, ruleCredential: true, ruleModelSet: true, ruleProbeCredential: true, ruleRealGatewayOnly: true}
	type pfCase struct {
		name   string
		mode   mode
		claude string
		edit   func(p *pfSetup)
		rule   string // "" passes
	}
	tests := []pfCase{
		{"real without the container marker", modeReal, realCC, func(p *pfSetup) { delete(p.vars, containerMarkerEnv) }, ruleContainerOnly},
		{"dry without either marker", modeDry, stubCC, func(p *pfSetup) { delete(p.vars, sandboxMarkerEnv) }, ruleContainerOnly},
		{"real with only the sandbox marker", modeReal, realCC, func(p *pfSetup) {
			delete(p.vars, containerMarkerEnv)
			p.vars[sandboxMarkerEnv] = "1"
		}, ruleContainerOnly},
		{"dry with the container marker", modeDry, stubCC, func(p *pfSetup) {
			delete(p.vars, sandboxMarkerEnv)
			p.vars[containerMarkerEnv] = "1"
		}, ""},
		{"TMUX set, even empty", modeDry, stubCC, func(p *pfSetup) { p.vars["TMUX"] = "" }, ruleTmuxUnset},
		{"HOME relative", modeDry, stubCC, func(p *pfSetup) { p.vars["HOME"] = "home" }, ruleHomeSet},
		{"store under HOME", modeDry, stubCC, func(p *pfSetup) { mkStore(t, p.home) }, ruleHomeHasStore},
		{"store under the passwd home", modeDry, stubCC, func(p *pfSetup) { mkStore(t, p.passwd) }, ruleHomeHasStore},
		{"real with 19 samples", modeReal, realCC, func(p *pfSetup) { p.cfg.samples = 19 }, ruleSampleFloor},
		{"real with 20 samples", modeReal, realCC, nil, ""},
		{"dry with 2 samples", modeDry, stubCC, nil, ""},
		{"real RN-9 only with 1 sample", modeReal, realCC, func(p *pfSetup) {
			p.cfg.samples = 1
			p.cases = []string{"rn9.drive", "rn9.resume"}
		}, ""},
		{"probe with 1 sample", modeProbe, realCC, func(p *pfSetup) { p.cfg.samples = 1 }, ""},
		{"real without a credential", modeReal, realCC, func(p *pfSetup) { delete(p.vars, "ANTHROPIC_AUTH_TOKEN") }, ruleCredential},
		{"real without ANTHROPIC_MODEL", modeReal, realCC, func(p *pfSetup) { delete(p.vars, "ANTHROPIC_MODEL") }, ruleModelSet},
		{"probe with a non-dummy token", modeProbe, realCC, func(p *pfSetup) { p.vars["ANTHROPIC_AUTH_TOKEN"] = "real-token" }, ruleProbeCredential},
		{"probe with no token", modeProbe, realCC, func(p *pfSetup) { delete(p.vars, "ANTHROPIC_AUTH_TOKEN") }, ""},
		{"dry with a non-stub claude", modeDry, realCC, nil, ruleDryStubOnly},
		{"real with Claude Code 2.1.279", modeReal, "2.1.279 (Claude Code)", nil, ruleVersionFloor},
		{"real with Claude Code 2.1.284", modeReal, "2.1.284 (Claude Code)", nil, ""},
		{"real with an unparseable version", modeReal, "Claude Code", nil, ruleVersionFloor},
		{"real with Claude Code 2.2.0", modeReal, "2.2.0 (Claude Code)", nil, ""},
		{"probe with Claude Code 2.1.120", modeProbe, "2.1.120 (Claude Code)", nil, ""},
		{"dry with a 2.1.120 stub", modeDry, "2.1.120 (measure-exit dry-run stub: exec-form args dropped)", nil, ""},
		{"real with a refused env name in the project layer", modeReal, realCC, func(p *pfSetup) { p.cfg.projectSettings = layer(offending) }, ruleRealGatewayOnly},
		{"real with a URL in the MCP layer's server env", modeReal, realCC, func(p *pfSetup) {
			p.cfg.mcpConfig = layer(`{"mcpServers": {"s": {"env": {"UPSTREAM": "https://x.invalid"}}}}`)
		}, ruleRealGatewayOnly},
		{"real with a refused env name in the user layer", modeReal, realCC, func(p *pfSetup) {
			writeFile(t, filepath.Join(p.home, ".claude", "settings.json"), offending)
		}, ruleRealGatewayOnly},
		{"real with the user layer a dangling link to the state file", modeReal, realCC, func(p *pfSetup) {
			if err := os.MkdirAll(filepath.Join(p.home, ".claude"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(p.home, claudeStateFile), filepath.Join(p.home, ".claude", "settings.json")); err != nil {
				t.Fatal(err)
			}
		}, ruleRealGatewayOnly},
		{"real with a project layer run.sh refuses (b.vyb)", modeReal, realCC, func(p *pfSetup) { p.cfg.projectSettings = credentialLayerCases[0].write(t) }, ruleRealGatewayOnly},
		{"real with a missing project layer", modeReal, realCC, func(p *pfSetup) { p.cfg.projectSettings = "/nonexistent/project.json" }, ""},
		{"dry with a refused env name in the project layer", modeDry, stubCC, func(p *pfSetup) { p.cfg.projectSettings = layer(offending) }, ""},
		{"probe with a refused env name in the user layer", modeProbe, realCC, func(p *pfSetup) {
			writeFile(t, filepath.Join(p.home, ".claude", "settings.json"), offending)
		}, ""},
		{"real with Claude Code's credentials file under HOME", modeReal, realCC, creds, ruleRealGatewayOnly},
		{"dry with Claude Code's credentials file under HOME", modeDry, stubCC, creds, ""},
		{"probe with Claude Code's credentials file under HOME", modeProbe, realCC, creds, ""},
	}
	for _, name := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_BASE_URL", "AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_BEARER_TOKEN_BEDROCK"} {
		name := name
		tests = append(tests, pfCase{"probe with " + name, modeProbe, realCC, func(p *pfSetup) { p.vars[name] = "x" }, ruleProbeCredential})
	}
	for _, name := range gatewayOnlyRefused {
		name := name
		tests = append(tests, pfCase{"real with " + name + " set, even empty", modeReal, realCC, func(p *pfSetup) { p.vars[name] = "" }, ruleRealGatewayOnly})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newPF(t, tc.mode)
			if tc.edit != nil {
				tc.edit(p)
			}
			_, err := p.run(t, tc.claude)
			if tc.rule == "" {
				if err != nil {
					t.Fatalf("want pass, got %v", err)
				}
				return
			}
			assertRefused(t, err, tc.rule)
			assertAbsent(t, "refusal", err.Error(), layerSecret)
			if envRules[tc.rule] {
				if entries, _ := os.ReadDir(p.tmuxBase); len(entries) != 0 {
					t.Errorf("an environment refusal created %d entries under the tmux base", len(entries))
				}
			}
		})
	}
}

// mkStore creates home/.agent-director.
func mkStore(t *testing.T, home string) {
	t.Helper()
	if err := os.Mkdir(filepath.Join(home, ".agent-director"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestPreflightPassRecordsIsolation(t *testing.T) {
	for _, m := range []mode{modeReal, modeDry} {
		t.Run(string(m), func(t *testing.T) {
			p := newPF(t, m)
			iso, err := p.run(t, "2.1.285 (measure-exit dry-run stub)")
			if err != nil {
				t.Fatal(err)
			}
			fi, err := os.Stat(iso.TmuxTmpdir)
			if err != nil || fi.Mode().Perm() != 0o700 || filepath.Dir(iso.TmuxTmpdir) != p.tmuxBase {
				t.Fatalf("private TMUX_TMPDIR %s: %v %v", iso.TmuxTmpdir, fi, err)
			}
			if iso.ExpectedSocket != privateSocket(iso.TmuxTmpdir) {
				t.Errorf("expected socket %s", iso.ExpectedSocket)
			}
			wantFloor := map[mode]string{modeReal: "2.1.280", modeDry: "exempt"}[m]
			if iso.Home != p.home || iso.PasswdHome != p.passwd || iso.Parallelism != 1 || iso.ClaudeVersionFloor != wantFloor ||
				iso.WorkDir != filepath.Join(p.home, "measure-exit-work") {
				t.Errorf("isolation %+v", iso)
			}
			summary := strings.Join(iso.summaryLines(), "\n")
			for _, want := range []string{p.home, p.passwd, iso.TmuxTmpdir, iso.ExpectedSocket, "mode:              " + string(m)} {
				if !strings.Contains(summary, want) {
					t.Errorf("summary lacks %q:\n%s", want, summary)
				}
			}
		})
	}
}

func TestCheckSocketPrivate(t *testing.T) {
	dir := "/tmp/mx-tmux-abc"
	for _, tc := range []struct {
		socket string
		ok     bool
	}{
		{dir + "/tmux-1000/default", true},
		{dir, false},
		{"/tmp/tmux-1000/default", false},
		{dir + "/../tmux-1000/default", false},
		{dir + "-evil/tmux-1000/default", false},
		{"tmux-1000/default", false},
		{"", false},
	} {
		err := checkSocketPrivate(dir, tc.socket)
		if tc.ok && err != nil {
			t.Errorf("%q: %v", tc.socket, err)
		}
		if !tc.ok {
			assertRefused(t, err, rulePrivateSocket)
		}
	}
}

func TestChildEnvDropsHostVariables(t *testing.T) {
	vars := map[string]string{
		"PATH": "/bin", "HOME": "/h", "ANTHROPIC_MODEL": "m", "ANTHROPIC_AUTH_TOKEN": "tok", "LC_CTYPE": "C",
		"TMUX": "/tmp/tmux-1/default,1,0", "TMUX_PANE": "%1", "CLAUDE_CONFIG_DIR": "/c", "CLAUDECODE": "1",
		"CLAUDE_CODE_SESSION_ID": "s", "CLAUDE_CODE_CHILD_SESSION": "1", "CLAUDE_PID": "9",
		"CLAUDE_CODE_MESSAGING_TOKEN": "x", "AGENT_DIRECTOR_INSTANCE_ID": "parent", "UNLISTED_HOST_VAR": "v",
	}
	got, err := fakeEnv(vars, "/h").childEnv("/tmp/mx-tmux-1", map[string]string{sessionEndBudgetEnv: "3000"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ANTHROPIC_AUTH_TOKEN=tok", "ANTHROPIC_MODEL=m", sessionEndBudgetEnv + "=3000", "HOME=/h",
		"LC_CTYPE=C", "PATH=/bin", "TMUX_TMPDIR=/tmp/mx-tmux-1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("child env\n got %q\nwant %q", got, want)
	}
	for _, name := range []string{"TMUX", "TMUX_TMPDIR", "CLAUDE_CONFIG_DIR", "CLAUDECODE", "CLAUDE_CODE_SESSION_ID", "AGENT_DIRECTOR_INSTANCE_ID"} {
		if _, err := fakeEnv(nil, "/h").childEnv("/tmp/x", map[string]string{name: "v"}); err == nil {
			t.Errorf("extra %s was not refused", name)
		}
	}
}

func TestRealModeEnvKeepsOnlyTheGateway(t *testing.T) {
	kept := []string{"ANTHROPIC_AUTH_TOKEN=tok", "ANTHROPIC_BASE_URL=https://g.invalid", "ANTHROPIC_MODEL=m", "PATH=/bin"}
	env := append([]string(nil), kept...)
	for _, name := range gatewayOnlyRefused {
		env = append(env, name+"=v")
	}
	if got := realModeEnv(env); !reflect.DeepEqual(got, kept) {
		t.Errorf("real mode's child env\n got %q\nwant %q", got, kept)
	}
}

func TestDryModeCarriesNoCredential(t *testing.T) {
	vars := map[string]string{"PATH": "/bin"}
	for k, v := range credentialSentinels {
		vars[k] = v
	}
	e := fakeEnv(vars, "/h")
	if got := withoutCredentials(e.environ()); !reflect.DeepEqual(got, []string{"PATH=/bin"}) {
		t.Errorf("withoutCredentials kept %q", got)
	}
	home := t.TempDir()
	rec, err := seedClaudeState(home, e.credentialFree(), "2.1.285 (measure-exit dry-run stub)")
	if err != nil {
		t.Fatal(err)
	}
	if rec.CredentialMode != credNone {
		t.Errorf("dry seed record %+v", rec)
	}
	assertAbsent(t, ".claude.json", readFile(t, rec.Path), sentinelValues()...)
}

// TestSeedClaudeState: the gateway token is the only credential mode, and
// no credential or approval entry is ever written.
func TestSeedClaudeState(t *testing.T) {
	for _, tc := range []struct {
		name string
		vars map[string]string
		mode string
	}{
		{"gateway", map[string]string{"ANTHROPIC_AUTH_TOKEN": "gateway-token-0123456789", "ANTHROPIC_BASE_URL": "https://g.invalid"}, credGateway},
		{"api key", map[string]string{"ANTHROPIC_API_KEY": "sk-ant-api03-0123456789-SUFFIX-abcdefghij0123"}, credNone},
		{"bedrock", map[string]string{"CLAUDE_CODE_USE_BEDROCK": "1", "AWS_SECRET_ACCESS_KEY": "aws-secret-0123456789"}, credNone},
		{"oauth", map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "oauth-token-0123456789"}, credNone},
		{"none", nil, credNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			rec, err := seedClaudeState(home, fakeEnv(tc.vars, home), "2.1.280 (Claude Code)")
			if err != nil {
				t.Fatal(err)
			}
			if rec.Path != filepath.Join(home, ".claude.json") || rec.CredentialMode != tc.mode {
				t.Fatalf("record %+v", rec)
			}
			fi, _ := os.Stat(rec.Path)
			body := readFile(t, rec.Path)
			if fi.Mode().Perm() != 0o600 || !strings.Contains(body, `"lastOnboardingVersion": "2.1.280"`) {
				t.Errorf("mode %v, body %s", fi.Mode(), body)
			}
			if strings.Contains(body, "customApiKeyResponses") {
				t.Errorf("an API-key approval entry was written: %s", body)
			}
			for _, v := range tc.vars {
				if len(v) > 4 {
					assertAbsent(t, ".claude.json", body, v[len(v)-12:])
				}
			}
		})
	}
	t.Run("an existing file is refused and kept", func(t *testing.T) {
		home := t.TempDir()
		writeFile(t, filepath.Join(home, ".claude.json"), "{\"mine\":1}")
		_, err := seedClaudeState(home, fakeEnv(nil, home), "2.1.285")
		assertRefused(t, err, ruleClaudeStateFresh)
		if readFile(t, filepath.Join(home, ".claude.json")) != "{\"mine\":1}" {
			t.Error("the existing file was changed")
		}
	})
}

func TestParseRunFlags(t *testing.T) {
	parse := func(args ...string) (config, error) {
		return parseRunFlags(args, io.Discard, func() string { return "generated-id" })
	}
	t.Run("real defaults", func(t *testing.T) {
		c, err := parse("-mode", "real", "-out", "/r")
		if err != nil {
			t.Fatal(err)
		}
		if c.samples != 22 || c.sampleCeiling != 120*time.Second || c.runID != "generated-id" || c.raisedHookTimeoutSeconds != 10 ||
			c.inputReadyTimeout != 60*time.Second || c.promptAcceptWait != 60*time.Second {
			t.Errorf("config %+v", c)
		}
	})
	t.Run("dry defaults apply only to flags not given", func(t *testing.T) {
		c, err := parse("-mode", "dry", "-out", "/r", "-samples", "5", "-run-id", "mine", "-prompt-accept-wait", "3s")
		if err != nil {
			t.Fatal(err)
		}
		if c.samples != 5 || c.runID != "mine" || c.sampleCeiling != 15*time.Second || c.raisedEnvTimeoutMS != 3000 || c.slowHookMS != 2000 ||
			c.inputReadyTimeout != 10*time.Second || c.promptAcceptWait != 3*time.Second {
			t.Errorf("config %+v", c)
		}
	})
	for _, args := range [][]string{
		{"-out", "/r"},
		{"-mode", "fast", "-out", "/r"},
		{"-mode", "real"},
		{"-mode", "real", "-out", "/r", "-samples", "0"},
		{"-mode", "real", "-out", "/r", "-mcp-config", "relative.json"},
		{"-mode", "real", "-out", "/r", "-slow-hook-ms", "10000"},
		{"-mode", "real", "-out", "/r", "-sample-ceiling", "10s"},
		{"-mode", "real", "-out", "/r", "-input-ready-timeout", "0s"},
		{"-mode", "dry", "-out", "/r", "-prompt-accept-wait", "-1s"},
		{"-mode", "real", "-out", "/r", "extra"},
		{"-mode", "real", "-out", "/r", "-no-such-flag"},
	} {
		if _, err := parse(args...); !errors.Is(err, errUsage) {
			t.Errorf("%q: want a usage error, got %v", args, err)
		}
	}
}

func TestCheckLocalLayer(t *testing.T) {
	for _, tc := range []struct {
		mode  mode
		local string
		cases []string
		rule  string
	}{
		{modeReal, "/l.json", []string{"rn6.idle", "rn6.idle.raised-hook"}, ruleLocalLayer},
		{modeReal, "/l.json", []string{"rn9.drive"}, ruleLocalLayer},
		{modeReal, "/l.json", []string{"rn6.idle", "rn2.pause"}, ""},
		{modeReal, "", []string{"rn6.idle.raised-env"}, ""},
		{modeDry, "/l.json", []string{"rn6.idle.raised-env"}, ""},
	} {
		sel, err := selectCases(caseRegistry, tc.cases, tc.mode)
		if err != nil {
			t.Fatal(err)
		}
		err = checkLocalLayer(config{mode: tc.mode, localSettings: tc.local}, sel)
		if tc.rule == "" && err != nil {
			t.Errorf("%v: %v", tc.cases, err)
		}
		if tc.rule != "" {
			assertRefused(t, err, tc.rule)
		}
	}
}
