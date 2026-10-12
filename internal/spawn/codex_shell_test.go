package spawn

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/codexfix"
)

// A Codex hook command is run by a shell (`sh -c <command>`); these tests run
// the composed commands in the shells the sandbox image has (SR-5.3, RN-14).

// requiredShells must be present; the others are run when found and skipped
// with a log line when not.
var (
	requiredShells = []string{"sh", "bash"}
	optionalShells = []string{"dash", "zsh", "fish"}
)

// shellsPresent returns the shells to run, failing the test when a required one
// is missing.
func shellsPresent(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, sh := range requiredShells {
		if _, err := exec.LookPath(sh); err != nil {
			t.Fatalf("required shell %s not found: %v", sh, err)
		}
		out = append(out, sh)
	}
	for _, sh := range optionalShells {
		if _, err := exec.LookPath(sh); err != nil {
			t.Logf("shell %s not present; its cases are skipped", sh)
			continue
		}
		out = append(out, sh)
	}
	return out
}

// shellPaths are the program paths a hook command must carry unchanged, as
// directories under the test's temp dir: plain, with a space, a single quote, a
// backslash and all three (SR-5.3).
var shellPaths = map[string]string{
	"plain": "bin", "space": "sp ace", "single quote": "it's", "backslash": `back\slash`, "combined": `it's a\dir`,
}

// writeRecorder writes an executable script at dir/ad that records its $0 and
// arguments, NUL separated, in the file named by $RECORD_FILE.
func writeRecorder(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent-director")
	script := "#!/bin/sh\nprintf '%s\\0' \"$0\" \"$@\" > \"$RECORD_FILE\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestCodexHookCommandsReadBackPath: every composed hook and help command,
// run with `-c` in every shell present, execs the recorder at the path
// unchanged, and the argument tail reaches it intact. The record is what the
// program saw: its path and each argument.
func TestCodexHookCommandsReadBackPath(t *testing.T) {
	shells := shellsPresent(t)
	base := t.TempDir()
	for name, dir := range shellPaths {
		path := writeRecorder(t, filepath.Join(base, dir))
		args := CodexSettingsArgs(codexSettingsInput{Exe: path, HelpBin: path, Relay: 4321, InjectHelp: true, CWD: "/work/proj"})
		hooks := codexfix.Split(t, append([]string{"codex"}, args...)).Hooks()
		if len(hooks) != 9 {
			t.Fatalf("%s: %d handlers composed; want 8 AD entries and the help entry", name, len(hooks))
		}
		for _, sh := range shells {
			t.Run(name+"/"+sh, func(t *testing.T) {
				for _, h := range hooks {
					want := []string{path, "hook", "--agent", "codex"}
					switch {
					case h.Kind == codexfix.HookHelp:
						want = []string{path, "help"}
					case h.Event == "PermissionRequest":
						want = append(want, "--timeout", "4321")
					}
					record := filepath.Join(t.TempDir(), "record")
					cmd := exec.Command(sh, "-c", h.Command)
					cmd.Env = append(os.Environ(), "RECORD_FILE="+record)
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Errorf("%s %s: %s -c %q: %v\n%s", h.Kind, h.Event, sh, h.Command, err, out)
						continue
					}
					raw, err := os.ReadFile(record)
					if err != nil {
						t.Errorf("%s %s: the recorder did not run: %v", h.Kind, h.Event, err)
						continue
					}
					if got := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00"); !slices.Equal(got, want) {
						t.Errorf("%s %s: recorder saw %q; want %q", h.Kind, h.Event, got, want)
					}
				}
			})
		}
	}
}

// denyReason is what the deny command writes to stderr.
const denyReason = "agent-director: the question tool is off for this agent (disable_ask_user); put the question in your reply instead\n"

// denyCommand returns the composed deny group's command.
func denyCommand(t *testing.T) string {
	t.Helper()
	args := CodexSettingsArgs(codexSettingsInput{Exe: "/x/ad", Relay: 86400, QuestionOff: true, CWD: "/work/proj"})
	deny := codexfix.Split(t, append([]string{"codex"}, args...)).HooksOfKind(codexfix.HookDeny)
	if len(deny) != 1 {
		t.Fatalf("deny groups = %d; want 1", len(deny))
	}
	return deny[0].Command
}

// runDeny runs the deny command under sh -c with env, feeding payload on stdin
// from a writer whose error is returned: a command that stopped reading early
// makes it fail with a broken pipe.
func runDeny(t *testing.T, sh string, env []string, payload []byte) (stdout, stderr string, exit int, writeErr error) {
	t.Helper()
	cmd := exec.Command(sh, "-c", denyCommand(t))
	cmd.Env = env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("%s: %v", sh, err)
	}
	_, writeErr = io.Copy(in, bytes.NewReader(payload))
	in.Close()
	runErr := cmd.Wait()
	if ee, ok := runErr.(*exec.ExitError); ok {
		exit = ee.ExitCode()
	} else if runErr != nil {
		t.Fatalf("%s: %v", sh, runErr)
	}
	return out.String(), errb.String(), exit, writeErr
}

// TestCodexDenyCommand: under sh -c, bash -c and (when present) zsh -c, with a
// payload larger than a pipe buffer on stdin, the deny command reads stdin to
// the end, writes only its reason to stderr, writes nothing to stdout and
// exits 2, in the environment AD gives a Codex agent (BASH_ENV empty).
func TestCodexDenyCommand(t *testing.T) {
	payload := []byte(`{"hook_event_name":"PreToolUse","tool_name":"request_user_input_async","pad":"` + strings.Repeat("x", 300_000) + `"}`)
	env := append(os.Environ(), "BASH_ENV="+composeEnv(Resolved{SpawnParams: SpawnParams{Agent: "codex"}})["BASH_ENV"])
	for _, sh := range []string{"sh", "bash", "zsh"} {
		t.Run(sh, func(t *testing.T) {
			if _, err := exec.LookPath(sh); err != nil {
				if sh == "zsh" {
					t.Skipf("zsh not present")
				}
				t.Fatalf("required shell %s not found: %v", sh, err)
			}
			stdout, stderr, exit, werr := runDeny(t, sh, env, payload)
			if werr != nil {
				t.Errorf("writing the payload failed (%v); the command must read stdin to the end", werr)
			}
			if exit != 2 || stdout != "" || stderr != denyReason {
				t.Errorf("exit %d, stdout %q, stderr %q; want exit 2, empty stdout and stderr %q", exit, stdout, stderr, denyReason)
			}
		})
	}
}

// TestCodexDenyCommandIgnoresStartupFile: a startup file that prints to stdout
// would reach the hook's stdout under bash; AD's Codex environment sets
// BASH_ENV empty, so with it the file is never read and stdout stays empty.
// Without it (the control) the file's output is on stdout, so the test sees it.
func TestCodexDenyCommandIgnoresStartupFile(t *testing.T) {
	noisy := filepath.Join(t.TempDir(), "noisy")
	if err := os.WriteFile(noisy, []byte("echo startup-noise\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := composeEnv(Resolved{SpawnParams: SpawnParams{Agent: "codex"}})["BASH_ENV"]; got != "" {
		t.Fatalf("a Codex launch sets BASH_ENV to %q; want empty", got)
	}
	payload := []byte(`{"hook_event_name":"PreToolUse"}`)
	stdout, _, exit, _ := runDeny(t, "bash", append(os.Environ(), "BASH_ENV="), payload)
	if stdout != "" || exit != 2 {
		t.Errorf("with BASH_ENV empty: stdout %q, exit %d; want empty stdout, exit 2", stdout, exit)
	}
	stdout, _, _, _ = runDeny(t, "bash", append(os.Environ(), "BASH_ENV="+noisy), payload)
	if !strings.Contains(stdout, "startup-noise") {
		t.Errorf("control: with BASH_ENV=%s stdout = %q; want the file's output, or this test proves nothing", strconv.Quote(noisy), stdout)
	}
}
