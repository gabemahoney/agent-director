package main_test

import (
	"encoding/json"
	"testing"
)

// TestEnvelopeParityVersion asserts the version envelope shape: exactly the
// two JSON string fields "version" and "commit" (values may be empty in test
// builds; presence is the invariant).
func TestEnvelopeParityVersion(t *testing.T) {
	stdout, stderr, code := runCLI(t, "version")
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q; want 0 and empty", code, stderr)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout)
	}
	_, v := parsed["version"].(string)
	_, c := parsed["commit"].(string)
	if !v || !c || len(parsed) != 2 {
		t.Errorf("version output = %v; want exactly the string keys version and commit", parsed)
	}
}

// TestEnvelopeParityListEmpty asserts the list envelope against an empty
// store is exactly {"spawns":[]}: a non-null empty array, never null or absent.
func TestEnvelopeParityListEmpty(t *testing.T) {
	stdout, stderr, code := runCLI(t, "list")
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q; want 0 and empty", code, stderr)
	}
	if stdout != "{\"spawns\":[]}\n" {
		t.Errorf("stdout = %q; want {\"spawns\":[]}", stdout)
	}
}

// TestEnvelopeParitySpawnCwdNotFound asserts the spawn-failure envelope for a
// nonexistent cwd: exit code 1, empty stdout, and an ErrCwdNotFound envelope
// with a description.
func TestEnvelopeParitySpawnCwdNotFound(t *testing.T) {
	stdout, stderr, code := runCLI(t, "spawn", "--cwd", "/no/such/path")
	if env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrCwdNotFound"); env.ErrDescription == "" {
		t.Errorf("err_description empty; want a human-readable message")
	}
}
