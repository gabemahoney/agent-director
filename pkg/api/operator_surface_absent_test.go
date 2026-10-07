package api_test

// operator_surface_absent_test.go pins b.vqr on the Go client's public
// surface: kill takes only an instance id, and there is no exported delete.
// Both operator actions live on the off-PATH agent-director-admin binary.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api"
)

// TestKillParamsHasOnlyClaudeInstanceID: KillParams has exactly one field,
// ClaudeInstanceID; the finished-row opt-in is not part of pkg/api.
func TestKillParamsHasOnlyClaudeInstanceID(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeOf(api.KillParams{})
	var fields []string
	for i := 0; i < typ.NumField(); i++ {
		fields = append(fields, typ.Field(i).Name)
	}
	if want := []string{"ClaudeInstanceID"}; !slices.Equal(fields, want) {
		t.Errorf("KillParams fields = %v; want exactly %v", fields, want)
	}
}

// TestPublicAPIHasNoDelete: neither *Client nor the package exports Delete.
func TestPublicAPIHasNoDelete(t *testing.T) {
	t.Parallel()
	if _, ok := reflect.TypeOf(&api.Client{}).MethodByName("Delete"); ok {
		t.Error("pkg/api.Client exports Delete; delete is an agent-director-admin verb only")
	}
	if _, ok := reflect.TypeOf(&api.Client{}).MethodByName("Kill"); !ok {
		t.Error("pkg/api.Client has no Kill; the method check above would pass vacuously")
	}

	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	var funcs []string
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
				funcs = append(funcs, fd.Name.Name)
				if fd.Name.Name == "Delete" {
					t.Errorf("%s: pkg/api exports func Delete; delete is an agent-director-admin verb only", fset.Position(fd.Pos()))
				}
			}
		}
	}
	if !slices.Contains(funcs, "Kill") {
		t.Error("the scan found no func Kill in pkg/api; the func check above would pass vacuously")
	}
}
