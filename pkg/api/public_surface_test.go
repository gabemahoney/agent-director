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
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestPublicSurface(t *testing.T) {
	t.Parallel()
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

// TestNoMigrationTriggerMethod is the SR-1.6 public-surface guard for pkg/api:
// no exported function or method may expose an agent-reachable schema-migration
// trigger. The store upgrades only under an out-of-band administrator sentinel
// (internal/store/migrate_auth.go); pkg/api deliberately exposes NO Migrate*
// method — ErrSchemaMigrationRequired is a fatal open-time error, not a verb.
//
// This walks the pkg/api AST directly (stdlib go/parser), so it is not a golden
// check and cannot be silenced by regenerating any manifest artifact: adding a
// (c *Client) Migrate(...) method would trip it at the source level.
func TestNoMigrationTriggerMethod(t *testing.T) {
	t.Parallel()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	pkgDir := filepath.Dir(thisFile)

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, pkgDir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parser.ParseDir(%s): %v", pkgDir, err)
	}
	apiPkg, ok := pkgs["api"]
	if !ok {
		t.Fatalf("package 'api' not found under %s", pkgDir)
	}

	for _, f := range apiPkg.Files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !ast.IsExported(fn.Name.Name) {
				continue
			}
			if strings.Contains(strings.ToLower(fn.Name.Name), "migrate") {
				label := fn.Name.Name
				if fn.Recv != nil && len(fn.Recv.List) > 0 {
					label = fmt.Sprintf("(%s).%s", surfaceExprStr(fn.Recv.List[0].Type), fn.Name.Name)
				}
				t.Errorf("pkg/api exposes exported func/method %s whose name implies a migration trigger; "+
					"SR-1.6 forbids any agent-reachable migration entry point (migration is admin-sentinel-gated only)", label)
			}
		}
	}
}

// aliasTargets are the types pkg/api re-exports by alias (type X = pkg.Y), by
// alias name; TestNoRemovedDisplayNames fails until a new alias joins them.
var aliasTargets = map[string]reflect.Type{
	"Spawn": reflect.TypeFor[api.Spawn](), "PermissionRow": reflect.TypeFor[api.PermissionRow](),
	"SubagentRow": reflect.TypeFor[api.SubagentRow](), "ListFilters": reflect.TypeFor[api.ListFilters](),
	"LiveSpawnIdentity": reflect.TypeFor[api.LiveSpawnIdentity](), "ExpireCandidate": reflect.TypeFor[api.ExpireCandidate](),
	"RowSnapshot": reflect.TypeFor[api.RowSnapshot](), "LaunchIdentity": reflect.TypeFor[api.LaunchIdentity](),
	"LaunchOwner": reflect.TypeFor[api.LaunchOwner](), "ProcessIdentity": reflect.TypeFor[api.ProcessIdentity](),
	"PaneIntent": reflect.TypeFor[api.PaneIntent](), "PaneCheck": reflect.TypeFor[api.PaneCheck](),
	"ResumePrior": reflect.TypeFor[api.ResumePrior](), "CondResult": reflect.TypeFor[api.CondResult](),
	"ProvisionalTranscript": reflect.TypeFor[api.ProvisionalTranscript](), "SessionHistoryEntry": reflect.TypeFor[api.SessionHistoryEntry](),
	"InputDeliveryBackend": reflect.TypeFor[api.InputDeliveryBackend](), "InputDeliverySession": reflect.TypeFor[api.InputDeliverySession](),
	"InputDeliveryIdentity": reflect.TypeFor[api.InputDeliveryIdentity](), "InputDeliveryAttempt": reflect.TypeFor[api.InputDeliveryAttempt](),
	"InputDeliveryWatermark": reflect.TypeFor[api.InputDeliveryWatermark](), "InputDeliveryProof": reflect.TypeFor[api.InputDeliveryProof](),
	"TmuxLookupAnswer": reflect.TypeFor[api.TmuxLookupAnswer](), "TmuxSession": reflect.TypeFor[api.TmuxSession](),
	"TmuxPane": reflect.TypeFor[api.TmuxPane](), "TmuxLabel": reflect.TypeFor[api.TmuxLabel](),
	"TmuxCreateReply": reflect.TypeFor[api.TmuxCreateReply](), "TmuxCall": reflect.TypeFor[api.TmuxCall](),
	"TmuxFailure": reflect.TypeFor[api.TmuxFailure](), "TmuxCallError": reflect.TypeFor[api.TmuxCallError](),
	"ProcChecker": reflect.TypeFor[api.ProcChecker](), "SpawnParams": reflect.TypeFor[api.SpawnParams](),
	"Permissions": reflect.TypeFor[api.Permissions](),
}

// TestNoRemovedDisplayNames is SR-1.3's guard (b.ob8): nothing pkg/api
// exports carries the removed naming, whether declared in pkg/api (names,
// struct fields and their JSON tags, interface methods, string constants) or
// reachable through a type it re-exports by alias (the target's exported
// fields, tags, methods and their types). The only state constants it exports
// are the two Displayed States.
func TestNoRemovedDisplayNames(t *testing.T) {
	t.Parallel()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	pkgDir := filepath.Dir(thisFile)
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, pkgDir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parser.ParseDir(%s): %v", pkgDir, err)
	}
	check := func(where, name string) {
		if apitest.RemovedDisplayNames.MatchString(name) {
			t.Errorf("%s exports removed naming: %s", where, name)
		}
	}
	var stateConsts []string
	for file, f := range pkgs["api"].Files {
		where := filepath.Base(file)
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.FuncDecl:
				if n.Name.IsExported() {
					check(where, n.Name.Name)
				}
			case *ast.TypeSpec:
				if !n.Name.IsExported() {
					break
				}
				check(where, n.Name.Name)
				if _, ok := n.Type.(*ast.SelectorExpr); ok && n.Assign.IsValid() && aliasTargets[n.Name.Name] == nil {
					t.Errorf("%s re-exports %s by alias; add it to aliasTargets", where, n.Name.Name)
				}
			case *ast.Field:
				for _, id := range n.Names {
					if id.IsExported() {
						check(where, id.Name)
					}
				}
				if n.Tag != nil {
					check(where, n.Tag.Value)
				}
			case *ast.GenDecl:
				for _, spec := range n.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, id := range vs.Names {
						if !id.IsExported() {
							continue
						}
						check(where, id.Name)
						var value ast.Expr
						if i < len(vs.Values) {
							value = vs.Values[i]
						}
						if lit, ok := value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
							check(where, lit.Value)
						}
						if sel, ok := value.(*ast.SelectorExpr); n.Tok == token.CONST && (strings.HasPrefix(id.Name, "State") || ok && strings.HasPrefix(sel.Sel.Name, "State")) {
							stateConsts = append(stateConsts, id.Name)
						}
					}
				}
			}
			return true
		})
	}
	sort.Strings(stateConsts)
	if want := []string{"StateIdleScreen", "StateNeedsIntelligence"}; !reflect.DeepEqual(stateConsts, want) {
		t.Errorf("pkg/api exports state constants %v; want exactly %v", stateConsts, want)
	}
	seen := map[reflect.Type]bool{}
	for _, target := range aliasTargets {
		reachableNames(target, seen, check)
	}
}

// reachableNames calls check with the name of t and of every exported method,
// field and field tag reachable from it through this module's types.
func reachableNames(t reflect.Type, seen map[reflect.Type]bool, check func(where, name string)) {
	if seen[t] || t.PkgPath() != "" && !strings.HasPrefix(t.PkgPath(), "github.com/gabemahoney/agent-director/") {
		return
	}
	seen[t] = true
	check(t.String(), t.Name())
	if t.Name() != "" && t.Kind() != reflect.Interface {
		reachableNames(reflect.PointerTo(t), seen, check) // its pointer's method set
	}
	for i := 0; i < t.NumMethod(); i++ {
		if m := t.Method(i); m.IsExported() {
			check(t.String(), m.Name)
			reachableNames(m.Type, seen, check)
		}
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
		reachableNames(t.Elem(), seen, check)
	case reflect.Map:
		reachableNames(t.Key(), seen, check)
		reachableNames(t.Elem(), seen, check)
	case reflect.Func:
		for i := 0; i < t.NumIn(); i++ {
			reachableNames(t.In(i), seen, check)
		}
		for i := 0; i < t.NumOut(); i++ {
			reachableNames(t.Out(i), seen, check)
		}
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			if f := t.Field(i); f.IsExported() {
				check(t.String(), f.Name+" "+string(f.Tag))
				reachableNames(f.Type, seen, check)
			}
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
