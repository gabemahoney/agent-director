package main_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// helpStdout is the JSON shape the help verb writes to stdout. The struct
// is local to this file because tests focus on the contract surface, not
// internal types.
type helpStdout struct {
	Verbs []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"verbs"`
}

func TestHelpStdoutIsValidJSON(t *testing.T) {
	stdout, stderr, code := runCLI(t, "help")
	if code != 0 {
		t.Fatalf("exit=%d want 0; stderr=%q", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr=%q want empty", stderr)
	}
	if len(stdout) == 0 || stdout[0] != '{' {
		t.Errorf("first byte of stdout = %q, want '{' (no prose preamble)", firstByte(stdout))
	}
	var parsed helpStdout
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout)
	}
	if len(parsed.Verbs) < 1 {
		t.Fatalf("verbs array empty; want at least one entry")
	}
}

func TestHelpStdoutListsHelpVerb(t *testing.T) {
	stdout, _, code := runCLI(t, "help")
	if code != 0 {
		t.Fatalf("exit=%d want 0", code)
	}
	var parsed helpStdout
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var found bool
	for _, v := range parsed.Verbs {
		if v.Name == "help" {
			found = true
			if v.Description == "" {
				t.Errorf("help verb has empty description")
			}
		}
	}
	if !found {
		t.Errorf("verbs list missing help entry: got %+v", parsed.Verbs)
	}
}

func TestHelpAndHelpFlagByteIdenticalStdout(t *testing.T) {
	home := t.TempDir()
	stdoutVerb, _, codeVerb := runCLIWithHome(t, home, "help")
	stdoutFlag, _, codeFlag := runCLIWithHome(t, home, "--help")
	if codeVerb != 0 || codeFlag != 0 {
		t.Fatalf("exit: verb=%d flag=%d, want both 0", codeVerb, codeFlag)
	}
	if stdoutVerb != stdoutFlag {
		t.Errorf("stdout not byte-identical:\nhelp=%q\n--help=%q", stdoutVerb, stdoutFlag)
	}
}

func TestHelpStdoutNoProsePreamble(t *testing.T) {
	stdout, _, code := runCLI(t, "help")
	if code != 0 {
		t.Fatalf("exit=%d want 0", code)
	}
	// Single-line JSON: no leading whitespace, no prose, exactly one '{' to
	// open. strings.HasPrefix is enough — we don't enforce a particular
	// trailing newline shape beyond what json/fmt.Fprintln emits.
	if !strings.HasPrefix(stdout, "{") {
		t.Errorf("stdout does not start with '{': %q", firstChunk(stdout))
	}
}

// firstByte returns the first byte of s as a string, or "<empty>" if s is
// empty. Used for diagnostics in assertions that care about the leading byte.
func firstByte(s string) string {
	if s == "" {
		return "<empty>"
	}
	return string(s[0])
}

// firstChunk returns up to the first 40 bytes of s for diagnostic output.
func firstChunk(s string) string {
	if len(s) > 40 {
		return s[:40]
	}
	return s
}
