package realtmux_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Per-test tmux isolation, the raw tmux runner and server cleanup (SRD
// SR-20.4). Every socket a helper touches with -S lies under the test's
// private TMUX_TMPDIR, so no helper can reach the default socket path.

// realTmux is one test's private tmux world: its TMUX_TMPDIR, the per-user
// directory in it, and the socket the helpers use (see at and fresh).
type realTmux struct {
	Dir     string // private TMUX_TMPDIR, as its real path
	UserDir string // Dir/tmux-<uid>, mode 0700
	Socket  string // socket path passed with -S; UserDir/default unless bound elsewhere
	tr      *tracker
}

// newRealTmux skips when tmux is absent, points TMUX_TMPDIR at a fresh short
// directory, unsets TMUX and TMUX_PANE (restored after the test) and registers
// cleanup that ends every server and stub the test starts.
func newRealTmux(t testing.TB) *realTmux {
	t.Helper()
	if tmuxPath == "" {
		t.Skip("tmux is not on PATH; real-tmux tests need it (the sandbox image installs tmux 3.3a)")
	}
	// Unix socket paths are limited to about 107 bytes, so the directory is
	// made directly under a short temp base, never under t.TempDir().
	base := os.TempDir()
	if len(base) > 32 {
		base = "/tmp"
	}
	made, err := os.MkdirTemp(base, "rt")
	if err != nil {
		t.Fatalf("make private TMUX_TMPDIR: %v", err)
	}
	dir, err := filepath.EvalSymlinks(made)
	if err != nil {
		t.Fatalf("real path of %s: %v", made, err)
	}
	t.Cleanup(func() { removeTree(t, dir) }) // registered first, so it runs last
	t.Setenv("TMUX_TMPDIR", dir)
	unsetEnv(t, "TMUX", "TMUX_PANE")
	userDir := filepath.Join(dir, "tmux-"+strconv.Itoa(os.Getuid()))
	if err := os.Mkdir(userDir, 0o700); err != nil {
		t.Fatalf("make per-user directory: %v", err)
	}
	tr := &tracker{dir: dir}
	t.Cleanup(func() { tr.cleanup(t) })
	return &realTmux{Dir: dir, UserDir: userDir, Socket: filepath.Join(userDir, "default"), tr: tr}
}

// unsetEnv removes each variable from the process environment for the rest
// of the test and restores its old value afterwards.
func unsetEnv(t testing.TB, keys ...string) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "") // registers the restore
		os.Unsetenv(k)
	}
}

// at returns the same world bound to another socket path, which must lie
// under the private TMUX_TMPDIR.
func (r *realTmux) at(t testing.TB, socket string) *realTmux {
	t.Helper()
	if !within(socket, r.Dir) {
		t.Fatalf("socket %s is outside the test's private TMUX_TMPDIR %s", socket, r.Dir)
	}
	c := *r
	c.Socket = socket
	return &c
}

// fresh returns the world bound to a new, unused socket path in UserDir.
func (r *realTmux) fresh(t testing.TB) *realTmux {
	t.Helper()
	return r.at(t, filepath.Join(r.UserDir, "s"+strconv.Itoa(r.tr.next())))
}

// trackServer and trackPane register a process the test started some other
// way, so cleanup ends it (helpers track their own).
func (r *realTmux) trackServer(pid int) { r.tr.addProc(pid) }
func (r *realTmux) trackPane(pid int)   { r.tr.addProc(pid) }

// within reports whether path is dir or lies under it (lexically).
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../") && !filepath.IsAbs(rel)
}

// rawResult is one tmux client run as observed: both streams and the exit
// status.
type rawResult struct {
	Stdout, Stderr string
	Exit           int
}

// rawCmd is a raw tmux invocation being configured; run executes it. By
// default it passes -u and -S <socket>, runs in the process's working
// directory with the process environment minus every AGENT_DIRECTOR_*
// variable (the sandbox sets AGENT_DIRECTOR_TEST_SANDBOX).
type rawCmd struct {
	rt       *realTmux
	noS, noU bool
	bare     bool
	add      []string
	drop     []string
	dir      string
}

// raw starts a raw invocation on the bound socket.
func (r *realTmux) raw() *rawCmd { return &rawCmd{rt: r} }

// withoutS omits -S, so tmux resolves the socket from TMUX / TMUX_TMPDIR
// (RN-5 cases). A server-starting or -killing command is refused unless
// that resolution lies under the private TMUX_TMPDIR.
func (c *rawCmd) withoutS() *rawCmd { c.noS = true; return c }

// withoutU omits -u.
func (c *rawCmd) withoutU() *rawCmd { c.noU = true; return c }

// withEnv adds or overrides KEY=VALUE entries (AGENT_DIRECTOR_* included).
func (c *rawCmd) withEnv(kv ...string) *rawCmd { c.add = append(c.add, kv...); return c }

// withoutEnv removes the named variables.
func (c *rawCmd) withoutEnv(keys ...string) *rawCmd { c.drop = append(c.drop, keys...); return c }

// bareEnv starts from an empty environment (env -i); withEnv entries are
// still added.
func (c *rawCmd) bareEnv() *rawCmd { c.bare = true; return c }

// inDir sets the working directory of the tmux client.
func (c *rawCmd) inDir(dir string) *rawCmd { c.dir = dir; return c }

// rawTimeout bounds one raw tmux run.
const rawTimeout = 30 * time.Second

// run executes tmux with args and returns what it printed and its exit
// status; it fails the test only if tmux could not run or timed out.
func (c *rawCmd) run(t testing.TB, args ...string) rawResult {
	t.Helper()
	env := c.environ()
	if c.noS {
		c.guardDefault(t, env, args)
	} else {
		c.rt.tr.addSocket(c.rt.Socket)
	}
	var argv []string
	if !c.noU {
		argv = append(argv, "-u")
	}
	if !c.noS {
		argv = append(argv, "-S", c.rt.Socket)
	}
	argv = append(argv, args...)
	res, err := execTmux(env, c.dir, rawTimeout, argv)
	if err != nil {
		t.Fatalf("raw tmux %s: %v", redact(strings.Join(args, " ")), err)
	}
	return res
}

// must runs like run and fails the test unless tmux exits 0 with an empty
// standard error; it returns standard output.
func (c *rawCmd) must(t testing.TB, args ...string) string {
	t.Helper()
	res := c.run(t, args...)
	if res.Exit != 0 || res.Stderr != "" {
		t.Fatalf("raw tmux %s: exit %d, stderr %q", redact(strings.Join(args, " ")), res.Exit, redact(res.Stderr))
	}
	return res.Stdout
}

// run and must are the default raw runner on the bound socket.
func (r *realTmux) run(t testing.TB, args ...string) rawResult {
	t.Helper()
	return r.raw().run(t, args...)
}

func (r *realTmux) must(t testing.TB, args ...string) string {
	t.Helper()
	return r.raw().must(t, args...)
}

// format reads one format value for a target (a $N session, %N pane or
// name), e.g. "#{pid}", "#{pane_pid}", "#{pane_dead}" or "#{socket_path}".
func (r *realTmux) format(t testing.TB, target, f string) string {
	t.Helper()
	return strings.TrimSuffix(r.must(t, "display-message", "-p", "-t", target, f), "\n")
}

// formatInt reads one numeric format value for a target.
func (r *realTmux) formatInt(t testing.TB, target, f string) int {
	t.Helper()
	v := r.format(t, target, f)
	n, err := strconv.Atoi(v)
	if err != nil {
		t.Fatalf("format %s of %s is not a number: %q", f, target, v)
	}
	return n
}

// label reads a session's own @ad_owner value by id ("" when unset). The
// value is returned for comparison only; never print it.
func (r *realTmux) label(t testing.TB, sessionID string) string {
	t.Helper()
	return strings.TrimSuffix(r.must(t, "show-options", "-qv", "-t", sessionID, "@ad_owner"), "\n")
}

// environ builds the raw client's environment.
func (c *rawCmd) environ() []string {
	var base []string
	if !c.bare {
		base = os.Environ()
	}
	skip := map[string]bool{}
	for _, k := range c.drop {
		skip[k] = true
	}
	for _, kv := range c.add {
		k, _, _ := strings.Cut(kv, "=")
		skip[k] = true
	}
	out := make([]string, 0, len(base)+len(c.add))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !skip[k] && !strings.HasPrefix(k, "AGENT_DIRECTOR_") {
			out = append(out, kv)
		}
	}
	return append(out, c.add...)
}

// serverCommands start or end a server; without -S they are allowed only
// when tmux would resolve a socket under the private TMUX_TMPDIR.
var serverCommands = map[string]bool{"new-session": true, "new": true, "start-server": true, "start": true, "kill-server": true}

// guardDefault fails the test before a -S-less run could start or kill a
// server outside the private TMUX_TMPDIR (the default path above all).
func (c *rawCmd) guardDefault(t testing.TB, env, args []string) {
	t.Helper()
	touches := false
	for _, a := range args {
		touches = touches || serverCommands[a]
	}
	if !touches {
		return
	}
	tmuxVar, _ := envValue(env, "TMUX")
	target, _, _ := strings.Cut(tmuxVar, ",")
	if target == "" {
		target, _ = envValue(env, "TMUX_TMPDIR")
		if target != "" && !filepath.IsAbs(target) {
			wd := c.dir
			if wd == "" {
				wd, _ = os.Getwd()
			}
			target = filepath.Join(wd, target)
		}
		if real, err := filepath.EvalSymlinks(target); err == nil {
			target = real
		} else {
			target = "" // tmux falls back to /tmp
		}
	}
	if target == "" || !within(target, c.rt.Dir) {
		t.Fatalf("refusing a raw tmux server command without -S: it would resolve outside the private TMUX_TMPDIR %s", c.rt.Dir)
	}
}

// execTmux runs the tmux binary directly with env, bounded by timeout.
func execTmux(env []string, dir string, timeout time.Duration, argv []string) (rawResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, tmuxPath, argv...)
	cmd.Env, cmd.Dir, cmd.WaitDelay = env, dir, time.Second
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := rawResult{Stdout: stdout.String(), Stderr: stderr.String(), Exit: -1}
	var exitErr *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return res, errors.New("no answer within " + timeout.String())
	case err == nil:
		res.Exit = 0
	case errors.As(err, &exitErr):
		res.Exit = exitErr.ExitCode()
	default:
		return res, err
	}
	return res, nil
}

// labelText matches an @ad_owner value (a chain's value included) so failure
// messages can hide it (SR-2.3: never print a label value).
var labelText = regexp.MustCompile(`ad1 [^\t\n]*`)

// redact hides every label value in s.
func redact(s string) string { return labelText.ReplaceAllString(s, "ad1 <label hidden>") }

// tracker records every socket and process a test's helpers start, for
// cleanup.
type tracker struct {
	mu      sync.Mutex
	dir     string
	n       int
	sockets []string
	procs   []procRef
}

// procRef identifies a process by pid and start time, so a reused pid is
// never signalled.
type procRef struct {
	pid   int
	start string
}

func (tr *tracker) next() int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.n++
	return tr.n
}

func (tr *tracker) addSocket(s string) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for _, have := range tr.sockets {
		if have == s {
			return
		}
	}
	tr.sockets = append(tr.sockets, s)
}

func (tr *tracker) addProc(pid int) {
	if st, ok := readStat(pid); ok && pid > 0 {
		tr.mu.Lock()
		defer tr.mu.Unlock()
		tr.procs = append(tr.procs, procRef{pid: pid, start: st.start})
	}
}

// cleanupBudget bounds each wait of the cleanup.
const cleanupBudget = 5 * time.Second

// cleanup restores the modes a test took away under the private directory,
// sends kill-server to every tracked socket and every socket found there,
// then kills any tracked server or stub process still alive.
func (tr *tracker) cleanup(t testing.TB) {
	restoreModes(tr.dir)
	env := append(withoutPrefix(os.Environ(), "AGENT_DIRECTOR_", "TMUX=", "TMUX_PANE="), "TMUX_TMPDIR="+tr.dir)
	tr.mu.Lock()
	sockets := append(append([]string(nil), tr.sockets...), socketsUnder(tr.dir)...)
	tr.mu.Unlock()
	for _, s := range sockets {
		if within(s, tr.dir) {
			_, _ = execTmux(env, "", cleanupBudget, []string{"-S", s, "kill-server"})
		}
	}
	alive := func() []procRef {
		var out []procRef
		for _, p := range tr.procs {
			if st, ok := readStat(p.pid); ok && st.start == p.start && !st.gone() {
				out = append(out, p)
			}
		}
		return out
	}
	deadline := time.Now().Add(cleanupBudget)
	for len(alive()) > 0 && time.Now().Before(deadline) {
		time.Sleep(pollInterval)
	}
	for _, p := range alive() {
		_ = syscallKill(p.pid)
	}
	deadline = time.Now().Add(cleanupBudget)
	for len(alive()) > 0 && time.Now().Before(deadline) {
		time.Sleep(pollInterval)
	}
	for _, p := range alive() {
		t.Errorf("cleanup: process %d started by the test is still alive", p.pid)
	}
}

// withoutPrefix drops every entry of environ whose text starts with one of
// the prefixes.
func withoutPrefix(environ []string, prefixes ...string) []string {
	var out []string
next:
	for _, kv := range environ {
		for _, p := range prefixes {
			if strings.HasPrefix(kv, p) {
				continue next
			}
		}
		out = append(out, kv)
	}
	return out
}

// restoreModes gives the owner back access to every directory and socket
// under dir (a test may set mode 000); symlinks are never followed.
func restoreModes(dir string) {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		switch {
		case d.IsDir() && info.Mode().Perm()&0o700 != 0o700:
			_ = os.Chmod(p, info.Mode().Perm()|0o700)
		case d.Type()&fs.ModeSocket != 0 && info.Mode().Perm()&0o600 != 0o600:
			_ = os.Chmod(p, info.Mode().Perm()|0o600)
		}
		return nil
	})
}

// socketsUnder lists the socket files under dir.
func socketsUnder(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type()&fs.ModeSocket != 0 {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// removeTree removes the private directory after restoring its modes.
func removeTree(t testing.TB, dir string) {
	restoreModes(dir)
	if err := os.RemoveAll(dir); err != nil {
		t.Logf("cleanup: remove %s: %v", dir, err)
	}
}
