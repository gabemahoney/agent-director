package errnames_test

import (
	"sort"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/errnames"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// TestScanHandlerSentinels verifies the scanner against the synthetic fixture
// in testdata/fixture_handlers.go. The fixture contains:
//   - A bare-ident fmt.Errorf %w pattern (should be collected)
//   - A selector-expr fmt.Errorf %w pattern (terminal name collected, pkg dropped)
//   - A non-Err-prefixed ident (ignored)
//   - A format string without %w (ignored)
func TestScanHandlerSentinels(t *testing.T) {
	got, err := errnames.ScanHandlerSentinels("testdata")
	if err != nil {
		t.Fatalf("ScanHandlerSentinels: %v", err)
	}

	want := []string{"ErrHandlerSentinelA", "ErrHandlerSentinelB"}
	if !stringSliceEqual(got, want) {
		t.Errorf("ScanHandlerSentinels(testdata) = %v, want %v", got, want)
	}
}

// TestScanHandlerSentinels_PKGApi verifies the scanner against the real
// pkg/api/ directory and asserts it finds at least the known set of
// directly-wrapped api sentinels. Uses a subset check (⊇) rather than
// exact equality so new sentinels don't break the test.
func TestScanHandlerSentinels_PKGApi(t *testing.T) {
	got, err := errnames.ScanHandlerSentinels("../")
	if err != nil {
		t.Fatalf("ScanHandlerSentinels(../): %v", err)
	}
	if len(got) == 0 {
		t.Fatal("ScanHandlerSentinels(../) returned no sentinels; expected at least the api-layer wraps")
	}

	// These sentinels are all directly wrapped via fmt.Errorf("%w: ...", X) in pkg/api/*.go.
	expected := []string{
		"ErrInvalidDecision",
		"ErrJsonlMissing",
		"ErrListInvalidLabel",
		"ErrNoSessionId",
		"ErrPauseTimeout",
		"ErrRelayModeOff",
		"ErrSendKeysWhileRelayed",
		"ErrSpawnNotInteractive",
		"ErrSpawnNotPausable",
		"ErrSpawnNotResumable",
	}
	gotSet := make(map[string]struct{}, len(got))
	for _, n := range got {
		gotSet[n] = struct{}{}
	}
	for _, name := range expected {
		if _, ok := gotSet[name]; !ok {
			t.Errorf("ScanHandlerSentinels(../) missing expected sentinel %q", name)
		}
	}
}

// TestManifestErrorNames verifies that ManifestErrorNames flattens and deduplicates
// ErrorNames across a synthetic slice of VerbDef entries.
func TestManifestErrorNames(t *testing.T) {
	verbs := []manifest.VerbDef{
		{Name: "verbA", ErrorNames: []string{"ErrFoo", "ErrBar"}},
		{Name: "verbB", ErrorNames: []string{"ErrBar", "ErrBaz"}}, // ErrBar duplicated
		{Name: "verbC", ErrorNames: []string{}},                    // empty — no contribution
	}

	got := errnames.ManifestErrorNames(verbs)
	want := []string{"ErrBar", "ErrBaz", "ErrFoo"}
	if !stringSliceEqual(got, want) {
		t.Errorf("ManifestErrorNames = %v, want %v", got, want)
	}
}

// TestManifestErrorNames_CallableSubset verifies that ManifestErrorNames is
// filter-agnostic: it returns ErrorNames for whatever slice the caller passes,
// including non-callable verbs if provided. The coherence gate (not this
// function) is responsible for calling manifest.CallableVerbs() to restrict
// the input to callable verbs only.
func TestManifestErrorNames_CallableSubset(t *testing.T) {
	verbs := []manifest.VerbDef{
		{Name: "callableVerb", Callable: true, ErrorNames: []string{"ErrFromCallable"}},
		{Name: "nonCallableVerb", Callable: false, ErrorNames: []string{"ErrFromNonCallable"}},
	}

	got := errnames.ManifestErrorNames(verbs)
	// Both entries should appear because ManifestErrorNames doesn't filter.
	want := []string{"ErrFromCallable", "ErrFromNonCallable"}
	if !stringSliceEqual(got, want) {
		t.Errorf("ManifestErrorNames (mixed callable) = %v, want %v", got, want)
	}

	// Now simulate what the coherence gate does: filter to callable first.
	callable := make([]manifest.VerbDef, 0)
	for _, v := range verbs {
		if v.Callable {
			callable = append(callable, v)
		}
	}
	got = errnames.ManifestErrorNames(callable)
	want = []string{"ErrFromCallable"}
	if !stringSliceEqual(got, want) {
		t.Errorf("ManifestErrorNames (callable only) = %v, want %v", got, want)
	}
}

// TestScanExportedSentinels verifies the scanner against the synthetic fixture
// in testdata/fixture_exports.go. The fixture contains ErrExportedA, ErrExportedB,
// and notAnError; only the two Err* names should be returned.
func TestScanExportedSentinels(t *testing.T) {
	got, err := errnames.ScanExportedSentinels("testdata")
	if err != nil {
		t.Fatalf("ScanExportedSentinels: %v", err)
	}

	// fixture_exports.go declares ErrExportedA and ErrExportedB.
	// fixture_handlers.go declares no vars at all.
	want := []string{"ErrExportedA", "ErrExportedB"}
	if !stringSliceEqual(got, want) {
		t.Errorf("ScanExportedSentinels(testdata) = %v, want %v", got, want)
	}
}

// TestScanExportedSentinels_PKGApi verifies the scanner against the real
// pkg/api/ directory and asserts it finds at least the well-known sentinels
// declared in pkg/api/errors.go and sibling files.
func TestScanExportedSentinels_PKGApi(t *testing.T) {
	got, err := errnames.ScanExportedSentinels("../")
	if err != nil {
		t.Fatalf("ScanExportedSentinels(../): %v", err)
	}
	if len(got) == 0 {
		t.Fatal("ScanExportedSentinels(../) returned no sentinels")
	}

	expected := []string{
		"ErrClientClosed",
		"ErrInvalidDecision",
		"ErrJsonlMissing",
		"ErrListInvalidLabel",
		"ErrNoSessionId",
		"ErrPauseTimeout",
		"ErrRelayModeOff",
		"ErrSendKeysWhileRelayed",
		"ErrSpawnNotInteractive",
		"ErrSpawnNotPausable",
		"ErrSpawnNotResumable",
	}
	gotSet := make(map[string]struct{}, len(got))
	for _, n := range got {
		gotSet[n] = struct{}{}
	}
	for _, name := range expected {
		if _, ok := gotSet[name]; !ok {
			t.Errorf("ScanExportedSentinels(../) missing expected sentinel %q", name)
		}
	}
}

// stringSliceEqual reports whether a and b are equal (same elements in same order).
func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}
