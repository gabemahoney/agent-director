// Package envelope_diff implements the harness primitives for the SRD SR-7.4
// envelope-diff regression test.
//
// The harness exercises every callable verb (per manifest.CallableVerbs) through
// two execution paths—a CLI subprocess and an in-process pkg/api.Client—against
// identical fixture stores, then asserts structural equivalence between the JSON
// envelopes produced by each path.
//
// # Envelope contract (E1 pin)
//
// Success path: the CLI writes the raw pkg/api result struct to stdout and exits
// 0. The Client returns the same typed result in-process. Both sides produce the
// same JSON bytes after normalization.
//
// Error path: the CLI writes {"err_name","err_description"} to stderr and exits
// non-zero. The Client returns a Go error; runClient marshals it to the same
// {"err_name","err_description"} shape for the differ to compare.
//
// # File layout
//
//	harness.go         — fixture-store copy + CLI / fake-tmux binary builders.
//	runners.go         — runCLI (subprocess) + runClient (in-process), both
//	                     producing JSON byte envelopes.
//	diff.go            — normalize + structuralDiff + diffEntry.
//	selectors.go       — selector parsing and path-matching for the diff's
//	                     ignore list.
//	manifest_loader.go — NonDetManifest loader.
//
// Per-verb test cases live in *_test.go files in this package (internal tests).
// Architecture reference: docs/architecture.md §"Envelope-diff regression harness".
package envelope_diff
