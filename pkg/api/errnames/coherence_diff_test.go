package errnames_test

// coherence_diff_test.go — canonical diff helper + induced-failure tests for
// the five-way coherence gate.
//
// computeCoherenceDiff is the shared implementation of the three enforced
// checks. TestFiveWayCoherence (coherence_test.go) calls it against real
// catalog/manifest/handler sources as the production gate. The induced-failure
// tests below call it with synthetic string slices to prove it fires correctly
// for each distinct drift direction — no live source tree required.

import (
	"fmt"
	"strings"
	"testing"
)

// CoherenceFinding records one coherence violation. Name is the err_name
// involved; Message is the actionable, human-readable text that mirrors the
// t.Errorf format strings in TestFiveWayCoherence.
type CoherenceFinding struct {
	Name    string
	Message string
}

// computeCoherenceDiff executes the same three enforced checks as
// TestFiveWayCoherence against caller-supplied string slices. It returns one
// CoherenceFinding per violation.
//
// The four sets correspond to:
//
//	handlerEmitted    — (a) sentinels referenced in pkg/api handler code
//	catalogNames      — (b) Name fields from errnames.Catalog
//	manifestErrorNames — (c) per-verb ErrorNames from callable manifest verbs
//	exportedSentinels  — (d) package-level var Err* declarations in pkg/api
//
// exportedSentinels is accepted for API symmetry; the compile-time guarantee
// that Catalog entries referencing pkg/api vars are consistent with the
// exported set means check (b)⊆(d) is not separately enforced here (matching
// coherence_test.go's intentional no-op loop for that direction).
func computeCoherenceDiff(
	handlerEmitted,
	catalogNames,
	manifestErrorNames,
	exportedSentinels []string,
) []CoherenceFinding {
	var findings []CoherenceFinding

	catalogSet := toSet(catalogNames)
	manifestSet := toSet(manifestErrorNames)

	// ── Check 1: (a) ⊆ (b) ──────────────────────────────────────────────────
	// Every sentinel referenced in handler code must have a Catalog entry.
	for _, name := range handlerEmitted {
		if _, ok := catalogSet[name]; !ok {
			findings = append(findings, CoherenceFinding{
				Name: name,
				Message: fmt.Sprintf(
					"sentinel %q referenced by handler code (pkg/api/*.go) "+
						"but missing from errnames.Catalog (pkg/api/errnames/catalog.go)",
					name,
				),
			})
		}
	}

	// ── Check 2: (c) ⊆ (b) ──────────────────────────────────────────────────
	// Every ErrorName in a callable verb's manifest must have a Catalog entry.
	for _, name := range manifestErrorNames {
		if _, ok := catalogSet[name]; !ok {
			findings = append(findings, CoherenceFinding{
				Name: name,
				Message: fmt.Sprintf(
					"err_name %q in callable VerbDef.ErrorNames "+
						"(pkg/api/manifest/manifest.go) "+
						"but missing from errnames.Catalog (pkg/api/errnames/catalog.go)",
					name,
				),
			})
		}
	}

	// ── Check 3: (b) ⊆ (c) (ErrInternal excepted) ──────────────────────────
	// Every Catalog entry must appear in at least one callable verb's ErrorNames.
	// Exception: "ErrInternal" is the Classify fallback; it is never in the manifest.
	const errInternalException = "ErrInternal"
	for _, name := range catalogNames {
		if name == errInternalException {
			continue
		}
		if _, ok := manifestSet[name]; !ok {
			findings = append(findings, CoherenceFinding{
				Name: name,
				Message: fmt.Sprintf(
					"err_name %q in errnames.Catalog (pkg/api/errnames/catalog.go) "+
						"but referenced by zero callable VerbDef.ErrorNames slices "+
						"(pkg/api/manifest/manifest.go); "+
						"either add it to the relevant verb's ErrorNames or remove it from the Catalog",
					name,
				),
			})
		}
	}

	return findings
}

// ── Induced-failure tests ────────────────────────────────────────────────────

// TestDiffCatalogVsManifest — drift direction: Catalog has ErrX but no callable
// verb lists it in ErrorNames. Check 3 (b⊆c) must fire with a message naming
// ErrX and pointing to manifest.go as the fix location.
func TestDiffCatalogVsManifest(t *testing.T) {
	findings := computeCoherenceDiff(
		nil,               // handlerEmitted — no handler pressure
		[]string{"ErrX"},  // catalogNames — ErrX registered in Catalog
		nil,               // manifestErrorNames — no verb lists it
		[]string{"ErrX"},  // exportedSentinels — declared in pkg/api
	)

	if len(findings) != 1 {
		t.Fatalf("want 1 finding (catalog-without-manifest drift), got %d: %v",
			len(findings), findings)
	}
	f := findings[0]
	if f.Name != "ErrX" {
		t.Errorf("finding.Name = %q, want %q", f.Name, "ErrX")
	}
	if !strings.Contains(f.Message, "zero callable VerbDef.ErrorNames") {
		t.Errorf("finding message lacks actionable manifest attribution; got: %s", f.Message)
	}
	if !strings.Contains(f.Message, "catalog.go") {
		t.Errorf("finding message lacks catalog.go reference; got: %s", f.Message)
	}
	if !strings.Contains(f.Message, "manifest.go") {
		t.Errorf("finding message lacks manifest.go reference; got: %s", f.Message)
	}
}

// TestDiffManifestVsCatalog — drift direction: a callable verb's ErrorNames
// references ErrX but ErrX has no Catalog entry. Check 2 (c⊆b) must fire
// with a message naming ErrX and pointing to catalog.go as the fix location.
func TestDiffManifestVsCatalog(t *testing.T) {
	findings := computeCoherenceDiff(
		nil,               // handlerEmitted — no handler pressure
		nil,               // catalogNames — no Catalog entry for ErrX
		[]string{"ErrX"},  // manifestErrorNames — a callable verb lists ErrX
		nil,               // exportedSentinels — empty
	)

	if len(findings) != 1 {
		t.Fatalf("want 1 finding (manifest-without-catalog drift), got %d: %v",
			len(findings), findings)
	}
	f := findings[0]
	if f.Name != "ErrX" {
		t.Errorf("finding.Name = %q, want %q", f.Name, "ErrX")
	}
	if !strings.Contains(f.Message, "callable VerbDef.ErrorNames") {
		t.Errorf("finding message lacks manifest attribution; got: %s", f.Message)
	}
	if !strings.Contains(f.Message, "catalog.go") {
		t.Errorf("finding message lacks catalog.go reference; got: %s", f.Message)
	}
}

// TestDiffHandlerVsCatalog — drift direction: handler code wraps ErrX via
// fmt.Errorf("%w: ...", ErrX) but ErrX has no Catalog entry. Check 1 (a⊆b)
// must fire with a message naming ErrX and pointing to catalog.go.
func TestDiffHandlerVsCatalog(t *testing.T) {
	findings := computeCoherenceDiff(
		[]string{"ErrX"},  // handlerEmitted — handler wraps ErrX
		nil,               // catalogNames — no Catalog entry
		nil,               // manifestErrorNames — empty
		[]string{"ErrX"},  // exportedSentinels — declared in pkg/api
	)

	if len(findings) != 1 {
		t.Fatalf("want 1 finding (handler-without-catalog drift), got %d: %v",
			len(findings), findings)
	}
	f := findings[0]
	if f.Name != "ErrX" {
		t.Errorf("finding.Name = %q, want %q", f.Name, "ErrX")
	}
	if !strings.Contains(f.Message, "handler code") {
		t.Errorf("finding message lacks handler-code attribution; got: %s", f.Message)
	}
	if !strings.Contains(f.Message, "catalog.go") {
		t.Errorf("finding message lacks catalog.go reference; got: %s", f.Message)
	}
}

// TestDiffNoDriftClean — baseline: all four sources agree on exactly {"ErrX"}.
// computeCoherenceDiff must return zero findings.
func TestDiffNoDriftClean(t *testing.T) {
	findings := computeCoherenceDiff(
		[]string{"ErrX"}, // handlerEmitted
		[]string{"ErrX"}, // catalogNames
		[]string{"ErrX"}, // manifestErrorNames
		[]string{"ErrX"}, // exportedSentinels
	)

	if len(findings) != 0 {
		t.Errorf("want 0 findings for fully coherent input, got %d: %v",
			len(findings), findings)
	}
}

// TestDiffExclusions — the ErrInternal exclusion: ErrInternal appears in
// handler code and Catalog but is intentionally absent from the manifest
// (it is the Classify fallback, not a per-verb error). Check 3 must skip it
// and return zero findings.
func TestDiffExclusions(t *testing.T) {
	findings := computeCoherenceDiff(
		[]string{"ErrInternal"}, // handlerEmitted — code may reference it
		[]string{"ErrInternal"}, // catalogNames — it lives in the Catalog
		nil,                     // manifestErrorNames — no verb lists it (by design)
		nil,                     // exportedSentinels — not a pkg/api var
	)

	if len(findings) != 0 {
		t.Errorf("want 0 findings for ErrInternal exclusion, got %d: %v",
			len(findings), findings)
	}
}

// TestDiffMultipleDriftDirections — compound case: two sentinels each drifting
// in a different direction simultaneously. Both findings must be reported so a
// developer sees all violations in one run rather than one-at-a-time.
func TestDiffMultipleDriftDirections(t *testing.T) {
	// ErrA: in handler but not catalog (check 1 fires).
	// ErrB: in catalog but not manifest (check 3 fires).
	findings := computeCoherenceDiff(
		[]string{"ErrA"},          // handlerEmitted
		[]string{"ErrB"},          // catalogNames
		nil,                       // manifestErrorNames
		[]string{"ErrA", "ErrB"},  // exportedSentinels
	)

	if len(findings) != 2 {
		t.Fatalf("want 2 findings (ErrA and ErrB drift), got %d: %v",
			len(findings), findings)
	}
	names := make(map[string]struct{}, len(findings))
	for _, f := range findings {
		names[f.Name] = struct{}{}
	}
	for _, want := range []string{"ErrA", "ErrB"} {
		if _, ok := names[want]; !ok {
			t.Errorf("expected a finding for %q but none found among %v", want, findings)
		}
	}
}
