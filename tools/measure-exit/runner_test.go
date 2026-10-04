package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// fakeEngineScript records each engine call and, for a probe run, writes the
// probed version's result: args received from FAKE_PROBE_MIN up, as the
// args-keeping stub does, and not received below it, as the args-dropping
// stub does; FAKE_PROBE_SILENT writes nothing. FAKE_TRAIL plants each probe's
// harness id in that trail, so the guard fails. The npm version listing
// prints FAKE_NPM_VERSIONS. A run exits FAKE_RUN_RC.
const fakeEngineScript = `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FAKE_ENGINE_LOG"
[[ "$1" == run ]] || exit 0
if [[ " $* " == *" npm view "* ]]; then printf '%s\n' "${FAKE_NPM_VERSIONS:-}"; exit 0; fi
out="" ver="" prev=""
for a in "$@"; do
    [[ "$prev" == -v && "$a" == *:/results ]] && out="${a%:/results}"
    [[ "$a" == agent-director-measure:probe-* ]] && ver="${a#agent-director-measure:probe-}"
    prev="$a"
done
if [[ -n "$ver" && -n "$out" && "$ver" != "${FAKE_PROBE_SILENT:-}" ]]; then
    r=args_not_received
    [[ "$(printf '%s\n%s\n' "$FAKE_PROBE_MIN" "$ver" | sort -V | head -n1)" == "$FAKE_PROBE_MIN" ]] && r=args_received
    printf '{"probe": {"versions": [{"version": "%s", "result": "%s"}]}}\n' "$ver" "$r" >"$out/results.json"
    printf 'run_id mx-probe-ids-%s\n' "$ver" >"$out/harness-ids.txt"
    [[ -n "${FAKE_TRAIL:-}" ]] && printf '{"id": "mx-probe-ids-%s"}\n' "$ver" >>"$FAKE_TRAIL"
fi
exit "${FAKE_RUN_RC:-0}"
`

// runnerSentinels are credential and model values that must never be printed.
var runnerSentinels = map[string]string{
	"ANTHROPIC_API_KEY":          "sk-ant-runner-sentinel-api-key-0123456789",
	"CLAUDE_CODE_OAUTH_TOKEN":    "runner-sentinel-oauth-0123456789",
	"ANTHROPIC_AUTH_TOKEN":       "runner-sentinel-gateway-token-0123456789",
	"ANTHROPIC_BASE_URL":         "https://runner-sentinel-gateway.invalid/v1",
	"ANTHROPIC_MODEL":            "runner-sentinel-model-id",
	"ANTHROPIC_SMALL_FAST_MODEL": "runner-sentinel-small-fast-model-id",
	"AWS_SECRET_ACCESS_KEY":      "runner-sentinel-aws-secret-0123456789",
}

// liveEnv is the sorted -e list of a measure or rn9 container: the markers
// and the four forwarded names, never a value.
var liveEnv = []string{"AGENT_DIRECTOR_MEASURE_CONTAINER=1", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL",
	"ANTHROPIC_SMALL_FAST_MODEL", "DISABLE_AUTOUPDATER=1"}

// hostSessionVars are the host's tmux and Claude Code session variables.
var hostSessionVars = map[string]string{
	"TMUX": "/tmp/tmux-1000/default,4242,0", "CLAUDE_CONFIG_DIR": "/host/claude-config", "CLAUDECODE": "1",
	"CLAUDE_CODE_SESSION_ID": "host-session-sentinel", "CLAUDE_CODE_CHILD_SESSION": "1", "CLAUDE_PID": "4242",
	"CLAUDE_CODE_MESSAGING_TOKEN": "host-messaging-sentinel", "CLAUDE_CODE_USE_BEDROCK": "1",
}

// runnerRig is a fixture host for run.sh: a home holding the paths it must
// never mount, staged layer sources, a guard fixture home, the recording
// engine shim and tmux/agent-director/go shims that must never be called.
type runnerRig struct {
	home, resultsRoot, guardHome, layers, engine, engineLog, toolLog, shimDir string
}

func newRunnerRig(t *testing.T) *runnerRig {
	t.Helper()
	r := &runnerRig{home: t.TempDir(), resultsRoot: t.TempDir(), guardHome: t.TempDir(), layers: t.TempDir(), shimDir: t.TempDir()}
	r.engine = filepath.Join(r.shimDir, "fake-engine")
	r.engineLog = filepath.Join(t.TempDir(), "engine.log")
	r.toolLog = filepath.Join(t.TempDir(), "tools.log")
	writeFile(t, filepath.Join(r.home, ".claude", "settings.json"), "{}")
	writeFile(t, filepath.Join(r.home, ".claude.json"), "{}")
	writeFile(t, filepath.Join(r.home, ".agent-director", "state.db"), "host db")
	writeFile(t, filepath.Join(r.guardHome, ".agent-director", "state.db"), "fixture db")
	writeFile(t, filepath.Join(r.guardHome, ".agent-director", "ad-trail.jsonl"), "{\"event\":\"fixture\"}\n")
	for _, name := range []string{"user", "managed", "project", "local"} {
		writeFile(t, filepath.Join(r.layers, name+".json"), `{"hooks": {}}`)
	}
	writeFile(t, filepath.Join(r.layers, "mcp.json"), `{"mcpServers": {"tools": {"command": "true"}}}`)
	if err := os.WriteFile(r.engine, []byte(fakeEngineScript), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tmux", "agent-director", "go", "measure-exit", "docker", "podman"} {
		shim := fmt.Sprintf("#!/bin/sh\necho \"%s $*\" >> '%s'\nexit 0\n", name, r.toolLog)
		if err := os.WriteFile(filepath.Join(r.shimDir, name), []byte(shim), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

// layerArgs stages every layer kind from the rig.
func (r *runnerRig) layerArgs(kinds ...string) []string {
	var args []string
	for _, k := range kinds {
		args = append(args, layerFlag(k), filepath.Join(r.layers, k+".json"))
	}
	return args
}

// layerFlag is run.sh's flag staging a layer of kind k.
func layerFlag(k string) string {
	if k == "mcp" {
		return "--mcp-config"
	}
	return "--" + k + "-settings"
}

// run runs run.sh mode with args, the rig's base options and env added to a
// clean environment; it returns the exit status and combined output.
func (r *runnerRig) run(t *testing.T, env map[string]string, mode string, args ...string) (int, string) {
	t.Helper()
	full := append([]string{mode, "--engine", r.engine, "--results-root", r.resultsRoot, "--guard-home", r.guardHome}, args...)
	cmd := exec.Command("./run.sh", full...)
	cmd.Env = []string{"PATH=" + r.shimDir + ":" + os.Getenv("PATH"), "HOME=" + r.home, "LC_ALL=C", "FAKE_ENGINE_LOG=" + r.engineLog}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, string(out)
	case errors.As(err, &exitErr):
		return exitErr.ExitCode(), string(out)
	}
	t.Fatal(err)
	return 0, ""
}

// engineCalls are the engine shim's recorded argv lines.
func (r *runnerRig) engineCalls() []string {
	b, _ := os.ReadFile(r.engineLog)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// assertNoToolCalls fails when the runner ran tmux, agent-director, go or a
// real engine on the host.
func (r *runnerRig) assertNoToolCalls(t *testing.T) {
	t.Helper()
	if b, err := os.ReadFile(r.toolLog); err == nil {
		t.Errorf("the runner ran a host program: %s", b)
	}
}

// containerLine is the printed container run command's tokens.
func containerLine(t *testing.T, out string) []string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, " run --rm --name measure-exit-") {
			return strings.Fields(strings.TrimPrefix(line, "+ "))
		}
	}
	t.Fatalf("no container command in:\n%s", out)
	return nil
}

// flagValues lists the values that follow flag in tokens.
func flagValues(tokens []string, flag string) []string {
	var out []string
	for i := 0; i+1 < len(tokens); i++ {
		if tokens[i] == flag {
			out = append(out, tokens[i+1])
		}
	}
	return out
}

// credsEnv is every runner sentinel plus the host session variables.
func credsEnv() map[string]string {
	env := map[string]string{}
	for k, v := range runnerSentinels {
		env[k] = v
	}
	for k, v := range hostSessionVars {
		env[k] = v
	}
	return env
}

// sentinelTexts lists every value the runner must never print.
func sentinelTexts() []string {
	var out []string
	for _, m := range []map[string]string{runnerSentinels, hostSessionVars} {
		for k, v := range m {
			if len(v) > 4 && k != "TMUX" {
				out = append(out, v)
			}
		}
	}
	return append(out, hostSessionVars["TMUX"])
}

func TestRunnerPrintOnlyContainerCommand(t *testing.T) {
	r := newRunnerRig(t)
	const runID = "mx-print-run-1"
	code, out := r.run(t, credsEnv(), "measure", append([]string{"--run-id", runID},
		r.layerArgs("user", "managed", "project", "local", "mcp")...)...)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if _, err := os.Stat(r.engineLog); err == nil {
		t.Errorf("print-only called the engine: %v", r.engineCalls())
	}
	r.assertNoToolCalls(t)
	if entries, _ := os.ReadDir(r.resultsRoot); len(entries) != 0 {
		t.Errorf("print-only created %d entries in the results root", len(entries))
	}
	assertAbsent(t, "print-only output", out, sentinelTexts()...)
	line := containerLine(t, out)

	gotEnv := flagValues(line, "-e")
	sort.Strings(gotEnv)
	if !reflect.DeepEqual(gotEnv, liveEnv) {
		t.Errorf("forwarded %q, want exactly %q", gotEnv, liveEnv)
	}

	stage := filepath.Join(r.resultsRoot, ".stage-"+runID, "layers")
	wantRO := map[string]string{
		filepath.Join(stage, "user.json"):    "/home/tester/.claude/settings.json",
		filepath.Join(stage, "managed.json"): "/etc/claude-code/managed-settings.json",
		filepath.Join(stage, "project.json"): "/opt/measure-exit/layers/project-settings.json",
		filepath.Join(stage, "local.json"):   "/opt/measure-exit/layers/local-settings.json",
		filepath.Join(stage, "mcp.json"):     "/opt/measure-exit/layers/mcp.json",
	}
	gotRO, writable := map[string]string{}, []string{}
	for _, v := range flagValues(line, "-v") {
		parts := strings.Split(v, ":")
		if len(parts) == 3 && parts[2] == "ro" {
			gotRO[parts[0]] = parts[1]
		} else {
			writable = append(writable, v)
		}
	}
	if !reflect.DeepEqual(gotRO, wantRO) {
		t.Errorf("read-only mounts %v, want %v", gotRO, wantRO)
	}
	if !reflect.DeepEqual(writable, []string{filepath.Join(r.resultsRoot, runID) + ":/results"}) {
		t.Errorf("writable mounts %q, want only the results directory", writable)
	}
	// /tmp is forbidden as a whole; the test's own temp dirs live under it.
	uid := fmt.Sprint(os.Getuid())
	for _, forbidden := range []string{r.home, filepath.Join(r.home, ".agent-director"), filepath.Join(r.home, ".claude"),
		filepath.Join(r.home, ".claude.json"), "/tmp", "/tmp/tmux-" + uid, "/var/run/docker.sock", "/run/docker.sock", "/run/podman"} {
		for _, v := range flagValues(line, "-v") {
			src := strings.SplitN(v, ":", 2)[0]
			if src == forbidden || (forbidden != "/tmp" && strings.HasPrefix(src, forbidden+"/")) {
				t.Errorf("mount %q exposes the forbidden host path %s", v, forbidden)
			}
		}
	}

	joined := strings.Join(line, " ")
	for _, want := range []string{"agent-director-measure:cc-2.1.280 measure-exit run -mode real -samples 22 -out /results -run-id " + runID,
		"-mcp-config /opt/measure-exit/layers/mcp.json", "-project-settings /opt/measure-exit/layers/project-settings.json"} {
		if !strings.Contains(joined, want) {
			t.Errorf("container command lacks %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "--network") {
		t.Errorf("networking changed without the opt-in: %s", joined)
	}
	for _, want := range []string{"credentials: forwarded by name only: ANTHROPIC_BASE_URL ANTHROPIC_AUTH_TOKEN ANTHROPIC_MODEL ANTHROPIC_SMALL_FAST_MODEL (values never printed)",
		"guard.sh snapshot --state " + filepath.Join(r.resultsRoot, ".stage-"+runID, "guard.state") + " --home " + r.guardHome + " --busy-host",
		"guard.sh verify --state", "--id " + runID + " --ids-file " + filepath.Join(r.resultsRoot, runID, "harness-ids.txt")} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// TestRunnerDeployedVersion pins every default Claude Code to the deployed
// 2.1.280 and checks real mode's floor accepts it.
func TestRunnerDeployedVersion(t *testing.T) {
	r := newRunnerRig(t)
	got := map[string]string{}
	for mode, prefix := range map[string]string{"rn9": "agent-director-measure:cc-", "probe": "agent-director-measure:probe-"} {
		_, out := r.run(t, nil, mode, "--run-id", "mx-version-"+mode)
		for _, tok := range containerLine(t, out) {
			if v, ok := strings.CutPrefix(tok, prefix); ok {
				got[mode+" image"] = v
			}
		}
	}
	for file, re := range map[string]string{"Dockerfile": `(?m)^ARG CLAUDE_CODE_VERSION=(\S+)`, "../../Makefile": `(?m)^MEASURE_CLAUDE_CODE_VERSION \?= (\S+)`} {
		if m := regexp.MustCompile(re).FindStringSubmatch(readFile(t, file)); len(m) == 2 {
			got[file] = m[1]
		}
	}
	want := map[string]string{"rn9 image": "2.1.280", "probe image": "2.1.280", "Dockerfile": "2.1.280", "../../Makefile": "2.1.280"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("default Claude Code versions %v, want %v", got, want)
	}
	if !versionAtLeast("2.1.280", realModeMinClaudeCode) {
		t.Errorf("real mode's floor %s refuses the deployed 2.1.280", realModeMinClaudeCode)
	}
}

func TestRunnerNetworkAndProbeCredentials(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    string
		args    []string
		network []string
		env     []string
	}{
		{"measure-opt-in", "measure", []string{"--host-network"}, []string{"host"}, liveEnv},
		{"rn9-default", "rn9", nil, nil, liveEnv},
		{"probe", "probe", []string{"--host-network"}, []string{"none"},
			[]string{"AGENT_DIRECTOR_MEASURE_CONTAINER=1", "ANTHROPIC_AUTH_TOKEN=mx-probe-dummy-token-not-a-secret", "DISABLE_AUTOUPDATER=1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunnerRig(t)
			code, out := r.run(t, credsEnv(), tc.mode, append([]string{"--run-id", "mx-net-" + tc.name}, tc.args...)...)
			if code != 0 {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			line := containerLine(t, out)
			env := flagValues(line, "-e")
			sort.Strings(env)
			if !reflect.DeepEqual(flagValues(line, "--network"), tc.network) || !reflect.DeepEqual(env, tc.env) {
				t.Errorf("network %q env %q:\n%s", flagValues(line, "--network"), env, strings.Join(line, " "))
			}
			if (tc.mode == "measure") != strings.Contains(out, "WARNING: host networking") {
				t.Errorf("host-network warning:\n%s", out)
			}
			// The npm version listing runs credential-free on the host network (b.rx8).
			listing := r.engine + " run --rm --network host agent-director-test npm view @anthropic-ai/claude-code versions --json"
			if (tc.mode == "probe") != strings.Contains("\n"+out+"\n", "\n"+listing+"\n") {
				t.Errorf("npm listing line %q:\n%s", listing, out)
			}
			assertAbsent(t, "output", out, sentinelTexts()...)
		})
	}
}

// TestRunnerPinnedVersions: a pinned candidate list replaces the npm listing.
func TestRunnerPinnedVersions(t *testing.T) {
	file := filepath.Join(t.TempDir(), "versions.txt")
	writeFile(t, file, "2.1.285\n2.1.120\n2.1.200\n")
	for name, args := range map[string][]string{"--versions": {"--versions", "2.1.285 2.1.120 2.1.200"}, "--versions-file": {"--versions-file", file}} {
		r := newRunnerRig(t)
		code, out := r.run(t, nil, "probe", args...)
		if code != 0 || !strings.Contains(out, "# 2b. candidate versions: pinned (--versions): 2.1.120 2.1.200 2.1.285\n") || strings.Contains(out, "npm view") {
			t.Errorf("%s: exit %d:\n%s", name, code, out)
		}
	}
}

// TestRunnerSessionCounts: measure and rn9 always pass -cases, by default
// the mode's registry ids with the MCP cases included (the driver refuses
// real mode without it), and print the sessions they start; print-only and
// --run alike.
func TestRunnerSessionCounts(t *testing.T) {
	var sampled, rn9 []string
	for _, c := range caseRegistry {
		switch {
		case c.sampled:
			sampled = append(sampled, c.id)
		case c.family == familyRN9 && c.id != probeCaseID:
			rn9 = append(rn9, c.id)
		}
	}
	expect := func(withMCP bool) string {
		agents, prompted := 0, 0
		for _, id := range sampled {
			if strings.Contains(id, ".mcp") && !withMCP {
				continue
			}
			agents += 22
			if strings.Contains(id, "midturn") {
				prompted += 22
			}
		}
		return fmt.Sprintf("sessions: %d real Claude Code agents (%d of them prompted), one at a time", agents, prompted)
	}
	if expect(false) != "sessions: 176 real Claude Code agents (66 of them prompted), one at a time" ||
		expect(true) != "sessions: 264 real Claude Code agents (66 of them prompted), one at a time" {
		t.Errorf("registry counts changed: %s / %s", expect(false), expect(true))
	}
	for _, tc := range []struct {
		name, mode string
		mcp        bool
		args       []string
		cases      []string
		sessions   string
	}{
		{"measure-vanilla", "measure", false, nil, sampled, expect(false) + `; MCP cases not measured (no --mcp-config; their rows read "not run")` + "\n"},
		{"measure-mcp", "measure", true, nil, sampled, expect(true) + "\n"},
		{"measure-named", "measure", false, []string{"--cases", "rn6.idle,rn2.pause"}, []string{"rn2.pause", "rn6.idle"},
			"sessions: 44 real Claude Code agents (0 of them prompted)"},
		{"rn9", "rn9", false, nil, rn9, "sessions: 5 real Claude Code agents (5 of them prompted), one at a time, " +
			"plus the teammates the team leads start (about 2 each): about 9 Claude Code sessions in all\n"},
	} {
		for _, run := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/run=%t", tc.name, run), func(t *testing.T) {
				r := newRunnerRig(t)
				args := append([]string{"--run-id", "mx-cases-" + tc.name}, tc.args...)
				if tc.mcp {
					args = append(args, r.layerArgs("mcp")...)
				}
				if run {
					args = append(args, "--run")
				}
				code, out := r.run(t, credsEnv(), tc.mode, args...)
				if code != 0 {
					t.Fatalf("exit %d:\n%s", code, out)
				}
				var line []string
				if !run {
					line = containerLine(t, out)
				}
				for _, c := range r.engineCalls() {
					if run && strings.HasPrefix(c, "run --rm --name measure-exit-") {
						line = strings.Fields(c)
					}
				}
				passed := flagValues(line, "-cases")
				if len(passed) != 1 {
					t.Fatalf("-cases %q in %q", passed, line)
				}
				got, want := strings.Split(passed[0], ","), append([]string(nil), tc.cases...)
				sort.Strings(got)
				sort.Strings(want)
				if !reflect.DeepEqual(got, want) {
					t.Errorf("-cases %q, want the mode's %q", got, want)
				}
				if !strings.Contains(out, tc.sessions) {
					t.Errorf("want %q in:\n%s", tc.sessions, out)
				}
				assertAbsent(t, "output", out, sentinelTexts()...)
			})
		}
	}
}

// TestRunnerLayerReport: a staged layer that is, or links to, Claude Code's
// state or credentials file, is not JSON, or holds a credential-like key is
// refused before anything runs, naming the key and never a value.
func TestRunnerLayerReport(t *testing.T) {
	refused := func(t *testing.T, tc credentialLayerCase, extra ...string) {
		r := newRunnerRig(t)
		code, out := r.run(t, credsEnv(), "measure", append([]string{layerFlag(tc.kind), tc.write(t)}, extra...)...)
		if code != 2 || !strings.Contains(out, "refusing the "+tc.kind+" layer") || !strings.Contains(out, tc.text) {
			t.Fatalf("exit %d, want 2 with %q:\n%s", code, tc.text, out)
		}
		assertAbsent(t, "output", out, append(sentinelTexts(), layerSecret)...)
		if _, err := os.Stat(r.engineLog); err == nil {
			t.Errorf("a refused layer reached the engine: %v", r.engineCalls())
		}
		if entries, _ := os.ReadDir(r.resultsRoot); len(entries) != 0 {
			t.Errorf("a refused layer left %d entries in the results root", len(entries))
		}
		r.assertNoToolCalls(t)
	}
	for _, tc := range credentialLayerCases {
		t.Run(tc.name, func(t *testing.T) { refused(t, tc) })
	}
	t.Run(credentialLayerCases[0].name+" under --run", func(t *testing.T) { refused(t, credentialLayerCases[0], "--run") })
	t.Run("a clean layer is staged and a missing one reported", func(t *testing.T) {
		r := newRunnerRig(t)
		writeFile(t, filepath.Join(r.layers, "user.json"), `{"env": {"PLAIN": "x"}}`)
		// A dangling link is missing too: run.sh never stages what the driver refuses (b.vyb).
		dangling := filepath.Join(r.layers, "dangling.json")
		if err := os.Symlink(filepath.Join(r.layers, "later", ".claude.json"), dangling); err != nil {
			t.Fatal(err)
		}
		code, out := r.run(t, nil, "measure", "--user-settings", filepath.Join(r.layers, "user.json"), "--project-settings", "/nonexistent/project.json",
			"--local-settings", dangling)
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, out)
		}
		for _, want := range []string{"layer user     staged from " + filepath.Join(r.layers, "user.json"), "layer project  MISSING: /nonexistent/project.json (not staged)",
			"layer local    MISSING: " + dangling + " (not staged)"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
		if mounts := strings.Join(flagValues(containerLine(t, out), "-v"), " "); strings.Contains(mounts, "project") || strings.Contains(mounts, "local.json") {
			t.Error("a missing layer was mounted")
		}
		if _, out := r.run(t, nil, "measure"); !strings.Contains(out, `settings: vanilla (no deployment layer staged); label the results "vanilla settings"`) {
			t.Errorf("no vanilla label:\n%s", out)
		}
	})
}

// TestRunnerLayerEnv: a staged layer whose env object (any depth) sets a
// LAYER_REFUSED_ENV or CLAUDE_CODE_USE_* name, or any name to a URL or an
// authorization header, is refused in print-only, naming the key and never
// the value; other values and URLs outside an env object pass.
func TestRunnerLayerEnv(t *testing.T) {
	const secret = "layer-env-runner-sentinel-0123456789"
	const nameText, valueText = ", which would take the agents off the gateway", " to a value that looks like a URL or an authorization header"
	env := func(name, value string) string { return `{"env": {"` + name + `": "` + value + `"}}` }
	for _, tc := range []struct {
		name, flag, body, key, text string // text "" passes
	}{
		{"ANTHROPIC_BASE_URL", "--user-settings", env("ANTHROPIC_BASE_URL", secret), "ANTHROPIC_BASE_URL", nameText},
		{"ANTHROPIC_CUSTOM_HEADERS", "--managed-settings", env("ANTHROPIC_CUSTOM_HEADERS", secret), "ANTHROPIC_CUSTOM_HEADERS", nameText},
		{"a CLAUDE_CODE_USE_ name not listed", "--project-settings", env("CLAUDE_CODE_USE_VERTEX", secret), "CLAUDE_CODE_USE_VERTEX", nameText},
		{"a lower-case provider switch", "--local-settings", env("claude_code_use_bedrock", secret), "claude_code_use_bedrock", nameText},
		{"CLAUDE_CODE_USE_BEDROCK", "--user-settings", env("CLAUDE_CODE_USE_BEDROCK", secret), "CLAUDE_CODE_USE_BEDROCK", nameText},
		{"AWS_REGION", "--user-settings", env("AWS_REGION", secret), "AWS_REGION", nameText},
		{"AWS_PROFILE", "--user-settings", env("AWS_PROFILE", secret), "AWS_PROFILE", nameText},
		{"AWS_DEFAULT_REGION", "--user-settings", env("AWS_DEFAULT_REGION", secret), "AWS_DEFAULT_REGION", nameText},
		{"a URL value", "--user-settings", env("PLAIN", "http://"+secret), "PLAIN", valueText},
		{"a Bearer value", "--managed-settings", env("PLAIN", "Bearer "+secret), "PLAIN", valueText},
		{"a bearer value after a tab", "--project-settings", env("PLAIN", `bearer\t`+secret), "PLAIN", valueText},
		{"an Authorization header value", "--local-settings", env("PLAIN", "Authorization: "+secret), "PLAIN", valueText},
		{"a Proxy-Authorization header value", "--user-settings", env("PLAIN", "Proxy-Authorization: "+secret), "PLAIN", valueText},
		{"an MCP server env URL", "--mcp-config", `{"mcpServers": {"s": {"command": "true", "env": {"UPSTREAM": "https://` + secret + `"}}}}`, "UPSTREAM", valueText},
		{"a plain value", "--user-settings", env("PLAIN", secret), "", ""},
		{"a URL outside an env object", "--user-settings", `{"apiUrl": "https://` + secret + `"}`, "", ""},
		{"a non-object env", "--user-settings", `{"env": "https://` + secret + `"}`, "", ""},
		{"a numeric value", "--user-settings", `{"env": {"PLAIN": 5}}`, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunnerRig(t)
			path := filepath.Join(t.TempDir(), "layer.json")
			writeFile(t, path, tc.body)
			code, out := r.run(t, nil, "measure", tc.flag, path)
			assertAbsent(t, "output", out, secret)
			r.assertNoToolCalls(t)
			if tc.text == "" {
				if code != 0 {
					t.Fatalf("exit %d, want 0:\n%s", code, out)
				}
				return
			}
			if want := "an env object in it sets " + tc.key + tc.text; code != 2 || !strings.Contains(out, want) {
				t.Fatalf("exit %d, want 2 with %q:\n%s", code, want, out)
			}
			if _, err := os.Stat(r.engineLog); err == nil {
				t.Errorf("a refused layer reached the engine: %v", r.engineCalls())
			}
			if entries, _ := os.ReadDir(r.resultsRoot); len(entries) != 0 {
				t.Errorf("a refused layer left %d entries in the results root", len(entries))
			}
		})
	}
}

// TestRunnerLayerRefusedEnvInStep: run.sh's LAYER_REFUSED_ENV holds every
// realModeRefusedEnv name and the gateway's two routing names, and the driver
// refuses each of its names too.
func TestRunnerLayerRefusedEnvInStep(t *testing.T) {
	m := regexp.MustCompile(`(?m)^readonly LAYER_REFUSED_ENV=\(([^)]*)\)`).FindStringSubmatch(readFile(t, "run.sh"))
	if m == nil {
		t.Fatal("run.sh has no readonly LAYER_REFUSED_ENV=(...) array")
	}
	listed := strings.Fields(m[1])
	for _, name := range append([]string{"ANTHROPIC_BASE_URL", "ANTHROPIC_CUSTOM_HEADERS"}, realModeRefusedEnv...) {
		if !slices.Contains(listed, name) {
			t.Errorf("run.sh LAYER_REFUSED_ENV lacks %s", name)
		}
	}
	for _, name := range listed {
		if !layerEnvNameRefused(name) {
			t.Errorf("run.sh refuses %s in a layer env; the driver does not", name)
		}
	}
}

// TestRunnerCredentialLayerInStep: run.sh's refuse_credential_layer case
// arms list exactly the driver's layerRefusedFileNames and
// layerCredentialKeyParts, and the driver refuses a layer for each (b.vyb).
func TestRunnerCredentialLayerInStep(t *testing.T) {
	fn := regexp.MustCompile(`(?ms)^refuse_credential_layer\(\) \{\n(.*?)^\}`).FindStringSubmatch(readFile(t, "run.sh"))
	if fn == nil {
		t.Fatal("run.sh has no refuse_credential_layer() function")
	}
	arm := func(re string) []string {
		m := regexp.MustCompile(re).FindStringSubmatch(fn[1])
		if m == nil {
			t.Fatalf("refuse_credential_layer has no case arm matching %s", re)
		}
		var out []string
		for _, p := range strings.Split(m[1], "|") {
			out = append(out, strings.Trim(strings.TrimSpace(p), "*"))
		}
		return out
	}
	names := arm(`(?m)^[ \t]*(\.[^\s|)]+(?:[ \t]*\|[ \t]*\.[^\s|)]+)*)\)`)
	parts := arm(`(?m)^[ \t]*(\*[A-Z]+\*(?:[ \t]*\|[ \t]*\*[A-Z]+\*)*)\)`)
	dir := t.TempDir()
	refusal := func(name, body string) string {
		path := filepath.Join(dir, name)
		writeFile(t, path, body)
		if err := checkLayerFiles([]layerFile{{"user", path}}); err != nil {
			return err.Error()
		}
		return ""
	}
	for _, n := range names {
		if !strings.Contains(refusal(n, "{}"), "Claude Code's "+n) {
			t.Errorf("run.sh refuses a layer named %s; the driver does not", n)
		}
	}
	for _, p := range parts {
		key := "my_" + strings.ToLower(p)
		if !strings.Contains(refusal("key-"+p+".json", `{"`+key+`": 1}`), "credential-like key "+key) {
			t.Errorf("run.sh refuses a key holding %s; the driver does not", p)
		}
	}
	for _, n := range layerRefusedFileNames {
		if !slices.Contains(names, n) {
			t.Errorf("the driver refuses a layer named %s; run.sh does not", n)
		}
	}
	for _, p := range layerCredentialKeyParts {
		if !slices.Contains(parts, p) {
			t.Errorf("the driver refuses a key holding %s; run.sh does not", p)
		}
	}
}

func TestRunnerRefusals(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	full := map[string]string{}
	for _, k := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL", "ANTHROPIC_SMALL_FAST_MODEL"} {
		full[k] = runnerSentinels[k]
	}
	without := func(name string) map[string]string {
		env := map[string]string{"ANTHROPIC_API_KEY": runnerSentinels["ANTHROPIC_API_KEY"]}
		for k, v := range full {
			if k != name {
				env[k] = v
			}
		}
		return env
	}
	badVersions := filepath.Join(t.TempDir(), "versions.txt")
	writeFile(t, badVersions, "2.1.120\n2.1\n")
	for _, tc := range []struct {
		name string
		env  map[string]string
		mode string
		args []string
		text string
	}{
		{"no-base-url", without("ANTHROPIC_BASE_URL"), "measure", []string{"--run"}, "ANTHROPIC_BASE_URL not set"},
		{"no-token", without("ANTHROPIC_AUTH_TOKEN"), "rn9", []string{"--run"}, "ANTHROPIC_AUTH_TOKEN not set"},
		{"no-model", without("ANTHROPIC_MODEL"), "measure", []string{"--run"}, "refusing to run: ANTHROPIC_MODEL not set"},
		{"no-small-fast-model", without("ANTHROPIC_SMALL_FAST_MODEL"), "rn9", []string{"--run"}, "refusing to run: ANTHROPIC_SMALL_FAST_MODEL not set"},
		{"bad-deployed-version", full, "probe", []string{"--deployed", "2.1"}, "must be versions X.Y.Z (got 2.1)"},
		{"results-in-worktree", full, "measure", []string{"--results-root", filepath.Join(repoRoot, "mx-results")}, "outside the worktree"},
		{"short-run-id", full, "measure", []string{"--run-id", "short"}, "--run-id must be"},
		{"unknown-mode", full, "fast", nil, "unknown mode"},
		{"versions-not-xyz", full, "probe", []string{"--run", "--versions", "2.1.120 2.1.x"}, "--versions and --versions-file take versions X.Y.Z only (got 2.1.x)"},
		{"versions-file-not-xyz", full, "probe", []string{"--run", "--versions-file", badVersions}, "take versions X.Y.Z only (got 2.1)"},
		{"versions-file-missing", full, "probe", []string{"--versions-file", "/nonexistent/versions.txt"}, "is not a readable file"},
		{"versions-outside-probe", full, "measure", []string{"--versions", "2.1.280"}, "--versions and --versions-file are for probe mode only"},
		{"probe-cases", full, "probe", []string{"--cases", probeCaseID}, "--cases is for measure and rn9"},
		{"l2-scenario-in-l1", full, "measure", []string{"--run", "--cases", "rn6.idle,rn9.drive"}, "--cases: rn9.drive is not a measure case"},
		{"l1-case-in-l2", full, "rn9", []string{"--run", "--cases", "rn6.idle"}, "--cases: rn6.idle is not a rn9 case"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunnerRig(t)
			code, out := r.run(t, tc.env, tc.mode, tc.args...)
			if code != 2 || !strings.Contains(out, tc.text) {
				t.Fatalf("exit %d, want 2 with %q:\n%s", code, tc.text, out)
			}
			if _, err := os.Stat(r.engineLog); err == nil {
				t.Errorf("a refused run called the engine: %v", r.engineCalls())
			}
			r.assertNoToolCalls(t)
			assertAbsent(t, "output", out, sentinelTexts()...)
		})
	}
}

func TestRunnerVerifiesAfterAFailedContainer(t *testing.T) {
	for _, guardMode := range []string{"busy", "quiet"} {
		t.Run(guardMode, func(t *testing.T) {
			r := newRunnerRig(t)
			const runID = "mx-failed-run-1"
			env := credsEnv()
			env["FAKE_RUN_RC"] = "1"
			code, out := r.run(t, env, "measure", append([]string{"--run", "--run-id", runID, "--guard-mode", guardMode}, r.layerArgs("user")...)...)
			results := filepath.Join(r.resultsRoot, runID)
			if code != 1 {
				t.Fatalf("exit %d, want 1:\n%s", code, out)
			}
			wantGuard := map[string]string{"busy": "guard passed (busy-host)", "quiet": "guard passed: both files identical"}[guardMode]
			if !strings.Contains(out, wantGuard) || !strings.Contains(out, "results: "+results) {
				t.Errorf("output lacks the guard's verify or the results path:\n%s", out)
			}
			if guardMode == "quiet" {
				s := sum(t, filepath.Join(r.guardHome, ".agent-director", "state.db"))
				if !strings.Contains(out, "before "+s) || !strings.Contains(out, "after  "+s) {
					t.Errorf("verify did not print state.db's checksum before and after:\n%s", out)
				}
			}
			if readFile(t, filepath.Join(results, "guard-status.txt")) != "pass\n" || readFile(t, filepath.Join(results, "container-status.txt")) != "1\n" {
				t.Error("guard or container status not recorded")
			}
			if _, err := os.Stat(filepath.Join(r.resultsRoot, ".stage-"+runID)); err == nil {
				t.Error("the staging directory was left behind")
			}
			calls := strings.Join(r.engineCalls(), "\n")
			if !strings.Contains(calls, "image inspect agent-director-measure:cc-2.1.280") || !strings.Contains(calls, "run --rm --name measure-exit-"+runID) {
				t.Errorf("engine calls:\n%s", calls)
			}
			assertAbsent(t, "output and engine argv", out+calls, sentinelTexts()...)
			r.assertNoToolCalls(t)
		})
	}
}

// TestRunnerProbeBisects probes the deployed version (default 2.1.280) first,
// then bisects, unless the deployed version drops the args (STOP: nothing
// more is listed, built or probed); the deployed verdict decides the exit
// status and is reported even when the guard fails, and every version
// directory gets the guard verdict.
func TestRunnerProbeBisects(t *testing.T) {
	versions := "2.1.120 2.1.150 2.1.200 2.1.250 2.1.280 2.1.285"
	npmListing := `["` + strings.ReplaceAll(versions, " ", `", "`) + `"]`
	probe := func(v, res string) string { return "probe " + v + ": " + res }
	runs := func(v string) string { return "deployed " + v + ": RUNS exec-form hooks (args_received)" }
	const stop = "deployed 2.1.280: does NOT run exec-form hooks (args_not_received). STOP: tell the user at once; " +
		"rc.1's hooks are ignored by the deployed version, and L1 and L2 must not run at 2.1.280"
	const min200 = "minimum: 2.1.200 (newest that ignores args: 2.1.150)"
	narrows200 := []string{probe("2.1.280", "args_received"), runs("2.1.280"),
		probe("2.1.120", "args_not_received"), probe("2.1.200", "args_received"), probe("2.1.150", "args_not_received"), min200}
	stop280 := []string{probe("2.1.280", "args_not_received"), stop, "bisect skipped: the deployed 2.1.280 does NOT run exec-form hooks (STOP)"}
	// npm is the fake npm listing; "" pins the candidates with --versions.
	for _, tc := range []struct {
		name, min, silent, npm string
		args                   []string
		code                   int
		guardFails             bool
		summary                []string
	}{
		{"deployed-runs-and-narrows", "2.1.200", "", "", nil, 0, false, narrows200},
		{"deployed-drops-args-stop", "2.1.285", "", "", nil, 3, false, stop280},
		{"deployed-undecided", "2.1.200", "2.1.280", "", nil, 1, false, []string{probe("2.1.280", "did_not_start"),
			"deployed 2.1.280: UNDECIDED (did_not_start); L0 has not shown whether 2.1.280 runs exec-form hooks",
			probe("2.1.120", "args_not_received"), probe("2.1.285", "args_received"), probe("2.1.200", "args_received"),
			probe("2.1.150", "args_not_received"), min200}},
		{"deployed-outside-the-range", "2.1.200", "", "", []string{"--deployed", "2.1.290"}, 0, false, []string{probe("2.1.290", "args_received"),
			runs("2.1.290"), probe("2.1.120", "args_not_received"), probe("2.1.285", "args_received"), probe("2.1.200", "args_received"),
			probe("2.1.150", "args_not_received"), min200}},
		{"deployed-at-the-known-good-end", "2.1.200", "", "", []string{"--deployed", "2.1.285"}, 0, false, []string{probe("2.1.285", "args_received"),
			runs("2.1.285"), probe("2.1.120", "args_not_received"), probe("2.1.200", "args_received"), probe("2.1.150", "args_not_received"), min200}},
		{"guard-fails-deployed-runs", "2.1.200", "", "", nil, 1, true, narrows200},
		{"guard-fails-and-stop", "2.1.285", "", "", nil, 3, true, stop280},
		{"npm-listing-narrows", "2.1.200", "", npmListing, nil, 0, false, narrows200},
		{"npm-listing-empty", "2.1.200", "", "[]", nil, 1, false, []string{probe("2.1.280", "args_received"), runs("2.1.280"),
			"bisect stopped: the npm version listing failed or listed nothing; pin the candidates with --versions"}},
		{"stop-skips-the-npm-listing", "2.1.285", "", npmListing, nil, 3, false, stop280},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunnerRig(t)
			const runID = "mx-probe-bisect"
			env := map[string]string{"FAKE_PROBE_MIN": tc.min, "FAKE_PROBE_SILENT": tc.silent, "FAKE_NPM_VERSIONS": tc.npm,
				"ANTHROPIC_API_KEY": runnerSentinels["ANTHROPIC_API_KEY"]}
			wantGuard := "pass\n"
			if tc.guardFails {
				env["FAKE_TRAIL"] = filepath.Join(r.guardHome, ".agent-director", "ad-trail.jsonl")
				wantGuard = "fail 1\n"
			}
			args := []string{"--run", "--run-id", runID}
			if tc.npm == "" {
				args = append(args, "--versions", versions)
			}
			code, out := r.run(t, env, "probe", append(args, tc.args...)...)
			results := filepath.Join(r.resultsRoot, runID)
			if code != tc.code {
				t.Fatalf("exit %d, want %d:\n%s", code, tc.code, out)
			}
			summary := strings.Split(strings.TrimSpace(readFile(t, filepath.Join(results, "probe-summary.txt"))), "\n")
			if !reflect.DeepEqual(summary, tc.summary) {
				t.Errorf("probe summary\n got %q\nwant %q", summary, tc.summary)
			}
			verdict := tc.summary[1]
			if readFile(t, filepath.Join(results, "deployed-verdict.txt")) != verdict+"\n" || !strings.Contains(out, "results: "+results+"\n"+verdict+"\n") {
				t.Errorf("the deployed verdict %q is not in deployed-verdict.txt and at the end of the output:\n%s", verdict, out)
			}
			if (code == 3) != strings.Contains(out, "run.sh: STOP: Claude Code 2.1.280 (deployed) does NOT run exec-form hooks; tell the user at once") {
				t.Errorf("exit %d and the STOP line disagree:\n%s", code, out)
			}
			var built []string
			listings := 0
			for _, c := range r.engineCalls() {
				f := strings.Fields(c)
				switch {
				case f[0] == "build":
					buildArgs := flagValues(f, "--build-arg")
					built = append(built, strings.TrimPrefix(buildArgs[1], "CLAUDE_CODE_VERSION="))
					if buildArgs[2] != "REQUIRE_NATIVE_CLAUDE=0" {
						t.Errorf("probe build %q does not waive the native-binary check", c)
					}
				case strings.Contains(c, " npm view "):
					listings++
					if c != "run --rm --network host agent-director-test npm view @anthropic-ai/claude-code versions --json" {
						t.Errorf("npm listing %q, want credential-free on the host network", c)
					}
				case f[0] == "run" && (!reflect.DeepEqual(flagValues(f, "--network"), []string{"none"}) || strings.Contains(c, "ANTHROPIC_API_KEY")):
					t.Errorf("probe container %q", c)
				}
			}
			if wantListings := map[bool]int{true: 1}[tc.npm != "" && tc.code != 3]; listings != wantListings {
				t.Errorf("%d npm listings, want %d", listings, wantListings)
			}
			var want []string
			for _, s := range tc.summary {
				if strings.HasPrefix(s, "probe ") {
					want = append(want, strings.TrimSuffix(strings.Fields(s)[1], ":"))
				}
			}
			if !reflect.DeepEqual(built, want) {
				t.Errorf("build order %q, want %q", built, want)
			}
			if got := readFile(t, filepath.Join(results, "guard-status.txt")); got != wantGuard {
				t.Errorf("guard-status.txt %q, want %q", got, wantGuard)
			}
			// decide takes one -in per version directory, so each needs the run's guard verdict.
			for _, v := range want {
				if got := readFile(t, filepath.Join(results, v, "guard-status.txt")); got != wantGuard {
					t.Errorf("%s/guard-status.txt %q, want the run's %q", v, got, wantGuard)
				}
			}
			assertAbsent(t, "output", out, runnerSentinels["ANTHROPIC_API_KEY"])
			r.assertNoToolCalls(t)
		})
	}
}
