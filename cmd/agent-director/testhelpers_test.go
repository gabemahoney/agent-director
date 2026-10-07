package main_test

// testhelpers_test.go is this package's one shared helper file: TestMain
// builds the binary once; execCLI and its wrappers run it under a per-test
// HOME (the tmux-facing ones with test/fake-tmux first on PATH); and the
// envelope, path, trail, store and fake-tmux helpers the tests share. The
// tests exec the real binary rather than mocking dispatch.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// binaryPath is the binary TestMain builds for every test in this package.
var binaryPath string

// TestMain builds the CLI binary once, then pins $HOME to a temp dir so
// in-process store writes (apitest seeding's trail records) never reach the
// real ~/.agent-director.
func TestMain(m *testing.M) {
	sandboxguard.Require()
	tmp, err := os.MkdirTemp("", "agent-director-test-")
	if err != nil {
		panic(err)
	}
	binaryPath = filepath.Join(tmp, "agent-director")
	build := exec.Command("go", "build", "-o", binaryPath, ".")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		panic(err)
	}
	home := filepath.Join(tmp, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		panic(err)
	}
	if err := os.Setenv("HOME", home); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}

const (
	// runDeadline bounds a run that sets no deadline of its own.
	runDeadline = 2 * time.Minute
	// surfaceDeadline bounds every run that must stop on its own (serve with
	// stdin held open, a hook, a no-verb run reading stdin).
	surfaceDeadline = 10 * time.Second
	// fakeHangBound caps a hung fake tmux's own wait, so no fake outlives a test.
	fakeHangBound = 10 * time.Second
)

// cliOpts says how execCLI runs a binary.
type cliOpts struct {
	bin       string        // binaryPath when ""
	dir       string        // the test's cwd when ""
	env       []string      // the child's whole environment
	stdin     string        // written to stdin, which is then closed unless holdOpen
	holdOpen  bool          // leave stdin open after writing it
	stdinFile *os.File      // stdin itself when set; stdin and holdOpen are then unused
	deadline  time.Duration // runDeadline when 0
}

// execCLI runs a binary as o says; timedOut is true when it was killed at
// the deadline.
func execCLI(t *testing.T, o cliOpts, args ...string) (stdout, stderr string, code int, timedOut bool) {
	t.Helper()
	if o.bin == "" {
		o.bin = binaryPath
	}
	if o.deadline == 0 {
		o.deadline = runDeadline
	}
	ctx, cancel := context.WithTimeout(context.Background(), o.deadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, o.bin, args...)
	cmd.Dir, cmd.Env, cmd.WaitDelay = o.dir, o.env, time.Second
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	var in io.WriteCloser
	if o.stdinFile != nil {
		cmd.Stdin = o.stdinFile
	} else {
		var err error
		if in, err = cmd.StdinPipe(); err != nil {
			t.Fatalf("StdinPipe: %v", err)
		}
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %q: %v", args, err)
	}
	if in != nil {
		_, _ = io.WriteString(in, o.stdin) // the process may already have exited
		if !o.holdOpen {
			_ = in.Close()
		}
	}
	err := cmd.Wait()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) && ctx.Err() == nil {
		t.Fatalf("run %q: %v", args, err)
	}
	return out.String(), errOut.String(), cmd.ProcessState.ExitCode(), ctx.Err() != nil
}

// mustRun is execCLI failing the test when the run outlives its deadline.
func mustRun(t *testing.T, o cliOpts, args ...string) (string, string, int) {
	t.Helper()
	stdout, stderr, code, timedOut := execCLI(t, o, args...)
	if timedOut {
		t.Fatalf("%q still running at its deadline; stderr=%q", args, stderr)
	}
	return stdout, stderr, code
}

// homeEnv is a run's environment: PATH, HOME=home (empty when home is "") and extra.
func homeEnv(home string, extra map[string]string) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

// runCLI runs the binary under a fresh HOME.
func runCLI(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	return runCLIWithHome(t, t.TempDir(), args...)
}

// runCLIWithHome runs the binary under home.
func runCLIWithHome(t *testing.T, home string, args ...string) (string, string, int) {
	t.Helper()
	return mustRun(t, cliOpts{env: homeEnv(home, nil)}, args...)
}

// runBounded runs the binary under home with env, writing stdin and closing
// it unless holdOpen; a run still alive at deadline is killed and timedOut is true.
func runBounded(t *testing.T, home string, env map[string]string, stdin string, holdOpen bool,
	deadline time.Duration, args ...string) (stdout, stderr string, code int, timedOut bool) {
	t.Helper()
	return execCLI(t, cliOpts{env: homeEnv(home, env), stdin: stdin, holdOpen: holdOpen, deadline: deadline}, args...)
}

// runInDir runs the binary in cwd dir with HOME=home (which may be "") and a
// hook payload on stdin, bounded by surfaceDeadline.
func runInDir(t *testing.T, dir, home string, args ...string) (string, string, int) {
	t.Helper()
	return mustRun(t, cliOpts{dir: dir, env: homeEnv(home, nil), stdin: `{"hook_event_name":"SessionStart"}`,
		deadline: surfaceDeadline}, args...)
}

// runSpawnCLI runs the binary under home with the fake tmux first on PATH.
func runSpawnCLI(t *testing.T, home, fakeTmuxDir string, args ...string) (string, string, int) {
	t.Helper()
	return runSpawnCLIEnv(t, home, fakeTmuxDir, nil, args...)
}

// runSpawnCLIEnv is runSpawnCLI plus extraEnv. The fake logs its calls to
// <home>/fake-tmux.log, and the child gets home's private TMUX_TMPDIR and no
// TMUX, so no two tests share a socket or fake table (SR-20.3).
func runSpawnCLIEnv(t *testing.T, home, fakeTmuxDir string, extraEnv map[string]string, args ...string) (string, string, int) {
	t.Helper()
	env := []string{
		"PATH=" + fakeTmuxDir + ":" + os.Getenv("PATH"),
		"HOME=" + home,
		"FAKE_TMUX_LOG=" + filepath.Join(home, "fake-tmux.log"),
		"TMUX_TMPDIR=" + spawnTmuxTmpdir(t, home),
	}
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	return mustRun(t, cliOpts{env: env}, args...)
}

// errorEnvelope mirrors the JSON shape main.go emits on stderr for an error.
type errorEnvelope struct {
	ErrName        string `json:"err_name"`
	ErrDescription string `json:"err_description"`
}

// parseEnvelope unmarshals a JSON error envelope from raw, failing the test
// on a parse error.
func parseEnvelope(t *testing.T, raw string) errorEnvelope {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("stderr is not JSON-parseable: %v\nstderr=%q", err, raw)
	}
	return env
}

// assertOnlyEnvelope fails unless the run exited 1 with empty stdout and
// stderr holding exactly one error envelope named want; it returns it.
func assertOnlyEnvelope(t *testing.T, stdout, stderr string, code int, want string) errorEnvelope {
	t.Helper()
	if code != 1 || stdout != "" {
		t.Fatalf("exit = %d, stdout = %q; want 1 and empty (stderr=%q)", code, stdout, stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != want {
		t.Errorf("err_name = %q (%s); want %s", env.ErrName, env.ErrDescription, want)
	}
	return env
}

// lastJSONLine returns the last line of s that begins with `{`, skipping the
// warning lines (e.g. "pre-trust failed for <path> …") a launch may print first.
func lastJSONLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if ln := strings.TrimSpace(lines[i]); strings.HasPrefix(ln, "{") {
			return ln
		}
	}
	return s
}

// directorDir returns home's .agent-director directory.
func directorDir(home string) string { return filepath.Join(home, ".agent-director") }

// stateDB returns home's state.db.
func stateDB(home string) string { return filepath.Join(directorDir(home), "state.db") }

// readTrailLines returns each line of home's ad-trail.jsonl, parsed, failing
// the test when the file cannot be read.
func readTrailLines(t *testing.T, home string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(directorDir(home), "ad-trail.jsonl"))
	if err != nil {
		t.Fatalf("readTrailLines: %v", err)
	}
	var rows []map[string]any
	for _, ln := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if ln == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatalf("readTrailLines: unmarshal %q: %v", ln, err)
		}
		rows = append(rows, m)
	}
	return rows
}

// trailOrNil is readTrailLines, or nil when home has no trail file.
func trailOrNil(t *testing.T, home string) []map[string]any {
	t.Helper()
	if _, err := os.Stat(filepath.Join(directorDir(home), "ad-trail.jsonl")); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return readTrailLines(t, home)
}

// eventsOf returns the lines of lines whose event is name, in order.
func eventsOf(lines []map[string]any, name string) []map[string]any {
	var out []map[string]any
	for _, l := range lines {
		if l["event"] == name {
			out = append(out, l)
		}
	}
	return out
}

// relayAttemptLines filters lines for ad.relay_attempt.completed events.
func relayAttemptLines(lines []map[string]any) []map[string]any {
	return eventsOf(lines, "ad.relay_attempt.completed")
}

// homeTree lists every path under home, relative and sorted.
func homeTree(t *testing.T, home string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(home, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != home {
			rel, _ := filepath.Rel(home, p)
			paths = append(paths, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", home, err)
	}
	sort.Strings(paths)
	return paths
}

// assertHomeTree fails unless home holds exactly want (relative paths).
func assertHomeTree(t *testing.T, home string, want ...string) {
	t.Helper()
	sort.Strings(want)
	if got := homeTree(t, home); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("home %s holds %q; want exactly %q", home, got, want)
	}
}

// passwdAgentDir returns the passwd home's .agent-director, failing unless it
// is absent and removing it at cleanup; outside the sandbox it skips, since a
// regression would write the real store.
func passwdAgentDir(t *testing.T) string {
	t.Helper()
	if os.Getenv(sandboxguard.EnvVar) != "1" {
		t.Skipf("runs only in the sandbox (%s=1): a regression writes the passwd home's store", sandboxguard.EnvVar)
	}
	u, err := user.Current()
	if err != nil || !filepath.IsAbs(u.HomeDir) {
		t.Fatalf("user.Current() = %v, %v; want an absolute passwd home", u, err)
	}
	dir := filepath.Join(u.HomeDir, ".agent-director")
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s must be absent before the run (Lstat: %v); the sandbox image has none", dir, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// bootstrapDB creates home's store by running `list`, a store-opening verb.
func bootstrapDB(t *testing.T, home string) {
	t.Helper()
	if _, stderr, code := runCLIWithHome(t, home, "list"); code != 0 {
		t.Fatalf("list bootstrap exit = %d; stderr=%q", code, stderr)
	}
}

// seedRowOnSocket seeds one row in state under a fresh HOME, on the socket
// the CLI child resolves there (SR-20.3); it returns the HOME, the row's id
// and its socket.
func seedRowOnSocket(t *testing.T, state string, opts ...apitest.SpawnOption) (home, id, socket string) {
	t.Helper()
	home = t.TempDir()
	socket = spawnSocket(t, home)
	opts = append([]apitest.SpawnOption{apitest.WithTmuxSocket(socket)}, opts...)
	id, err := apitest.SeedSpawn(stateDB(home), "", state, "", "", "", true, opts...)
	if err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	return home, id, socket
}

// rowColumns reads id's row straight from home's store.
func rowColumns(t *testing.T, home, id string) apitest.SpawnColumns {
	t.Helper()
	cols, err := apitest.ReadSpawnColumns(stateDB(home), id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns: %v", err)
	}
	return cols
}

// launchIdentity reads id's launch token and socket, and the store's id.
func launchIdentity(t *testing.T, home, id string) (token, socket, storeID string) {
	t.Helper()
	cols := rowColumns(t, home, id)
	storeID, err := apitest.ReadStoreID(stateDB(home))
	if err != nil {
		t.Fatalf("ReadStoreID: %v", err)
	}
	token, _ = cols.LaunchToken.(string)
	socket, _ = cols.TmuxSocket.(string)
	return token, socket, storeID
}

// statusOf runs `status` for id and returns its state, or its err_name when
// the verb fails.
func statusOf(t *testing.T, home, fakeDir, id string) string {
	t.Helper()
	stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "status", "--claude-instance-id", id)
	if code != 0 {
		return parseEnvelope(t, lastJSONLine(stderr)).ErrName
	}
	var st struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(stdout), &st); err != nil {
		t.Fatalf("parse status %q: %v", stdout, err)
	}
	return st.State
}

// listIDs runs `list` under home with args and returns the instance ids it
// shows, sorted.
func listIDs(t *testing.T, home, fakeDir string, args ...string) []string {
	t.Helper()
	stdout, stderr, code := runSpawnCLI(t, home, fakeDir, append([]string{"list"}, args...)...)
	if code != 0 {
		t.Fatalf("list %q exit = %d; stderr=%q", args, code, stderr)
	}
	var res struct {
		Spawns []struct {
			ClaudeInstanceID string `json:"claude_instance_id"`
		} `json:"spawns"`
	}
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("parse list %q: %v", stdout, err)
	}
	ids := []string{}
	for _, sp := range res.Spawns {
		ids = append(ids, sp.ClaudeInstanceID)
	}
	sort.Strings(ids)
	return ids
}

// withTestProcessPane records this test process as the row's pane process
// (its pid and real start time). The test process is the parent of every
// hook it pipes into the built CLI, so those hooks pass the SR-22.9 gate.
func withTestProcessPane(t *testing.T) apitest.SpawnOption {
	t.Helper()
	pid := os.Getpid()
	start, alive, known := probe.NewProcChecker().StartTime(pid)
	if !known || !alive || start == "" {
		t.Fatalf("StartTime(test pid %d) = (%q, alive=%v, known=%v); want the live start time", pid, start, alive, known)
	}
	return apitest.WithLaunchIdentity(store.LaunchIdentity{
		Token: "5eed0000000000a1", Socket: apitest.TestSocket, PaneID: apitest.TestPaneID,
		PanePID: pid, PaneStarttime: start,
	})
}

// writeQueryTimeout writes home's config with query_timeout_ms as its only setting.
func writeQueryTimeout(t *testing.T, cfgPath string, d time.Duration) {
	t.Helper()
	apitest.WriteTmuxConfig(t, cfgPath, apitest.TmuxInt(config.TmuxQueryTimeoutMs, d.Milliseconds()))
}

// buildFakeTmux returns the directory of faketmuxfix's fake "tmux", built
// once per test binary, for a PATH prepend.
func buildFakeTmux(t *testing.T) string {
	t.Helper()
	return faketmuxfix.Dir(t)
}

// spawnTmuxTmpdir returns home's private TMUX_TMPDIR, <home>/tmux-tmpdir,
// creating it (mode 0700) so tmux's resolution takes it rather than /tmp.
func spawnTmuxTmpdir(t *testing.T, home string) string {
	t.Helper()
	dir := filepath.Join(home, "tmux-tmpdir")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir TMUX_TMPDIR: %v", err)
	}
	return dir
}

// spawnSocket returns the socket a launch under home resolves, making its
// per-user directory as the launch does; it gives this test process the
// child's tmux environment (home's TMUX_TMPDIR, no TMUX).
func spawnSocket(t *testing.T, home string) string {
	t.Helper()
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", spawnTmuxTmpdir(t, home))
	socket, err := tmux.ResolveSocket(true)
	if err != nil {
		t.Fatalf("resolve socket: %v", err)
	}
	return socket
}

// fakeTable is a fake table whose server is this test process holding sessions.
func fakeTable(sessions ...faketmuxfix.Session) faketmuxfix.Table {
	return faketmuxfix.Table{Server: &faketmuxfix.Server{PID: os.Getpid(), Start: time.Now().Unix()}, Sessions: sessions}
}

// paneSession is a session labelled for row id with token, holding one pane
// paneID that carries token's @ad_pane and captures capture.
func paneSession(sessionID, name, token, id, storeID, paneID string, pid int, capture string) faketmuxfix.Session {
	return faketmuxfix.Session{
		ID: sessionID, Created: time.Now().Unix(), Name: name, Label: tmuxfix.LabelValue(token, sessionID, id, storeID),
		Panes: []faketmuxfix.Pane{{ID: paneID, PID: pid, Capture: capture, AdPane: tmuxfix.PaneLabelValue(token, paneID)}},
	}
}

// ownSession is id's own labelled session "$3" holding the agent's pane,
// capturing capture.
func ownSession(t *testing.T, home, id, capture string) faketmuxfix.Session {
	t.Helper()
	token, _, storeID := launchIdentity(t, home, id)
	name, _ := rowColumns(t, home, id).TmuxSessionName.(string)
	return paneSession("$3", name, token, id, storeID, apitest.TestPaneID, apitest.TestPanePID, capture)
}

// fakeTmuxInvocations returns each argv the fake logged under home, without
// argv[0], in call order; nil when the fake was never run.
func fakeTmuxInvocations(t *testing.T, home string) [][]string {
	t.Helper()
	var out [][]string
	for _, argv := range faketmuxfix.ReadLog(t, filepath.Join(home, "fake-tmux.log")) {
		out = append(out, argv[1:])
	}
	return out
}

// assertInvocationKinds fails unless the fake's invocations under home are,
// in order, the named commands, none of them a create it did not name.
func assertInvocationKinds(t *testing.T, home string, want ...string) [][]string {
	t.Helper()
	invs := fakeTmuxInvocations(t, home)
	if len(invs) != len(want) {
		t.Fatalf("fake-tmux invocations = %d; want %d (%v): %q", len(invs), len(want), want, invs)
	}
	for i, argv := range invs {
		if !slices.Contains(argv, want[i]) {
			t.Errorf("invocation %d = %q; want a %s", i, argv, want[i])
		}
		if want[i] != "new-session" && slices.Contains(argv, "new-session") {
			t.Errorf("invocation %d is a create; want only a %s: %q", i, want[i], argv)
		}
	}
	return invs
}

// expireGoneID is the finished row seedExpireRows seeds for the b.fji H6
// --older-than follow (advice_follow_cli_test.go).
const expireGoneID = "id-exp-a-gone"

// seedExpireRows seeds each id as a row ended in January on the socket the
// CLI child resolves, and returns the HOME and the socket.
func seedExpireRows(t *testing.T, ids []string) (home, socket string) {
	t.Helper()
	home = t.TempDir()
	socket = spawnSocket(t, home)
	ended := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, id := range ids {
		if _, err := apitest.SeedSpawn(stateDB(home), id, store.StateEnded, "", "", "", true,
			apitest.WithTmuxSocket(socket), apitest.WithEndedAt(ended)); err != nil {
			t.Fatalf("SeedSpawn %s: %v", id, err)
		}
	}
	return home, socket
}
