// Package notesheredoc_test is a synthetic-regression test for the
// heredoc/shell-metacharacter safety of generate-release-notes.ts.
//
// BACKGROUND
// ==========
// generate-release-notes.ts uses ASCII unit-separator (0x1f) and
// record-separator (0x1e) as delimiters for git log output instead of
// newlines, ensuring that commit messages containing backticks, heredoc
// sequences (<<EOF), command substitution ($(...)), or dollar-brace
// variables (${VAR}) survive verbatim through the script.  This test
// creates an ephemeral git repository, plants a commit whose subject
// contains all four shell-special forms, and verifies that each appears
// unchanged in the markdown emitted by generate-release-notes.ts.
//
// DESIGN
// ======
// 1. Skip     : testing.Short() → skip (network/slow guard), bun not on
//               PATH → skip (dependency guard).
// 2. Ephemeral repo: t.TempDir() — Go cleans it up automatically after
//               the test (pass or fail).
// 3. Git setup: git init, user.email, user.name (required for commits).
// 4. Baseline : create README, commit, tag v0.0.1.
// 5. Payload  : create another file, commit with a subject containing
//               `like this`, cat <<EOF, $(date), and ${HOME}.
// 6. Script   : bun run pkg/ts-bun-client/scripts/generate-release-notes.ts
//               --from v0.0.1 --repo-root <ephemeral-repo>
//               Run from the main repo so the script can resolve its own
//               imports; --repo-root redirects git operations to the
//               ephemeral repo.
// 7. Assertions: each shell-special substring appears verbatim in stdout.
//
// WHY SUBJECT, NOT BODY
// =====================
// generate-release-notes.ts renders only c.subject (the first git log
// line) in its bullet output.  The body (%b) is parsed but not emitted.
// Accordingly, the shell-special strings are placed in the commit subject
// where the script will include them in the markdown.
package notesheredoc_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot walks up from the package's working directory until it finds
// a go.mod file, identifying the main repository root.
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
	panic("unreachable")
}

// gitCmd runs a git command in the given directory, failing the test on
// any error.
func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestNotesHeredocSurvivesVerbatim creates an ephemeral git repo with a
// commit whose subject contains backticks, a heredoc sequence, command
// substitution, and a dollar-brace variable, then asserts each appears
// verbatim in the markdown emitted by generate-release-notes.ts.
func TestNotesHeredocSurvivesVerbatim(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs bun and git")
	}

	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not on PATH — skipping notes-heredoc regression test")
	}

	mainRepo := repoRoot(t)

	// ── 1. Create ephemeral git repo ──────────────────────────────────────
	// t.TempDir() is auto-cleaned after the test completes (pass or fail).
	ephemeralRepo := filepath.Join(t.TempDir(), "notes-test")
	if err := os.MkdirAll(ephemeralRepo, 0o755); err != nil {
		t.Fatalf("mkdir ephemeral repo: %v", err)
	}

	// ── 2. git init + identity ────────────────────────────────────────────
	gitCmd(t, ephemeralRepo, "init")
	gitCmd(t, ephemeralRepo, "config", "user.email", "test@example.com")
	gitCmd(t, ephemeralRepo, "config", "user.name", "Test User")

	// ── 3. Baseline commit + tag v0.0.1 ──────────────────────────────────
	readmePath := filepath.Join(ephemeralRepo, "README.md")
	if err := os.WriteFile(readmePath, []byte("# notes-heredoc test repo\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	gitCmd(t, ephemeralRepo, "add", "README.md")
	gitCmd(t, ephemeralRepo, "commit", "-m", "chore: initial commit")
	gitCmd(t, ephemeralRepo, "tag", "v0.0.1")

	// ── 4. Payload commit: subject with shell-special characters ──────────
	//
	// Each form below must survive verbatim through generate-release-notes.ts:
	//   `like this`  — backtick quoting (not command substitution in TS)
	//   cat <<EOF    — heredoc delimiter
	//   $(date)      — command substitution notation
	//   ${HOME}      — dollar-brace variable notation
	//
	// exec.Command does NOT pass args through a shell, so none of these
	// forms will be expanded by the Go test process or by git itself.
	const payloadSubject = "fix: handle `like this` and cat <<EOF and $(date) and ${HOME}"

	payloadFile := filepath.Join(ephemeralRepo, "payload.txt")
	if err := os.WriteFile(payloadFile, []byte("payload\n"), 0o644); err != nil {
		t.Fatalf("write payload.txt: %v", err)
	}
	gitCmd(t, ephemeralRepo, "add", "payload.txt")
	gitCmd(t, ephemeralRepo, "commit", "-m", payloadSubject)

	// ── 5. Run generate-release-notes.ts ─────────────────────────────────
	//
	// The script lives in the main repo; --repo-root redirects its git
	// operations to the ephemeral repo so we get exactly our two commits.
	scriptPath := filepath.Join("pkg", "ts-bun-client", "scripts", "generate-release-notes.ts")
	cmd := exec.Command(
		"bun", "run", scriptPath,
		"--from", "v0.0.1",
		"--repo-root", ephemeralRepo,
	)
	cmd.Dir = mainRepo // script resolves its own imports relative to main repo

	var stdoutBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = os.Stderr // surface bun/ts errors in test output

	if err := cmd.Run(); err != nil {
		t.Fatalf("generate-release-notes.ts exited non-zero: %v\nstdout:\n%s", err, stdoutBuf.String())
	}

	output := stdoutBuf.String()
	t.Logf("generate-release-notes.ts output:\n%s", output)

	// ── 6. Assertions: each shell-special form appears verbatim ──────────
	cases := []struct {
		name    string
		needle  string
	}{
		{"backtick", "`like this`"},
		{"heredoc-delimiter", "cat <<EOF"},
		{"command-substitution", "$(date)"},
		{"dollar-brace-variable", "${HOME}"},
	}

	for _, tc := range cases {
		if !strings.Contains(output, tc.needle) {
			t.Errorf("assert %s: %q not found in output\nfull output:\n%s",
				tc.name, tc.needle, output)
		}
	}
}
