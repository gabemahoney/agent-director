package installsh_test

// The cutover-outcome fixture (reusable test fixture) routes only the
// candidate's migrate-journal attempt to the existing administrator child
// runner, a FIFO barrier or a canned response. Installer tests needing a
// chosen outcome use this fixture, never a second stand-in or child runner.
// Signal modes share the FIFO barrier, including the post-attempt snapshot
// in standin. Start runs a chosen bash in a process group with an optional
// pipe writer/reader; all group members are reaped by this fixture.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

var outcomeChild struct {
	once sync.Once
	path string
	err  error
}

type cutoverFixture struct {
	h                                                  *moveHome
	Control, After, Output, Calls, Finished, Ready, Go string
	ready, goFile                                      *os.File
	reader                                             *exec.Cmd
}

func newCutoverFixture(t *testing.T, h *moveHome) *cutoverFixture {
	t.Helper()
	outcomeChild.once.Do(func() {
		outcomeChild.path = filepath.Join(h.S.Root, "admin-child.test")
		cmd := exec.Command("go", "test", "-c", "-o", outcomeChild.path, "./cmd/agent-director-admin")
		cmd.Dir = filepath.Join("..", "..")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			outcomeChild.err = fmt.Errorf("build administrator child: %v: %s", err, out)
		}
	})
	if outcomeChild.err != nil {
		t.Fatal(outcomeChild.err)
	}
	f := &cutoverFixture{h: h}
	for name, p := range map[string]*string{"control": &f.Control, "after": &f.After, "output": &f.Output,
		"calls": &f.Calls, "finished": &f.Finished, "ready": &f.Ready, "go": &f.Go} {
		*p = filepath.Join(h.Root, "cutover-"+name)
	}
	h.Write(f.Output, "", 0o600) // before-start outcomes have no attempt to relay
	router := filepath.Join(h.Root, "cutover-router")
	// The control's first line is real, child:<seams>, canned, block, or signal.
	// block takes its eventual mode from line two. An absent control delegates
	// to the real administrator, including when advice is followed literally.
	h.Write(router, `#!/bin/bash
printf '%s\n' "$*" >>`+moveQuote(f.Calls)+`
attempt=0
for a; do
    case "$a" in
        migrate-journal) attempt=1 ;;
        --check-only|--help) attempt=0; break ;;
    esac
done
if [[ "$attempt" == 0 ]]; then exec `+moveQuote(h.S.Current.Admin)+` "$@"; fi
mode=real
if [[ -f `+moveQuote(f.Control)+` ]]; then IFS= read -r mode <`+moveQuote(f.Control)+`; fi
if [[ "$mode" == block ]]; then
    printf 'ready\n' >`+moveQuote(f.Ready)+`
    IFS= read -r go <`+moveQuote(f.Go)+`
    [[ "$go" == go ]] || exit 96
    mode=$(sed -n '2p' `+moveQuote(f.Control)+`)
fi
# The supervisor preserves the delegate's output/status; the subshell resets
# its recording traps and execs, so the group signal reaches the actual child.
trap ':' INT TERM HUP
(
trap - INT TERM HUP
case "$mode" in
    real) exec `+moveQuote(h.S.Current.Admin)+` "$@" ;;
    child:*) export AD_ADMIN_CHILD="${mode#child:}"; exec `+moveQuote(outcomeChild.path)+` -test.run '^TestMigrateJournalAdminChild$' -- "$@" ;;
    canned) exec bash `+moveQuote(f.Control+".response")+` ;;
    signal)
        cancelled() {
            kill -TERM "$blocker" 2>/dev/null
            wait "$blocker" 2>/dev/null
            cat `+moveQuote(f.Control+".response")+`
            exit 1
        }
        trap cancelled INT TERM HUP
        cat <`+moveQuote(f.Go)+` >/dev/null 2>&1 &
        blocker=$!
        printf 'ready\n' >`+moveQuote(f.Ready)+`
        wait "$blocker"
        exit 96 ;;
    *) echo "unknown outcome mode: $mode" >&2; exit 98 ;;
esac
) >`+moveQuote(f.Output)+` 2>&1
rc=$?
printf '%s\n' "$rc" >`+moveQuote(f.Finished)+`
if [[ -f `+moveQuote(f.After)+` ]]; then bash `+moveQuote(f.After)+`; fi
cat `+moveQuote(f.Output)+`
exit "$rc"
`, 0o755)
	h.Pair = movePair{h.S.Current.Main, h.S.ScheduleProbe.Main}
	h.Env["SCHED_TEST_DELEGATE"] = router
	h.Env["SCHED_TEST_DELEGATE_EXEC"] = "1"
	h.Env["AD_TEST_READY"], h.Env["AD_TEST_GO"] = f.Ready, f.Go
	// h.command deliberately strips ambient variables. Preserve the actual
	// guard environment for the child binary; never invent a sandbox marker.
	for _, key := range []string{sandboxguard.EnvVar, sandboxguard.BypassEnvVar} {
		if value := os.Getenv(key); value != "" {
			h.Env[key] = value
		}
	}
	return f
}

func (f *cutoverFixture) Choose(mode string) { f.h.Write(f.Control, mode+"\n", 0o600) }
func (f *cutoverFixture) Clear() {
	f.h.t.Helper()
	for _, p := range []string{f.Control, f.After} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			f.h.t.Fatal(err)
		}
	}
}

// Block makes a bounded FIFO handshake; no timing sleeps choose the race.
func (f *cutoverFixture) Block(next string) {
	f.Barrier()
	f.Choose("block\n" + next)
}

func (f *cutoverFixture) Barrier() {
	t := f.h.t
	t.Helper()
	for path, dst := range map[string]**os.File{f.Ready: &f.ready, f.Go: &f.goFile} {
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
		if err != nil {
			t.Fatal(err)
		}
		*dst = file
		t.Cleanup(func() { _ = file.Close() })
	}
}

func (f *cutoverFixture) Await() {
	f.h.t.Helper()
	if err := f.ready.SetReadDeadline(time.Now().Add(moveWait)); err != nil {
		f.h.t.Fatal(err)
	}
	if line, err := bufio.NewReader(f.ready).ReadString('\n'); err != nil || line != "ready\n" {
		f.h.t.Fatalf("attempt barrier: %q, %v", line, err)
	}
}

func (f *cutoverFixture) Continue() {
	f.h.t.Helper()
	if _, err := f.goFile.WriteString("go\n"); err != nil {
		f.h.t.Fatal(err)
	}
}

type cutoverEnvelope struct {
	ErrName string `json:"err_name"`
	Details struct {
		Refusal      map[string]any `json:"refusal"`
		Cutover      string         `json:"cutover"`
		Steps        *bool          `json:"steps_committed"`
		AttemptStore string         `json:"attempt_store"`
		Copies       []struct {
			Copy    string `json:"copy"`
			Belongs string `json:"belongs_at"`
		} `json:"copies"`
		Remove []string `json:"remove"`
	} `json:"details"`
}

// Check reuses preflightEnvelopes for exit/cause equality and checks the
// byte-preserved relay, second-envelope rule, and retained paths.
func (f *cutoverFixture) Check(res moveResult, cause, cutover string, kept bool) cutoverEnvelope {
	t := f.h.t
	t.Helper()
	lines := preflightEnvelopes(t, res, cause)
	raw, err := os.ReadFile(f.Output)
	if err != nil {
		t.Fatal(err)
	}
	var relayed *cutoverEnvelope
	n := 0
	status, _ := os.ReadFile(f.Finished)
	if string(status) == "0\n" {
		// Successful migrate-journal output belongs on stdout. Only its
		// interrupted installer outcome is an error envelope on stderr.
		var success struct {
			Authority string `json:"authority"`
		}
		if err := json.Unmarshal(raw, &success); err != nil || success.Authority != "sqlite" {
			t.Fatalf("successful attempt: %q (%v)", raw, err)
		}
		raw = nil
	}
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if len(raw) == 0 {
			break
		}
		var env cutoverEnvelope
		want := "  " + line
		if json.Unmarshal([]byte(line), &env) == nil && env.ErrName != "" {
			relayed = &env
			want = line
			if n >= len(lines) || lines[n] != line {
				t.Errorf("relay differs: got %q, want %q", lines, line)
			}
			n++
		}
		if !strings.Contains(res.Stderr, want+"\n") {
			t.Errorf("missing relayed line %q:\n%s", want, res.Stderr)
		}
	}
	if relayed == nil || relayed.ErrName != cause || kept {
		n++
	}
	if len(lines) != n {
		t.Errorf("got %d envelopes; want %d: %v", len(lines), n, lines)
	}
	var last cutoverEnvelope
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatal(err)
	}
	if last.Details.Cutover != cutover {
		t.Errorf("cutover %q; want %q", last.Details.Cutover, cutover)
	}
	if relayed != nil && relayed.Details.Refusal != nil && !reflect.DeepEqual(last.Details.Refusal, relayed.Details.Refusal) {
		t.Errorf("refusal changed: got %#v; relayed %#v", last.Details.Refusal, relayed.Details.Refusal)
	}
	if cutover == "not_committed" && strings.Contains(res.Stderr, "A commit may have completed") {
		t.Error("not_committed reports a possible commit")
	}
	for _, copy := range last.Details.Copies {
		if _, err := os.Lstat(copy.Copy); err != nil {
			t.Errorf("kept copy: %v", err)
		}
		outcomeLines(t, res.Stderr, "  kept copy: "+copy.Copy+" -> "+copy.Belongs)
	}
	for _, p := range last.Details.Remove {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("path still to remove: %v", err)
		}
		outcomeLines(t, res.Stderr, "  still to remove: "+p)
	}
	if !kept && len(restoreCopyNames(t, f.h)) != 0 {
		t.Error("restore copies outlived the run")
	}
	return last
}

func outcomeLines(t *testing.T, text string, lines ...string) {
	t.Helper()
	for _, line := range lines {
		if !strings.Contains(text, line+"\n") {
			t.Errorf("missing line %q:\n%s", line, text)
		}
	}
}

// Follow runs the actual printed command, replacing only its deliberately
// unexecutable backup placeholder. Piped advice repeats the original pipe.
func (f *cutoverFixture) Follow(res moveResult, piped bool, backup string) moveResult {
	t, h := f.h.t, f.h
	t.Helper()
	outcomeLines(t, res.Stderr, "  The new backup directory must be absolute, must not exist yet, and must lie outside every legacy input root and AD's old and new directories.")
	if piped {
		outcomeLines(t, res.Stderr, "  run the same install command again, with --journal-backup naming a new directory")
		return schedRunArgv(t, h, h.S.Install, true, setFlag(schedArgv(h), "--journal-backup", backup))
	}
	return f.runAdvice(res, "<new-backup-directory>", backup)
}

func (f *cutoverFixture) runAdvice(res moveResult, placeholder, path string) moveResult {
	t, h := f.h.t, f.h
	t.Helper()
	var command string
	for _, line := range strings.Split(res.Stderr, "\n") {
		if strings.HasPrefix(line, "    ") && strings.Contains(line, placeholder) {
			if command != "" {
				t.Fatalf("multiple commands for %s", placeholder)
			}
			command = strings.TrimPrefix(line, "    ")
		}
	}
	if command == "" {
		t.Fatalf("no command for %s:\n%s", placeholder, res.Stderr)
	}
	cmd := h.command()
	cmd.Args = []string{h.S.Bash, "-c", strings.ReplaceAll(command, placeholder, moveQuote(path))}
	// The command itself establishes cwd, including after that cwd moved.
	cmd.Dir = h.Root
	return schedExec(t, cmd)
}

func (f *cutoverFixture) Install(piped, closed bool) moveResult {
	h := f.h
	if !closed {
		return schedRunArgv(h.t, h, h.S.Install, piped, schedArgv(h))
	}
	cmd := h.command()
	cmd.Args = append([]string{h.S.Bash, "-c", `exec "$@" 2>&-`, "closed-stderr"}, cmd.Args...)
	return schedExec(h.t, cmd)
}

// Start shares moveRunning's process-group signal/reap guard. The writer
// delivers the complete script then holds stdin open until signalled; the
// optional reader is a real cat, so INT closes stdout's only reader.
func (f *cutoverFixture) Start(bash string, piped, reader, closed bool) *moveRunning {
	h, t := f.h, f.h.t
	t.Helper()
	r := &moveRunning{cmd: h.command()}
	if bash == "" {
		bash = h.S.Bash
	}
	r.cmd.Path, r.cmd.Args[0] = bash, bash
	if piped {
		r.cmd.Args = append([]string{bash, "-s", "--"}, schedArgv(h)...)
	}
	if closed {
		r.cmd.Args = append([]string{bash, "-c", `exec "$@" 2>&-`, "closed-stderr"}, r.cmd.Args...)
	}
	r.cmd.Stdout, r.cmd.Stderr = &r.stdout, &r.stderr
	r.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var children []*exec.Cmd
	var ends []*os.File
	pipe := func() (*os.File, *os.File) {
		rd, wr, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		ends = append(ends, rd, wr)
		return rd, wr
	}
	var inputRead, inputWrite, holdRead, holdWrite, outputRead, outputWrite *os.File
	if piped {
		inputRead, inputWrite = pipe()
		holdRead, holdWrite = pipe()
		r.cmd.Stdin = inputRead
	}
	if reader {
		outputRead, outputWrite = pipe()
		r.cmd.Stdout = outputWrite
	}
	// Register before starting any process, including failure during setup.
	h.OnRelease(func() {
		if r.cmd.Process != nil {
			r.Signal(syscall.SIGKILL)
		}
		for _, e := range ends {
			_ = e.Close()
		}
		for _, child := range children {
			if child.ProcessState == nil {
				_ = child.Process.Kill()
				_ = child.Wait()
			}
		}
		if r.cmd.Process != nil {
			r.Wait(t)
		}
	})
	if err := r.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	start := func(cmd *exec.Cmd) {
		cmd.Env, cmd.Dir = r.cmd.Env, r.cmd.Dir
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: r.cmd.Process.Pid}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		children = append(children, cmd)
	}
	if piped {
		writer := exec.Command(h.S.Bash, "-c", `cat "$1"; exec cat`, "source-writer", h.S.Install)
		writer.Stdin, writer.Stdout = holdRead, inputWrite
		start(writer)
		_ = inputRead.Close()
		_ = inputWrite.Close()
		_ = holdRead.Close()
		_ = holdWrite // cleanup owns the pipe's remaining end
	}
	if reader {
		f.reader = exec.Command(h.RealTool("cat"))
		f.reader.Stdin, f.reader.Stdout = outputRead, &r.stdout
		start(f.reader)
		_ = outputRead.Close()
		_ = outputWrite.Close()
	}
	return r
}

// ReaderEnded is called before releasing the attempt, proving later writes
// have no stdout reader rather than relying on signal-delivery timing.
func (f *cutoverFixture) ReaderEnded() {
	t := f.h.t
	t.Helper()
	err := f.reader.Wait()
	if err == nil || f.reader.ProcessState == nil {
		t.Fatalf("reader survived INT: %v", err)
	}
	ws, ok := f.reader.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGINT {
		t.Fatalf("reader status: %v", f.reader.ProcessState)
	}
}
