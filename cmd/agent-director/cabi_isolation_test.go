package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCmdAgentDirectorDoesNotImportPkgCabi enforces the Epic 2 layer-boundary
// invariant: nothing under cmd/agent-director/ may import pkg/cabi.
// The C-ABI shim is a pkg-layer concern; cmd must stay clean of it.
func TestCmdAgentDirectorDoesNotImportPkgCabi(t *testing.T) {
	const forbidden = "github.com/gabemahoney/agent-director/pkg/cabi"

	// Walk from the directory this test lives in.
	// os.Getwd() inside a test returns the package directory.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}

	fset := token.NewFileSet()

	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Skip test files — the invariant targets production code.
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}

		f, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Errorf("parse %s: %v", path, parseErr)
			return nil
		}

		for _, imp := range f.Imports {
			// imp.Path.Value is a quoted string, e.g. `"github.com/..."`
			importPath := strings.Trim(imp.Path.Value, `"`)
			if importPath == forbidden {
				rel, _ := filepath.Rel(dir, path)
				t.Errorf(
					"cmd/agent-director/%s imports pkg/cabi; "+
						"cmd-side must not depend on the C-ABI shim per Epic 2 layer boundary",
					rel,
				)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("filepath.Walk: %v", err)
	}
}
