package api_test

// aliases_test.go covers the alias round-trip property introduced in the k7
// phase: api.Spawn, api.PermissionRow, and api.ListFilters are type aliases
// for internal/store types, so they are assignment-compatible and share
// identical field layouts without any explicit conversion.
//
// It also covers expire's api.ExpireCandidate alias and api.ExpireTmux
// implementers, and SRD Appendix F.3's internal/tmux re-exports: the eight Tmux*
// type aliases, the start-time reader alias api.ProcChecker, and the structural rule that every internal/tmux Call*, Fail*
// and Label* constant is re-declared in pkg/api as Tmux<Name> = tmux.<Name>.

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
	"runtime"
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

// TestSpawnAliasRoundTrip proves that api.Spawn and store.Spawn are the same
// type: a value of one can be assigned directly to the other and all fields
// are preserved across the round-trip.
func TestSpawnAliasRoundTrip(t *testing.T) {
	orig := store.Spawn{
		ClaudeInstanceID: "alias-test-id",
		State:            store.StateWaiting,
		TmuxSessionName:  "alias-sess",
		RelayMode:        "off",
	}

	// store.Spawn → api.Spawn (no conversion syntax needed for type aliases)
	var asAPI api.Spawn = orig
	if asAPI.ClaudeInstanceID != orig.ClaudeInstanceID {
		t.Errorf("ClaudeInstanceID: got %q; want %q", asAPI.ClaudeInstanceID, orig.ClaudeInstanceID)
	}
	if asAPI.State != orig.State {
		t.Errorf("State: got %q; want %q", asAPI.State, orig.State)
	}

	// api.Spawn → store.Spawn (reverse direction)
	var asStore store.Spawn = asAPI
	if asStore.TmuxSessionName != orig.TmuxSessionName {
		t.Errorf("TmuxSessionName: got %q; want %q", asStore.TmuxSessionName, orig.TmuxSessionName)
	}
	if asStore.RelayMode != orig.RelayMode {
		t.Errorf("RelayMode: got %q; want %q", asStore.RelayMode, orig.RelayMode)
	}
}

// TestPermissionRowAliasRoundTrip proves api.PermissionRow ↔ store.PermissionRow
// interop: direct assignment works in both directions without conversion.
func TestPermissionRowAliasRoundTrip(t *testing.T) {
	orig := store.PermissionRow{
		ClaudeInstanceID: "perm-alias-id",
		ToolName:         "Bash",
		ToolInput:        `{"cmd":"echo"}`,
	}

	// store.PermissionRow → api.PermissionRow
	var asAPI api.PermissionRow = orig
	if asAPI.ClaudeInstanceID != orig.ClaudeInstanceID {
		t.Errorf("ClaudeInstanceID: got %q; want %q", asAPI.ClaudeInstanceID, orig.ClaudeInstanceID)
	}
	if asAPI.ToolName != orig.ToolName {
		t.Errorf("ToolName: got %q; want %q", asAPI.ToolName, orig.ToolName)
	}
	if asAPI.ToolInput != orig.ToolInput {
		t.Errorf("ToolInput: got %q; want %q", asAPI.ToolInput, orig.ToolInput)
	}

	// api.PermissionRow → store.PermissionRow (reverse direction)
	var asStore store.PermissionRow = asAPI
	if asStore.ClaudeInstanceID != orig.ClaudeInstanceID {
		t.Errorf("round-trip ClaudeInstanceID: got %q; want %q",
			asStore.ClaudeInstanceID, orig.ClaudeInstanceID)
	}
}

// TestListFiltersAliasRoundTrip proves api.ListFilters ↔ store.ListFilters
// interop: direct assignment works and field values are preserved.
func TestListFiltersAliasRoundTrip(t *testing.T) {
	orig := store.ListFilters{
		State:  []string{store.StateWaiting, store.StateWorking},
		Parent: "parent-id",
		Limit:  10,
	}

	// store.ListFilters → api.ListFilters
	var asAPI api.ListFilters = orig
	if asAPI.Parent != orig.Parent {
		t.Errorf("Parent: got %q; want %q", asAPI.Parent, orig.Parent)
	}
	if asAPI.Limit != orig.Limit {
		t.Errorf("Limit: got %d; want %d", asAPI.Limit, orig.Limit)
	}
	if len(asAPI.State) != len(orig.State) {
		t.Errorf("State len: got %d; want %d", len(asAPI.State), len(orig.State))
	}

	// api.ListFilters → store.ListFilters (reverse direction)
	var asStore store.ListFilters = asAPI
	if asStore.Parent != orig.Parent {
		t.Errorf("round-trip Parent: got %q; want %q", asStore.Parent, orig.Parent)
	}
	if asStore.Limit != orig.Limit {
		t.Errorf("round-trip Limit: got %d; want %d", asStore.Limit, orig.Limit)
	}
}

// TestExpireCandidateAliasRoundTrip proves api.ExpireCandidate is store.ExpireCandidate
// itself: assignment works both ways and every field, Identity and Snapshot included, survives.
func TestExpireCandidateAliasRoundTrip(t *testing.T) {
	if a, s := reflect.TypeFor[api.ExpireCandidate](), reflect.TypeFor[store.ExpireCandidate](); a != s {
		t.Fatalf("api.ExpireCandidate is %s.%s; want the alias of store.ExpireCandidate", a.PkgPath(), a.Name())
	}
	orig := store.ExpireCandidate{
		ClaudeInstanceID: "exp-alias-id",
		TmuxSessionName:  "exp-alias-sess",
		PID:              4242,
		ProcStarttime:    "1700000000",
		Identity: store.LaunchIdentity{
			Token: "0123456789abcdef", Socket: "/tmp/exp-alias.sock", ServerPID: 77,
			ServerStart: 1700000001, ServerStarttime: "1700000002",
			PaneID: "%3", PanePID: 4241, PaneStarttime: "1700000003",
		},
		Snapshot: store.RowSnapshot{
			RowVersion: 5, StartedAt: "2026-09-30 12:00:00", ClaudeSessionID: "sess-uuid",
			PID: 4242, ProcStarttime: "1700000000", TmuxSessionName: "exp-alias-sess",
		},
	}
	v := reflect.ValueOf(orig)
	for i := range v.NumField() {
		if v.Field(i).IsZero() {
			t.Fatalf("fixture leaves ExpireCandidate.%s zero; set every field", v.Type().Field(i).Name)
		}
	}

	var asAPI api.ExpireCandidate = orig
	var asStore store.ExpireCandidate = asAPI
	if !reflect.DeepEqual(asAPI, orig) || !reflect.DeepEqual(asStore, orig) {
		t.Errorf("round-trip changed the candidate: api %+v, store %+v; want %+v", asAPI, asStore, orig)
	}
}

// TestTmuxAliasesAreIdentical checks each F.3 Tmux* alias and api.ProcChecker is the
// internal/tmux type itself, not a new defined type (so errors.As and assignment interoperate).
func TestTmuxAliasesAreIdentical(t *testing.T) {
	cases := []struct {
		name      string
		api, orig reflect.Type
	}{
		{"TmuxLookupAnswer", reflect.TypeFor[api.TmuxLookupAnswer](), reflect.TypeFor[tmux.LookupAnswer]()},
		{"TmuxSession", reflect.TypeFor[api.TmuxSession](), reflect.TypeFor[tmux.Session]()},
		{"TmuxPane", reflect.TypeFor[api.TmuxPane](), reflect.TypeFor[tmux.Pane]()},
		{"TmuxLabel", reflect.TypeFor[api.TmuxLabel](), reflect.TypeFor[tmux.Label]()},
		{"TmuxCreateReply", reflect.TypeFor[api.TmuxCreateReply](), reflect.TypeFor[tmux.CreateReply]()},
		{"TmuxCall", reflect.TypeFor[api.TmuxCall](), reflect.TypeFor[tmux.Call]()},
		{"TmuxFailure", reflect.TypeFor[api.TmuxFailure](), reflect.TypeFor[tmux.Failure]()},
		{"TmuxCallError", reflect.TypeFor[api.TmuxCallError](), reflect.TypeFor[tmux.CallError]()},
		{"ProcChecker", reflect.TypeFor[api.ProcChecker](), reflect.TypeFor[tmux.ProcChecker]()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.api != tc.orig {
				t.Errorf("api.%s is %s.%s; want the alias of %s.%s",
					tc.name, tc.api.PkgPath(), tc.api.Name(), tc.orig.PkgPath(), tc.orig.Name())
			}
		})
	}
}

// TestTmuxSentinelAliases checks each pkg/api tmux sentinel is the internal/tmux
// sentinel itself, so a wrapped one matches it under errors.Is and classifies to its name.
func TestTmuxSentinelAliases(t *testing.T) {
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
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	apiDir := filepath.Dir(thisFile)
	apiConsts := parseExportedConsts(t, apiDir)
	tmuxConsts := parseExportedConsts(t, filepath.Join(apiDir, "..", "..", "internal", "tmux"))

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
