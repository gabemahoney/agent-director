// Command check-doccomments verifies that every exported identifier in a Go
// package has a non-empty doc comment suitable for pkg.go.dev rendering.
//
// It walks the package's exported top-level declarations using the Go AST:
//   - top-level functions and methods (FuncDecl with exported Name)
//   - type declarations (GenDecl/TypeSpec with exported Name)
//   - exported fields of exported struct types (Field.Doc or Field.Comment)
//   - variable declarations (GenDecl/ValueSpec with exported Name)
//   - constant declarations (GenDecl/ValueSpec with exported Name)
//
// For each exported identifier that lacks a non-empty CommentGroup, one
// diagnostic line is emitted to stdout:
//
//	file.go:42: exported func Foo has no doc comment
//	file.go:55: exported field Options.StorePath has no doc comment
//
// The tool exits 0 when every exported identifier is documented, 1 when one
// or more are missing, and 2 on a usage or parse error.
//
// Usage:
//
//	go run ./tools/check-doccomments -package ./pkg/api
//
// Flags:
//
//	-package dir   Filesystem path to the Go package directory to check
//	               (default: ./). Test files (*_test.go) are excluded.
//
// # Reproducing a CI failure locally
//
//	make check-doccomments
//
// Add the missing comment(s) flagged in the output, then re-run.
// See tools/check-doccomments/main_test.go for fixture-based examples.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
)

func main() {
	pkgDir := flag.String("package", "./", "directory of the Go package to check")
	flag.Parse()

	missing, err := check(*pkgDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "check-doccomments: %v\n", err)
		os.Exit(2)
	}
	if len(missing) > 0 {
		for _, m := range missing {
			fmt.Println(m)
		}
		fmt.Fprintf(os.Stderr, "\ncheck-doccomments: %d exported identifier(s) in %s lack doc comments\n", len(missing), *pkgDir)
		fmt.Fprintf(os.Stderr, "Add a // comment immediately before each flagged declaration, then re-run.\n")
		os.Exit(1)
	}
}

// check parses the Go package at dir and returns one diagnostic string per
// exported identifier that lacks a non-empty doc comment. Test files
// (*_test.go) are excluded. Returns a non-nil error only on parse failure.
func check(dir string) ([]string, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, isNotTestFile, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", dir, err)
	}

	var missing []string
	for _, pkg := range pkgs {
		// Iterate files in a deterministic order.
		filenames := make([]string, 0, len(pkg.Files))
		for name := range pkg.Files {
			filenames = append(filenames, name)
		}
		sort.Strings(filenames)

		for _, filename := range filenames {
			file := pkg.Files[filename]
			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					checkFuncDecl(fset, d, &missing)
				case *ast.GenDecl:
					checkGenDecl(fset, d, &missing)
				}
			}
		}
	}
	return missing, nil
}

// checkFuncDecl checks that an exported function or method has a doc comment.
func checkFuncDecl(fset *token.FileSet, d *ast.FuncDecl, missing *[]string) {
	if !d.Name.IsExported() {
		return
	}
	if d.Doc == nil || strings.TrimSpace(d.Doc.Text()) == "" {
		pos := fset.Position(d.Pos())
		kind := "func"
		if d.Recv != nil && len(d.Recv.List) > 0 {
			kind = "method"
		}
		*missing = append(*missing, fmt.Sprintf(
			"%s:%d: exported %s %s has no doc comment",
			pos.Filename, pos.Line, kind, d.Name.Name,
		))
	}
}

// checkGenDecl checks exported identifiers in a CONST, VAR, or TYPE declaration
// group. For non-grouped declarations (no parentheses) the GenDecl.Doc is the
// effective comment. For grouped declarations each Spec carries its own Doc.
// For struct type declarations, exported fields are also checked.
func checkGenDecl(fset *token.FileSet, d *ast.GenDecl, missing *[]string) {
	for _, spec := range d.Specs {
		// Resolve effective doc: grouped declarations use per-spec Doc;
		// non-grouped declarations use the GenDecl's Doc.
		var doc *ast.CommentGroup
		if d.Lparen.IsValid() {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				doc = s.Doc
			case *ast.ValueSpec:
				doc = s.Doc
			}
		} else {
			doc = d.Doc
		}

		hasDoc := doc != nil && strings.TrimSpace(doc.Text()) != ""

		switch s := spec.(type) {
		case *ast.TypeSpec:
			if s.Name.IsExported() {
				if !hasDoc {
					pos := fset.Position(s.Pos())
					*missing = append(*missing, fmt.Sprintf(
						"%s:%d: exported type %s has no doc comment",
						pos.Filename, pos.Line, s.Name.Name,
					))
				}
				// For exported struct types, also check exported fields.
				if st, ok := s.Type.(*ast.StructType); ok {
					checkStructFields(fset, s.Name.Name, st, missing)
				}
			}
		case *ast.ValueSpec:
			for _, name := range s.Names {
				if name.IsExported() && !hasDoc {
					pos := fset.Position(name.Pos())
					*missing = append(*missing, fmt.Sprintf(
						"%s:%d: exported %s %s has no doc comment",
						pos.Filename, pos.Line, tokenKind(d.Tok), name.Name,
					))
				}
			}
		}
	}
}

// checkStructFields walks the exported fields of a struct type and asserts
// that each has either a preceding doc comment (Field.Doc) or a trailing
// inline comment (Field.Comment). Per godoc convention, either placement is
// acceptable for struct field documentation.
//
// Embedded fields (those with no explicit name, e.g. `io.Reader`) are skipped
// because they are documented by the embedded type itself.
func checkStructFields(fset *token.FileSet, typeName string, st *ast.StructType, missing *[]string) {
	for _, field := range st.Fields.List {
		// Skip embedded fields — they carry no Names.
		if len(field.Names) == 0 {
			continue
		}

		hasComment := (field.Doc != nil && strings.TrimSpace(field.Doc.Text()) != "") ||
			(field.Comment != nil && strings.TrimSpace(field.Comment.Text()) != "")

		for _, name := range field.Names {
			if name.IsExported() && !hasComment {
				pos := fset.Position(name.Pos())
				*missing = append(*missing, fmt.Sprintf(
					"%s:%d: exported field %s.%s has no doc comment",
					pos.Filename, pos.Line, typeName, name.Name,
				))
			}
		}
	}
}

// tokenKind returns a human-readable name for the token type used in GenDecl
// (token.CONST, token.VAR, or token.TYPE).
func tokenKind(tok token.Token) string {
	switch tok {
	case token.CONST:
		return "const"
	case token.VAR:
		return "var"
	default:
		return "decl"
	}
}

// isNotTestFile returns true for Go source files that are not test files.
// It is used as the filter argument to parser.ParseDir so that *_test.go
// files are excluded from the doc-comment check.
func isNotTestFile(fi os.FileInfo) bool {
	return !strings.HasSuffix(fi.Name(), "_test.go")
}
