package errnames_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/errnames"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// TestFiveWayCoherence asserts that the five sources of err_name truth are
// mutually consistent:
//
//	(a) Sentinels directly referenced in handler code (pkg/api/*.go)
//	    — from ScanHandlerSentinels.
//	(b) Names in pkg/api/errnames.Catalog.
//	(c) Per-verb ErrorNames from manifest.CallableVerbs() (callable subset only).
//	(d) Package-level var Err* declarations in pkg/api.
//	(e) Committed catalog.json and surface.json (covered by
//	    TestCatalogJSONUpToDate and TestSurfaceJSONUpToDate).
//
// Non-callable verbs (help, serve, hook) are intentionally excluded from
// source (c): they have no handler code in pkg/api/*.go, so including their
// ErrorNames would produce false-positive failures.
func TestFiveWayCoherence(t *testing.T) {
	// ── collect sources ──────────────────────────────────────────────────────

	// (a) Sentinels referenced by handler code.
	handlerEmitted, err := errnames.ScanHandlerSentinels("../")
	if err != nil {
		t.Fatalf("ScanHandlerSentinels: %v", err)
	}

	// (b) Catalog names + cabi-scoped subset.
	catalogNames := make([]string, 0, len(errnames.Catalog))
	var cabiScopedNames []string
	for _, entry := range errnames.Catalog {
		catalogNames = append(catalogNames, entry.Name)
		if entry.Scope == "cabi" {
			cabiScopedNames = append(cabiScopedNames, entry.Name)
		}
	}

	// (c) ErrorNames from callable verbs only.
	manifestNames := errnames.ManifestErrorNames(manifest.CallableVerbs())

	// (d) Exported pkg/api var Err* declarations.
	exportedNames, err := errnames.ScanExportedSentinels("../")
	if err != nil {
		t.Fatalf("ScanExportedSentinels: %v", err)
	}

	// ── coherence checks ─────────────────────────────────────────────────────
	// computeCoherenceDiff (coherence_diff_test.go) enforces checks 1 (a⊆b),
	// 3 (c⊆b), and 4 (b⊆c). The induced-failure tests in coherence_diff_test.go
	// prove it fires correctly for each drift direction.
	//
	// Check 2 ((b)⊆(d) for api-origin entries) is enforced at compile time:
	// catalog.go imports pkg/api and references api.ErrX directly, so any
	// api-origin Catalog entry whose sentinel is missing from pkg/api will
	// fail compilation. Loop omitted per engineering guide dead-code rule.
	//
	// cabiScopedNames: cabi-scoped Catalog entries (e.g. ErrUnknownHandle) are
	// exempt from Check 3 (b⊆c) — no callable verb declares them in ErrorNames
	// because they originate exclusively in pkg/cabi's dispatch layer.
	for _, f := range computeCoherenceDiff(handlerEmitted, catalogNames, manifestNames, exportedNames, cabiScopedNames) {
		t.Errorf("%s", f.Message)
	}
}

// TestCatalogJSONUpToDate verifies that the committed pkg/api/errnames/catalog.json
// matches the output of the catalog generator against the current in-tree catalog.go.
// A mismatch means catalog.go was edited without running `make errnames-json`.
func TestCatalogJSONUpToDate(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}

	catalogPath := filepath.Join(wd, "catalog.json")
	committed, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatalf("read committed catalog.json: %v", err)
	}

	// Run the generator. It writes to catalogPath (same file) using
	// runtime.Caller(0) to locate the output directory.
	cmd := exec.Command("go", "run", "./generate.go")
	cmd.Dir = wd
	out, runErr := cmd.CombinedOutput()

	// Read whatever the generator produced, then restore the original content
	// so the working tree is always left clean.
	generated, readErr := os.ReadFile(catalogPath)
	restoreErr := os.WriteFile(catalogPath, committed, 0o644)

	if runErr != nil {
		t.Fatalf("catalog generator failed: %v\n%s", runErr, out)
	}
	if readErr != nil {
		t.Fatalf("read generated catalog.json: %v", readErr)
	}
	if restoreErr != nil {
		t.Fatalf("restore catalog.json: %v", restoreErr)
	}

	if !bytes.Equal(committed, generated) {
		t.Errorf(
			"pkg/api/errnames/catalog.json is stale; run `make errnames-json` to update it\n%s",
			jsonDiffSnippet(string(committed), string(generated)),
		)
	}
}

// TestSurfaceJSONUpToDate verifies that the committed pkg/api/manifest/surface.json
// matches the output of the surface generator against the current in-tree manifest.go.
// A mismatch means manifest.go was edited without running `make surface-json`.
func TestSurfaceJSONUpToDate(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}

	manifestDir := filepath.Join(wd, "..", "manifest")
	surfacePath := filepath.Join(manifestDir, "surface.json")

	committed, err := os.ReadFile(surfacePath)
	if err != nil {
		t.Fatalf("read committed surface.json: %v", err)
	}

	cmd := exec.Command("go", "run", "./generate.go")
	cmd.Dir = manifestDir
	out, runErr := cmd.CombinedOutput()

	generated, readErr := os.ReadFile(surfacePath)
	restoreErr := os.WriteFile(surfacePath, committed, 0o644)

	if runErr != nil {
		t.Fatalf("surface generator failed: %v\n%s", runErr, out)
	}
	if readErr != nil {
		t.Fatalf("read generated surface.json: %v", readErr)
	}
	if restoreErr != nil {
		t.Fatalf("restore surface.json: %v", restoreErr)
	}

	if !bytes.Equal(committed, generated) {
		t.Errorf(
			"pkg/api/manifest/surface.json is stale; run `make surface-json` to update it\n%s",
			jsonDiffSnippet(string(committed), string(generated)),
		)
	}
}

// toSet converts a sorted string slice into a set.
func toSet(ss []string) map[string]struct{} {
	m := make(map[string]struct{}, len(ss))
	for _, s := range ss {
		m[s] = struct{}{}
	}
	return m
}

// jsonDiffSnippet returns a short human-readable diff of two JSON strings.
// It shows the first line where they diverge plus a few lines of context,
// sufficient for a developer to know which field changed.
func jsonDiffSnippet(want, got string) string {
	wantLines := splitLines(want)
	gotLines := splitLines(got)

	minLen := len(wantLines)
	if len(gotLines) < minLen {
		minLen = len(gotLines)
	}

	for i := 0; i < minLen; i++ {
		if wantLines[i] != gotLines[i] {
			start := i - 2
			if start < 0 {
				start = 0
			}
			endW := i + 4
			if endW > len(wantLines) {
				endW = len(wantLines)
			}
			endG := i + 4
			if endG > len(gotLines) {
				endG = len(gotLines)
			}
			var buf bytes.Buffer
			fmt.Fprintf(&buf, "(first diff at line %d)\n", i+1)
			fmt.Fprintf(&buf, "committed:\n")
			for _, l := range wantLines[start:endW] {
				fmt.Fprintf(&buf, "  %s\n", l)
			}
			fmt.Fprintf(&buf, "regenerated:\n")
			for _, l := range gotLines[start:endG] {
				fmt.Fprintf(&buf, "  %s\n", l)
			}
			return buf.String()
		}
	}
	if len(wantLines) != len(gotLines) {
		return fmt.Sprintf("line count differs: committed %d lines, regenerated %d lines",
			len(wantLines), len(gotLines))
	}
	return ""
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
