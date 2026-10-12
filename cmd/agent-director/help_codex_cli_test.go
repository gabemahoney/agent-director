package main_test

// help_codex_cli_test.go covers the built binary's `help` run by a Codex agent's
// SessionStart hook (SR-4.5): with AGENT_DIRECTOR_AGENT=codex and a SessionStart
// payload on stdin, help prints Codex's structured SessionStart output; every
// other run prints the plain form, byte for byte. The payloads are Epic 1's
// Codex captures under internal/hook/testdata.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codexAgentEnv is a Codex launch's agent signal.
var codexAgentEnv = map[string]string{"AGENT_DIRECTOR_AGENT": "codex"}

// codexPayload returns a Codex hook payload fixture.
func codexPayload(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "hook", "testdata", "codex-hook-payloads", name))
	if err != nil {
		t.Fatalf("read Codex payload %s: %v", name, err)
	}
	return string(raw)
}

// runHelp runs the binary under a fresh home with env and o's stdin, returning
// stdout, stderr and the exit code, and failing the test unless the home is
// still empty after the run.
func runHelp(t *testing.T, env map[string]string, o cliOpts, args ...string) (string, string, int) {
	t.Helper()
	home := t.TempDir()
	stdout, stderr, code := runNoVerb(t, home, env, o, args...)
	assertHomeTree(t, home)
	return stdout, stderr, code
}

// TestHelpCodexSessionStartStructuredForm: with the Codex signal and a Codex
// SessionStart payload (any source) on stdin, help prints exactly one JSON
// object, {"hookSpecificOutput":{"hookEventName":"SessionStart",
// "additionalContext":<the plain output>}}, within Codex's context limit, with
// exit 0, no stderr and nothing created under HOME.
func TestHelpCodexSessionStartStructuredForm(t *testing.T) {
	plain := strings.TrimSuffix(helpVerbStdout(t), "\n")
	cases := []struct {
		name, payload string
		args          []string
	}{
		{"startup", "session-start-startup.json", []string{"help"}},
		{"compact", "session-start-compact.json", []string{"help"}},
		{"help alias", "session-start-startup.json", []string{"--help"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runHelp(t, codexAgentEnv, cliOpts{stdin: codexPayload(t, tc.payload)}, tc.args...)
			if code != 0 || stderr != "" {
				t.Fatalf("exit=%d stderr=%q; want exit 0 and nothing on stderr", code, stderr)
			}
			if !strings.HasSuffix(stdout, "\n") || strings.Count(stdout, "\n") != 1 {
				t.Errorf("stdout is %d lines; want one line ending in a newline", strings.Count(stdout, "\n"))
			}
			var top map[string]json.RawMessage
			dec := json.NewDecoder(strings.NewReader(stdout))
			if err := dec.Decode(&top); err != nil {
				t.Fatalf("stdout is not a JSON object: %v\n%.300q", err, stdout)
			}
			if dec.More() {
				t.Errorf("stdout holds more than one JSON value:\n%.300q", stdout)
			}
			if len(top) != 1 || top["hookSpecificOutput"] == nil {
				t.Fatalf("top-level keys = %v; want only hookSpecificOutput", keysOf(top))
			}
			var inner map[string]string
			if err := json.Unmarshal(top["hookSpecificOutput"], &inner); err != nil || len(inner) != 2 {
				t.Fatalf("hookSpecificOutput = %s (err %v); want an object of two string fields", top["hookSpecificOutput"], err)
			}
			if inner["hookEventName"] != "SessionStart" {
				t.Errorf("hookEventName = %q; want SessionStart", inner["hookEventName"])
			}
			ctx, ok := inner["additionalContext"]
			if !ok {
				t.Fatalf("hookSpecificOutput has no additionalContext: %v", inner)
			}
			if ctx != plain {
				t.Errorf("additionalContext differs from the plain help output (%d bytes, want %d)", len(ctx), len(plain))
			}
			if tokens := (len(ctx) + 3) / 4; tokens > 4000 {
				t.Errorf("additionalContext is %d bytes, ceil(bytes/4) = %d; want at most 4000", len(ctx), tokens)
			}
		})
	}
}

// keysOf lists the keys of m.
func keysOf(m map[string]json.RawMessage) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestHelpCodexNegativeCasesPrintPlain: without the Codex signal, or with it but
// no SessionStart payload on stdin (a terminal, nothing, input held open past
// the deadline, not JSON, another event's payload), help prints the plain form
// byte for byte, exit 0, no stderr, nothing created under HOME, and returns
// within the run deadline.
func TestHelpCodexNegativeCasesPrintPlain(t *testing.T) {
	plain := helpVerbStdout(t)
	startup := codexPayload(t, "session-start-startup.json")
	cases := []struct {
		name     string
		env      map[string]string
		stdin    string
		holdOpen bool
		devNull  bool
	}{
		{name: "no variable, SessionStart payload", stdin: startup},
		{name: "variable empty, SessionStart payload", env: map[string]string{"AGENT_DIRECTOR_AGENT": ""}, stdin: startup},
		{name: "variable claude, SessionStart payload", env: map[string]string{"AGENT_DIRECTOR_AGENT": "claude"}, stdin: startup},
		{name: "end of input", env: codexAgentEnv, devNull: true},
		{name: "empty input", env: codexAgentEnv, stdin: ""},
		{name: "open pipe that never sends data", env: codexAgentEnv, holdOpen: true},
		{name: "payload on a pipe still open at the deadline", env: codexAgentEnv, stdin: startup, holdOpen: true},
		{name: "not JSON", env: codexAgentEnv, stdin: "hello there\n"},
		{name: "truncated JSON", env: codexAgentEnv, stdin: `{"hook_event_name":"SessionStart"`},
		{name: "JSON without an event", env: codexAgentEnv, stdin: `{"source":"startup"}`},
		{name: "Stop payload", env: codexAgentEnv, stdin: codexPayload(t, "stop.json")},
		{name: "UserPromptSubmit payload", env: codexAgentEnv, stdin: codexPayload(t, "user-prompt-submit.json")},
		{name: "SessionEnd payload", env: codexAgentEnv, stdin: codexPayload(t, "session-end-other.json")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := cliOpts{stdin: tc.stdin, holdOpen: tc.holdOpen}
			if tc.devNull {
				devNull, err := os.Open(os.DevNull)
				if err != nil {
					t.Fatalf("open %s: %v", os.DevNull, err)
				}
				defer devNull.Close()
				o.stdinFile = devNull
			}
			stdout, stderr, code := runHelp(t, tc.env, o, "help")
			if code != 0 || stderr != "" {
				t.Errorf("exit=%d stderr=%q; want exit 0 and nothing on stderr", code, stderr)
			}
			if stdout != plain {
				t.Errorf("stdout differs from the plain help (%d bytes, want %d):\n%.300q", len(stdout), len(plain), stdout)
			}
		})
	}
}

// TestHelpCodexTerminalStdinPrintsPlain: with the Codex signal, a real pty
// holding a whole SessionStart payload and EOF still gives the plain form: a
// terminal is never read (Linux; skipped elsewhere).
func TestHelpCodexTerminalStdinPrintsPlain(t *testing.T) {
	plain := helpVerbStdout(t)
	master, slave := openPTY(t)
	// Canonical mode: the line, then ^D, reads as the payload and then EOF.
	payload := strings.TrimSpace(codexPayload(t, "session-start-startup.json")) + "\n\x04"
	if _, err := master.Write([]byte(payload)); err != nil {
		t.Fatalf("write payload to pty: %v", err)
	}
	stdout, stderr, code := runHelp(t, codexAgentEnv, cliOpts{stdinFile: slave}, "help")
	if code != 0 || stderr != "" {
		t.Errorf("exit=%d stderr=%q; want exit 0 and nothing on stderr", code, stderr)
	}
	if stdout != plain {
		t.Errorf("stdout differs from the plain help (%d bytes, want %d):\n%.300q", len(stdout), len(plain), stdout)
	}
}
