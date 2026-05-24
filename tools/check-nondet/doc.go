// Package main implements the check-nondet checker.
//
// # Scope: verb-coverage only
//
// This tool verifies that every callable verb in manifest.CallableVerbs()
// has a corresponding top-level key in test/envelope-diff/nondeterministic.json,
// and that no extraneous keys appear in the JSON (keys that name verbs not in
// CallableVerbs(), such as the non-callable verbs serve, hook, or help).
//
// # Known limitation (SN-6): field-coverage drift is NOT detected
//
// check-nondet catches missing or extraneous verb *keys*. It does NOT detect
// the related failure mode where a callable verb's ResultFields gains a new
// non-deterministic field without the JSON's selector list being extended.
// Field-coverage drift is handled out-of-band: manual review at PR time, plus
// the success-diff test failing visibly when a new non-deterministic field
// starts producing diffs.
package main
