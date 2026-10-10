package api_test

// pane_text_unread_test.go — the review check of b.146 step 2b's rule that
// agent-director never reads, matches or acts on Claude Code's screen text
// (decision 7 A; the ticket's required behaviour 8), made a test so a later
// change cannot slip past it.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestNoCodePathReadsPaneText: in pkg/api's production code only readPane
// (read-pane, which returns the text to its caller) and capturePaneHash (the
// hash check) capture the pane, and the captured value goes only to
// paneSHA256, the result's Pane field and tmux.StripANSI, whose result goes
// back into the same variable or straight to one of the other two: nothing
// compares, searches or branches on what it says. No other function reads a
// ReadPaneResult's Pane, paneSHA256 does nothing but hash the bytes, and no
// file imports regexp.
func TestNoCodePathReadsPaneText(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	capturers := map[string]bool{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, imp := range f.Imports {
			if imp.Path.Value == `"regexp"` {
				t.Errorf("%s imports regexp; agent-director matches no pane text", path)
			}
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			if fd.Name.Name == "paneSHA256" {
				assertOnlyCalls(t, fset, fd, "sha256.Sum256", "hex.EncodeToString")
			}
			for _, v := range capturedVars(fd) {
				capturers[fd.Name.Name] = true
				assertPaneOnlyPassedOn(t, fset, fd, v)
			}
			if fd.Name.Name != "readPane" {
				assertNoResultPaneRead(t, fset, fd)
			}
		}
	}
	got := make([]string, 0, len(capturers))
	for name := range capturers {
		got = append(got, name)
	}
	if slices.Sort(got); !slices.Equal(got, []string{"capturePaneHash", "readPane"}) {
		t.Errorf("functions capturing the pane = %v; want only capturePaneHash and readPane", got)
	}
}

// capturedVars returns the names fd assigns a CapturePaneID result to.
func capturedVars(fd *ast.FuncDecl) []string {
	var out []string
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 {
			return true
		}
		if call, ok := as.Rhs[0].(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "CapturePaneID" {
				if id, ok := as.Lhs[0].(*ast.Ident); ok {
					out = append(out, id.Name)
				}
			}
		}
		return true
	})
	return out
}

// assertPaneOnlyPassedOn fails for each use of v in fd other than an
// assignment to it, an argument of paneSHA256, the value of a Pane field, or
// an argument of tmux.StripANSI whose result is assigned back to v or used
// in one of those two ways.
func assertPaneOnlyPassedOn(t *testing.T, fset *token.FileSet, fd *ast.FuncDecl, v string) {
	t.Helper()
	var stack []ast.Node
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if id, ok := n.(*ast.Ident); ok && id.Name == v && !paneUseAllowed(stack, id, v) {
			t.Errorf("%s: %s uses the captured pane %q other than to strip, hash or return it", fset.Position(id.Pos()), fd.Name.Name, v)
		}
		stack = append(stack, n)
		return true
	})
}

// paneUseAllowed reports whether e, the captured pane v or tmux.StripANSI of
// it, is used under stack's last node only to be assigned, hashed or returned
// as the Pane field; a stripped value only when assigned back to v.
func paneUseAllowed(stack []ast.Node, e ast.Expr, v string) bool {
	switch p := stack[len(stack)-1].(type) {
	case *ast.AssignStmt:
		if id, ok := e.(*ast.Ident); ok {
			return slices.Contains(p.Lhs, ast.Expr(id))
		}
		i := slices.Index(p.Rhs, e)
		if i < 0 || len(p.Lhs) != len(p.Rhs) {
			return false
		}
		lhs, ok := p.Lhs[i].(*ast.Ident)
		return ok && lhs.Name == v
	case *ast.CallExpr:
		switch funcName(p.Fun) {
		case "paneSHA256":
			return true
		case "tmux.StripANSI":
			_, raw := e.(*ast.Ident)
			return raw && len(stack) > 1 && paneUseAllowed(stack[:len(stack)-1], p, v)
		}
	case *ast.KeyValueExpr:
		key, ok := p.Key.(*ast.Ident)
		return ok && key.Name == "Pane" && p.Value == e
	}
	return false
}

// assertNoResultPaneRead fails for each read in fd of the Pane field of a
// ReadPaneResult: of a variable fd declares as one or assigns a ReadPane or
// readPane result to, or of such a call itself.
func assertNoResultPaneRead(t *testing.T, fset *token.FileSet, fd *ast.FuncDecl) {
	t.Helper()
	isResult := func(e ast.Expr) bool {
		switch x := e.(type) {
		case *ast.CallExpr:
			name := funcName(x.Fun)
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
				name = sel.Sel.Name
			}
			return name == "ReadPane" || name == "readPane"
		case *ast.CompositeLit:
			return funcName(x.Type) == "ReadPaneResult"
		}
		return false
	}
	results := map[string]bool{}
	ast.Inspect(fd, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			if len(x.Rhs) == 1 && isResult(x.Rhs[0]) {
				if id, ok := x.Lhs[0].(*ast.Ident); ok {
					results[id.Name] = true
				}
			}
		case *ast.ValueSpec:
			for i, name := range x.Names {
				if funcName(x.Type) == "ReadPaneResult" || i < len(x.Values) && isResult(x.Values[i]) {
					results[name.Name] = true
				}
			}
		case *ast.Field:
			for _, name := range x.Names {
				if funcName(x.Type) == "ReadPaneResult" {
					results[name.Name] = true
				}
			}
		}
		return true
	})
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Pane" {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && results[id.Name] || isResult(sel.X) {
			t.Errorf("%s: %s reads a ReadPaneResult's Pane; only readPane may handle the pane text", fset.Position(sel.Pos()), fd.Name.Name)
		}
		return true
	})
}

// assertOnlyCalls fails for any call in fd to a function other than allowed
// (a conversion to a slice type is not a call).
func assertOnlyCalls(t *testing.T, fset *token.FileSet, fd *ast.FuncDecl, allowed ...string) {
	t.Helper()
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if _, conv := call.Fun.(*ast.ArrayType); !conv && !slices.Contains(allowed, funcName(call.Fun)) {
			t.Errorf("%s: %s calls %s; want only %v", fset.Position(call.Pos()), fd.Name.Name, funcName(call.Fun), allowed)
		}
		return true
	})
}

// funcName renders a call's function as pkg.Name or Name ("" otherwise).
func funcName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		if x, ok := f.X.(*ast.Ident); ok {
			return x.Name + "." + f.Sel.Name
		}
	}
	return ""
}
