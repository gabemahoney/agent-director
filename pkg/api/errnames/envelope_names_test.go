package errnames_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// envelopeNameExclusions maps each err_name that may reach an envelope without
// a Catalog entry to the one package directory allowed to write it: MCP's
// ErrUnknownTool is no CLI name (docs/architecture.md "Catalog source").
var envelopeNameExclusions = map[string]string{"ErrUnknownTool": "internal/mcp"}

// extraNameCarriers maps struct types beyond those with a json:"err_name"
// field to the field a binary writes as an envelope's err_name: setupClient,
// runOnClient and serveHandlerWith write a clisetup.OpenError's Name.
var extraNameCarriers = map[string]string{"OpenError": "Name"}

// TestEnvelopeErrNamesCatalogued fails when cmd/ or internal/ hands an error
// envelope an err_name literal or constant that errnames.Catalog lacks (b.zvb).
func TestEnvelopeErrNamesCatalogued(t *testing.T) {
	sites := scanEnvelopeNames(t, filepath.Join("..", "..", ".."), "cmd", "internal")
	for _, s := range uncataloguedSites(sites) {
		t.Errorf("%s: err_name %q reaches an error envelope but is not in errnames.Catalog; "+
			"add a Catalog entry (docs/architecture.md \"Catalog source\")", s.pos, s.name)
	}

	// Names each package writes by a different resolution path, so a scan
	// that stops seeing a writer fails here rather than passing on nothing.
	floor := map[string][]string{
		"cmd/agent-director":       {"ErrInvalidFlags", "ErrJSONMarshal", "ErrStoreOpen", "ErrTrailWrite", "ErrUnknownVerb"},
		"cmd/agent-director-admin": {"ErrInvalidFlags", "ErrJSONMarshal", "ErrStoreOpen", "ErrUnknownVerb"},
		"internal/clisetup":        {"ErrConfigMalformed", "ErrSchemaMigrationRequired", "ErrSchemaMismatch", "ErrStoreOpen"},
		"internal/mcp":             {"ErrUnknownTool"},
	}
	found := make(map[[2]string]bool, len(sites))
	for _, s := range sites {
		found[[2]string{s.dir, s.name}] = true
	}
	for dir, names := range floor {
		for _, n := range names {
			if !found[[2]string{dir, n}] {
				t.Errorf("scan found no envelope write of %s in %s: the scan no longer sees that package's writers, "+
					"or the package stopped writing it (then update this floor)", n, dir)
			}
		}
	}
}

// TestEnvelopeErrNamesCatalogued_NegativeControl runs the same scan and check
// over testdata/envelope, which hands its writers and envelope literals every
// resolvable kind of name.
func TestEnvelopeErrNamesCatalogued_NegativeControl(t *testing.T) {
	sites := scanEnvelopeNames(t, "testdata", "envelope")

	bad := []string{"ErrFixtureAnonymous", "ErrFixtureConst", "ErrFixtureKeyed", "ErrFixtureLiteral",
		"ErrFixtureLocal", "ErrFixtureMap", "ErrFixturePositional", "ErrFixtureReturned", "ErrFixtureSingle",
		"ErrUnknownTool"}
	if got := siteNames(uncataloguedSites(sites)); !stringSliceEqual(got, bad) {
		t.Errorf("uncatalogued names = %v, want %v", got, bad)
	}
	if got, want := siteNames(sites), append([]string{"ErrInvalidFlags", "ErrUnknownVerb"}, bad...); !stringSliceEqual(got, want) {
		t.Errorf("all names = %v, want %v", got, want)
	}
}

// envelopeSite is one err_name that reaches an error envelope.
type envelopeSite struct {
	pos  string // file:line, relative to the scan base
	dir  string // package directory, relative to the scan base
	name string
}

// uncataloguedSites returns the sites whose name is neither in
// errnames.Catalog nor excluded for the site's package.
func uncataloguedSites(sites []envelopeSite) []envelopeSite {
	catalogued := make(map[string]bool, len(errnames.Catalog))
	for _, e := range errnames.Catalog {
		catalogued[e.Name] = true
	}
	var out []envelopeSite
	for _, s := range sites {
		if !catalogued[s.name] && envelopeNameExclusions[s.name] != s.dir {
			out = append(out, s)
		}
	}
	return out
}

// siteNames returns the sites' distinct names, sorted.
func siteNames(sites []envelopeSite) []string {
	seen := make(map[string]bool)
	var out []string
	for _, s := range sites {
		if !seen[s.name] {
			seen[s.name] = true
			out = append(out, s.name)
		}
	}
	sort.Strings(out)
	return out
}

// scanEnvelopeNames parses the non-test Go files of every package under each
// base/dir (testdata skipped) and returns each err_name that reaches an error
// envelope as a string it can resolve.
//
// An envelope is a literal of a struct type, named or anonymous, with a
// json:"err_name" field or one in extraNameCarriers, or a map literal with an
// "err_name" key. A writer is a package function that puts a parameter in an
// envelope's name field or passes it to another writer's name parameter,
// found to a fixpoint (writeError, writeApiErrorAndDispatch). The names are
// the envelope name fields and writer name arguments that resolve to a string:
// a literal, a package constant or variable, a local variable assigned one, or
// a same-package function's result.
//
// Dynamic names are out of scope: errnames.Classify's (always a Catalog name),
// oe.Name (checked where clisetup sets it), and any other call, field or
// parameter; so are other packages' constants, method writers, struct
// literals with elided types, and name fields or map keys set by assignment
// rather than in a literal.
func scanEnvelopeNames(t *testing.T, base string, dirs ...string) []envelopeSite {
	t.Helper()
	fset := token.NewFileSet()
	var pkgs []*envelopePkg
	for _, d := range dirs {
		root := filepath.Join(base, d)
		err := filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
			if err != nil || !e.IsDir() {
				return err
			}
			if e.Name() == "testdata" && path != root {
				return filepath.SkipDir
			}
			p, err := parseEnvelopePkg(fset, base, path)
			if p != nil {
				pkgs = append(pkgs, p)
			}
			return err
		})
		if err != nil {
			t.Fatalf("scan %s: %v", root, err)
		}
	}
	if len(pkgs) == 0 {
		t.Fatalf("scan of %v under %s found no Go packages", dirs, base)
	}

	carriers := make(map[string]carrier)
	for _, p := range pkgs {
		p.addCarriers(carriers)
	}
	var sites []envelopeSite
	for _, p := range pkgs {
		writers := p.writers(carriers)
		for _, f := range p.files {
			for _, decl := range f.Decls {
				eachNameExpr(decl, carriers, writers, func(e ast.Expr) {
					pos := fset.Position(e.Pos())
					rel, _ := filepath.Rel(base, pos.Filename)
					for _, name := range p.resolve(e, decl, make(map[any]bool)) {
						sites = append(sites, envelopeSite{
							pos: fmt.Sprintf("%s:%d", filepath.ToSlash(rel), pos.Line), dir: p.dir, name: name})
					}
				})
			}
		}
	}
	sort.Slice(sites, func(i, j int) bool { return sites[i].pos < sites[j].pos })
	return sites
}

// envelopePkg is one parsed package directory.
type envelopePkg struct {
	dir    string                   // relative to the scan base, slash-separated
	files  []*ast.File              // its non-test files
	funcs  map[string]*ast.FuncDecl // its functions without receivers
	values map[string]ast.Expr      // its package-level const and var initialisers
}

// parseEnvelopePkg parses dir's non-test Go files; it returns nil when there
// are none.
func parseEnvelopePkg(fset *token.FileSet, base, dir string) (*envelopePkg, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(base, dir)
	if err != nil {
		return nil, err
	}
	p := &envelopePkg{dir: filepath.ToSlash(rel), funcs: map[string]*ast.FuncDecl{}, values: map[string]ast.Expr{}}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, 0)
		if err != nil {
			return nil, err
		}
		p.files = append(p.files, f)
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					p.funcs[d.Name.Name] = d
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok && len(vs.Values) == len(vs.Names) {
						for i, id := range vs.Names {
							p.values[id.Name] = vs.Values[i]
						}
					}
				}
			}
		}
	}
	if len(p.files) == 0 {
		return nil, nil
	}
	return p, nil
}

// carrier is an envelope struct's err_name field: by name for keyed
// literals, by position for positional ones.
type carrier struct {
	field string
	index int
}

// addCarriers records the package's named envelope struct types, by type name.
func (p *envelopePkg) addCarriers(into map[string]carrier) {
	for _, f := range p.files {
		ast.Inspect(f, func(n ast.Node) bool {
			if ts, ok := n.(*ast.TypeSpec); ok {
				if st, ok := ts.Type.(*ast.StructType); ok {
					if c, ok := structCarrier(ts.Name.Name, st); ok {
						into[ts.Name.Name] = c
					}
				}
			}
			return true
		})
	}
}

// structCarrier returns the err_name field of struct type st, named typ (""
// when anonymous).
func structCarrier(typ string, st *ast.StructType) (carrier, bool) {
	i := 0
	for _, fld := range st.Fields.List {
		if len(fld.Names) == 0 {
			i++ // embedded
		}
		for _, id := range fld.Names {
			if isErrNameField(typ, id.Name, fld.Tag) {
				return carrier{field: id.Name, index: i}, true
			}
			i++
		}
	}
	return carrier{}, false
}

// isErrNameField reports whether field of struct type typ holds an envelope's
// err_name.
func isErrNameField(typ, field string, tag *ast.BasicLit) bool {
	if extraNameCarriers[typ] == field {
		return true
	}
	if tag == nil {
		return false
	}
	s, err := strconv.Unquote(tag.Value)
	if err != nil {
		return false
	}
	name, _, _ := strings.Cut(reflect.StructTag(s).Get("json"), ",")
	return name == "err_name"
}

// writers returns the package's writer functions, each with the indexes of
// its name parameters.
func (p *envelopePkg) writers(carriers map[string]carrier) map[string]map[int]bool {
	writers := make(map[string]map[int]bool)
	for changed := true; changed; {
		changed = false
		for name, fd := range p.funcs {
			params := make(map[*ast.Object]int)
			i := 0
			for _, fld := range fd.Type.Params.List {
				for _, id := range fld.Names {
					params[id.Obj] = i
					i++
				}
				if len(fld.Names) == 0 {
					i++
				}
			}
			eachNameExpr(fd, carriers, writers, func(e ast.Expr) {
				id, ok := e.(*ast.Ident)
				if !ok || id.Obj == nil {
					return
				}
				if idx, ok := params[id.Obj]; ok && !writers[name][idx] {
					if writers[name] == nil {
						writers[name] = make(map[int]bool)
					}
					writers[name][idx] = true
					changed = true
				}
			})
		}
	}
	return writers
}

// eachNameExpr calls fn with each expression root puts in an envelope's name
// field or passes as a writer's name argument.
func eachNameExpr(root ast.Node, carriers map[string]carrier, writers map[string]map[int]bool, fn func(ast.Expr)) {
	ast.Inspect(root, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CompositeLit:
			c, isCarrier := carriers[typeName(n.Type)]
			if st, ok := n.Type.(*ast.StructType); ok {
				c, isCarrier = structCarrier("", st)
			}
			for i, el := range n.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					if isCarrier && i == c.index {
						fn(el)
					}
					continue
				}
				switch k := kv.Key.(type) {
				case *ast.Ident: // a struct field
					if isCarrier && k.Name == c.field {
						fn(kv.Value)
					}
				case *ast.BasicLit: // a map key
					if s, err := strconv.Unquote(k.Value); err == nil && s == "err_name" {
						fn(kv.Value)
					}
				}
			}
		case *ast.CallExpr:
			id, ok := n.Fun.(*ast.Ident)
			if !ok || (id.Obj != nil && id.Obj.Kind != ast.Fun) {
				return true
			}
			for i := range writers[id.Name] {
				if i < len(n.Args) {
					fn(n.Args[i])
				}
			}
		}
		return true
	})
}

// typeName returns a composite literal type's terminal name, "" for any other
// type expression.
func typeName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}

// resolve returns the strings e can hold within scope, the declaration it
// appears in; anything it cannot follow is dynamic and yields none.
func (p *envelopePkg) resolve(e ast.Expr, scope ast.Node, seen map[any]bool) []string {
	switch e := e.(type) {
	case *ast.BasicLit:
		if s, err := strconv.Unquote(e.Value); err == nil && e.Kind == token.STRING {
			return []string{s}
		}
	case *ast.ParenExpr:
		return p.resolve(e.X, scope, seen)
	case *ast.CallExpr:
		return p.results(e, 0, seen)
	case *ast.Ident:
		if e.Obj == nil { // declared in another file of the package, or predeclared
			if v, ok := p.values[e.Name]; ok && !seen[e.Name] {
				seen[e.Name] = true
				return p.resolve(v, nil, seen)
			}
			return nil
		}
		if seen[e.Obj] {
			return nil
		}
		seen[e.Obj] = true
		var out []string
		if vs, ok := e.Obj.Decl.(*ast.ValueSpec); ok {
			for i, id := range vs.Names {
				if id.Obj == e.Obj {
					out = append(out, p.assignedValue(len(vs.Names), vs.Values, i, scope, seen)...)
				}
			}
		}
		if scope != nil {
			ast.Inspect(scope, func(n ast.Node) bool {
				if as, ok := n.(*ast.AssignStmt); ok {
					for i, lhs := range as.Lhs {
						if id, ok := lhs.(*ast.Ident); ok && id.Obj == e.Obj {
							out = append(out, p.assignedValue(len(as.Lhs), as.Rhs, i, scope, seen)...)
						}
					}
				}
				return true
			})
		}
		return out
	}
	return nil
}

// assignedValue resolves the value the i-th of n left-hand names gets from
// rhs: rhs[i], or result i of a single multi-value call.
func (p *envelopePkg) assignedValue(n int, rhs []ast.Expr, i int, scope ast.Node, seen map[any]bool) []string {
	switch {
	case len(rhs) == n:
		return p.resolve(rhs[i], scope, seen)
	case len(rhs) == 1:
		if call, ok := rhs[0].(*ast.CallExpr); ok {
			return p.results(call, i, seen)
		}
	}
	return nil
}

// resultKey marks one function result as already being resolved.
type resultKey struct {
	fd *ast.FuncDecl
	i  int
}

// results returns the strings call's same-package callee returns as result i.
func (p *envelopePkg) results(call *ast.CallExpr, i int, seen map[any]bool) []string {
	id, ok := call.Fun.(*ast.Ident)
	if !ok || (id.Obj != nil && id.Obj.Kind != ast.Fun) {
		return nil
	}
	fd := p.funcs[id.Name]
	if fd == nil || fd.Body == nil || seen[resultKey{fd, i}] {
		return nil
	}
	seen[resultKey{fd, i}] = true
	n := fd.Type.Results.NumFields()
	var out []string
	ast.Inspect(fd.Body, func(node ast.Node) bool {
		switch r := node.(type) {
		case *ast.FuncLit:
			return false // its returns are its own
		case *ast.ReturnStmt:
			if len(r.Results) == n && i < n {
				out = append(out, p.resolve(r.Results[i], fd, seen)...)
			}
		}
		return true
	})
	return out
}
