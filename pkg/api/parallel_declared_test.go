package api_test

// parallel_declared_test.go pins b.yo5: pkg/api's tests ran one after another
// (about 38 min under -race) because almost none called t.Parallel. Every
// top-level test now starts with t.Parallel() or says why it cannot, so a new
// test makes that choice on purpose.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// TestEveryTestDeclaresParallelOrSerial: every top-level test in pkg/api starts
// with t.Parallel() or with a "// Serial: <reason>" comment (b.yo5).
func TestEveryTestDeclaresParallelOrSerial(t *testing.T) {
	t.Parallel()
	paths, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	parallel, serial := 0, 0
	for _, p := range paths {
		f, err := parser.ParseFile(fset, p, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || !isTopLevelTest(fd) {
				continue
			}
			switch {
			case startsWithParallel(fd):
				parallel++
			case startsWithSerialReason(f, fd):
				serial++
			default:
				t.Errorf("%s: %s starts with neither t.Parallel() nor a \"// Serial: <reason>\" comment. Run it in "+
					"parallel unless it sets the environment, reads the shared trail beyond its own random row ids, "+
					"or changes other process-wide state; then say which (b.yo5)", fset.Position(fd.Pos()), fd.Name.Name)
			}
		}
	}
	if parallel == 0 || serial == 0 {
		t.Errorf("found %d parallel and %d serial tests; the check above would pass vacuously", parallel, serial)
	}
}

// isTopLevelTest reports whether fd is a func TestXxx(t *testing.T) go test runs, TestMain excluded.
func isTopLevelTest(fd *ast.FuncDecl) bool {
	name, ok := strings.CutPrefix(fd.Name.Name, "Test")
	if !ok || fd.Recv != nil || fd.Body == nil || fd.Name.Name == "TestMain" || fd.Type.Params.NumFields() != 1 {
		return false
	}
	r, _ := utf8.DecodeRuneInString(name)
	return name == "" || !unicode.IsLower(r)
}

// startsWithParallel reports whether fd's first statement is <its *testing.T>.Parallel().
func startsWithParallel(fd *ast.FuncDecl) bool {
	if len(fd.Body.List) == 0 || len(fd.Type.Params.List[0].Names) == 0 {
		return false
	}
	stmt, ok := fd.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := stmt.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Parallel" {
		return false
	}
	recv, ok := sel.X.(*ast.Ident)
	return ok && recv.Name == fd.Type.Params.List[0].Names[0].Name
}

// startsWithSerialReason reports whether the first comment in fd's body, before
// its first statement, is "// Serial: " followed by a reason.
func startsWithSerialReason(f *ast.File, fd *ast.FuncDecl) bool {
	end := fd.Body.Rbrace
	if len(fd.Body.List) > 0 {
		end = fd.Body.List[0].Pos()
	}
	for _, cg := range f.Comments {
		if cg.Pos() > fd.Body.Lbrace && cg.Pos() < end {
			reason, ok := strings.CutPrefix(cg.List[0].Text, "// Serial:")
			return ok && strings.TrimSpace(reason) != ""
		}
	}
	return false
}
