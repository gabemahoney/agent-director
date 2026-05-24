package main_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestEnvelopeParityHelp asserts the help envelope shape: a single JSON
// object with "verbs" as a non-null, non-empty array of verb objects that
// each carry a non-empty "name" field.
func TestEnvelopeParityHelp(t *testing.T) {
	stdout, stderr, code := runCLI(t, "help")
	if code != 0 {
		t.Fatalf("exit=%d want 0; stderr=%q", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr=%q want empty", stderr)
	}
	var parsed struct {
		Verbs []json.RawMessage `json:"verbs"`
	}
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout)
	}
	if parsed.Verbs == nil {
		t.Fatal("verbs is null; want non-null array")
	}
	if len(parsed.Verbs) == 0 {
		t.Fatal("verbs is empty; want at least one entry")
	}
	for i, raw := range parsed.Verbs {
		var verb struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &verb); err != nil {
			t.Errorf("verbs[%d] is not a valid object: %v", i, err)
			continue
		}
		if verb.Name == "" {
			t.Errorf("verbs[%d].name is empty", i)
		}
	}
}

// TestEnvelopeParityVersion asserts the version envelope shape: a single JSON
// object with exactly two fields — "version" and "commit" — both JSON strings.
// Values may be empty in test builds (no ldflags); presence is the invariant.
func TestEnvelopeParityVersion(t *testing.T) {
	stdout, stderr, code := runCLI(t, "version")
	if code != 0 {
		t.Fatalf("exit=%d want 0; stderr=%q", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr=%q want empty", stderr)
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout)
	}
	for _, key := range []string{"version", "commit"} {
		raw, ok := parsed[key]
		if !ok {
			t.Errorf("top-level key %q missing from version output", key)
			continue
		}
		// Must be a JSON string (not null, not a number).
		if !strings.HasPrefix(string(raw), `"`) {
			t.Errorf("key %q value %s is not a JSON string", key, raw)
		}
	}
	if n := len(parsed); n != 2 {
		t.Errorf("version output has %d top-level keys; want exactly 2 {version,commit}: %v", n, parsed)
	}
}

// TestEnvelopeParityListEmpty asserts the list envelope against an empty
// store is exactly {"spawns":[]} — a non-null empty array, never
// {"spawns":null} and never an absent field. This is the critical regression
// gate for the empty-slice-not-null invariant.
func TestEnvelopeParityListEmpty(t *testing.T) {
	stdout, stderr, code := runCLI(t, "list")
	if code != 0 {
		t.Fatalf("exit=%d want 0; stderr=%q", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr=%q want empty", stderr)
	}
	var parsed struct {
		Spawns []json.RawMessage `json:"spawns"`
	}
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout=%q", err, stdout)
	}
	// nil slice means the JSON had "spawns":null (or the key was absent).
	if parsed.Spawns == nil {
		t.Errorf("spawns is null or absent; want non-null empty array []. stdout=%q", stdout)
	}
	if n := len(parsed.Spawns); n != 0 {
		t.Errorf("len(spawns)=%d; want 0 (empty store)", n)
	}
	// Re-marshal to assert the canonical empty-array wire shape.
	remarshaled, err := json.Marshal(parsed.Spawns)
	if err != nil {
		t.Fatalf("re-marshal spawns: %v", err)
	}
	if string(remarshaled) != "[]" {
		t.Errorf("spawns re-marshals to %s; want [] (non-null empty array)", remarshaled)
	}
}

// TestEnvelopeParitySpawnCwdNotFound asserts the spawn-failure envelope for
// a nonexistent cwd: exit code 1, empty stdout, and stderr is a JSON object
// with err_name="ErrCwdNotFound". Pins both the exit-code contract and the
// exact sentinel name as a regression gate.
func TestEnvelopeParitySpawnCwdNotFound(t *testing.T) {
	stdout, stderr, code := runCLI(t, "spawn", "--cwd", "/no/such/path")
	if code != 1 {
		t.Fatalf("exit=%d want 1; stdout=%q stderr=%q", code, stdout, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout=%q want empty on error", stdout)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrCwdNotFound" {
		t.Errorf("err_name=%q want ErrCwdNotFound", env.ErrName)
	}
	if env.ErrDescription == "" {
		t.Errorf("err_description empty; want non-empty human-readable message")
	}
}
