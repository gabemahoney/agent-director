// diff.go provides JSON normalization and structural diffing for the
// envelope-diff harness.
//
// normalize re-encodes JSON with sorted map keys so that two semantically
// identical JSON values produce byte-identical output regardless of key order
// or whitespace.
//
// structuralDiff walks two normalized JSON trees and records mismatches as
// diffEntry values with path-style notation (e.g. ".spawns[0].claude_instance_id").
// Callers supply an ignore list of selectors (see selectors.go) to suppress
// non-deterministic fields such as generated IDs and timestamps.
package envelope_diff

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// ── normalize ─────────────────────────────────────────────────────────────────

// normalize re-encodes b with sorted map keys and no extra whitespace.
// Two semantically equivalent JSON inputs produce byte-identical output.
//
// Go's encoding/json sorts map[string]interface{} keys alphabetically when
// marshaling, so the interface{} round-trip is sufficient for normalization.
func normalize(b []byte) ([]byte, error) {
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("normalize: unmarshal: %w", err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("normalize: marshal: %w", err)
	}
	return out, nil
}

// ── diffEntry ─────────────────────────────────────────────────────────────────

// diffEntry records a single mismatch between the CLI envelope (Want) and the
// Client envelope (Got) at a specific JSON path.
type diffEntry struct {
	// Path is the dot-bracket JSON path of the differing node
	// (e.g. ".spawns[0].claude_instance_id").
	Path string

	// Want is the value from the CLI envelope (the "expected" side).
	Want interface{}

	// Got is the value from the Client envelope (the "actual" side).
	Got interface{}

	// Kind categorises the diff: "value" (same type, different value),
	// "type" (different JSON types), "missing_cli" (key absent in CLI
	// envelope), "missing_client" (key absent in Client envelope).
	Kind string
}

// String formats the entry as a single human-readable line:
//
//	path: cli=<want> client=<got>
//
// Missing-side variants note the absence explicitly.
func (e diffEntry) String() string {
	switch e.Kind {
	case "missing_cli":
		return fmt.Sprintf("%s: missing on cli; client=%v", e.Path, e.Got)
	case "missing_client":
		return fmt.Sprintf("%s: cli=%v; missing on client", e.Path, e.Want)
	case "type":
		return fmt.Sprintf("%s: cli=%T(%v) client=%T(%v)", e.Path, e.Want, e.Want, e.Got, e.Got)
	default: // "value"
		return fmt.Sprintf("%s: cli=%v client=%v", e.Path, e.Want, e.Got)
	}
}

// ── structuralDiff ────────────────────────────────────────────────────────────

// structuralDiff parses a and b as JSON, walks both trees together, and returns
// all mismatches that are not covered by the ignore list.
//
// a is treated as the CLI envelope ("want"); b as the Client envelope ("got").
//
// ignore contains path selectors (see selectors.go) for nodes to skip.
// An empty ignore list causes every difference to be reported.
//
// Returned entries are in depth-first traversal order; the slice is nil (not
// empty) when the two envelopes are equivalent under the ignore list.
func structuralDiff(a, b []byte, ignore []string) []diffEntry {
	var av, bv interface{}
	if err := json.Unmarshal(a, &av); err != nil {
		return []diffEntry{{Path: ".", Want: string(a), Got: "(parse error: " + err.Error() + ")", Kind: "type"}}
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return []diffEntry{{Path: ".", Want: "(parse error: " + err.Error() + ")", Got: string(b), Kind: "type"}}
	}

	var out []diffEntry
	diffValues(av, bv, "", ignore, &out)
	return out
}

// diffValues recursively compares av (CLI side) and bv (Client side) at the
// given JSON path. Mismatches are appended to entries.
func diffValues(av, bv interface{}, path string, ignore []string, entries *[]diffEntry) {
	if shouldIgnore(path, ignore) {
		return
	}

	// Both nil → equal.
	if av == nil && bv == nil {
		return
	}

	switch a := av.(type) {
	case map[string]interface{}:
		b, ok := bv.(map[string]interface{})
		if !ok {
			*entries = append(*entries, diffEntry{Path: path, Want: av, Got: bv, Kind: "type"})
			return
		}
		diffMaps(a, b, path, ignore, entries)

	case []interface{}:
		b, ok := bv.([]interface{})
		if !ok {
			*entries = append(*entries, diffEntry{Path: path, Want: av, Got: bv, Kind: "type"})
			return
		}
		diffSlices(a, b, path, ignore, entries)

	default:
		// Scalar: nil, bool, float64, string.
		if reflect.TypeOf(av) != reflect.TypeOf(bv) {
			*entries = append(*entries, diffEntry{Path: path, Want: av, Got: bv, Kind: "type"})
			return
		}
		if !reflect.DeepEqual(av, bv) {
			*entries = append(*entries, diffEntry{Path: path, Want: av, Got: bv, Kind: "value"})
		}
	}
}

// diffMaps compares two JSON object values. Keys present in only one side are
// reported as missing; keys present in both are recursed into.
func diffMaps(a, b map[string]interface{}, path string, ignore []string, entries *[]diffEntry) {
	// Keys in a (CLI side).
	for k, av := range a {
		childPath := childField(path, k)
		if shouldIgnore(childPath, ignore) {
			continue
		}
		bv, exists := b[k]
		if !exists {
			*entries = append(*entries, diffEntry{Path: childPath, Want: av, Got: nil, Kind: "missing_client"})
			continue
		}
		diffValues(av, bv, childPath, ignore, entries)
	}
	// Keys only in b (Client side).
	for k, bv := range b {
		childPath := childField(path, k)
		if shouldIgnore(childPath, ignore) {
			continue
		}
		if _, exists := a[k]; !exists {
			*entries = append(*entries, diffEntry{Path: childPath, Want: nil, Got: bv, Kind: "missing_cli"})
		}
	}
}

// diffSlices compares two JSON array values element by element.
func diffSlices(a, b []interface{}, path string, ignore []string, entries *[]diffEntry) {
	maxLen := len(a)
	if len(b) > maxLen {
		maxLen = len(b)
	}
	for i := 0; i < maxLen; i++ {
		childPath := childIndex(path, i)
		if shouldIgnore(childPath, ignore) {
			continue
		}
		switch {
		case i >= len(a):
			*entries = append(*entries, diffEntry{Path: childPath, Want: nil, Got: b[i], Kind: "missing_cli"})
		case i >= len(b):
			*entries = append(*entries, diffEntry{Path: childPath, Want: a[i], Got: nil, Kind: "missing_client"})
		default:
			diffValues(a[i], b[i], childPath, ignore, entries)
		}
	}
}

// shouldIgnore returns true if path matches any selector in ignore.
func shouldIgnore(path string, ignore []string) bool {
	for _, sel := range ignore {
		if pathMatchesSelector(path, sel) {
			return true
		}
	}
	return false
}

// childField returns the JSON path for field key under parent path.
// e.g. childField("", "foo") → ".foo", childField(".arr[0]", "id") → ".arr[0].id"
func childField(path, key string) string {
	return path + "." + key
}

// childIndex returns the JSON path for array index i under parent path.
// e.g. childIndex(".ids", 0) → ".ids[0]"
func childIndex(path string, i int) string {
	return fmt.Sprintf("%s[%d]", path, i)
}
