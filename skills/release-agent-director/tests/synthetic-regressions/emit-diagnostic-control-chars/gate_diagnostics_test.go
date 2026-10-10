package emitdiagnosticcontrolchars_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// oddLine is a first stderr line that hand-rolled JSON could not carry: a
// quote, a backslash, a TAB, a C0 control and a CR before the newline.
const oddLine = "E401 \"auth failed\"\tC:\\token\x01 here\r"

// fakeFailingBin writes name into dir as a command that writes "args=[<args>] ",
// then oddLine and a second line, to stderr and exits 1.
func fakeFailingBin(t *testing.T, dir, name string) {
	t.Helper()
	errFile := filepath.Join(dir, name+".stderr")
	if err := os.WriteFile(errFile, []byte(oddLine+"\nsecond\tline\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", errFile, err)
	}
	script := "#!/bin/sh\nprintf 'args=[%s] ' \"$*\" >&2\ncat '" + errFile + "' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
}

// TestConvertedGatesEmitValidJSON (b.v46): gates that hand-rolled their SR-14
// line now emit, through emit_diagnostic, one valid line with these fields.
func TestConvertedGatesEmitValidJSON(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH — emit_diagnostic needs it")
	}
	cases := []struct {
		name     string
		script   string   // under gates/
		args     []string // gate arguments
		fakeBin  string   // command faked on PATH, if any
		env      []string // extra environment
		wantExit int
		wantDiag map[string]any // nil: no stderr at all
	}{
		{
			name: "gh_auth_first_line_only", script: "preflight/gh-auth.sh", fakeBin: "gh", wantExit: 1,
			wantDiag: map[string]any{"gate": "preflight.gh-auth", "description": "args=[auth status] " + oddLine},
		},
		{
			name: "npm_whoami_first_line_only", script: "preflight/npm-whoami.sh", fakeBin: "npm", wantExit: 1,
			wantDiag: map[string]any{"gate": "preflight.npm-whoami", "description": "args=[whoami] " + oddLine},
		},
		{
			name: "npm_whoami_with_registry", script: "preflight/npm-whoami.sh", fakeBin: "npm", wantExit: 1,
			env:      []string{"NPM_REGISTRY=http://registry.invalid/"},
			wantDiag: map[string]any{"gate": "preflight.npm-whoami", "description": "args=[whoami --registry http://registry.invalid/] " + oddLine},
		},
		{
			name: "npm_token_unset", script: "preflight/npm-token-present.sh", wantExit: 1,
			wantDiag: map[string]any{"gate": "preflight.npm-token-present", "description": "NPM_TOKEN environment variable is not set or is empty."},
		},
		{name: "npm_token_set", script: "preflight/npm-token-present.sh", env: []string{"NPM_TOKEN=x"}},
		{
			name: "worktree_target_with_quote_and_tab", script: "branch/create-release-worktree.sh",
			args: []string{"1.2.3\"\tx"}, wantExit: 2,
			wantDiag: map[string]any{"gate": "branch.worktree-create", "description": "target version 1.2.3\"\tx is not strict SemVer"},
		},
		{
			name: "worktree_no_target", script: "branch/create-release-worktree.sh", wantExit: 2,
			wantDiag: map[string]any{"gate": "branch.worktree-create", "description": "target version  is not strict SemVer"},
		},
	}
	gates := filepath.Join(repoRoot(t), "skills", "release-agent-director", "gates")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bin := t.TempDir()
			if tc.fakeBin != "" {
				fakeFailingBin(t, bin, tc.fakeBin)
			}
			env := []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + t.TempDir()}
			for _, e := range os.Environ() {
				if k, _, _ := strings.Cut(e, "="); k != "PATH" && k != "HOME" && k != "NPM_TOKEN" && k != "NPM_REGISTRY" {
					env = append(env, e)
				}
			}
			cmd := exec.Command("bash", append([]string{filepath.Join(gates, tc.script)}, tc.args...)...)
			cmd.Dir = t.TempDir() // nothing may touch the shared worktree
			cmd.Env = append(env, tc.env...)
			var stdout, stderr strings.Builder
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			_ = cmd.Run()
			if got := cmd.ProcessState.ExitCode(); got != tc.wantExit {
				t.Fatalf("exit = %d, want %d\nstderr: %q", got, tc.wantExit, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("gate wrote to stdout: %q", stdout.String())
			}
			if tc.wantDiag == nil {
				if stderr.Len() != 0 {
					t.Errorf("passing gate wrote to stderr: %q", stderr.String())
				}
				return
			}
			got := decodeOneLine(t, stderr.String())
			if c, _ := got["corrective_action"].(string); c == "" {
				t.Errorf("corrective_action = %#v, want a non-empty string", got["corrective_action"])
			}
			delete(got, "corrective_action")
			want := map[string]any{"offending_file_or_artifact": nil}
			for k, v := range tc.wantDiag {
				want[k] = v
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("decoded diagnostic\n got %q\nwant %q", got, want)
			}
		})
	}
}

// TestNoHandRolledDiagnosticJSON (b.v46): no gate script printf's an SR-14 line
// itself; each goes through emit_diagnostic, which escapes with jq.
func TestNoHandRolledDiagnosticJSON(t *testing.T) {
	handRolled := regexp.MustCompile(`printf.*\{\\?"gate\\?"`)
	gates := filepath.Join(repoRoot(t), "skills", "release-agent-director", "gates")
	err := filepath.WalkDir(gates, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".sh") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			if handRolled.MatchString(line) {
				t.Errorf("%s:%d hand-rolls an SR-14 diagnostic; call emit_diagnostic instead:\n%s", path, i+1, line)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", gates, err)
	}
}
