package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

// TestMain refuses to run outside the sandbox: the script tests read the
// passwd home and drive guard.sh and run.sh.
func TestMain(m *testing.M) {
	sandboxguard.Require()
	os.Exit(m.Run())
}

// clockStart is the virtual clock's first instant.
var clockStart = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// virtualClock advances only when Sleep is called.
type virtualClock struct{ now time.Time }

func (c *virtualClock) Now() time.Time        { return c.now }
func (c *virtualClock) Sleep(d time.Duration) { c.now = c.now.Add(d) }

// fakeEnv is an environment over vars, with passwdHome as the passwd entry.
func fakeEnv(vars map[string]string, passwdHome string) environment {
	return environment{
		lookupEnv: func(k string) (string, bool) { v, ok := vars[k]; return v, ok },
		environ: func() []string {
			out := make([]string, 0, len(vars))
			for k, v := range vars {
				out = append(out, k+"="+v)
			}
			sort.Strings(out)
			return out
		},
		passwdHome: func() (string, error) { return passwdHome, nil },
		getuid:     func() int { return 1000 },
	}
}

// fakeExec records every agent-director and tmux argv and answers from reply.
type fakeExec struct {
	calls [][]string
	reply func(argv []string) (stdout, stderr string, code int)
}

func (f *fakeExec) exec(_ context.Context, argv, _ []string) ([]byte, []byte, int, error) {
	f.calls = append(f.calls, append([]string(nil), argv...))
	if f.reply == nil {
		return nil, nil, 0, nil
	}
	o, e, c := f.reply(argv)
	return []byte(o), []byte(e), c, nil
}

// count is how many recorded calls contain every one of words.
func (f *fakeExec) count(words ...string) int {
	n := 0
	for _, c := range f.calls {
		joined := " " + strings.Join(c, " ") + " "
		all := true
		for _, w := range words {
			all = all && strings.Contains(joined, " "+w+" ")
		}
		if all {
			n++
		}
	}
	return n
}

// fakeProcs answers start-time reads.
type fakeProcs func(pid int) (start string, alive, known bool)

func (f fakeProcs) StartTime(pid int) (string, bool, bool) { return f(pid) }

// rig is a harness on a temp tree with a fake exec, a virtual clock and a
// recorded run log and identifiers file.
type rig struct {
	h   *harness
	ex  *fakeExec
	clk *virtualClock
	log *bytes.Buffer
	ids *bytes.Buffer
}

// newRig builds a rig in mode m; the private socket is <tmux>/tmux-1000/default.
func newRig(t *testing.T, m mode) *rig {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	tmuxDir := filepath.Join(dir, "tmux")
	for _, d := range []string{home, tmuxDir, filepath.Join(dir, "out"), filepath.Join(dir, "work")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	env := fakeEnv(map[string]string{"PATH": os.Getenv("PATH"), "HOME": home}, home)
	r := &rig{ex: &fakeExec{}, clk: &virtualClock{now: clockStart}, log: &bytes.Buffer{}, ids: &bytes.Buffer{}}
	scr := newScrubber(env)
	cfg := config{mode: m, samples: 2, outDir: filepath.Join(dir, "out"), midTurnPrompt: defaultMidTurnPrompt,
		raisedHookTimeoutSeconds: 3, raisedEnvTimeoutMS: 3000, slowHookMS: 2000,
		sampleCeiling: 2 * time.Second, readyTimeout: time.Second, stepTimeout: time.Second}
	iso := isolation{RunID: "mx-test-run", Mode: m, Home: home, TmuxTmpdir: tmuxDir,
		ExpectedSocket: privateSocket(tmuxDir), WorkDir: filepath.Join(dir, "work"), ClaudeCode: "2.1.285"}
	r.h = &harness{cfg: cfg, env: env, iso: iso, clock: r.clk, ids: newIDWriter(r.ids), scr: scr,
		res: newResults(iso, clockStart), out: &bytes.Buffer{},
		procs: fakeProcs(func(int) (string, bool, bool) { return "start-1", true, true })}
	r.h.inv = &invoker{log: newRunLog(r.log, scr), exec: r.ex.exec, clock: r.clk, scr: scr,
		agentDirector: "agent-director", tmux: "tmux"}
	return r
}

// privateSocket is the socket tmux resolves under a private TMUX_TMPDIR.
func privateSocket(tmuxDir string) string { return filepath.Join(tmuxDir, "tmux-1000", "default") }

// writeFile writes content to path, creating its directory.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// readFile returns path's content.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// assertAbsent fails when text contains any of secrets.
func assertAbsent(t *testing.T, what, text string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if strings.Contains(text, s) {
			t.Errorf("%s contains the sentinel %q", what, s)
		}
	}
}

// credentialSentinels sets a sentinel value for every forwardable credential.
var credentialSentinels = map[string]string{
	"ANTHROPIC_API_KEY":        "sk-ant-sentinel-api-key-0123456789abcdef",
	"CLAUDE_CODE_OAUTH_TOKEN":  "sentinel-oauth-token-0123456789",
	"ANTHROPIC_AUTH_TOKEN":     "sentinel-gateway-token-0123456789",
	"ANTHROPIC_BASE_URL":       "https://user:sentinel-url-secret@gateway.invalid",
	"AWS_ACCESS_KEY_ID":        "AKIASENTINEL0123456789",
	"AWS_SECRET_ACCESS_KEY":    "sentinel-aws-secret-0123456789",
	"AWS_SESSION_TOKEN":        "sentinel-aws-session-0123456789",
	"AWS_BEARER_TOKEN_BEDROCK": "sentinel-bedrock-bearer-0123456789",
}

// sentinelValues lists credentialSentinels' values.
func sentinelValues() []string {
	out := make([]string, 0, len(credentialSentinels))
	for _, v := range credentialSentinels {
		out = append(out, v)
	}
	return out
}
