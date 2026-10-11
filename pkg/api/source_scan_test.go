package api_test

// source_scan_test.go holds the reusable pieces of the repo's name scans (the
// SR-4.5 old-names test in old_names_test.go; the SR-11.7 old-path test reuses
// them): the texts to scan, the exceptions that exempt a file, a Markdown
// section, a declaration block or a Go name, each with its reason, and a
// walker over the exported names a Go source declares. They know no old name.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// scanSource is one text to scan: a repo-relative path, or the name of a text
// built in the test (such as "api.Help()").
type scanSource struct{ name, text string }

// repoFile reads the repo-relative file rel as a scanSource.
func repoFile(t testing.TB, rel string) scanSource {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(mdRepoRoot, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return scanSource{name: rel, text: string(data)}
}

// repoGlob reads every file matching the repo-relative glob pattern that keep
// accepts (nil keeps all), in name order.
func repoGlob(t testing.TB, pattern string, keep func(rel string) bool) []scanSource {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(mdRepoRoot, pattern))
	if err != nil || len(paths) == 0 {
		t.Fatalf("glob %s: %v, %d matches; want at least one", pattern, err, len(paths))
	}
	var out []scanSource
	for _, p := range paths {
		rel, _ := filepath.Rel(mdRepoRoot, p)
		if rel = filepath.ToSlash(rel); keep == nil || keep(rel) {
			out = append(out, repoFile(t, rel))
		}
	}
	return out
}

// repoTree reads every file under the repo-relative directory dir, in path order.
func repoTree(t testing.TB, dir string) []scanSource {
	t.Helper()
	var out []scanSource
	err := filepath.WalkDir(filepath.Join(mdRepoRoot, dir), func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() && (e.Name() == "node_modules" || e.Name() == "testdata") {
			return filepath.SkipDir
		}
		if !e.IsDir() {
			rel, _ := filepath.Rel(mdRepoRoot, path)
			out = append(out, repoFile(t, filepath.ToSlash(rel)))
		}
		return nil
	})
	if err != nil || len(out) == 0 {
		t.Fatalf("walk %s: %v, %d files; want at least one", dir, err, len(out))
	}
	return out
}

// scanException exempts one place from a scan. Every exception names its file
// and gives its reason; at most one of heading, block and ident narrows it.
type scanException struct {
	file    string // repo-relative path, or a built text's name; alone it exempts the whole file
	heading string // a Markdown heading title in file: its section, up to the next heading of its level or higher
	block   string // text in the line that opens a declaration in file: that line to its closing line
	ident   string // a Go name in file: "Name", "Type.Field", "Type.Method" or "Func.param"
	reason  string // why the name may stay there (the SR that allows it)
}

// scanRules are a scan's exceptions. An exception whose target does not exist
// (a file not scanned, a heading or block not found, a Go name not declared)
// is stale and fails the scan, so an exception cannot outlive what it exempts.
type scanRules struct {
	list []scanException
	hit  []bool
}

func newScanRules(list ...scanException) *scanRules {
	for _, e := range list {
		if e.file == "" || e.reason == "" {
			panic(fmt.Sprintf("scanException %+v: needs a file and a reason", e))
		}
	}
	return &scanRules{list: list, hit: make([]bool, len(list))}
}

// skipLines returns the 1-based lines of src that its exceptions exempt.
func (r *scanRules) skipLines(t testing.TB, src scanSource) map[int]bool {
	t.Helper()
	lines := strings.Split(src.text, "\n")
	skip := map[int]bool{}
	cover := func(from, to int) { // 0-based from, exclusive to
		for i := from; i < to; i++ {
			skip[i+1] = true
		}
	}
	for i, e := range r.list {
		if e.file != src.name || e.ident != "" {
			continue
		}
		switch {
		case e.heading != "":
			doc := readMD(t, filepath.Join(mdRepoRoot, src.name))
			hs := doc.titled(e.heading)
			if len(hs) != 1 {
				t.Errorf("%s has %d headings titled %q; want exactly one for its exception", src.name, len(hs), e.heading)
				continue
			}
			end := len(lines)
			for _, h := range doc.headings {
				if h.line > hs[0].line && h.level <= hs[0].level {
					end = h.line
					break
				}
			}
			cover(hs[0].line, end)
			r.hit[i] = true
		case e.block != "":
			from := -1
			for n, l := range lines {
				if strings.Contains(l, e.block) {
					from = n
					break
				}
			}
			if from < 0 {
				continue
			}
			indent := lines[from][:len(lines[from])-len(strings.TrimLeft(lines[from], " \t"))]
			to := len(lines)
			for n := from + 1; n < len(lines); n++ {
				rest, ok := strings.CutPrefix(lines[n], indent)
				if ok && rest != "" && strings.ContainsRune("]})", rune(rest[0])) {
					to = n + 1
					break
				}
			}
			cover(from, to)
			r.hit[i] = true
		default:
			cover(0, len(lines))
			r.hit[i] = true
		}
	}
	return skip
}

// goExempt reports whether an exception exempts the Go name path declared in
// file.
func (r *scanRules) goExempt(file, path string) bool {
	exempt := false
	for i, e := range r.list {
		if e.file == file && e.ident == path {
			r.hit[i], exempt = true, true
		}
	}
	return exempt
}

// requireAllHit fails the test for each stale exception.
func (r *scanRules) requireAllHit(t testing.TB) {
	t.Helper()
	for i, e := range r.list {
		if !r.hit[i] {
			t.Errorf("stale exception %+v: its target does not exist", e)
		}
	}
}

// scanText calls find with each line of each source that no exception exempts
// and returns "name:line: hit" for every hit it reports.
func scanText(t testing.TB, srcs []scanSource, rules *scanRules, find func(line string) []string) []string {
	t.Helper()
	var out []string
	for _, src := range srcs {
		skip := rules.skipLines(t, src)
		for n, line := range strings.Split(src.text, "\n") {
			if skip[n+1] {
				continue
			}
			for _, hit := range find(line) {
				out = append(out, fmt.Sprintf("%s:%d: %s", src.name, n+1, hit))
			}
		}
	}
	return out
}

// goScope says which declarations of a Go package a walk reports.
type goScope struct {
	all   bool            // every exported declaration
	funcs bool            // every exported function (also a function variable) and method of an exported type, with its parameters
	types map[string]bool // the named types, with their fields, tags and methods
}

// goName is one string a Go source declares on its exported surface: an
// exported name, or the name in a json or toml struct tag. path is the name as
// an exception writes it: "Name", "Type.Field", "Type.Method" or "Func.param".
type goName struct {
	file, kind, path, text string
	line                   int
}

// goNames returns the strings of the exported declarations of f (file rel)
// that sc covers: exported names, the parameter and result names of functions,
// methods and interface methods, and the json and toml tag names of struct
// fields.
func goNames(fset *token.FileSet, rel string, f *ast.File, sc goScope) []goName {
	var out []goName
	add := func(pos token.Pos, kind, path, text string) {
		out = append(out, goName{file: rel, line: fset.Position(pos).Line, kind: kind, path: path, text: text})
	}
	params := func(owner string, ft *ast.FuncType) {
		for _, list := range []*ast.FieldList{ft.Params, ft.Results} {
			if list == nil {
				continue
			}
			for _, fld := range list.List {
				for _, n := range fld.Names {
					add(n.Pos(), "parameter", owner+"."+n.Name, n.Name)
				}
			}
		}
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if !ast.IsExported(d.Name.Name) {
				continue
			}
			owner := d.Name.Name
			if d.Recv != nil {
				recv := recvTypeName(d.Recv.List[0].Type)
				if !ast.IsExported(recv) || !(sc.all || sc.funcs || sc.types[recv]) {
					continue
				}
				owner = recv + "." + d.Name.Name
			} else if !(sc.all || sc.funcs) {
				continue
			}
			add(d.Name.Pos(), "func", owner, d.Name.Name)
			params(owner, d.Type)
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.ValueSpec:
					ft, isFunc := s.Type.(*ast.FuncType) // a function variable, such as adminapi.KillFinished
					for _, n := range s.Names {
						if !n.IsExported() || !(sc.all || sc.funcs && isFunc) {
							continue
						}
						add(n.Pos(), "name", n.Name, n.Name)
						if isFunc {
							params(n.Name, ft)
						}
					}
				case *ast.TypeSpec:
					if !s.Name.IsExported() || !(sc.all || sc.types[s.Name.Name]) {
						continue
					}
					add(s.Name.Pos(), "type", s.Name.Name, s.Name.Name)
					ast.Inspect(s.Type, func(n ast.Node) bool {
						switch n := n.(type) {
						case *ast.StructType:
							for _, fld := range n.Fields.List {
								for _, name := range fld.Names {
									if name.IsExported() {
										add(name.Pos(), "field", s.Name.Name+"."+name.Name, name.Name)
									}
								}
								if fld.Tag == nil {
									continue
								}
								tag, _ := strconv.Unquote(fld.Tag.Value)
								for _, key := range []string{"json", "toml"} {
									if v, _, _ := strings.Cut(reflect.StructTag(tag).Get(key), ","); v != "" && v != "-" {
										path := s.Name.Name
										if len(fld.Names) > 0 {
											path += "." + fld.Names[0].Name
										}
										add(fld.Tag.Pos(), key+" tag", path, v)
									}
								}
							}
						case *ast.InterfaceType:
							for _, m := range n.Methods.List {
								ft, ok := m.Type.(*ast.FuncType)
								if !ok || len(m.Names) == 0 || !m.Names[0].IsExported() {
									continue
								}
								owner := s.Name.Name + "." + m.Names[0].Name
								add(m.Names[0].Pos(), "method", owner, m.Names[0].Name)
								params(owner, ft)
							}
						}
						return true
					})
				}
			}
		}
	}
	return out
}

// recvTypeName is the name of the type a method's receiver expression names.
func recvTypeName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.StarExpr:
		return recvTypeName(e.X)
	case *ast.IndexExpr:
		return recvTypeName(e.X)
	case *ast.IndexListExpr:
		return recvTypeName(e.X)
	case *ast.Ident:
		return e.Name
	}
	return ""
}

// goDirNames returns goNames of every non-test Go file of the repo-relative
// directory dir, and of its subdirectories when recursive.
func goDirNames(t testing.TB, dir string, recursive bool, sc goScope) []goName {
	t.Helper()
	fset := token.NewFileSet()
	var out []goName
	err := filepath.WalkDir(filepath.Join(mdRepoRoot, dir), func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			if path != filepath.Join(mdRepoRoot, dir) && (!recursive || e.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(mdRepoRoot, path)
		out = append(out, goNames(fset, filepath.ToSlash(rel), f, sc)...)
		return nil
	})
	if err != nil || len(out) == 0 {
		t.Fatalf("scan Go names of %s: %v, %d names; want at least one", dir, err, len(out))
	}
	return out
}

// aliasedInternalTypes maps each internal package directory (repo-relative) to
// the types that the exported type aliases of dir's non-test files (`type X =
// pkg.T`) re-export from it.
func aliasedInternalTypes(t testing.TB, dir string) map[string]map[string]bool {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(mdRepoRoot, dir, "*.go"))
	if err != nil {
		t.Fatalf("glob %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	out := map[string]map[string]bool{}
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		imports := map[string]string{}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			name := path[strings.LastIndex(path, "/")+1:]
			if imp.Name != nil {
				name = imp.Name.Name
			}
			imports[name] = path
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !ts.Assign.IsValid() || !ts.Name.IsExported() {
					continue
				}
				sel, ok := ts.Type.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok {
					continue
				}
				path := imports[pkg.Name]
				if i := strings.Index(path, "/internal/"); i >= 0 {
					rel := path[i+1:]
					if out[rel] == nil {
						out[rel] = map[string]bool{}
					}
					out[rel][sel.Sel.Name] = true
				}
			}
		}
	}
	return out
}
