// Package emitdiagnosticcontrolchars_test is a synthetic-regression test for
// gates/lib/emit-diagnostic.sh (b.v46). The old emitter escaped only `\`, `"`
// and newline, so a TAB or other C0 control character from raw command output
// (`go test` FAIL lines, compiler errors) made the SR-14 line invalid JSON and
// run-parallel.sh's `grep '^{' | jq -sc .` dropped every diagnostic of the gate.
// gate_diagnostics_test.go covers the gates that printf'd their own line.
package emitdiagnosticcontrolchars_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// c0AndDel is every C0 control character but NUL (no argv can carry one), then DEL.
var c0AndDel = func() string {
	var b strings.Builder
	for c := byte(1); c < 0x20; c++ {
		b.WriteByte(c)
	}
	b.WriteByte(0x7f)
	return b.String()
}()

// repoRoot walks up from the package working directory until it finds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("repoRoot: could not find go.mod walking up from %s", dir)
		}
		dir = parent
	}
}

// emitDecoded calls emit_diagnostic with args as its four arguments.
func emitDecoded(t *testing.T, args ...string) map[string]any {
	t.Helper()
	return runEmit(t, `emit_diagnostic "$@"`, "", args...)
}

// runEmit sources the real emit-diagnostic.sh, runs call under bash with args
// and stdin, requires nothing on stdout and one JSON line on stderr, and decodes it.
func runEmit(t *testing.T, call, stdin string, args ...string) map[string]any {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH — emit_diagnostic needs it")
	}
	lib := filepath.Join(repoRoot(t), "skills", "release-agent-director", "gates", "lib", "emit-diagnostic.sh")
	cmd := exec.Command("bash", append([]string{"-c", `source "$0" && ` + call, lib}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("emit_diagnostic: %v\nstderr: %.2000q", err, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("emit_diagnostic wrote to stdout: %q", stdout.String())
	}
	return decodeOneLine(t, stderr.String())
}

// decodeOneLine requires stderr to be exactly one line starting with '{' (all
// run-parallel.sh keeps) and decodes it as strict JSON.
func decodeOneLine(t *testing.T, stderr string) map[string]any {
	t.Helper()
	line, rest, _ := strings.Cut(stderr, "\n")
	if rest != "" || !strings.HasPrefix(line, "{") {
		t.Fatalf("want exactly one stderr line starting with '{', got %.2000q", stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("diagnostic is not valid JSON: %v\nline: %.2000q", err, line)
	}
	return got
}

// TestEmitDiagnosticControlCharsRoundTrip (b.v46): each field decodes back to
// exactly the argument it was given, whatever control characters it carries.
func TestEmitDiagnosticControlCharsRoundTrip(t *testing.T) {
	cases := []struct{ name, text string }{
		{"tab", "a\tb"},
		{"go_test_fail_line", "FAIL\texample.test/pkg\t0.012s"},
		{"go_compiler_error", "not enough arguments in call to f\n\thave (number)\n\twant (int, int)"},
		{"carriage_return", "line one\r\nline two\r"},
		{"every_c0_but_nul_and_del", c0AndDel},
		{"quote_and_backslash", `say "hi" \n C:\dir\`},
		{"trailing_newline", "ends in a newline\n"},
		{"non_ascii_utf8", "größe → ok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			want := map[string]any{
				"gate":                       "gate:" + tc.text,
				"offending_file_or_artifact": "file:" + tc.text,
				"description":                "desc:" + tc.text,
				"corrective_action":          "fix:" + tc.text,
			}
			got := emitDecoded(t, "gate:"+tc.text, "file:"+tc.text, "desc:"+tc.text, "fix:"+tc.text)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("decoded diagnostic\n got %q\nwant %q", got, want)
			}
		})
	}
}

// TestEmitDiagnosticDescriptionExact: the description reaches jq on stdin, so an
// empty one and trailing newlines must come back byte for byte.
func TestEmitDiagnosticDescriptionExact(t *testing.T) {
	for _, desc := range []string{"", "\n\n", "x\n\n"} {
		t.Run(strconv.Quote(desc), func(t *testing.T) {
			t.Parallel()
			if got := emitDecoded(t, "g", "null", desc, "c")["description"]; got != desc {
				t.Errorf("description = %q, want %q", got, desc)
			}
		})
	}
}

// TestEmitDiagnosticLargeDescription (b.v46): a description over Linux's 128 KiB
// single-argument limit, as a long `go test` tail is, still yields one exact
// diagnostic; passed to jq as an argument, its exec fails and nothing is written.
func TestEmitDiagnosticLargeDescription(t *testing.T) {
	t.Parallel()
	line := "\tx_test.go:12: got \"a\\b\"\r\n"
	big := strings.Repeat(line, (200<<10)/len(line)) + "FAIL\texample.test/pkg\t0.012s"
	// "$(cat)" builds the argument inside bash: an exec argument would hit the
	// limit before emit_diagnostic runs. Its trailing-newline strip is moot here.
	got := runEmit(t, `emit_diagnostic "$1" null "$(cat)" "$2"`, big, "g", "fix")
	if got["description"] != big {
		d, _ := got["description"].(string)
		t.Errorf("description did not round-trip: got %d bytes, want %d", len(d), len(big))
	}
}

// TestEmitDiagnosticOffendingNull: "null" or an empty offending argument
// becomes JSON null, not a string.
func TestEmitDiagnosticOffendingNull(t *testing.T) {
	for _, offending := range []string{"null", ""} {
		t.Run("offending="+offending, func(t *testing.T) {
			t.Parallel()
			got := emitDecoded(t, "g", offending, "d", "c")
			v, ok := got["offending_file_or_artifact"]
			if !ok || v != nil {
				t.Errorf("offending_file_or_artifact = %#v (present %v), want JSON null", v, ok)
			}
		})
	}
}
