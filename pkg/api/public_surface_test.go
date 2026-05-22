package api_test

// TestPublicSurface enforces the public-surface invariant for pkg/api:
// no exported function, method, variable, or const declaration may have a
// signature that references an internal/* import path.
//
// Type alias declarations (type Foo = other.Bar) are explicitly whitelisted
// — the engineer's k7 phase introduced aliases for Spawn, PermissionRow, and
// ListFilters so that external consumers can name those types without importing
// internal/store themselves.
//
// Implementation: parse the non-test Go source files in pkg/api using go/ast
// and go/parser (stdlib only, no external tools). For each file build an
// alias→importPath map; then walk exported declarations and check whether any
// type expression in a signature resolves to an internal/* package.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPublicSurface(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	pkgDir := filepath.Dir(thisFile)

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, pkgDir, func(fi os.FileInfo) bool {
		name := fi.Name()
		// Skip test files; they may legitimately import internal/store for
		// fixture construction and should not be checked here.
		return !strings.HasSuffix(name, "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parser.ParseDir(%s): %v", pkgDir, err)
	}

	apiPkg, ok := pkgs["api"]
	if !ok {
		t.Fatalf("package 'api' not found under %s", pkgDir)
	}

	for _, f := range apiPkg.Files {
		imp := buildSurfaceImportMap(f)
		for _, decl := range f.Decls {
			checkSurfaceDecl(t, decl, imp)
		}
	}
}

// buildSurfaceImportMap returns a map from import alias to full import path
// for every import in f. Blank (_) and dot (.) imports are skipped.
func buildSurfaceImportMap(f *ast.File) map[string]string {
	m := make(map[string]string)
	for _, spec := range f.Imports {
		path := strings.Trim(spec.Path.Value, `"`)
		var alias string
		if spec.Name != nil {
			switch spec.Name.Name {
			case "_", ".":
				continue
			default:
				alias = spec.Name.Name
			}
		} else {
			parts := strings.Split(path, "/")
			alias = parts[len(parts)-1]
		}
		m[alias] = path
	}
	return m
}

// checkSurfaceDecl inspects a top-level declaration and calls t.Errorf for
// every internal/* path found in an exported signature.
func checkSurfaceDecl(t *testing.T, decl ast.Decl, imp map[string]string) {
	t.Helper()
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if !ast.IsExported(d.Name.Name) {
			return
		}
		label := d.Name.Name
		if d.Recv != nil && len(d.Recv.List) > 0 {
			label = fmt.Sprintf("(%s).%s", surfaceExprStr(d.Recv.List[0].Type), d.Name.Name)
		}
		checkSurfaceFuncType(t, label, d.Type, imp)

	case *ast.GenDecl:
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				if !ast.IsExported(s.Name.Name) {
					continue
				}
				// Type alias declarations are explicitly whitelisted:
				//   type Spawn = store.Spawn
				// The alias itself is the approved bridge; checking it
				// would produce a false positive.
				if s.Assign.IsValid() {
					continue
				}
				// For interface types, check each exported method's signature.
				if iface, ok := s.Type.(*ast.InterfaceType); ok {
					for _, method := range iface.Methods.List {
						ft, ok := method.Type.(*ast.FuncType)
						if !ok {
							continue // embedded interface — no FuncType
						}
						mname := "<method>"
						if len(method.Names) > 0 {
							mname = method.Names[0].Name
						}
						checkSurfaceFuncType(t,
							fmt.Sprintf("%s.%s", s.Name.Name, mname),
							ft, imp)
					}
				}

			case *ast.ValueSpec:
				for _, name := range s.Names {
					if !ast.IsExported(name.Name) {
						continue
					}
					if s.Type == nil {
						continue // type inferred from value; untyped consts OK
					}
					if path := internalInSurfaceExpr(s.Type, imp); path != "" {
						t.Errorf("exported var/const %s: type references internal path %s",
							name.Name, path)
					}
				}
			}
		}
	}
}

// checkSurfaceFuncType reports violations for parameters and results of ft.
func checkSurfaceFuncType(t *testing.T, label string, ft *ast.FuncType, imp map[string]string) {
	t.Helper()
	for _, group := range []*ast.FieldList{ft.Params, ft.Results} {
		if group == nil {
			continue
		}
		for _, field := range group.List {
			if path := internalInSurfaceExpr(field.Type, imp); path != "" {
				t.Errorf("exported func/method %s: signature references internal path %q",
					label, path)
			}
		}
	}
}

// internalInSurfaceExpr recursively walks an AST type expression and returns
// the first internal/* import path it finds via a SelectorExpr whose package
// qualifier is a known import alias, or "" if none.
func internalInSurfaceExpr(expr ast.Expr, imp map[string]string) string {
	if expr == nil {
		return ""
	}
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		if x, ok := e.X.(*ast.Ident); ok {
			if path, found := imp[x.Name]; found && isInternalImportPath(path) {
				return path
			}
		}
	case *ast.StarExpr:
		return internalInSurfaceExpr(e.X, imp)
	case *ast.ArrayType:
		if p := internalInSurfaceExpr(e.Len, imp); p != "" {
			return p
		}
		return internalInSurfaceExpr(e.Elt, imp)
	case *ast.MapType:
		if p := internalInSurfaceExpr(e.Key, imp); p != "" {
			return p
		}
		return internalInSurfaceExpr(e.Value, imp)
	case *ast.ChanType:
		return internalInSurfaceExpr(e.Value, imp)
	case *ast.Ellipsis:
		return internalInSurfaceExpr(e.Elt, imp)
	case *ast.FuncType:
		for _, group := range []*ast.FieldList{e.Params, e.Results} {
			if group == nil {
				continue
			}
			for _, field := range group.List {
				if p := internalInSurfaceExpr(field.Type, imp); p != "" {
					return p
				}
			}
		}
	}
	return ""
}

// isInternalImportPath reports whether path is an internal/* package.
func isInternalImportPath(path string) bool {
	return strings.Contains(path, "/internal/") || strings.HasPrefix(path, "internal/")
}

// surfaceExprStr returns a short human-readable string for a receiver type
// expression, used only for error messages.
func surfaceExprStr(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + surfaceExprStr(t.X)
	}
	return "?"
}
