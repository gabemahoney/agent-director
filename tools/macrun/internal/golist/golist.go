// Package golist lists, statically and from source alone, the top-level Go
// cases a package's test build compiles for one platform: its Test functions
// and its Examples with an output comment, as go test finds them. It runs no
// go command, so it gives the same answer on Linux and on the Mac. Any
// macrun code that lists a package's Go cases uses List with these contexts.
package golist

import (
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/doc"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

// DarwinCgoEnabled is the cgo setting of the Mac's go test: a native build
// on macOS has cgo on. DarwinContext lists with it, and macrun's part build
// sets CGO_ENABLED from it, so the listed cases and the built ones agree.
const DarwinCgoEnabled = true

// linuxCgoEnabled is the cgo setting of a native go test on Linux.
const linuxCgoEnabled = true

// DarwinContext returns the build context of go test on the Mac:
// darwin/arm64, cgo as DarwinCgoEnabled says, and no custom build tags.
func DarwinContext() build.Context {
	return platformContext("darwin", "arm64", DarwinCgoEnabled)
}

// LinuxContext returns the build context of go test on Linux: linux/amd64,
// cgo on, and no custom build tags.
func LinuxContext() build.Context {
	return platformContext("linux", "amd64", linuxCgoEnabled)
}

// platformContext starts from build.Default, so GOROOT and the toolchain's
// release tags carry over, and sets the platform. It keeps only the
// goexperiment.* tool tags, because the others name the host architecture's
// features (such as amd64.v1) and would be wrong for another one.
func platformContext(goos, goarch string, cgo bool) build.Context {
	ctx := build.Default
	ctx.GOOS = goos
	ctx.GOARCH = goarch
	ctx.CgoEnabled = cgo
	ctx.BuildTags = nil
	ctx.ToolTags = nil
	for _, tag := range build.Default.ToolTags {
		if strings.HasPrefix(tag, "goexperiment.") {
			ctx.ToolTags = append(ctx.ToolTags, tag)
		}
	}
	return ctx
}

// Listing is what one build context compiles of one package dir.
type Listing struct {
	// InBuild reports whether the dir is a package of the build: it is in
	// the root's module and the build compiles at least one of its .go
	// files, test or non-test.
	InBuild bool
	// Cases are the top-level Test functions and Examples with an output
	// comment that the build's in-package and external test files hold,
	// named as go test -json names them, sorted, each once.
	Cases []string
}

// List lists the package at dir, a slash-separated path relative to root,
// the root of a Go module, under ctx. A dir that doesn't exist, lies under a
// regular file, isn't a directory, or is in a nested module is not a package
// of the build and lists no error. Any other fault reading or parsing the
// package is an error naming the dir and the file.
func List(ctx build.Context, root, dir string) (Listing, error) {
	abs := filepath.Join(root, filepath.FromSlash(dir))
	info, err := os.Stat(abs)
	if isAbsent(err) || (err == nil && !info.IsDir()) {
		return Listing{}, nil
	}
	if err != nil {
		return Listing{}, fmt.Errorf("package %s: %w", dir, err)
	}
	nested, err := inNestedModule(root, dir)
	if err != nil || nested {
		return Listing{}, err
	}
	pkg, err := ctx.ImportDir(abs, 0)
	var noGo *build.NoGoError
	if err != nil && !errors.As(err, &noGo) {
		return Listing{}, fmt.Errorf("package %s for %s/%s: %w", dir, ctx.GOOS, ctx.GOARCH, err)
	}
	listing := Listing{
		InBuild: len(pkg.GoFiles)+len(pkg.CgoFiles)+len(pkg.TestGoFiles)+len(pkg.XTestGoFiles) > 0,
	}
	seen := map[string]bool{}
	fset := token.NewFileSet()
	for _, name := range append(append([]string{}, pkg.TestGoFiles...), pkg.XTestGoFiles...) {
		cases, err := fileCases(fset, abs, path.Join(dir, name))
		if err != nil {
			return Listing{}, fmt.Errorf("package %s: %w", dir, err)
		}
		for _, c := range cases {
			if !seen[c] {
				seen[c] = true
				listing.Cases = append(listing.Cases, c)
			}
		}
	}
	sort.Strings(listing.Cases)
	return listing, nil
}

// inNestedModule reports whether dir, relative to root, is in a module other
// than root's: it or a dir between it and root holds a go.mod. go test ./...
// from root doesn't reach such a package.
func inNestedModule(root, dir string) (bool, error) {
	for d := dir; d != "." && d != "/" && d != ""; d = path.Dir(d) {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(d), "go.mod"))
		if err == nil {
			return true, nil
		}
		if !isAbsent(err) {
			return false, fmt.Errorf("package %s: %w", dir, err)
		}
	}
	return false, nil
}

// isAbsent reports whether err says a path doesn't exist, either because
// its last element is missing or because an element before it is a regular
// file, which Go reports as ENOTDIR rather than fs.ErrNotExist.
func isAbsent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// fileCases parses the test file rel (slash-separated, relative to the root)
// in the package at abs and returns its cases, following go test's own rules
// in cmd/go/internal/load/test.go.
func fileCases(fset *token.FileSet, abs, rel string) ([]string, error) {
	src, err := os.ReadFile(filepath.Join(abs, path.Base(rel)))
	if err != nil {
		return nil, fmt.Errorf("read test file %s: %w", rel, err)
	}
	f, err := parser.ParseFile(fset, rel, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse test file: %w", err)
	}
	testingName := testingImportName(f)
	var cases []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		if name := fn.Name.Name; isTestName(name, "Test") && takesTestingT(fn, testingName) {
			cases = append(cases, name)
		}
	}
	for _, ex := range doc.Examples(f) {
		if ex.Output != "" || ex.EmptyOutput {
			cases = append(cases, "Example"+ex.Name)
		}
	}
	return cases, nil
}

// isTestName reports whether name is prefix, or prefix followed by a
// character that isn't a lower-case letter, as go test requires of a Test,
// Benchmark, Fuzz or Example function's name.
func isTestName(name, prefix string) bool {
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	if len(name) == len(prefix) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(name[len(prefix):])
	return !unicode.IsLower(r)
}

// testingImportName returns the name f uses for the testing package: "."
// for a dot import, "" when f doesn't import it, or the package name.
func testingImportName(f *ast.File) string {
	for _, imp := range f.Imports {
		if p, err := strconv.Unquote(imp.Path.Value); err != nil || p != "testing" {
			continue
		}
		if imp.Name == nil {
			return "testing"
		}
		if imp.Name.Name != "_" {
			return imp.Name.Name
		}
	}
	return ""
}

// takesTestingT reports whether fn is a non-generic function with no results
// and one parameter of type *testing.T, testing being imported as
// testingName.
func takesTestingT(fn *ast.FuncDecl, testingName string) bool {
	typ := fn.Type
	if testingName == "" || typ.TypeParams != nil || (typ.Results != nil && len(typ.Results.List) > 0) {
		return false
	}
	params := typ.Params.List
	if len(params) != 1 || len(params[0].Names) > 1 {
		return false
	}
	star, ok := params[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	switch x := star.X.(type) {
	case *ast.Ident:
		return testingName == "." && x.Name == "T"
	case *ast.SelectorExpr:
		pkg, ok := x.X.(*ast.Ident)
		return ok && pkg.Name == testingName && x.Sel.Name == "T"
	}
	return false
}
