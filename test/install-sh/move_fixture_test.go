package installsh_test

// The move fixture: what the tests of install.sh's move of AD's directory
// (~/.agent-director to ~/.config/agent-director, SR-11.7) build on. Lanes
// that add cases extend it from their own files and never edit this one:
//
//   - A case starts with newMoveHome(t) (its own HOME, tools dir and TMPDIR
//     under t.TempDir()), builds an old install with (*moveHome).BuildOld, and
//     runs install.sh with Install (or StartInstall, to signal it mid-run).
//   - h.WriteTool and h.WrapTool put a stand-in for any command install.sh
//     runs (cp, mktemp, claude, ps, lsof, sqlite3) first on its PATH; the
//     script reads the case's h.Env variables.
//   - h.Snapshot and moveDiff compare a whole HOME by SR-14.3's rule.
//   - h.StartServe and h.HoldSQLite start the processes a refusal looks for
//     and release them when the case ends.
//
// Everything is built once per package run (moveFixture): the binary pairs, a
// loose copy of install.sh outside any git checkout (so the source-tree check
// is skipped) and the PATH toolbox the sandbox image's missing file(1) is
// added to. There is no TestMain here (advice_follow_test.go owns it), so the
// shared directory is left to the sandbox container; moveFixtureShutdown
// removes it for a TestMain that wants to.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	// moveVersion and moveCommit stamp every binary pair, so a pair is
	// install.sh's matching one; moveCommit is a commit no checkout has.
	moveVersion = "0.0.9-move"
	moveCommit  = "0123456789abcdef0123456789abcdef01234567"
	moveVersPkg = "github.com/gabemahoney/agent-director/internal/version"
	// moveOtherDir is the directory an earlier release's version result names.
	moveOtherDir = "~/.agent-director"
	// moveWait bounds every wait on a process the fixture started.
	moveWait = 20 * time.Second
)

// moveInputs are what the fixture runs or builds from, relative to the repo
// root: a change to any of them reruns the tests under go test's cache.
var moveInputs = []string{"cmd", "internal", "pkg", "skills/install-agent-director", "test/install-sh", "go.mod", "go.sum"}

var moveTracked sync.Once

// moveTrackInputs stats every file of moveInputs. go test keys a cached pass on
// the files the test process itself opens or stats (a directory walk lists
// names only), and the pair is built, and install.sh run, by child processes.
func moveTrackInputs() {
	moveTracked.Do(func() {
		root := filepath.Join("..", "..")
		skip := filepath.Join(root, "pkg", "ts-bun-client")
		for _, in := range moveInputs {
			_ = filepath.WalkDir(filepath.Join(root, in), func(p string, d fs.DirEntry, err error) error {
				switch {
				case err != nil:
					return nil // an entry that vanishes mid-walk only changes what is tracked
				case d.IsDir() && p == skip:
					return fs.SkipDir
				case !d.IsDir():
					_, _ = os.Lstat(p)
				}
				return nil
			})
		}
	})
}

// movePair is an agent-director and an agent-director-admin binary.
type movePair struct{ Main, Admin string }

// moveShared is what is built once per package run.
type moveShared struct {
	Root    string // the shared directory
	Bash    string // the bash that runs install.sh
	SQLite  string // the real sqlite3
	Install string // install.sh, in no git checkout
	Toolbox string // the PATH every case sees after its own tools dir
	// Current is the pair built from this tree. NoConfigDir and OtherDir are
	// stand-in pairs of an earlier release: its version result has no
	// config_dir, or names moveOtherDir. All three carry one version stamp.
	Current, NoConfigDir, OtherDir movePair
	// OldBinary is the stand-in an old install keeps at bin/agent-director
	// (and admin/agent-director-admin): it runs `serve` until stdin closes.
	OldBinary string
	SQLFixt   string // testdata/v0.11.1-store.sql
}

var moveShare struct {
	once sync.Once
	v    *moveShared
	err  error
}

// moveFixture builds the shared fixture on first use.
func moveFixture(t *testing.T) *moveShared {
	t.Helper()
	moveTrackInputs()
	moveShare.once.Do(func() { moveShare.v, moveShare.err = moveBuildShared() })
	if moveShare.err != nil {
		t.Fatalf("move fixture: %v", moveShare.err)
	}
	return moveShare.v
}

// moveFixtureShutdown removes the shared directory; a TestMain may call it.
func moveFixtureShutdown() {
	if moveShare.v != nil {
		_ = os.RemoveAll(moveShare.v.Root)
	}
}

func moveBuildShared() (*moveShared, error) {
	root, err := os.MkdirTemp("", "move-fixture-")
	if err != nil {
		return nil, err
	}
	if root, err = filepath.EvalSymlinks(root); err != nil {
		return nil, err
	}
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		return nil, err
	}
	s := &moveShared{Root: root, Toolbox: filepath.Join(root, "toolbox")}
	if s.Bash, err = exec.LookPath("bash"); err != nil {
		return nil, err
	}
	if s.SQLite, err = exec.LookPath("sqlite3"); err != nil {
		return nil, err
	}
	if s.SQLFixt, err = filepath.Abs(filepath.Join("testdata", "v0.11.1-store.sql")); err != nil {
		return nil, err
	}

	// install.sh, in no git checkout and with no bin/ beside it.
	src, err := os.ReadFile(filepath.Join(repo, "skills", "install-agent-director", "install.sh"))
	if err != nil {
		return nil, err
	}
	s.Install = filepath.Join(root, "loose", "skills", "install-agent-director", "install.sh")
	if err := os.MkdirAll(filepath.Dir(s.Install), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(s.Install, src, 0o755); err != nil {
		return nil, err
	}

	build := func(dir, name, flags, pkg string) (string, error) {
		out := filepath.Join(root, dir, name)
		cmd := exec.Command("go", "build", "-ldflags", flags, "-o", out, pkg)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("go build %s: %v\n%s", pkg, err, b)
		}
		return out, nil
	}
	real := fmt.Sprintf("-X %s.Version=%s -X %s.Commit=%s", moveVersPkg, moveVersion, moveVersPkg, moveCommit)
	standin := func(dir, configDir string) (string, error) {
		return build(dir, "standin", fmt.Sprintf("-X main.version=%s -X main.commit=%s -X main.configDir=%s", moveVersion, moveCommit, configDir),
			"./test/install-sh/testdata/standin")
	}
	if s.Current.Main, err = build("current", "agent-director", real, "./cmd/agent-director"); err != nil {
		return nil, err
	}
	if s.Current.Admin, err = build("current", "agent-director-admin", real, "./cmd/agent-director-admin"); err != nil {
		return nil, err
	}
	if s.OldBinary, err = standin("earlier-none", ""); err != nil {
		return nil, err
	}
	s.NoConfigDir = movePair{s.OldBinary, s.OldBinary}
	other, err := standin("earlier-other", moveOtherDir)
	if err != nil {
		return nil, err
	}
	s.OtherDir = movePair{other, other}

	return s, moveBuildToolbox(s.Toolbox)
}

// moveFakeFile describes ELF the way file(1) does, enough for install.sh's
// architecture probe, for the sandbox image that has no file(1).
const moveFakeFile = `#!/bin/bash
p="${!#}"
hex() { od -An -tx1 -j "$1" -N "$2" "$p" | tr -d ' \n'; }
case "$(hex 0 4)" in
    7f454c46)
        bits=32; [[ "$(hex 4 1)" == 02 ]] && bits=64
        endian=LSB; [[ "$(hex 5 1)" == 02 ]] && endian=MSB
        case "$(hex 18 2)" in 3e00) m=x86-64 ;; b700) m="ARM aarch64" ;; *) m="machine 0x$(hex 18 2)" ;; esac
        echo "ELF ${bits}-bit ${endian} executable, ${m}, version 1 (SYSV), statically linked" ;;
    cffaedfe) echo "Mach-O 64-bit executable" ;;
    *) echo "data" ;;
esac
`

// moveBuildToolbox fills dir with a link to every executable on PATH (the first
// of a name wins), without the commands a case must not reach (claude, curl,
// gh, agent-director), then adds a claude that only prints a version and, when
// the host has none, a file.
func moveBuildToolbox(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		entries, _ := os.ReadDir(d)
		for _, e := range entries {
			p := filepath.Join(d, e.Name())
			if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
				continue
			}
			if err := os.Symlink(p, filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
		}
	}
	for _, n := range []string{"claude", "curl", "gh", "agent-director"} {
		_ = os.Remove(filepath.Join(dir, n))
	}
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/bash\necho '0.0.0 (fake claude, move tests)'\n"), 0o755); err != nil {
		return err
	}
	if _, err := exec.LookPath("file"); err != nil {
		return os.WriteFile(filepath.Join(dir, "file"), []byte(moveFakeFile), 0o755)
	}
	return nil
}

// moveHome is one case's world: its own HOME, a tools dir first on PATH, a
// TMPDIR, and the flags and environment install.sh runs with. Nothing a case
// does reads or writes outside Root.
type moveHome struct {
	t     *testing.T
	S     *moveShared
	Root  string
	Home  string // physical path
	Tools string // first on PATH: a case's stand-ins
	Tmp   string // TMPDIR, which install.sh's temp files land in
	Old   string // ~/.agent-director
	New   string // ~/.config/agent-director
	// Pair is the --binary and --admin-binary pair; Flags follow it, then a
	// call's own arguments. Env is added to the install's environment, which
	// is HOME, PATH and TMPDIR only. Cwd is the install's working directory.
	Pair  movePair
	Flags []string
	Env   map[string]string
	Cwd   string
	// Vars holds what a case learns while it sets up (a pid, say); a moveWant
	// string names one as {name}, and {HOME} is Home.
	Vars map[string]string

	release []func()
}

// newMoveHome makes a case's HOME (empty) and its tools dir and TMPDIR.
func newMoveHome(t *testing.T) *moveHome {
	t.Helper()
	s := moveFixture(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &moveHome{t: t, S: s, Root: root, Home: filepath.Join(root, "home"), Tools: filepath.Join(root, "tools"),
		Tmp: filepath.Join(root, "tmp"), Pair: s.Current, Flags: []string{"--no-hooks", "--no-symlink"}, Env: map[string]string{}, Vars: map[string]string{}}
	for _, d := range []string{h.Home, h.Tools, h.Tmp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h.Old = filepath.Join(h.Home, ".agent-director")
	h.New = filepath.Join(h.Home, ".config", "agent-director")
	h.Cwd = h.Home
	t.Cleanup(h.Release)
	return h
}

// OnRelease registers a function Release runs (last registered first).
func (h *moveHome) OnRelease(f func()) { h.release = append(h.release, f) }

// Release runs the registered functions once: it stops every process the case
// started and undoes what a case did to hold the install (a lock, a mode). A
// case calls it before its closing Snapshot; it also runs when the case ends.
func (h *moveHome) Release() {
	for len(h.release) > 0 {
		f := h.release[len(h.release)-1]
		h.release = h.release[:len(h.release)-1]
		f()
	}
}

// Write writes text to path, creating its directories, with mode.
func (h *moveHome) Write(path, text string, mode os.FileMode) {
	h.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		h.t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		h.t.Fatal(err)
	}
}

// Link makes link, creating its directories, naming target as given.
func (h *moveHome) Link(target, link string) {
	h.t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		h.t.Fatal(err)
	}
}

// Copy copies the file src to dst (creating its directories) with mode.
func (h *moveHome) Copy(src, dst string, mode os.FileMode) {
	h.t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		h.t.Fatal(err)
	}
	h.Write(dst, string(b), mode)
}

// WriteTool writes the bash stand-in Tools/name running script, and returns its
// path. The script sees the install's environment, h.Env included.
func (h *moveHome) WriteTool(name, script string) string {
	h.t.Helper()
	p := filepath.Join(h.Tools, name)
	h.Write(p, "#!/bin/bash\n"+script+"\n", 0o755)
	return p
}

// RealTool is the path of the command a stand-in for name wraps: the one in the
// shared toolbox.
func (h *moveHome) RealTool(name string) string { return filepath.Join(h.S.Toolbox, name) }

// WrapTool writes a stand-in for name that runs preamble (bash; $@ is the
// call's arguments, ${!#} its last) and then the real command with the same
// arguments, and returns its path.
func (h *moveHome) WrapTool(name, preamble string) string {
	h.t.Helper()
	return h.WriteTool(name, preamble+"\nexec "+moveQuote(h.RealTool(name))+` "$@"`)
}

// moveQuote single-quotes s for bash.
func moveQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// moveResult is a finished install.sh run.
type moveResult struct {
	Code           int
	Stdout, Stderr string
}

// Last is stderr's last non-empty line.
func (r moveResult) Last() string {
	lines := strings.Split(strings.TrimRight(r.Stderr, "\n"), "\n")
	return lines[len(lines)-1]
}

func (h *moveHome) command(args ...string) *exec.Cmd {
	argv := append([]string{h.S.Install, "--binary", h.Pair.Main, "--admin-binary", h.Pair.Admin}, h.Flags...)
	cmd := exec.Command(h.S.Bash, append(argv, args...)...)
	cmd.Dir = h.Cwd
	cmd.Env = []string{"HOME=" + h.Home, "PATH=" + h.Tools + ":" + h.S.Toolbox, "TMPDIR=" + h.Tmp}
	keys := make([]string, 0, len(h.Env))
	for k := range h.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cmd.Env = append(cmd.Env, k+"="+h.Env[k])
	}
	return cmd
}

// moveRunning is an install.sh started by StartInstall.
type moveRunning struct {
	cmd            *exec.Cmd
	stdout, stderr bytes.Buffer
}

// StartInstall starts install.sh (the pair, h.Flags, then args) in its own
// process group and returns at once.
func (h *moveHome) StartInstall(args ...string) *moveRunning {
	h.t.Helper()
	r := &moveRunning{cmd: h.command(args...)}
	r.cmd.Stdout, r.cmd.Stderr = &r.stdout, &r.stderr
	r.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := r.cmd.Start(); err != nil {
		h.t.Fatal(err)
	}
	h.OnRelease(func() { _ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL) })
	return r
}

// Signal sends sig to install.sh and every process it started.
func (r *moveRunning) Signal(sig syscall.Signal) { _ = syscall.Kill(-r.cmd.Process.Pid, sig) }

// Wait waits for install.sh to end and returns its result; a signal that ended
// it gives Code 128+signal, as a shell reports it.
func (r *moveRunning) Wait(t *testing.T) moveResult {
	t.Helper()
	err := r.cmd.Wait()
	res := moveResult{Stdout: r.stdout.String(), Stderr: r.stderr.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.Code = ee.ExitCode()
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.Code = 128 + int(ws.Signal())
		}
	default:
		t.Fatalf("install.sh: %v", err)
	}
	return res
}

// Install runs install.sh (the pair, h.Flags, then args) to its end.
func (h *moveHome) Install(args ...string) moveResult {
	h.t.Helper()
	return h.StartInstall(args...).Wait(h.t)
}

// Run runs a command in the case's environment (a binary the install put in
// place, say) and returns its result.
func (h *moveHome) Run(name string, args ...string) moveResult {
	h.t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = h.Cwd
	cmd.Env = []string{"HOME=" + h.Home, "PATH=" + h.Tools + ":" + h.S.Toolbox, "TMPDIR=" + h.Tmp}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	res := moveResult{}
	var ee *exec.ExitError
	if err := cmd.Run(); errors.As(err, &ee) {
		res.Code = ee.ExitCode()
	} else if err != nil {
		h.t.Fatalf("%s: %v", name, err)
	}
	res.Stdout, res.Stderr = out.String(), errb.String()
	return res
}

// moveProc is a process the fixture started and waits to print a line.
type moveProc struct {
	Pid     int
	cmd     *exec.Cmd
	drained chan struct{}
	once    sync.Once
}

// Kill stops the process (SIGKILL, so nothing it holds is cleaned up) and
// reaps it; calling it again does nothing.
func (p *moveProc) Kill() {
	p.once.Do(func() {
		_ = p.cmd.Process.Kill()
		<-p.drained
		_ = p.cmd.Wait()
	})
}

// startProc starts cmd with its stdin open, writes input to it, and waits until
// it prints the line ready. The process is killed by Release.
func (h *moveHome) startProc(cmd *exec.Cmd, input, ready string) *moveProc {
	h.t.Helper()
	cmd.Env = []string{"HOME=" + h.Home}
	in, err := cmd.StdinPipe()
	if err != nil {
		h.t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		h.t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		h.t.Fatal(err)
	}
	p := &moveProc{Pid: cmd.Process.Pid, cmd: cmd, drained: make(chan struct{})}
	h.OnRelease(p.Kill)
	seen := make(chan struct{})
	go func() {
		defer close(p.drained)
		sc := bufio.NewScanner(out)
		var closed bool
		for sc.Scan() {
			if !closed && sc.Text() == ready {
				closed = true
				close(seen)
			}
		}
	}()
	if input != "" {
		if _, err := io.WriteString(in, input); err != nil {
			h.t.Fatalf("%s: %v", cmd.Path, err)
		}
	}
	select {
	case <-seen:
	case <-time.After(moveWait):
		h.t.Fatalf("%s did not print %q within %v", cmd.Path, ready, moveWait)
	}
	return p
}

// StartServe runs the stand-in binary at path as `serve` (an MCP server that
// lasts until its session ends) and returns once it runs. path may be a link.
func (h *moveHome) StartServe(path string) *moveProc {
	h.t.Helper()
	return h.startProc(exec.Command(path, "serve"), "", "ready")
}

// HoldSQLite starts the real sqlite3 on db, runs sql in it and leaves it
// running (killed by Release or Kill). Without -wal checkpointing, a SIGKILL
// leaves what sql wrote as unflushed pages in db's -wal.
func (h *moveHome) HoldSQLite(db, sql string) *moveProc {
	h.t.Helper()
	return h.startProc(exec.Command(h.S.SQLite, "-batch", "-init", "/dev/null", db), sql+"\nSELECT 'ready';\n", "ready")
}

// Sqlite runs the real sqlite3 on db with input on its stdin.
func (h *moveHome) Sqlite(db, input string, args ...string) string {
	h.t.Helper()
	cmd := exec.Command(h.S.SQLite, append(append([]string{"-batch", "-init", "/dev/null"}, args...), db)...)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		h.t.Fatalf("sqlite3 %s: %v\n%s", db, err, out)
	}
	return string(out)
}

// WriteStore creates a v0.11.1 store at path, from testdata/v0.11.1-store.sql.
func (h *moveHome) WriteStore(path string) {
	h.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		h.t.Fatal(err)
	}
	b, err := os.ReadFile(h.S.SQLFixt)
	if err != nil {
		h.t.Fatal(err)
	}
	h.Sqlite(path, string(b))
}

// moveLiveRow is the SQL that inserts a row in state, with the id live-<state>.
func moveLiveRow(state string) string {
	return fmt.Sprintf("INSERT INTO spawns (claude_instance_id, state, cwd, tmux_session_name, relay_mode) "+
		"VALUES ('live-%[1]s', '%[1]s', '/work/live', 'ad-live-%[1]s', 'off');", state)
}

// moveOldSpec is what BuildOld builds; moveOldOpt options change it.
type moveOldSpec struct {
	config   func(h *moveHome) string
	store    func(h *moveHome) string
	noStore  bool
	rollback bool
	live     []string
}

type moveOldOpt func(*moveOldSpec)

// withMoveConfig sets config.toml's text (it may name paths under h.Home).
func withMoveConfig(f func(h *moveHome) string) moveOldOpt {
	return func(s *moveOldSpec) { s.config = f }
}

// withMoveStore puts the store at f's path, not Old/state.db; the caller's
// config.toml must name it.
func withMoveStore(f func(h *moveHome) string) moveOldOpt {
	return func(s *moveOldSpec) { s.store = f }
}

// withMoveNoStore leaves the old install without a store.
func withMoveNoStore() moveOldOpt { return func(s *moveOldSpec) { s.noStore = true } }

// withMoveRollbackStore makes the store a rollback-journal one with no -wal.
func withMoveRollbackStore() moveOldOpt { return func(s *moveOldSpec) { s.rollback = true } }

// withMoveLive adds a row in each of states to the store.
func withMoveLive(states ...string) moveOldOpt {
	return func(s *moveOldSpec) { s.live = append(s.live, states...) }
}

// moveOldConfig is an old install's config.toml: no path keys.
const moveOldConfig = "# agent-director config\n[defaults]\ninject_help_hook = true\n\n[store]\nbusy_timeout_ms = 5000\n"

// BuildOld builds ~/.agent-director shaped like v0.11.1's: config.toml,
// templates, a store (testdata/v0.11.1-store.sql, with one more row and a
// live row per withMoveLive held as unflushed pages in its -wal, as a store
// whose agent was killed leaves), the trail, the error log, bin/ and admin/
// holding the stand-in, and backups/.
func (h *moveHome) BuildOld(opts ...moveOldOpt) {
	h.t.Helper()
	spec := moveOldSpec{config: func(*moveHome) string { return moveOldConfig }, store: func(h *moveHome) string { return filepath.Join(h.Old, "state.db") }}
	for _, o := range opts {
		o(&spec)
	}
	if err := os.MkdirAll(h.Old, 0o700); err != nil {
		h.t.Fatal(err)
	}
	h.Write(filepath.Join(h.Old, "config.toml"), spec.config(h), 0o600)
	h.Write(filepath.Join(h.Old, "templates", "worker.toml"), "name = \"worker\"\nclaude_args = [\"--model\", \"sonnet\"]\n", 0o644)
	h.Write(filepath.Join(h.Old, "templates", "reviewer.toml"), "name = \"reviewer\"\n", 0o600)
	h.Write(filepath.Join(h.Old, "ad-trail.jsonl"), "{\"event\":\"spawn\",\"id\":\"ended-1\"}\n{\"event\":\"end\",\"id\":\"ended-1\"}\n", 0o600)
	h.Write(filepath.Join(h.Old, "errors.log"), "2024-03-01T10:00:00Z ErrExample: an old error\n", 0o640)
	h.Write(filepath.Join(h.Old, "backups", "state.db.pre-migrate-20240101-000000"), "an earlier store's backup\n", 0o600)
	h.Copy(h.S.OldBinary, filepath.Join(h.Old, "bin", "agent-director"), 0o755)
	h.Copy(h.S.OldBinary, filepath.Join(h.Old, "admin", "agent-director-admin"), 0o755)
	if spec.noStore {
		return
	}
	db := spec.store(h)
	h.WriteStore(db)
	if spec.rollback {
		h.Sqlite(db, "PRAGMA journal_mode=DELETE;\n"+strings.Join(moveMap(spec.live, moveLiveRow), "\n"))
		return
	}
	extra := "INSERT INTO spawns (claude_instance_id, state, cwd, tmux_session_name, relay_mode) VALUES ('ended-3', 'ended', '/work/four', 'ad-ended-3', 'off');\n"
	h.HoldSQLite(db, "PRAGMA wal_autocheckpoint=0;\n"+extra+strings.Join(moveMap(spec.live, moveLiveRow), "\n")).Kill()
	if fi, err := os.Stat(db + "-wal"); err != nil || fi.Size() == 0 {
		h.t.Fatalf("the old store has no unflushed -wal pages (%v)", err)
	}
}

func moveMap(in []string, f func(string) string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = f(s)
	}
	return out
}

// moveTree maps each path under HOME (relative to it) to what SR-14.3 compares.
type moveTree map[string]string

var moveSQLiteMagic = []byte("SQLite format 3\x00")

// Snapshot describes everything under HOME by SR-14.3's rule: a store (a file
// that starts with SQLite's magic) by mode, user_version and `.sha3sum
// --schema`; every other file by mode and bytes; a directory by mode and a link
// by its target; an -shm and an empty -wal not at all. Paths in ignore are left
// out.
func (h *moveHome) Snapshot(ignore ...string) moveTree {
	h.t.Helper()
	tree := moveTree{}
	err := filepath.WalkDir(h.Home, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(h.Home, p)
		for _, ig := range ignore {
			if rel == ig {
				return nil
			}
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		mode := fi.Mode()
		switch {
		case mode&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			tree[rel] = "link -> " + target
		case mode.IsDir():
			tree[rel] = fmt.Sprintf("dir %v", mode)
		case mode.IsRegular():
			tree[rel] = h.describeFile(p, rel, fi)
			if tree[rel] == "" {
				delete(tree, rel)
			}
		default:
			tree[rel] = fmt.Sprintf("special %v", mode)
		}
		return nil
	})
	if err != nil {
		h.t.Fatalf("snapshot %s: %v", h.Home, err)
	}
	return tree
}

func (h *moveHome) describeFile(p, rel string, fi fs.FileInfo) string {
	switch {
	case strings.HasSuffix(rel, "-shm"):
		return ""
	case strings.HasSuffix(rel, "-wal") && fi.Size() == 0:
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return fmt.Sprintf("file %v unreadable: %v", fi.Mode(), err)
	}
	if !bytes.HasPrefix(b, moveSQLiteMagic) {
		return fmt.Sprintf("file %v %x", fi.Mode(), sha256.Sum256(b))
	}
	cmd := exec.Command(h.S.SQLite, "-readonly", "-batch", "-init", "/dev/null", p)
	cmd.Stdin = strings.NewReader(".sha3sum --schema\nPRAGMA user_version;\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("store %v unreadable: %v %s", fi.Mode(), err, out)
	}
	return fmt.Sprintf("store %v %s", fi.Mode(), strings.Join(strings.Fields(string(out)), " "))
}

// moveDiff describes how got differs from want, one path a line; "" when equal.
func moveDiff(want, got moveTree) string {
	var lines []string
	for p, w := range want {
		if g, ok := got[p]; !ok {
			lines = append(lines, fmt.Sprintf("removed: %s (%s)", p, w))
		} else if g != w {
			lines = append(lines, fmt.Sprintf("changed: %s\n    was %s\n    now %s", p, w, g))
		}
	}
	for p, g := range got {
		if _, ok := want[p]; !ok {
			lines = append(lines, fmt.Sprintf("added: %s (%s)", p, g))
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// moveWant is what a refusal's run must show.
type moveWant struct {
	Code int
	// Err is the err_name on stderr's last line; "" means exit 3's refusal, which
	// has none.
	Err string
	// Stderr and Stdout are substrings the stream holds; StderrRE are regular
	// expressions it matches; NoStdout and NoStderr are substrings it lacks.
	Stderr, Stdout, StderrRE, NoStdout, NoStderr []string
}

// expand replaces {HOME} and each {name} of h.Vars in s.
func (h *moveHome) expand(s string) string {
	s = strings.ReplaceAll(s, "{HOME}", h.Home)
	for k, v := range h.Vars {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return s
}

func (h *moveHome) expandAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = h.expand(s)
	}
	return out
}

// Check fails the case unless res is the refusal want describes and tmp holds
// no temp file install.sh left. Its strings may hold {HOME} and h.Vars names.
func (w moveWant) Check(t *testing.T, h *moveHome, res moveResult) {
	t.Helper()
	w.Stderr, w.Stdout, w.NoStdout, w.NoStderr = h.expandAll(w.Stderr), h.expandAll(w.Stdout), h.expandAll(w.NoStdout), h.expandAll(w.NoStderr)
	w.StderrRE = h.expandAll(w.StderrRE)
	if res.Code != w.Code {
		t.Errorf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", res.Code, w.Code, res.Stdout, res.Stderr)
	}
	if w.Err != "" {
		if want := "install.sh: err_name=" + w.Err; res.Last() != want {
			t.Errorf("last stderr line = %q, want %q\nstderr:\n%s", res.Last(), want, res.Stderr)
		}
	} else if strings.Contains(res.Stderr, "err_name=") {
		t.Errorf("stderr names an err_name, which exit %d has none of:\n%s", w.Code, res.Stderr)
	}
	for _, s := range w.Stderr {
		if !strings.Contains(res.Stderr, s) {
			t.Errorf("stderr lacks %q:\n%s", s, res.Stderr)
		}
	}
	for _, s := range w.Stdout {
		if !strings.Contains(res.Stdout, s) {
			t.Errorf("stdout lacks %q:\n%s", s, res.Stdout)
		}
	}
	for _, s := range w.StderrRE {
		if !regexp.MustCompile(s).MatchString(res.Stderr) {
			t.Errorf("stderr does not match %q:\n%s", s, res.Stderr)
		}
	}
	for _, s := range w.NoStdout {
		if strings.Contains(res.Stdout, s) {
			t.Errorf("stdout holds %q:\n%s", s, res.Stdout)
		}
	}
	for _, s := range w.NoStderr {
		if strings.Contains(res.Stderr, s) {
			t.Errorf("stderr holds %q:\n%s", s, res.Stderr)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(h.Tmp, "*")); len(left) > 0 {
		t.Errorf("install.sh left temp files behind: %v", left)
	}
}
