package api_test

// aliases_test.go covers pkg/api's aliases of internal types (the k7 store
// aliases, api.ExpireCandidate and SRD Appendix F.3's internal/tmux
// re-exports), the api.ExpireTmux and api.ProcChecker implementers, the tmux
// sentinels, and the rule that every internal/tmux Call*, Fail* and Label*
// constant is re-declared in pkg/api as Tmux<Name> = tmux.<Name>.

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// The Recorder is the test double for api.TmuxClient (F.3: "*tmux.Client and
// tmuxfix.Recorder implement it"); *tmux.Client's assertion is in client.go.
var _ api.TmuxClient = (*tmuxfix.Recorder)(nil)

// expire's tmux surface (Appendix F.3): the Recorder and the production
// client both satisfy api.ExpireTmux.
var (
	_ api.ExpireTmux = (*tmuxfix.Recorder)(nil)
	_ api.ExpireTmux = (*tmux.Client)(nil)
)

// Kill's start-time reader (SR-3.8): the production reader and the shared
// process-checker fake both satisfy api.ProcChecker.
var (
	_ api.ProcChecker = probe.NewProcChecker()
	_ api.ProcChecker = (*procfix.Checker)(nil)
)

// TestAliasesAreIdentical checks each alias is the internal type itself, not a
// new defined type, so values assign both ways with every field and errors.As
// interoperates: the k7 store aliases, expire's candidate, the F.3 Tmux*
// aliases and api.ProcChecker.
func TestAliasesAreIdentical(t *testing.T) {
	t.Parallel()
	for name, pair := range map[string][2]reflect.Type{
		"Spawn":            {reflect.TypeFor[api.Spawn](), reflect.TypeFor[store.Spawn]()},
		"PermissionRow":    {reflect.TypeFor[api.PermissionRow](), reflect.TypeFor[store.PermissionRow]()},
		"ListFilters":      {reflect.TypeFor[api.ListFilters](), reflect.TypeFor[store.ListFilters]()},
		"ExpireCandidate":  {reflect.TypeFor[api.ExpireCandidate](), reflect.TypeFor[store.ExpireCandidate]()},
		"TmuxLookupAnswer": {reflect.TypeFor[api.TmuxLookupAnswer](), reflect.TypeFor[tmux.LookupAnswer]()},
		"TmuxSession":      {reflect.TypeFor[api.TmuxSession](), reflect.TypeFor[tmux.Session]()},
		"TmuxPane":         {reflect.TypeFor[api.TmuxPane](), reflect.TypeFor[tmux.Pane]()},
		"TmuxLabel":        {reflect.TypeFor[api.TmuxLabel](), reflect.TypeFor[tmux.Label]()},
		"TmuxCreateReply":  {reflect.TypeFor[api.TmuxCreateReply](), reflect.TypeFor[tmux.CreateReply]()},
		"TmuxCall":         {reflect.TypeFor[api.TmuxCall](), reflect.TypeFor[tmux.Call]()},
		"TmuxFailure":      {reflect.TypeFor[api.TmuxFailure](), reflect.TypeFor[tmux.Failure]()},
		"TmuxCallError":    {reflect.TypeFor[api.TmuxCallError](), reflect.TypeFor[tmux.CallError]()},
		"ProcChecker":      {reflect.TypeFor[api.ProcChecker](), reflect.TypeFor[tmux.ProcChecker]()},
	} {
		if pair[0] != pair[1] {
			t.Errorf("api.%s is %s.%s; want the alias of %s.%s", name, pair[0].PkgPath(), pair[0].Name(), pair[1].PkgPath(), pair[1].Name())
		}
	}
}

// TestTmuxSentinelAliases checks each pkg/api tmux sentinel is the internal/tmux
// sentinel itself, so a wrapped one matches it under errors.Is and classifies to its name.
func TestTmuxSentinelAliases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		api, orig error
		// killWraps: kill's descriptions wrap this sentinel, so its text is
		// checked against apitest.DescKillSentinelText.
		killWraps bool
	}{
		{"ErrTmuxNotAvailable", api.ErrTmuxNotAvailable, tmux.ErrTmuxNotAvailable, true},
		{"ErrTmuxSessionCreate", api.ErrTmuxSessionCreate, tmux.ErrTmuxSessionCreate, false},
		{"ErrTmuxKillFailed", api.ErrTmuxKillFailed, tmux.ErrTmuxKillFailed, true},
		{"ErrTmuxUnresponsive", api.ErrTmuxUnresponsive, tmux.ErrTmuxUnresponsive, true},
		{"ErrTmuxSessionConflict", api.ErrTmuxSessionConflict, tmux.ErrTmuxSessionConflict, true},
		{"ErrTmuxSendKeys", api.ErrTmuxSendKeys, tmux.ErrTmuxSendKeys, false},
		{"ErrTmuxCaptureFailed", api.ErrTmuxCaptureFailed, tmux.ErrTmuxCaptureFailed, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.api == nil || tc.api != tc.orig {
				t.Fatalf("api.%s (%v) is not the tmux.%s sentinel value; want api.%s = tmux.%s", tc.name, tc.api, tc.name, tc.name, tc.name)
			}
			wrapped := fmt.Errorf("verb failed: %w", tc.orig)
			if !errors.Is(wrapped, tc.api) {
				t.Errorf("errors.Is(wrapped tmux.%s, api.%s) = false; want true", tc.name, tc.name)
			}
			if got, _ := errnames.Classify(wrapped); got != tc.name {
				t.Errorf("errnames.Classify(wrapped tmux.%s) = %q; want %q", tc.name, got, tc.name)
			}
			c := apitest.DescCase{Name: tc.name + " sentinel text"}
			if tc.killWraps {
				c = apitest.DescKillSentinelText(tc.name)
			}
			apitest.AssertDescription(t, tc.orig.Error(), c)
		})
	}
}

// tmuxImportPath is the package whose constants F.3 re-declares in pkg/api.
const tmuxImportPath = "github.com/gabemahoney/agent-director/internal/tmux"

// constDecl is one exported const spec: its value expression (nil for an
// implicit iota repeat) and its file's import-name-to-path map.
type constDecl struct {
	value   ast.Expr
	imports map[string]string
}

// parseExportedConsts returns the exported constants declared in dir's non-test
// Go files, keyed by name.
func parseExportedConsts(t *testing.T, dir string) map[string]constDecl {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parser.ParseDir(%s): %v", dir, err)
	}
	out := map[string]constDecl{}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			imports := map[string]string{}
			for _, is := range f.Imports {
				p := strings.Trim(is.Path.Value, `"`)
				name := path.Base(p)
				if is.Name != nil {
					name = is.Name.Name
				}
				imports[name] = p
			}
			for _, d := range f.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					continue
				}
				for _, s := range gd.Specs {
					vs := s.(*ast.ValueSpec)
					for i, n := range vs.Names {
						if !n.IsExported() {
							continue
						}
						var v ast.Expr
						if i < len(vs.Values) {
							v = vs.Values[i]
						}
						out[n.Name] = constDecl{value: v, imports: imports}
					}
				}
			}
		}
	}
	return out
}

// TestTmuxConstantsRedeclared checks every exported internal/tmux Call*, Fail*
// and Label* constant has a pkg/api twin declared as Tmux<Name> = tmux.<Name>.
func TestTmuxConstantsRedeclared(t *testing.T) {
	t.Parallel()
	apiConsts := parseExportedConsts(t, ".")
	tmuxConsts := parseExportedConsts(t, filepath.Join("..", "..", "internal", "tmux"))

	for _, prefix := range []string{"Call", "Fail", "Label"} {
		t.Run(prefix, func(t *testing.T) {
			var names []string
			for name := range tmuxConsts {
				if strings.HasPrefix(name, prefix) {
					names = append(names, name)
				}
			}
			sort.Strings(names)
			if len(names) == 0 {
				t.Fatalf("found no %s* constants in internal/tmux; the parse is wrong", prefix)
			}
			for _, name := range names {
				d, ok := apiConsts["Tmux"+name]
				if !ok {
					t.Errorf("pkg/api lacks Tmux%s = tmux.%s", name, name)
					continue
				}
				sel, isSel := d.value.(*ast.SelectorExpr)
				var pkgIdent *ast.Ident
				if isSel {
					pkgIdent, _ = sel.X.(*ast.Ident)
				}
				if pkgIdent == nil || d.imports[pkgIdent.Name] != tmuxImportPath || sel.Sel.Name != name {
					got := "an implicit repeat"
					if d.value != nil {
						got = types.ExprString(d.value)
					}
					t.Errorf("pkg/api declares Tmux%s as %s; want tmux.%s", name, got, name)
				}
			}
			for name := range apiConsts {
				if orig, ok := strings.CutPrefix(name, "Tmux"+prefix); ok {
					if _, found := tmuxConsts[prefix+orig]; !found {
						t.Errorf("pkg/api declares %s, which has no internal/tmux %s%s", name, prefix, orig)
					}
				}
			}
		})
	}
}
