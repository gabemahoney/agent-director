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

	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
)

// TestMain refuses to run outside the sandbox: the script tests read the
// passwd home and drive guard.sh and run.sh.
func TestMain(m *testing.M) {
	sandboxguard.Require()
	os.Exit(m.Run())
}

// clockStart is the virtual clock's first instant.
var clockStart = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// The fake agent's start time and another process's, for a reused pid.
const (
	agentStart = procstarttimefix.LinuxProcStarttime
	otherStart = procstarttimefix.DarwinProcStarttime
)

// sleepClock is the harness's clock over the shared tmuxfix.Clock: Sleep
// calls sleep, which is the clock's Advance unless a test wraps it.
type sleepClock struct {
	*tmuxfix.Clock
	sleep func(time.Duration)
}

func (c *sleepClock) Sleep(d time.Duration) { c.sleep(d) }

// newSleepClock returns a sleepClock at clockStart.
func newSleepClock() *sleepClock {
	c := tmuxfix.NewClock(clockStart)
	return &sleepClock{Clock: c, sleep: c.Advance}
}

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
	for i := callIndex(f.calls, 0, words...); i >= 0; i = callIndex(f.calls, i+1, words...) {
		n++
	}
	return n
}

// callIndex is the index of the first call at or after from containing every
// one of words as whole argv tokens, or -1.
func callIndex(calls [][]string, from int, words ...string) int {
	for i := from; i < len(calls); i++ {
		joined := " " + strings.Join(calls[i], " ") + " "
		all := true
		for _, w := range words {
			all = all && strings.Contains(joined, " "+w+" ")
		}
		if all {
			return i
		}
	}
	return -1
}

// rig is a harness on a temp tree with a fake exec, a virtual clock, an
// empty process fake (every pid gone) and a recorded run log and
// identifiers file.
type rig struct {
	h   *harness
	ex  *fakeExec
	clk *sleepClock
	pc  *procfix.Checker
	log *bytes.Buffer
	ids *bytes.Buffer
}

// setAfterSleep sets pid to p in the process fake once d of sleep has
// passed in total from now, or at once when d is zero.
func (r *rig) setAfterSleep(d time.Duration, pid int, p procfix.Process) {
	if d <= 0 {
		r.pc.Set(pid, p)
		return
	}
	prev, slept := r.clk.sleep, time.Duration(0)
	r.clk.sleep = func(x time.Duration) {
		prev(x)
		if slept += x; slept >= d {
			r.pc.Set(pid, p)
		}
	}
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
	r := &rig{ex: &fakeExec{}, clk: newSleepClock(), pc: procfix.New(), log: &bytes.Buffer{}, ids: &bytes.Buffer{}}
	scr := newScrubber(env)
	cfg := config{mode: m, samples: 2, outDir: filepath.Join(dir, "out"), midTurnPrompt: defaultMidTurnPrompt,
		raisedHookTimeoutSeconds: 3, raisedEnvTimeoutMS: 3000, slowHookMS: 2000,
		sampleCeiling: 2 * time.Second, readyTimeout: time.Second, stepTimeout: time.Second}
	iso := isolation{RunID: "mx-test-run", Mode: m, Home: home, TmuxTmpdir: tmuxDir,
		ExpectedSocket: privateSocket(tmuxDir), WorkDir: filepath.Join(dir, "work"), ClaudeCode: "2.1.285"}
	r.h = &harness{cfg: cfg, env: env, iso: iso, clock: r.clk, ids: newIDWriter(r.ids), scr: scr,
		res: newResults(iso, clockStart), out: &bytes.Buffer{}, procs: r.pc}
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
