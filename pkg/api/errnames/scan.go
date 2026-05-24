package errnames

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// ScanHandlerSentinels walks every non-test .go file under srcDir and returns
// the deduplicated, sorted list of sentinel identifier names directly
// referenced in fmt.Errorf("%w: ...", <sentinel>) call patterns.
//
// Both bare identifiers (ErrFoo) and selector-qualified identifiers
// (pkg.ErrFoo) are collected; only the terminal name is returned (without
// package prefix). The scanner is intentionally conservative: it matches
// only the well-known fmt.Errorf %w-wrap pattern.
func ScanHandlerSentinels(srcDir string) ([]string, error) {
	seen := make(map[string]struct{})
	fset := token.NewFileSet()

	err := filepath.WalkDir(srcDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			// Skip testdata subdirectories — they contain fixture files with
			// synthetic sentinel names that are not real catalog entries.
			// Do NOT skip when testdata IS the srcDir: tests may point the
			// scanner directly at a testdata directory to verify it against
			// synthetic fixtures.
			if d.Name() == "testdata" && filepath.Clean(path) != filepath.Clean(srcDir) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		f, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}

		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if !isFmtErrorf(call.Fun) {
				return true
			}
			if len(call.Args) < 2 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if !stringLitStartsWithPercW(lit.Value) {
				return true
			}
			if name := sentinelName(call.Args[1]); name != "" {
				seen[name] = struct{}{}
			}
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return sortedKeys(seen), nil
}

// ManifestErrorNames flattens every verb's ErrorNames slice from the provided
// VerbDef slice into a deduplicated, sorted list. Callers decide which verbs
// to pass: the five-way coherence test passes manifest.CallableVerbs() to
// exclude non-callable verbs (help, serve, hook) from the gate.
//
// The function itself is filter-agnostic; it returns ErrorNames for whatever
// slice the caller supplies.
func ManifestErrorNames(verbs []manifest.VerbDef) []string {
	seen := make(map[string]struct{})
	for _, v := range verbs {
		for _, name := range v.ErrorNames {
			seen[name] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

// ScanExportedSentinels reads every non-test .go file directly in pkgDir
// (non-recursive) and returns the deduplicated, sorted list of package-level
// variable names that start with "Err".
func ScanExportedSentinels(pkgDir string) ([]string, error) {
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{})
	fset := token.NewFileSet()

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		path := filepath.Join(pkgDir, name)
		f, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return nil, parseErr
		}

		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, ident := range vs.Names {
					if strings.HasPrefix(ident.Name, "Err") {
						seen[ident.Name] = struct{}{}
					}
				}
			}
		}
	}
	return sortedKeys(seen), nil
}

// isFmtErrorf reports whether expr is the fmt.Errorf selector expression.
func isFmtErrorf(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "fmt" && sel.Sel.Name == "Errorf"
}

// stringLitStartsWithPercW reports whether the Go string literal src
// (including surrounding " or ` delimiters) begins with the verb "%w" after
// stripping the delimiters.
func stringLitStartsWithPercW(src string) bool {
	if len(src) < 4 { // minimum: "%w" inside delimiters = 4 chars
		return false
	}
	inner := src[1 : len(src)-1]
	return strings.HasPrefix(inner, "%w")
}

// sentinelName extracts the terminal identifier name from an AST expression
// used as a sentinel in a fmt.Errorf call. Returns "" for non-sentinel patterns.
//
//   - *ast.Ident → ident.Name if it starts with "Err" (bare sentinel, e.g. ErrFoo)
//   - *ast.SelectorExpr → sel.Sel.Name if it starts with "Err" (e.g. pkg.ErrFoo → "ErrFoo")
func sentinelName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		if strings.HasPrefix(e.Name, "Err") {
			return e.Name
		}
	case *ast.SelectorExpr:
		if strings.HasPrefix(e.Sel.Name, "Err") {
			return e.Sel.Name
		}
	}
	return ""
}

// sortedKeys returns the keys of m as a sorted slice.
func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
