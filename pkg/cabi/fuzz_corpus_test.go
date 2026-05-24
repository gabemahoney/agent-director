package main

// fuzz_corpus_test.go defines the shared seed corpus and the
// assertWellFormedEnvelope post-condition helper used by every FuzzAd* target
// in fuzz_verbs_test.go.
//
// No build tag: this file is included in all test builds. Fuzz targets always
// run their seed corpus as ordinary sub-tests when -fuzz is not supplied, so
// the corpus must be reachable in default `go test ./pkg/cabi/...` runs.
//
// Corpus coverage matrix (minimum per Epic AC):
//   ≥6  JSON-syntax errors
//   ≥3  wrong-type / null handle cases
//   ≥2  truncated UTF-8 sequences embedded in JSON string values
//   ≥1  oversized payload (≥4 MiB)
//   ≥3  injection-style strings
//   ≥2  depth-bomb cases
//   ≥2  valid-JSON-with-noise cases

import (
	"encoding/json"
	"strings"
	"testing"
)

// sharedFuzzCorpus is the slice of seed strings seeded into every FuzzAd*
// target via f.Add. Populated by compile-time literals and the init() below.
var sharedFuzzCorpus []string

func init() {
	// ── Compile-time literals ─────────────────────────────────────────────────

	sharedFuzzCorpus = []string{
		// ── JSON-syntax errors (6) ───────────────────────────────────────────
		``,                          // empty input — not valid JSON at all
		`{`,                         // bare open brace, incomplete object
		`}`,                         // bare close brace, invalid JSON
		`{"unclosed": "string`,      // unclosed string literal
		`[1, 2, "unclosed array`,    // unclosed array
		`{"trailing": "comma",}`,    // trailing comma (invalid JSON)

		// ── Wrong-type / null handle (3) ────────────────────────────────────
		`{"handle": ""}`,            // empty-string handle
		`{"handle": null}`,          // null handle value
		`{"handle": 42}`,            // integer where string expected

		// ── Truncated UTF-8 embedded in JSON string values (2) ──────────────
		// 0xC0 is a lone high byte that cannot start a valid UTF-8 sequence.
		"{\"handle\": \"\xc0\"}",
		// 0xE2 0x80 is a truncated three-byte UTF-8 sequence (missing third byte).
		"{\"handle\": \"\xe2\x80\"}",

		// ── Injection-style strings (3) ──────────────────────────────────────
		// SQL injection fragment.
		`{"handle": "'; DROP TABLE spawns; --"}`,
		// Embedded NUL byte.
		"{\"handle\": \"abc\x00def\"}",
		// Backslash path-traversal sequences.
		`{"handle": "\\..\\..\\etc/passwd"}`,

		// ── Valid JSON with noise (2) ─────────────────────────────────────────
		// Extra unknown fields that the verb parsers silently ignore.
		`{"handle": "abc", "extra_unknown_field": "ignored", "another": "noise"}`,
		// All legitimate fields present but set to null.
		`{"handle": null, "store_path": null, "create_if_missing": null}`,
	}

	// ── Large entries built programmatically ─────────────────────────────────

	// Oversized payload: a "handle" value that exceeds 4 MiB, exercising any
	// length caps or memory-pressure paths in the JSON parsing layer.
	bigVal := strings.Repeat("x", 4*1024*1024)
	oversized, _ := json.Marshal(map[string]string{"handle": bigVal})
	sharedFuzzCorpus = append(sharedFuzzCorpus, string(oversized))

	// Depth-bomb: 10 000 levels of nested JSON arrays.
	// json.Unmarshal will hit a recursion / depth limit and return an error,
	// which the verb wrapper must turn into a well-formed ErrInternal envelope.
	nestedArrays := strings.Repeat("[", 10000) + strings.Repeat("]", 10000)
	sharedFuzzCorpus = append(sharedFuzzCorpus, nestedArrays)

	// Depth-bomb: 10 000 levels of nested JSON objects.
	nestedObjects := strings.Repeat(`{"a":`, 10000) + `{}` + strings.Repeat("}", 10000)
	sharedFuzzCorpus = append(sharedFuzzCorpus, nestedObjects)
}

// assertWellFormedEnvelope asserts the structural contract of every envelope
// returned by a cabi verb wrapper:
//
//  1. raw parses as valid JSON.
//  2. The top-level value is a JSON object (not array, number, string, or null).
//  3. If "err_name" is present: it is a non-empty string, and "err_description"
//     is also present as a non-empty string — the two-field error shape.
//  4. If "err_name" is absent: success shape — no further structural constraint
//     is imposed (payloads differ per verb).
func assertWellFormedEnvelope(tb testing.TB, raw []byte) {
	tb.Helper()

	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		tb.Errorf("assertWellFormedEnvelope: not valid JSON: %v — raw: %q", err, raw)
		return
	}

	errNameVal, hasErrName := m["err_name"]
	if !hasErrName {
		// Success shape: verify no orphaned err_description.
		if _, hasDesc := m["err_description"]; hasDesc {
			tb.Errorf("assertWellFormedEnvelope: err_description present without err_name — raw: %s", raw)
		}
		return
	}

	// Error shape: err_name must be a non-empty string.
	errName, nameOK := errNameVal.(string)
	if !nameOK || errName == "" {
		tb.Errorf("assertWellFormedEnvelope: err_name is not a non-empty string: %T(%v) — raw: %s",
			errNameVal, errNameVal, raw)
	}

	// Error shape: err_description must be a non-empty string.
	errDesc, descOK := m["err_description"].(string)
	if !descOK || errDesc == "" {
		tb.Errorf("assertWellFormedEnvelope: err_description missing or not a non-empty string — raw: %s", raw)
	}
}
