package smoke_test

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestSmokeImportGraph guards that the smoke test package only imports:
//   - github.com/gabemahoney/agent-director/pkg/api (and its subpackages)
//   - github.com/gabemahoney/agent-director/internal/testsupport/
//   - standard library packages (no dot before first slash component)
//   - external third-party packages (no module prefix)
//
// Any other internal/ import (e.g. internal/api, internal/store) fails the test.
// Only direct imports in the smoke test's own .go files are checked; transitive
// imports of pkg/api are outside scope (guarded by pkg/api's own import audit).
//
// Implementation uses go/parser to extract import statements from source —
// no external tooling required, runs in under a second.
func TestSmokeImportGraph(t *testing.T) {
	const modulePath = "github.com/gabemahoney/agent-director"

	// Locate this test file's directory — that's the smoke package directory.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller: could not determine test file path")
	}
	smokeDir := filepath.Dir(thisFile)

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, smokeDir, func(fi os.FileInfo) bool {
		return strings.HasSuffix(fi.Name(), ".go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parser.ParseDir(%q): %v", smokeDir, err)
	}
	if len(pkgs) == 0 {
		t.Fatalf("parser.ParseDir: no Go files found in %q", smokeDir)
	}

	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, imp := range file.Imports {
				path, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Errorf("bad import path literal %q: %v", imp.Path.Value, err)
					continue
				}
				if err := validateSmokeImport(modulePath, path); err != nil {
					t.Errorf("disallowed import in smoke package: %v", err)
				}
			}
		}
	}
}

// validateSmokeImport returns an error if importPath is a disallowed import
// for the smoke test package.
//
// Allowed:
//   - stdlib: paths that contain no dot in the first path component
//     (e.g. "testing", "reflect", "go/parser", "os/user")
//   - external modules: paths whose first component has a dot but does not
//     start with the module root (e.g. "golang.org/x/tools/...",
//     "github.com/some/other/pkg")
//   - github.com/gabemahoney/agent-director/pkg/api (and subpackages)
//   - github.com/gabemahoney/agent-director/internal/testsupport/ subpackages
//
// Disallowed:
//   - any other github.com/gabemahoney/agent-director/internal/... path
func validateSmokeImport(modulePath, importPath string) error {
	// Not our module at all → stdlib or external dep → allowed.
	if !strings.HasPrefix(importPath, modulePath) {
		return nil
	}

	// Strip module prefix to get the in-module path.
	rest := strings.TrimPrefix(importPath, modulePath)
	if rest == "" || rest == "/" {
		// The module root itself — unusual but not internal, allow.
		return nil
	}
	// rest is e.g. "/pkg/api", "/internal/testsupport/storefix"

	// pkg/api and its subpackages.
	if rest == "/pkg/api" || strings.HasPrefix(rest, "/pkg/api/") {
		return nil
	}

	// internal/testsupport/ subpackages.
	if strings.HasPrefix(rest, "/internal/testsupport/") {
		return nil
	}

	// Anything else under internal/ is disallowed.
	if strings.HasPrefix(rest, "/internal/") {
		return fmt.Errorf("%q: internal packages other than internal/testsupport/ are not allowed in the smoke package", importPath)
	}

	// Other in-module paths (cmd/, tools/, etc.) are allowed for now.
	return nil
}
