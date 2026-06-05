package main_test

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestTrailPathEnvCases is a table-driven test covering the three
// AGENT_DIRECTOR_STATE_DIR variants that trail-path must honor:
//
//   - env-set:   the printed path uses the env-provided directory.
//   - env-unset: the printed path uses <home>/.agent-director.
//   - env-empty: explicit empty string is treated identically to unset.
//
// Each case also asserts:
//   - Exit code is 0.
//   - Stdout is a single line followed by a newline (no JSON envelope).
//   - The path ends in "ad-trail.jsonl".
func TestTrailPathEnvCases(t *testing.T) {
	cases := []struct {
		name        string
		envOverride map[string]string // nil → do not set AGENT_DIRECTOR_STATE_DIR
		wantPath    func(home string) string
	}{
		{
			name: "env_set",
			envOverride: func() map[string]string {
				// populated per-test below using stateDir
				return nil
			}(),
			// wantPath is populated per-test for this case
		},
		{
			name:        "env_unset",
			envOverride: map[string]string{},
			wantPath: func(home string) string {
				return filepath.Join(home, ".agent-director", "ad-trail.jsonl")
			},
		},
		{
			name:        "env_empty_string",
			envOverride: map[string]string{"AGENT_DIRECTOR_STATE_DIR": ""},
			wantPath: func(home string) string {
				return filepath.Join(home, ".agent-director", "ad-trail.jsonl")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			env := tc.envOverride
			wantPath := tc.wantPath

			if tc.name == "env_set" {
				stateDir := t.TempDir()
				env = map[string]string{"AGENT_DIRECTOR_STATE_DIR": stateDir}
				wantPath = func(_ string) string {
					return filepath.Join(stateDir, "ad-trail.jsonl")
				}
			}

			stdout, stderr, code := runCLIWithEnv(t, home, env, "", "trail-path")
			if code != 0 {
				t.Fatalf("exit = %d; want 0\nstderr=%s", code, stderr)
			}

			// Stdout must be a single line — no JSON wrapper, no extra output.
			got := strings.TrimRight(stdout, "\n")
			if strings.Contains(got, "\n") {
				t.Errorf("stdout contains multiple lines: %q", stdout)
			}

			// Path must end in "ad-trail.jsonl".
			if !strings.HasSuffix(got, "ad-trail.jsonl") {
				t.Errorf("path %q does not end in ad-trail.jsonl", got)
			}

			// Path must match the expected value for this case.
			want := wantPath(home)
			if got != want {
				t.Errorf("path = %q; want %q", got, want)
			}

			// stdout must be terminated with exactly one newline.
			if !strings.HasSuffix(stdout, "\n") {
				t.Errorf("stdout %q is not newline-terminated", stdout)
			}
		})
	}
}

// TestTrailPathNoDBRequired verifies that trail-path succeeds even when
// state.db does not exist. The verb is special-cased in main.go before
// setupClient (SR-A-6, t3.4uk.ou.zv.9y).
func TestTrailPathNoDBRequired(t *testing.T) {
	// home has no .agent-director directory; state.db cannot exist.
	home := t.TempDir()

	stdout, stderr, code := runCLIWithEnv(t, home, map[string]string{}, "", "trail-path")
	if code != 0 {
		t.Fatalf("exit = %d; want 0 (state.db must not be required)\nstderr=%s", code, stderr)
	}

	got := strings.TrimRight(stdout, "\n")
	if !strings.HasSuffix(got, "ad-trail.jsonl") {
		t.Errorf("path %q does not end in ad-trail.jsonl", got)
	}
}

// TestTrailPathStdoutIsRawPath asserts that stdout is not a JSON envelope —
// trail-path intentionally deviates from the manifest-driven JSON pattern so
// shell callers can use the output directly (e.g. xargs dirname).
func TestTrailPathStdoutIsRawPath(t *testing.T) {
	home := t.TempDir()
	stateDir := t.TempDir()

	stdout, stderr, code := runCLIWithEnv(t, home,
		map[string]string{"AGENT_DIRECTOR_STATE_DIR": stateDir},
		"", "trail-path")
	if code != 0 {
		t.Fatalf("exit = %d; want 0\nstderr=%s", code, stderr)
	}

	got := strings.TrimRight(stdout, "\n")

	// Must not start with '{' — not a JSON object.
	if strings.HasPrefix(got, "{") {
		t.Errorf("stdout looks like a JSON envelope: %q", got)
	}

	// Must be a non-empty filesystem path string.
	if got == "" {
		t.Errorf("stdout is empty; expected a path")
	}
}
