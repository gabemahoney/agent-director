package installsh_test

// The cutover-outcome fixture (reusable test fixture) routes only the
// candidate's migrate-journal attempt to the existing administrator child
// runner, a FIFO barrier or a canned response. Installer tests needing a
// chosen outcome use this fixture, never a second stand-in or child runner.

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
	router := filepath.Join(h.Root, "cutover-router")
	// The control's first line is real, child:<seams>, canned, or block.
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
case "$mode" in
    real) `+moveQuote(h.S.Current.Admin)+` "$@" ;;
    child:*) AD_ADMIN_CHILD="${mode#child:}" `+moveQuote(outcomeChild.path)+` -test.run '^TestMigrateJournalAdminChild$' -- "$@" ;;
    canned) bash `+moveQuote(f.Control+".response")+` ;;
    *) echo "unknown outcome mode: $mode" >&2; exit 98 ;;
esac >`+moveQuote(f.Output)+` 2>&1
rc=$?
: >`+moveQuote(f.Finished)+`
if [[ -f `+moveQuote(f.After)+` ]]; then bash `+moveQuote(f.After)+`; fi
cat `+moveQuote(f.Output)+`
exit "$rc"
`, 0o755)
	h.Pair = movePair{h.S.Current.Main, h.S.ScheduleProbe.Main}
	h.Env["SCHED_TEST_DELEGATE"] = router
	h.Env["SCHED_TEST_DELEGATE_EXEC"] = "1"
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
	f.Choose("block\n" + next)
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
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
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
