package main_test

// no_exec_form_cli_test.go covers a no-verb run of the built binary (SR-22.9,
// "A Claude Code that does not run exec-form hooks"; SR-14; AC-HOOK-06): a hook
// payload on stdin prints nothing, exits 0 and writes one ad.hook.ignored
// no_exec_form with no store or config; any other stdin prints `help`. Every
// run is bounded, because a stdin read with no deadline hangs on an open pipe.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
)

// noExecFormTranscript is a payload transcript_path; hook_session_id is its
// basename without the extension.
const noExecFormTranscript = "/home/agent/.claude/projects/-work/0a1b2c3d-noexec-uuid.jsonl"

// runNoVerb runs the binary under home with env and stdin, failing the test
// when it outlives surfaceDeadline (the binary's own stdin deadline is 1 s).
func runNoVerb(t *testing.T, home string, env map[string]string, o cliOpts, args ...string) (string, string, int) {
	t.Helper()
	o.env, o.deadline = homeEnv(home, env), surfaceDeadline
	return mustRun(t, o, args...)
}

// helpVerbStdout returns the `help` verb's stdout from a fresh home.
func helpVerbStdout(t *testing.T) string {
	t.Helper()
	stdout, stderr, code := runCLIWithHome(t, t.TempDir(), "help")
	if code != 0 || stdout == "" {
		t.Fatalf("help: exit=%d stdout=%q stderr=%q; want exit 0 and output", code, stdout, stderr)
	}
	return stdout
}

// oversizedHookPayload is a valid SessionStart payload padded to exactly n
// bytes, so only the size cap can reject it.
func oversizedHookPayload(n int) string {
	const head, tail = `{"hook_event_name":"SessionStart","pad":"`, `"}`
	return head + strings.Repeat("x", n-len(head)-len(tail)) + tail
}

// TestNoExecFormCLIHookPayloadIgnored: a hook payload gives no output, exit 0 and one full SR-14
// no_exec_form record, no ad.hook.fired, and a home holding only ad-trail.jsonl (no store or config).
func TestNoExecFormCLIHookPayloadIgnored(t *testing.T) {
	pid := os.Getpid()
	wantCommand, ok := probe.NewCommandNameReader().CommandName(pid)
	if !ok || wantCommand == "" {
		t.Fatalf("CommandName(test pid %d) = (%q, %v); want the test process's name", pid, wantCommand, ok)
	}
	const instanceID = "id-noexec-1"
	sessionStart := `{"hook_event_name":"SessionStart","session_id":"0a1b2c3d-noexec-uuid",` +
		`"transcript_path":"` + noExecFormTranscript + `","source":"startup"}` + "\n"
	trail := filepath.Join(".agent-director", "ad-trail.jsonl")
	config := filepath.Join(".agent-director", "config.toml")

	cases := []struct {
		name          string
		stdin         string
		instanceID    string // AGENT_DIRECTOR_INSTANCE_ID; "" leaves it unset
		globalFlags   bool   // run as `--home <recordHome>` with HOME another dir
		badConfig     bool   // plant a malformed config.toml first
		wantEvent     string
		wantInstance  any
		wantHookSessn any
	}{
		{name: "SessionStart with instance id and transcript", stdin: sessionStart, instanceID: instanceID,
			wantEvent: "SessionStart", wantInstance: instanceID, wantHookSessn: "0a1b2c3d-noexec-uuid"},
		{name: "Stop without instance id or transcript", stdin: `{"hook_event_name":"Stop"}`,
			wantEvent: "Stop", wantInstance: nil, wantHookSessn: nil},
		{name: "invalid instance id records null", stdin: `{"hook_event_name":"PreToolUse"}`, instanceID: "bad/id",
			wantEvent: "PreToolUse", wantInstance: nil, wantHookSessn: nil},
		{name: "--home only global flag writes under that home", stdin: sessionStart, instanceID: instanceID,
			globalFlags: true, wantEvent: "SessionStart", wantInstance: instanceID, wantHookSessn: "0a1b2c3d-noexec-uuid"},
		{name: "exactly the 1 MiB cap", stdin: oversizedHookPayload(int(hook.MaxPayloadBytes)),
			wantEvent: "SessionStart", wantInstance: nil, wantHookSessn: nil},
		{name: "malformed config is not loaded", stdin: sessionStart, instanceID: instanceID, badConfig: true,
			wantEvent: "SessionStart", wantInstance: instanceID, wantHookSessn: "0a1b2c3d-noexec-uuid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			envHome, recordHome := t.TempDir(), t.TempDir()
			var args []string
			if tc.globalFlags {
				args = []string{"--home", recordHome}
			} else {
				envHome = recordHome
			}
			wantTree := []string{".agent-director", trail}
			if tc.badConfig {
				if err := os.MkdirAll(directorDir(recordHome), 0o700); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				if err := os.WriteFile(filepath.Join(recordHome, config), []byte("this is = not [valid toml\n"), 0o600); err != nil {
					t.Fatalf("write malformed config: %v", err)
				}
				wantTree = append(wantTree, config)
			}
			env := map[string]string{}
			if tc.instanceID != "" {
				env["AGENT_DIRECTOR_INSTANCE_ID"] = tc.instanceID
			}

			stdout, stderr, code := runNoVerb(t, envHome, env, cliOpts{stdin: tc.stdin}, args...)
			if code != 0 || stdout != "" || stderr != "" {
				t.Errorf("exit=%d stdout=%.300q stderr=%q; want exit 0 and nothing on stdout or stderr", code, stdout, stderr)
			}

			lines := readTrailLines(t, recordHome)
			if len(lines) != 1 || lines[0]["event"] != "ad.hook.ignored" {
				t.Fatalf("trail = %v; want exactly one ad.hook.ignored (no ad.hook.fired)", lines)
			}
			want := map[string]any{
				"claude_instance_id": tc.wantInstance,
				"hook_event":         tc.wantEvent,
				"reason":             store.HookReasonNoExecForm,
				"parent_pid":         float64(pid),
				"parent_command":     wantCommand,
				"hook_session_id":    tc.wantHookSessn,
				"row_session_id":     nil,
				"row_pane_pid":       nil,
				"launcher_pid":       nil,
				"source":             "ad_hook",
			}
			for k, v := range want {
				if got, present := lines[0][k]; !present || got != v {
					t.Errorf("ad.hook.ignored %s = %v (present=%v); want %v", k, got, present, v)
				}
			}
			assertHomeTree(t, recordHome, wantTree...)
			if tc.globalFlags {
				assertHomeTree(t, envHome)
			}
		})
	}
}

// TestNoExecFormCLINonHookStdinPrintsHelp: stdin that is not a hook payload (or is over 1 MiB, or
// still open at the deadline) gives output byte-identical to `help`, no stderr and nothing written.
func TestNoExecFormCLINonHookStdinPrintsHelp(t *testing.T) {
	help := helpVerbStdout(t)
	hookPayload := `{"hook_event_name":"SessionStart","transcript_path":"` + noExecFormTranscript + `"}` + "\n"

	cases := []struct {
		name     string
		devNull  bool // stdin is /dev/null (end of input at once)
		stdin    string
		holdOpen bool // the pipe stays open after stdin is written
		args     []string
	}{
		{name: "end of input", devNull: true},
		{name: "empty input", stdin: ""},
		{name: "object without hook_event_name", stdin: `{"session_id":"s","transcript_path":"` + noExecFormTranscript + `"}`},
		{name: "empty hook_event_name", stdin: `{"hook_event_name":""}`},
		{name: "null hook_event_name", stdin: `{"hook_event_name":null}`},
		{name: "non-string hook_event_name", stdin: `{"hook_event_name":42}`},
		{name: "event_name only", stdin: `{"event_name":"SessionStart","transcript_path":"` + noExecFormTranscript + `"}`},
		{name: "JSON array", stdin: `[` + hookPayload + `]`},
		{name: "JSON null", stdin: `null`},
		{name: "non-JSON text", stdin: "hello there\n"},
		{name: "truncated JSON", stdin: `{"hook_event_name":"SessionStart"`},
		{name: "over 1 MiB", stdin: oversizedHookPayload(int(hook.MaxPayloadBytes) + 1)},
		{name: "open pipe that never sends data", holdOpen: true},
		{name: "payload on a pipe still open at the deadline", stdin: hookPayload, holdOpen: true},
		{name: "help verb with a payload", stdin: hookPayload, args: []string{"help"}},
		{name: "--help with a payload", stdin: hookPayload, args: []string{"--help"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			env := map[string]string{"AGENT_DIRECTOR_INSTANCE_ID": "id-noexec-help"}
			o := cliOpts{stdin: tc.stdin, holdOpen: tc.holdOpen}
			if tc.devNull {
				devNull, err := os.Open(os.DevNull)
				if err != nil {
					t.Fatalf("open %s: %v", os.DevNull, err)
				}
				defer devNull.Close()
				o.stdinFile = devNull
			}
			stdout, stderr, code := runNoVerb(t, home, env, o, tc.args...)
			if code != 0 || stderr != "" {
				t.Errorf("exit=%d stderr=%q; want exit 0 and nothing on stderr", code, stderr)
			}
			if stdout != help {
				t.Errorf("stdout differs from `help` (%d bytes, want %d):\n%.300q", len(stdout), len(help), stdout)
			}
			assertHomeTree(t, home)
		})
	}
}

// TestNoExecFormCLITerminalStdinPrintsHelp: a real pty holding a whole payload and EOF still gives
// help and writes nothing, so a terminal is never read (Linux; skipped elsewhere).
func TestNoExecFormCLITerminalStdinPrintsHelp(t *testing.T) {
	help := helpVerbStdout(t)
	master, slave := openPTY(t)
	// Canonical mode: the line, then ^D, reads as the payload and then EOF.
	payload := `{"hook_event_name":"SessionStart","transcript_path":"` + noExecFormTranscript + `"}` + "\n\x04"
	if _, err := master.Write([]byte(payload)); err != nil {
		t.Fatalf("write payload to pty: %v", err)
	}

	home := t.TempDir()
	env := map[string]string{"AGENT_DIRECTOR_INSTANCE_ID": "id-noexec-pty"}
	stdout, stderr, code := runNoVerb(t, home, env, cliOpts{stdinFile: slave})
	if code != 0 || stderr != "" {
		t.Errorf("exit=%d stderr=%q; want exit 0 and nothing on stderr", code, stderr)
	}
	if stdout != help {
		t.Errorf("stdout differs from `help` (%d bytes, want %d):\n%.300q", len(stdout), len(help), stdout)
	}
	assertHomeTree(t, home)
}
