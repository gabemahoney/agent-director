package installsh_test

// Staged-install glue (reusable test fixture) reuses moveHome, the cached
// binary builder, physical
// snapshots and the cutover outcome/FIFO fixture. The syscall observer follows
// the 5g CLI observer, expanded to the installer's complete descendant tree.
import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/gabemahoney/agent-director/internal/installversion"
	"github.com/gabemahoney/agent-director/internal/layout"
	"github.com/gabemahoney/agent-director/internal/probe"
)

const stagedStamp = "0.0.10-staged"

var stagedBuild struct {
	sync.Once
	pair movePair
	err  error
}

// stagedPair builds one additional stamp, with only the admin's per-engine
// Observe hook wired by a Go overlay. Compiled compatibility facts are real.
func stagedPair(t *testing.T) movePair {
	t.Helper()
	s := moveFixture(t)
	stagedBuild.Do(func() {
		repo, err := filepath.Abs("../..")
		if err != nil {
			stagedBuild.err = err
			return
		}
		repo, err = filepath.EvalSymlinks(repo)
		if err != nil {
			stagedBuild.err = err
			return
		}
		dir := filepath.Join(s.Root, "staged")
		if err = os.MkdirAll(dir, 0700); err != nil {
			stagedBuild.err = err
			return
		}
		adapter := filepath.Join(repo, "internal/adminapi/install_versions.go")
		data, err := os.ReadFile(adapter)
		if err != nil {
			stagedBuild.err = err
			return
		}
		needle := "return publishVersion(ctx, installversion.Engine{Root: root}, runtimeBinary, self)"
		if strings.Count(string(data), needle) != 1 {
			stagedBuild.err = fmt.Errorf("publication hook construction changed")
			return
		}
		data = []byte(strings.Replace(string(data), needle, "return publishVersion(ctx, installversion.Engine{Root: root, Hooks: installversion.Hooks{Observe: installerTestObserve(root)}}, runtimeBinary, self)", 1))
		adapterCopy := filepath.Join(dir, "install_versions.go")
		hookCopy := filepath.Join(dir, "observe.go")
		hook, err := os.ReadFile("testdata/staged-observe.go.in")
		if err != nil {
			stagedBuild.err = err
			return
		}
		overlay, err := json.Marshal(map[string]any{"Replace": map[string]string{adapter: adapterCopy, filepath.Join(repo, "internal/adminapi/installer_test_observe.go"): hookCopy}})
		if err != nil {
			stagedBuild.err = err
			return
		}
		overlayPath := filepath.Join(dir, "overlay.json")
		for p, b := range map[string][]byte{adapterCopy: data, hookCopy: hook, overlayPath: overlay} {
			if err = os.WriteFile(p, b, 0600); err != nil {
				stagedBuild.err = err
				return
			}
		}
		flags := fmt.Sprintf("-X %s.Version=%s -X %s.Commit=%s", moveVersPkg, stagedStamp, moveVersPkg, moveCommit)
		stagedBuild.pair.Main, err = moveBuildBinary(repo, filepath.Join(dir, "agent-director"), flags, "./cmd/agent-director")
		if err == nil {
			stagedBuild.pair.Admin, err = moveBuildBinary(repo, filepath.Join(dir, "agent-director-admin"), flags, "./cmd/agent-director-admin", "-overlay", overlayPath)
		}
		stagedBuild.err = err
	})
	if stagedBuild.err != nil {
		t.Fatal(stagedBuild.err)
	}
	return stagedBuild.pair
}

var stagedEvidence = []string{layout.InstallContract, layout.InstallInitialization, "current", "versions", "publications", "install.lock", "bin/agent-director", "admin/agent-director-admin"}

func stagedMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func stagedLayout(t *testing.T, h *moveHome, stamp string) {
	t.Helper()
	versions, err := installversion.ListInstalled(h.New)
	stagedMust(t, err)
	if len(versions) == 0 {
		t.Fatal("no retained complete build")
	}
	selection, err := installversion.ReadPublished(h.New)
	stagedMust(t, err)
	if selection == nil || selection.Version != stamp {
		t.Fatalf("selected %+v; want %s", selection, stamp)
	}
	init, err := installversion.ReadInitialization(h.New)
	stagedMust(t, err)
	if !init.Complete {
		t.Fatalf("incomplete establishment: %+v", init)
	}
	for _, rel := range stagedEvidence {
		_, err := os.Lstat(filepath.Join(h.New, rel))
		stagedMust(t, err)
	}
	for rel, target := range map[string]string{"bin/agent-director": "../current/agent-director", "admin/agent-director-admin": "../current/agent-director-admin"} {
		got, err := os.Readlink(filepath.Join(h.New, rel))
		stagedMust(t, err)
		if got != target {
			t.Fatalf("%s -> %q; want %q", rel, got, target)
		}
		r := h.Run(filepath.Join(h.New, rel), "version")
		var v struct{ Version, Commit string }
		if r.Code != 0 || json.Unmarshal([]byte(r.Stdout), &v) != nil || v.Version != stamp || v.Commit != moveCommit {
			t.Fatalf("unusable canonical pair: %+v", r)
		}
	}
	fi, err := os.Stat(filepath.Join(h.New, "admin"))
	stagedMust(t, err)
	if fi.Mode().Perm() != 0700 {
		t.Fatalf("admin mode %v", fi.Mode())
	}
}

// PhysicalSnapshot already captures bytes/mode/mtime/links. Add inode identity
// for the selected paths, including links (which must not be recreated).
type stagedPhysical struct {
	tree   moveTree
	inodes map[string]os.FileInfo
}

func stagedSnapshot(t *testing.T, h *moveHome, paths ...string) stagedPhysical {
	t.Helper()
	s := stagedPhysical{tree: moveTree{}, inodes: map[string]os.FileInfo{}}
	all := h.PhysicalSnapshot()
	for rel, value := range all {
		for _, path := range paths {
			if rel == path || strings.HasPrefix(rel, path+"/") || strings.HasPrefix(rel, path+".") {
				fi, err := os.Lstat(filepath.Join(h.Home, rel))
				stagedMust(t, err)
				if fi.IsDir() {
					break
				} // containers can gain immutable children
				s.tree[rel] = value
				s.inodes[rel] = fi
				break
			}
		}
	}
	return s
}
func (s stagedPhysical) Unchanged(t *testing.T, h *moveHome) {
	t.Helper()
	after := h.PhysicalSnapshot()
	for p, old := range s.tree {
		fi, err := os.Lstat(filepath.Join(h.Home, p))
		if err != nil || after[p] != old || !os.SameFile(s.inodes[p], fi) {
			t.Errorf("changed %s: %v\nwas %s\nnow %s", p, err, old, after[p])
		}
	}
}

// stagedPoison makes accidental old/new/default/configured path fallthrough
// observable. Setup and assertions run outside the observation interval.
func stagedPoison(h *moveHome, custom bool) []string {
	paths := []string{}
	for _, dir := range []string{h.Old, h.New, filepath.Join(h.Home, "outside")} {
		name := "state.db"
		if strings.HasSuffix(dir, "outside") {
			name = "external-runtime.sqlite"
		}
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			p := filepath.Join(dir, name+suffix)
			h.Write(p, "not a database: never inspect this runtime file\n", 0600)
			rel, _ := filepath.Rel(h.Home, p)
			paths = append(paths, rel)
		}
	}
	cfg := "[store]\nbusy_timeout_ms = 0\n"
	if custom {
		cfg += "db_path = " + fmt.Sprintf("%q", filepath.Join(h.Home, "outside/external-runtime.sqlite")) + "\n"
	}
	h.Write(filepath.Join(h.New, "config.toml"), cfg, 0600)
	for _, rel := range []string{"launch/0123456789abcdef/fedcba9876543210/wake", "input/0123456789abcdef/fedcba9876543210/attempt.json", "ad-trail.jsonl"} {
		h.Write(filepath.Join(h.New, rel), "runtime evidence\n", 0600)
		paths = append(paths, filepath.Join(moveNewRel, rel))
	}
	return paths
}

// stagedProcess pins a private runtime identity throughout a case.
type stagedProcess struct {
	process *moveProc
	start   string
}

// Keep the observed runtime alive across intermediate h.Release calls from
// procedure shells. Its separate moveHome owns cleanup at the case's end.
func stagedRuntime(t *testing.T, h *moveHome) *stagedProcess {
	t.Helper()
	if h.stagedRuntime == nil {
		owner := newMoveHome(t)
		p := owner.startProc(exec.Command(h.S.Bash, "-c", "printf 'ready\\n'; IFS= read -r done"), "", "ready")
		start, alive, known := probe.NewProcChecker().StartTime(p.Pid)
		if !alive || !known {
			t.Fatal("case runtime identity unavailable after readiness")
		}
		h.stagedRuntime = &stagedProcess{process: p, start: start}
	}
	h.stagedRuntime.check(t)
	return h.stagedRuntime
}

func (p *stagedProcess) check(t *testing.T) {
	t.Helper()
	start, alive, known := probe.NewProcChecker().StartTime(p.process.Pid)
	if !alive || !known || start != p.start {
		t.Fatal("case runtime exited or changed identity during observation")
	}
}

// stagedTrace is mandatory attempted-access instrumentation, not a hash check.
// -f follows forks, execs and Go threads. -yy decodes descriptor paths; raw
// buffers avoid confusing embedded strings copied from executables with paths.
type stagedTrace struct {
	h       *moveHome
	log     string
	running *moveRunning
}

func stagedTraceCommand(t *testing.T, cmd *exec.Cmd) string {
	t.Helper()
	tracer, err := exec.LookPath("strace")
	if err != nil {
		t.Fatal("installer access observation requires strace and own-child tracing; no instrumentation skip")
	}
	log := filepath.Join(t.TempDir(), "syscalls.log")
	args := []string{"-f", "-qq", "-I", "4", "-s", "4096", "-yy", "-o", log, "-e", "trace=%file,%desc,%process,%signal,%network", "-e", "raw=read,write,readv,writev,pread64,pwrite64,preadv,pwritev,getdents64", "--"}
	cmd.Args = append(append([]string{tracer}, args...), cmd.Args...)
	cmd.Path = tracer
	return log
}
func stagedStartTrace(t *testing.T, h *moveHome, cmd *exec.Cmd) *stagedTrace {
	t.Helper()
	stagedRuntime(t, h)
	f := &stagedTrace{h: h, log: stagedTraceCommand(t, cmd)}
	r := &moveRunning{cmd: cmd}
	cmd.Stdout, cmd.Stderr = &r.stdout, &r.stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stagedMust(t, cmd.Start())
	h.OnRelease(func() { r.Signal(syscall.SIGKILL); r.Wait(t) })
	f.running = r
	return f
}
func (f *stagedTrace) Finish(t *testing.T) (moveResult, string) {
	t.Helper()
	r := f.running.Wait(t)
	data, err := os.ReadFile(f.log)
	stagedMust(t, err)
	trace := string(data)
	if !strings.Contains(trace, "execve(") || strings.Contains(r.Stderr, "strace:") || !strings.Contains(trace, "exit_group(") {
		t.Fatalf("incomplete/unavailable observer: %+v\n%s", r, trace)
	}
	stagedRuntime(t, f.h).check(t)
	return r, trace
}

// strace may split a syscall while another thread runs. Rejoin by caller tid
// before deriving clone ownership or checking a signal's target.
func stagedTraceLines(trace string) []string {
	var lines []string
	pending := map[string]string{}
	for _, line := range strings.Split(trace, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pid := fields[0]
		if i := strings.Index(line, "<unfinished ...>"); i >= 0 {
			pending[pid] = line[:i]
			continue
		}
		if i := strings.Index(line, "<... "); i >= 0 {
			if j := strings.Index(line, " resumed>"); j >= 0 && pending[pid] != "" {
				line = pending[pid] + line[j+len(" resumed>"):]
				delete(pending, pid)
			}
		}
		lines = append(lines, line)
	}
	// A syscall interrupted by a fault signal is still an attempted access.
	for _, line := range pending {
		lines = append(lines, line)
	}
	return lines
}

func stagedForbidden(trace string, runtimePID int) []string {
	var hits []string
	paths := regexp.MustCompile(`state\.db|external-runtime\.sqlite|migrate-authorized|ad-trail\.jsonl|(?:/|")(?:launch|input)(?:/|"|>)`)
	verbs := regexp.MustCompile(`"(?:snapshot-store|migrate-journal|list|serve|pause|kill|sweep|resume|send-keys|decide|sqlite3|tmux|ps|pgrep)"`)
	ipc := regexp.MustCompile(`\b(?:connect|sendto|sendmsg)\(.*(?:/agent-director/|/ad-|/tmux-)`)
	lines := stagedTraceLines(trace)
	caller := regexp.MustCompile(`^\s*([0-9]+)\s+`)
	thread := regexp.MustCompile(`\bclone3?\(.*CLONE_THREAD.*= ([0-9]+)(?:\s|$)`)
	signal := regexp.MustCompile(`\b(kill|tkill|tgkill)\((-?[0-9]+),\s*([^,)]*)`)
	runtimePath := regexp.MustCompile(fmt.Sprintf(`(?:/proc/|")%d(?:/|"|>)`, runtimePID))
	traced := map[int]bool{}
	threadParent := map[int]int{}
	for _, line := range lines {
		if m := caller.FindStringSubmatch(line); m != nil {
			pid, _ := strconv.Atoi(m[1])
			traced[pid] = true
			if child := thread.FindStringSubmatch(line); child != nil {
				tid, _ := strconv.Atoi(child[1])
				threadParent[tid] = pid
				traced[tid] = true
			}
		}
	}
	group := func(pid int) int {
		for threadParent[pid] != 0 {
			pid = threadParent[pid]
		}
		return pid
	}
	for _, line := range lines {
		if strings.Contains(line, "execve(") {
			// tmux -V is prerequisite version discovery, not a server read or mutation.
			// MCP registration records argv for a future server; it does not
			// execute that runtime. A real server descendant is still traced.
			metadata := strings.Contains(line, `["claude", "mcp", "add",`)
			if verbs.MatchString(line) && !strings.Contains(line, `["tmux", "-V"]`) && !metadata {
				hits = append(hits, line)
			}
			continue // argv alone is not a filesystem attempt
		}
		foreignSignal := false
		if m := signal.FindStringSubmatch(line); m != nil {
			c := caller.FindStringSubmatch(line)
			if c == nil {
				foreignSignal = true
			} else {
				pid, _ := strconv.Atoi(c[1])
				target, _ := strconv.Atoi(m[2])
				// Nonpositive targets can reach a process group or every process.
				foreignSignal = target <= 0 || !traced[target] || group(target) != group(pid)
				if m[1] == "tgkill" {
					tid, err := strconv.Atoi(m[3])
					foreignSignal = foreignSignal || err != nil || target != group(pid) || !traced[tid] || group(tid) != target
				}
			}
		}
		if paths.MatchString(line) || ipc.MatchString(line) || runtimePath.MatchString(line) || foreignSignal {
			hits = append(hits, line)
		}
	}
	return hits
}
func stagedObserve(t *testing.T, h *moveHome, args ...string) (moveResult, string) {
	t.Helper()
	return stagedObserveCmd(t, h, h.command(args...))
}
func stagedObserveCmd(t *testing.T, h *moveHome, cmd *exec.Cmd) (moveResult, string) {
	t.Helper()
	r, trace := stagedStartTrace(t, h, cmd).Finish(t)
	if hits := stagedForbidden(trace, stagedRuntime(t, h).process.Pid); len(hits) != 0 {
		t.Fatalf("installer attempted runtime access:\n%s", strings.Join(hits, "\n"))
	}
	return r, trace
}

func stagedFault(t *testing.T, h *moveHome, f *cutoverFixture, point, action string) string {
	t.Helper()
	f.Barrier()
	p := filepath.Join(h.New, ".installer-test-fault.json")
	b, err := json.Marshal(map[string]string{"Point": point, "Ready": f.Ready, "Go": f.Go, "Action": action})
	stagedMust(t, err)
	h.Write(p, string(b), 0600)
	return p
}

// Observe the existing persistent doc shell, including its installer/admin
// descendants. Release closes the shell before the final trace is inspected.
func stagedOpsShell(t *testing.T, h *moveHome) (*opsShell, func()) {
	t.Helper()
	var log string
	var stderr bytes.Buffer
	runtime := stagedRuntime(t, h)
	sh := newOpsShell(h, func(cmd *exec.Cmd) { log = stagedTraceCommand(t, cmd); cmd.Stderr = &stderr })
	return sh, func() {
		h.Release()
		b, err := os.ReadFile(log)
		stagedMust(t, err)
		trace := string(b)
		if stderr.Len() != 0 {
			t.Fatalf("document observer stderr: %s", stderr.String())
		}
		if !strings.Contains(trace, "execve(") || !strings.Contains(trace, "exit_group(") {
			t.Fatal("document observer did not complete")
		}
		runtime.check(t)
		if hits := stagedForbidden(trace, runtime.process.Pid); len(hits) != 0 {
			t.Fatalf("document follow attempted runtime access:\n%s", strings.Join(hits, "\n"))
		}
	}
}
