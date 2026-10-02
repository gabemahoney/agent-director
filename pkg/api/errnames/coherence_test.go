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

	// (b) Catalog names.
	catalogNames := make([]string, 0, len(errnames.Catalog))
	for _, entry := range errnames.Catalog {
		catalogNames = append(catalogNames, entry.Name)
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
	for _, f := range computeCoherenceDiff(handlerEmitted, catalogNames, manifestNames, exportedNames) {
		t.Errorf("%s", f.Message)
	}
}

// TestNoCabiResidue is a regression guard for bug b.me4. The cabi layer was
// removed in PR #12; if anyone re-adds the ErrUnknownHandle sentinel, this
// test fires before review can drift.
func TestNoCabiResidue(t *testing.T) {
	for _, entry := range errnames.Catalog {
		if entry.Name == "ErrUnknownHandle" {
			t.Errorf("ErrUnknownHandle re-added to Catalog; the cabi layer is gone — remove this entry")
		}
	}
}

// TestCatalogJSONUpToDate verifies that the committed pkg/api/errnames/catalog.json
// matches the output of the catalog generator against the current in-tree catalog.go.
// A mismatch means catalog.go was edited without running `make errnames-json`.
func TestCatalogJSONUpToDate(t *testing.T) {
	assertGeneratedUpToDate(t, ".", "catalog.json", "errnames-json")
}

// TestSurfaceJSONUpToDate verifies that the committed pkg/api/manifest/surface.json
// matches the output of the surface generator against the current in-tree manifest.go.
// A mismatch means manifest.go was edited without running `make surface-json`.
func TestSurfaceJSONUpToDate(t *testing.T) {
	assertGeneratedUpToDate(t, filepath.Join("..", "manifest"), "surface.json", "surface-json")
}

// assertGeneratedUpToDate runs pkgDir/generate.go from a temp copy, so its
// output lands in the temp dir and the committed file is never written (a
// rewrite raced the manifest tests reading it), then diffs against the committed file.
func assertGeneratedUpToDate(t *testing.T, pkgDir, outName, makeTarget string) {
	t.Helper()
	committedPath := filepath.Join(pkgDir, outName)
	committed, err := os.ReadFile(committedPath)
	if err != nil {
		t.Fatalf("read committed %s: %v", outName, err)
	}

	// The generator writes beside its own source (runtime.Caller(0)); running
	// a copy redirects the write. cmd.Dir keeps the build inside this module.
	src, err := os.ReadFile(filepath.Join(pkgDir, "generate.go"))
	if err != nil {
		t.Fatalf("read generator: %v", err)
	}
	tmp := t.TempDir()
	genCopy := filepath.Join(tmp, "generate.go")
	if err := os.WriteFile(genCopy, src, 0o644); err != nil {
		t.Fatalf("copy generator: %v", err)
	}
	cmd := exec.Command("go", "run", genCopy)
	cmd.Dir = pkgDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s generator failed: %v\n%s", outName, err, out)
	}
	generated, err := os.ReadFile(filepath.Join(tmp, outName))
	if err != nil {
		t.Fatalf("read generated %s: %v", outName, err)
	}

	if !bytes.Equal(committed, generated) {
		t.Errorf(
			"%s is stale; run `make %s` to update it\n%s",
			filepath.ToSlash(filepath.Clean(filepath.Join("pkg", "api", "errnames", committedPath))),
			makeTarget,
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
