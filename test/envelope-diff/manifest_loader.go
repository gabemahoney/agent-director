// manifest_loader.go loads the non-deterministic-fields manifest that the
// structuralDiff ignore list is built from.
//
// The manifest file is test/envelope-diff/nondeterministic.json. It maps each
// callable verb name to the list of JSON path selectors (see selectors.go) that
// should be excluded from the structural diff because their values are
// non-deterministic across invocations (e.g. generated IDs, timestamps,
// build-time version fields).
//
// Example nondeterministic.json:
//
//	{
//	  "spawn":   ["claude_instance_id"],
//	  "version": ["version", "commit"],
//	  "list":    ["spawns[*].claude_instance_id", "spawns[*].started_at"],
//	  "expire":  []
//	}
//
// Every verb in manifest.CallableVerbs() must appear as a key; extra keys for
// non-callable verbs (serve, hook) are also disallowed. These invariants are
// enforced by the CI doc-drift gate (T2 deliverable), not by loadNonDetManifest
// itself.
//
// If nondeterministic.json does not yet exist (common during scaffold-only
// delivery before the test writer's pass), loadNonDetManifest logs a warning
// and returns an empty manifest so callers can still compile and run without the
// file. Malformed JSON is always fatal.
package envelope_diff

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// NonDetManifest maps each callable verb name to the list of JSON path
// selectors that should be excluded from the structural diff.
//
// A nil slice and an empty (non-nil) slice have different semantics:
//   - Absent key ("HasVerb → false"): the verb is unknown to the manifest;
//     Selectors returns an error.
//   - Present key with empty slice ("HasVerb → true"): the verb is known and
//     has no non-deterministic fields; Selectors returns ([]string{}, nil).
//
// See selectors.go for the selector grammar.
type NonDetManifest map[string][]string

// HasVerb reports whether verb is keyed in the manifest (regardless of whether
// the selector list is empty).
func (m NonDetManifest) HasVerb(verb string) bool {
	_, ok := m[verb]
	return ok
}

// Selectors returns the selector slice for verb.
//
//   - Known verb with selectors: returns (selectors, nil).
//   - Known verb with no selectors: returns ([]string{}, nil).
//   - Unknown verb: returns (nil, error) naming the offending verb.
//
// Callers should handle the error with t.Fatalf, keeping the manifest type
// *testing.T-free for reuse in non-test contexts.
func (m NonDetManifest) Selectors(verb string) ([]string, error) {
	sels, ok := m[verb]
	if !ok {
		return nil, fmt.Errorf("verb %q absent from nondeterministic.json", verb)
	}
	return sels, nil
}

// loadNonDetManifest reads and parses test/envelope-diff/nondeterministic.json
// relative to the repo root and returns the manifest. It is safe to call from
// multiple goroutines concurrently.
//
// If the file does not exist, a warning is logged and an empty manifest is
// returned so callers can proceed without the file during scaffold-only
// deliveries. Malformed JSON is fatal (calls t.Fatalf).
func loadNonDetManifest(t *testing.T) NonDetManifest {
	t.Helper()

	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("loadNonDetManifest: %v", err)
	}

	manifestPath := filepath.Join(root, "test", "envelope-diff", "nondeterministic.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			t.Logf("loadNonDetManifest: %s not found; returning empty manifest (nondeterministic fields will not be suppressed)", manifestPath)
			return NonDetManifest{}
		}
		t.Fatalf("loadNonDetManifest: read %s: %v", manifestPath, err)
	}

	m, err := loadNonDetManifestFromBytes(data)
	if err != nil {
		t.Fatalf("loadNonDetManifest: %v", err)
	}
	return m
}

// loadNonDetManifestFromBytes parses raw JSON bytes into a NonDetManifest.
// This is a pure function: it takes no *testing.T so it can be used in unit
// tests that supply in-memory JSON without touching the filesystem.
//
// Returns an error if the JSON is malformed; the error string includes the byte
// offset of the first syntax error when available.
func loadNonDetManifestFromBytes(b []byte) (NonDetManifest, error) {
	var m NonDetManifest
	if err := json.Unmarshal(b, &m); err != nil {
		// Surface byte offset for syntax errors to ease debugging.
		var syntaxErr *json.SyntaxError
		if errors.As(err, &syntaxErr) {
			return nil, fmt.Errorf("nondeterministic.json: syntax error at byte offset %d: %w", syntaxErr.Offset, err)
		}
		return nil, fmt.Errorf("nondeterministic.json: %w", err)
	}
	return m, nil
}
