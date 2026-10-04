package main_test

// cli_manifest_parity_test.go is the structural CLI/MCP parity guard (b.vqr,
// docs/engineering-guide.md "CLI/MCP parity"). It type-checks every non-test
// source of this package, whether or not the file imports flag, and fails
// when a verb's FlagSet registers a flag that is not one of the verb's
// manifest params, a manifest param has no flag, or a flag has no usage text.
//
// Every flag.FlagSet value must be a local variable defined from
// flag.NewFlagSet with a constant name naming its verb, and used only as a
// method receiver. Anything else whose flags the scan cannot attribute to a
// verb fails it: a FlagSet passed, stored, returned, reassigned, embedded or
// obtained from a helper or another package; a FlagSet method value; any
// FlagSet method called on another receiver; flag.CommandLine and the flag
// package's own registration functions; and an interface method with a
// FlagSet registration method's name and signature. A file this platform's
// build excludes is checked by syntax only: it must not import flag or call a
// method named and shaped like a registration.
//
// Out of its reach: arguments a verb parses by hand, without the flag package
// (reading os.Args or its argv slice itself).

import (
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// flagRegistrars maps each flag.FlagSet method that defines a flag to the
// argument positions of the flag's name and its usage text.
var flagRegistrars = map[string][2]int{
	"Bool": {0, 2}, "BoolVar": {1, 3}, "BoolFunc": {0, 1},
	"Duration": {0, 2}, "DurationVar": {1, 3},
	"Float64": {0, 2}, "Float64Var": {1, 3}, "Func": {0, 1},
	"Int": {0, 2}, "IntVar": {1, 3}, "Int64": {0, 2}, "Int64Var": {1, 3},
	"String": {0, 2}, "StringVar": {1, 3}, "TextVar": {1, 3},
	"Uint": {0, 2}, "UintVar": {1, 3}, "Uint64": {0, 2}, "Uint64Var": {1, 3},
	"Var": {1, 2},
}

// flagSetReaders are the flag.FlagSet methods that define no flag.
var flagSetReaders = map[string]bool{
	"Arg": true, "Args": true, "ErrorHandling": true, "Init": true, "Lookup": true, "NArg": true,
	"NFlag": true, "Name": true, "Output": true, "Parse": true, "Parsed": true,
	"PrintDefaults": true, "Set": true, "SetOutput": true, "Visit": true, "VisitAll": true,
}

// nonFlagParams are the manifest params a CLI verb takes other than as a flag.
var nonFlagParams = map[string][]string{
	"spawn":      {"claude_args"}, // the argv after --
	"trail-emit": {"sub_verb"},    // the first positional argument
	"hook":       {"stdin"},       // the hook payload on stdin
}

// cliFlag is one flag registration found in the CLI's sources.
type cliFlag struct{ name, usage, at string }

// cliScan is what the source scan found: each verb's flags, keyed by the verb
// its FlagSet is named for, the verbs that have a FlagSet, and the keys of the
// dispatch table handlers returns.
type cliScan struct {
	flags      map[string][]cliFlag
	sets       map[string]bool
	dispatched []string
}

// TestCLIFlagsAreManifestParams: every main-CLI verb registers exactly its
// manifest params as flags (dashed), each with usage text, and the CLI and
// the manifest have the same verbs (b.vqr).
func TestCLIFlagsAreManifestParams(t *testing.T) {
	s := scanCLISources(t)
	if !slices.ContainsFunc(s.flags["kill"], func(f cliFlag) bool { return f.name == "claude-instance-id" }) {
		t.Fatalf("the scan found no kill FlagSet registering claude-instance-id (found %v); the checks below would pass vacuously", s.flags)
	}

	cliVerbs := map[string]bool{"hook": true} // run() handles hook before dispatch
	for _, v := range s.dispatched {
		if !strings.HasPrefix(v, "-") { // --help is help's alias
			cliVerbs[v] = true
		}
	}
	if !cliVerbs["kill"] || !cliVerbs["help"] {
		t.Fatalf("the dispatch table scan found %v; want the CLI's verbs", s.dispatched)
	}
	for _, verb := range sortedKeys(s.sets) {
		if !cliVerbs[verb] {
			t.Errorf("a FlagSet is named %q, which is no CLI verb; its flags cannot be checked", verb)
		}
	}

	inManifest := map[string]bool{}
	for _, v := range manifest.Verbs {
		inManifest[v.Name] = true
		if !cliVerbs[v.Name] {
			t.Errorf("manifest verb %q is not a main-CLI verb", v.Name)
			continue
		}
		checkVerbFlags(t, v, s.flags[v.Name])
	}
	for _, verb := range sortedKeys(cliVerbs) {
		if !inManifest[verb] {
			t.Errorf("main-CLI verb %q is not in the manifest, so help and MCP do not offer it", verb)
		}
	}
}

// TestMCPExposedVerbExceptions: the only manifest verbs MCP does not expose
// are the machine plumbing hook, serve and trail-emit (b.vqr).
func TestMCPExposedVerbExceptions(t *testing.T) {
	var hidden []string
	for _, v := range manifest.Verbs {
		if !mcp.ExposedVerb(v.Name) {
			hidden = append(hidden, v.Name)
		}
	}
	sort.Strings(hidden)
	if want := []string{"hook", "serve", "trail-emit"}; !slices.Equal(hidden, want) {
		t.Errorf("verbs MCP does not expose = %v; want exactly %v", hidden, want)
	}
}

// checkVerbFlags reports v's flags that are not its manifest params, its
// manifest params with no flag, and flags with empty usage text.
func checkVerbFlags(t *testing.T, v manifest.VerbDef, flags []cliFlag) {
	t.Helper()
	want := map[string]bool{}
	var names []string
	for _, p := range v.Params {
		names = append(names, p.Name)
		if !slices.Contains(nonFlagParams[v.Name], p.Name) {
			want[strings.ReplaceAll(p.Name, "_", "-")] = true
		}
	}
	for _, p := range nonFlagParams[v.Name] {
		if !slices.Contains(names, p) {
			t.Errorf("nonFlagParams lists %s param %s, which the manifest does not have", v.Name, p)
		}
	}
	got := map[string]bool{}
	for _, f := range flags {
		got[f.name] = true
		if !want[f.name] {
			t.Errorf("%s: %s registers flag --%s, which is not a %s manifest param (manifest params: %v)", f.at, v.Name, f.name, v.Name, names)
		}
		if strings.TrimSpace(f.usage) == "" {
			t.Errorf("%s: %s flag --%s has empty usage text", f.at, v.Name, f.name)
		}
	}
	for _, name := range sortedKeys(want) {
		if !got[name] {
			t.Errorf("%s manifest param %s has no CLI flag --%s", v.Name, strings.ReplaceAll(name, "-", "_"), name)
		}
	}
}

// scanCLISources type-checks the package's non-test sources for this platform
// and returns what cliScan describes, failing the test on every FlagSet use
// it cannot attribute to a verb.
func scanCLISources(t *testing.T) cliScan {
	t.Helper()
	bp, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("read the package: %v", err)
	}
	if len(bp.CgoFiles) > 0 {
		t.Fatalf("cgo files %v; the scan does not type-check them", bp.CgoFiles)
	}
	fset := token.NewFileSet()
	parse := func(name string) *ast.File {
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		return f
	}
	var files []*ast.File
	for _, name := range bp.GoFiles {
		files = append(files, parse(name))
	}
	for _, name := range bp.IgnoredGoFiles {
		if !strings.HasSuffix(name, "_test.go") {
			checkExcludedFile(t, fset, parse(name))
		}
	}

	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	imp := exportImporter(t, fset)
	if _, err := (&types.Config{Importer: imp}).Check("main", fset, files, info); err != nil {
		t.Fatalf("type-check the package: %v", err)
	}

	s := cliScan{flags: map[string][]cliFlag{}, sets: map[string]bool{}}
	for _, f := range files {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == "handlers" {
				s.dispatched = append(s.dispatched, mapLiteralKeys(info, fd)...)
			}
		}
	}
	var problems []problem
	s.scanTyped(fset, files, info, flagSetMethods(imp), func(n ast.Node, format string, args ...any) {
		problems = append(problems, problem{n.Pos(), fset.Position(n.Pos()).String() + ": " + fmt.Sprintf(format, args...)})
	})
	sort.SliceStable(problems, func(i, j int) bool { return problems[i].pos < problems[j].pos })
	for _, p := range problems {
		t.Error(p.msg)
	}
	return s
}

// problem is one FlagSet use the scan cannot attribute, at pos.
type problem struct {
	pos token.Pos
	msg string
}

// scanTyped records each tracked FlagSet's flag registrations under its verb
// and reports, through bad, every FlagSet use it cannot attribute to a verb.
// fsMethods are (*flag.FlagSet)'s methods by name, for the interface check.
func (s *cliScan) scanTyped(fset *token.FileSet, files []*ast.File, info *types.Info, fsMethods map[string]*types.Func,
	bad func(n ast.Node, format string, args ...any)) {
	// The tracked FlagSets: local variables defined from flag.NewFlagSet.
	verbOf := map[*types.Var]string{}
	bound := map[*ast.CallExpr]bool{}
	define := func(id *ast.Ident, rhs ast.Expr) {
		call, ok := ast.Unparen(rhs).(*ast.CallExpr)
		if !ok || flagFunc(info, call.Fun) != "NewFlagSet" {
			return
		}
		v, _ := info.Defs[id].(*types.Var)
		if v == nil || v.Parent() == v.Pkg().Scope() {
			return // reported below as an unattributed flag.NewFlagSet
		}
		bound[call] = true
		name, ok := constString(info, call.Args[0])
		if !ok || len(strings.Fields(name)) == 0 {
			bad(call, "a FlagSet's name is not a constant naming a verb")
			return
		}
		verb := strings.Fields(name)[0]
		verbOf[v], s.sets[verb] = verb, true
	}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.AssignStmt:
				if n.Tok == token.DEFINE && len(n.Lhs) == len(n.Rhs) {
					for i, l := range n.Lhs {
						if id, ok := l.(*ast.Ident); ok {
							define(id, n.Rhs[i])
						}
					}
				}
			case *ast.ValueSpec:
				if len(n.Names) == len(n.Values) {
					for i, id := range n.Names {
						define(id, n.Values[i])
					}
				}
			}
			return true
		})
	}
	tracked := func(e ast.Expr) string {
		if id, ok := e.(*ast.Ident); ok {
			if v, ok := info.Uses[id].(*types.Var); ok {
				return verbOf[v]
			}
		}
		return ""
	}

	receivers := map[ast.Expr]bool{}   // the X of every selector
	methodRecvs := map[ast.Expr]bool{} // the X of every FlagSet method selector, which flagSetCall checks
	for _, f := range files {
		var stack []ast.Node
		ast.Inspect(f, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			var parent ast.Node
			if len(stack) > 0 {
				parent = stack[len(stack)-1]
			}
			stack = append(stack, n)
			switch n := n.(type) {
			case *ast.CallExpr:
				name := flagFunc(info, n.Fun)
				if _, reg := flagRegistrars[name]; reg {
					bad(n, "flag.%s registers on flag.CommandLine, which no verb owns", name)
				}
				if name == "NewFlagSet" && !bound[n] {
					bad(n, "a flag.NewFlagSet is not a new local variable's value; its flags cannot be attributed to a verb")
				}
			case *ast.SelectorExpr:
				receivers[n.X] = true
				if v, ok := info.Uses[n.Sel].(*types.Var); ok && v.Pkg() != nil && v.Pkg().Path() == "flag" && v.Name() == "CommandLine" {
					bad(n, "names flag.CommandLine, which no verb owns")
				}
				sel := info.Selections[n]
				if sel == nil {
					return true
				}
				m, ok := sel.Obj().(*types.Func)
				if !ok {
					return true
				}
				call, _ := parent.(*ast.CallExpr)
				switch {
				case isFlagSetMethod(m):
					methodRecvs[n.X] = true
					s.flagSetCall(info, n, sel, call, tracked(n.X), fset.Position(n.Pos()).String(), bad)
				case sel.Kind() == types.MethodVal && types.IsInterface(sel.Recv()):
					_, reg := flagRegistrars[m.Name()]
					if fm := fsMethods[m.Name()]; reg && fm != nil && types.Identical(m.Type(), fm.Type()) {
						bad(n, "calls %s, an interface method with FlagSet.%s's signature; a flag it registers cannot be attributed to a verb", types.ExprString(n), m.Name())
					}
				}
			}
			return true
		})
	}

	for e, tv := range info.Types {
		if tv.IsType() || !isFlagSet(tv.Type) {
			continue
		}
		if call, ok := e.(*ast.CallExpr); ok && flagFunc(info, call.Fun) == "NewFlagSet" {
			continue // reported above when not bound
		}
		if methodRecvs[e] || (receivers[e] && tracked(e) != "") {
			continue
		}
		bad(e, "the FlagSet %s is passed, stored, returned or obtained other than as a local variable defined from flag.NewFlagSet and used as a receiver; its flags cannot be attributed to a verb", types.ExprString(e))
	}
}

// flagSetCall records the flag a selected flag.FlagSet method registers, if
// it registers one, at position at. The method must be called (call is the
// selector's parent call) directly on a tracked FlagSet variable, whose verb
// is verb.
func (s *cliScan) flagSetCall(info *types.Info, n *ast.SelectorExpr, sel *types.Selection, call *ast.CallExpr, verb, at string,
	bad func(n ast.Node, format string, args ...any)) {
	m := n.Sel.Name
	if sel.Kind() != types.MethodVal || call == nil || call.Fun != n {
		bad(n, "FlagSet.%s is used as a method value or expression; a flag it registers cannot be attributed to a verb", m)
		return
	}
	if verb == "" || len(sel.Index()) != 1 {
		bad(n, "FlagSet.%s is called on %s, which is not a local FlagSet variable defined from flag.NewFlagSet; its flags cannot be attributed to a verb", m, types.ExprString(n.X))
		return
	}
	pos, ok := flagRegistrars[m]
	if !ok {
		if !flagSetReaders[m] {
			bad(n, "%s FlagSet method %s is unknown to the scan; add it to flagRegistrars or flagSetReaders", verb, m)
		}
		return
	}
	name, okName := constString(info, call.Args[pos[0]])
	usage, okUsage := constString(info, call.Args[pos[1]])
	if !okName || !okUsage {
		bad(call, "%s FlagSet.%s's flag name or usage is not a constant string; the scan cannot read it", verb, m)
		return
	}
	s.flags[verb] = append(s.flags[verb], cliFlag{name: name, usage: usage, at: at})
}

// checkExcludedFile fails on a file this platform's build excludes that
// imports flag or calls a method named like a flag registration with its
// arity, since only a build for its platform could attribute the flag.
func checkExcludedFile(t *testing.T, fset *token.FileSet, f *ast.File) {
	t.Helper()
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == "flag" {
			t.Errorf("%s: a file this platform's build excludes imports flag; the scan cannot type-check its FlagSets", fset.Position(imp.Pos()))
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if pos, reg := flagRegistrars[sel.Sel.Name]; reg && len(call.Args) > pos[1] {
				t.Errorf("%s: a file this platform's build excludes calls %s, which may register a flag the scan cannot attribute", fset.Position(call.Pos()), types.ExprString(sel))
			}
		}
		return true
	})
}

// exportImporter returns an importer that reads the export data `go list
// -export` gives for this package's dependencies.
func exportImporter(t *testing.T, fset *token.FileSet) types.Importer {
	t.Helper()
	out, err := exec.Command("go", "list", "-export", "-deps", "-f", "{{if .Export}}{{.ImportPath}}={{.Export}}{{end}}", ".").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.Fatalf("go list -export: %v\n%s", err, ee.Stderr)
		}
		t.Fatalf("go list -export: %v", err)
	}
	exports := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if path, file, ok := strings.Cut(line, "="); ok {
			exports[path] = file
		}
	}
	return importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		file, ok := exports[path]
		if !ok {
			return nil, fmt.Errorf("go list -export gave no export data for %q", path)
		}
		return os.Open(file)
	})
}

// flagSetMethods returns (*flag.FlagSet)'s methods by name, or none when the
// package does not depend on flag.
func flagSetMethods(imp types.Importer) map[string]*types.Func {
	out := map[string]*types.Func{}
	pkg, err := imp.Import("flag")
	if err != nil {
		return out
	}
	ms := types.NewMethodSet(types.NewPointer(pkg.Scope().Lookup("FlagSet").Type()))
	for i := 0; i < ms.Len(); i++ {
		if f, ok := ms.At(i).Obj().(*types.Func); ok {
			out[f.Name()] = f
		}
	}
	return out
}

// flagFunc returns the name of the flag package function fun names, or "".
func flagFunc(info *types.Info, fun ast.Expr) string {
	var id *ast.Ident
	switch f := ast.Unparen(fun).(type) {
	case *ast.SelectorExpr:
		id = f.Sel
	case *ast.Ident:
		id = f
	}
	if id == nil {
		return ""
	}
	fn, ok := info.Uses[id].(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != "flag" || fn.Type().(*types.Signature).Recv() != nil {
		return ""
	}
	return fn.Name()
}

// isFlagSet reports whether t is flag.FlagSet or a pointer to it.
func isFlagSet(t types.Type) bool {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := t.(*types.Named)
	return ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == "flag" && n.Obj().Name() == "FlagSet"
}

// isFlagSetMethod reports whether m is a method of flag.FlagSet.
func isFlagSetMethod(m *types.Func) bool {
	recv := m.Type().(*types.Signature).Recv()
	return recv != nil && isFlagSet(recv.Type())
}

// constString returns e's value when it is a constant string.
func constString(info *types.Info, e ast.Expr) (string, bool) {
	tv, ok := info.Types[e]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(tv.Value), true
}

// mapLiteralKeys returns the constant string keys of the map literals in fd's
// body.
func mapLiteralKeys(info *types.Info, fd *ast.FuncDecl) []string {
	var keys []string
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if _, isMap := lit.Type.(*ast.MapType); !isMap {
			return true
		}
		for _, el := range lit.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				if k, ok := constString(info, kv.Key); ok {
					keys = append(keys, k)
				}
			}
		}
		return false
	})
	return keys
}

// sortedKeys returns m's keys in order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
