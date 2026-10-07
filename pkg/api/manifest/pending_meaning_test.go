package manifest_test

import (
	"cmp"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// pendingSite is one manifest text that must define pending as a launch in
// progress (SR-22.1, SR-20.6): a verb's description, or one param's or result
// field's description. require and mustNot are lower-case key phrases.
type pendingSite struct {
	name    string
	verb    string
	param   string
	result  string
	require []string
	mustNot []string
}

// launchInProgress is the core of SR-22.1's one meaning of pending, shared by
// the state texts.
var launchInProgress = []string{
	"a launch (spawn, reuse or resume)",
	"in progress",
	"has not reported in",
	"resumed pending row keeps its session id and history",
}

// pendingSites lists every manifest site that states the meaning of pending;
// send-keys' allow_pending sites are in allowPendingSites.
var pendingSites = []pendingSite{
	{name: "status description", verb: "status", require: launchInProgress},
	{name: "status state", verb: "status", result: "state", require: launchInProgress},
	{name: "get state", verb: "get", result: "state", require: launchInProgress},
	{name: "list spawns", verb: "list", result: "spawns", require: []string{
		"same meaning of pending",
		"a launch (spawn, reuse or resume) in progress",
		"has not reported in",
		"resumed pending row keeps its session id and history",
	}},
	{
		name:    "spawn description",
		verb:    "spawn",
		require: []string{"the row is pending from its insert until the agent reports in"},
		mustNot: []string{"state moves from pending to waiting on the first sessionstart hook"},
	},
	{name: "resume description", verb: "resume", require: []string{
		"moves the row to pending, keeping its session id and history",
		"stays pending until the agent reports in",
		"restores the row to its prior ended or missing state",
		"the session may have been created and the row stays pending",
		"do not retry until get shows the row ended or missing",
		"a pending row (a launch in progress",
	}},
}

// TestManifestDefinesPendingAsLaunchInProgress pins SR-22.1's meaning of
// pending at every site in pendingSites (AC-DOC-17, AC-DOC-12).
func TestManifestDefinesPendingAsLaunchInProgress(t *testing.T) {
	for _, s := range pendingSites {
		t.Run(s.name, func(t *testing.T) {
			text := strings.ToLower(siteText(t, s.verb, s.param, s.result))
			for _, p := range s.require {
				if !strings.Contains(text, p) {
					t.Errorf("%s: missing %q in %q", s.name, p, text)
				}
			}
			for _, p := range s.mustNot {
				if strings.Contains(text, p) {
					t.Errorf("%s: still carries retired %q in %q", s.name, p, text)
				}
			}
		})
	}
}

// allowPendingSite is one SR-18.14 site of send-keys' allow_pending text
// (AC-DOC-14): its kind and a reader returning the raw text.
type allowPendingSite struct {
	name string
	kind apitest.AllowPendingSite
	text func(t *testing.T) string
}

// allowPendingSites lists every SR-18.14 site outside the Markdown docs.
var allowPendingSites = []allowPendingSite{
	{"manifest send-keys param allow_pending", apitest.AllowPendingFlag, func(t *testing.T) string {
		return siteText(t, "send-keys", "allow_pending", "")
	}},
	{"CLI send-keys --allow-pending usage", apitest.AllowPendingFlag, func(t *testing.T) string {
		return cliFlagUsage(t, "parseSendKeysFlags", "allow-pending")
	}},
	{"Go doc SendKeysParams.AllowPending", apitest.AllowPendingFlag, func(t *testing.T) string {
		return apiDeclDoc(t, "SendKeysParams.AllowPending")
	}},
	{"Go doc SendKeys state precondition", apitest.AllowPendingRefusals, func(t *testing.T) string {
		return docParagraph(t, apiDeclDoc(t, "SendKeys"), "State precondition:")
	}},
	{"Go doc ErrSpawnNotInteractive", apitest.AllowPendingRefusals, func(t *testing.T) string {
		return apiDeclDoc(t, "ErrSpawnNotInteractive")
	}},
	{"TypeScript golden SendKeysParams.allow_pending", apitest.AllowPendingFlag, func(t *testing.T) string {
		return tsGoldenFieldDoc(t, "SendKeysParams", "allow_pending")
	}},
}

// TestAllowPendingSitesStatePendingLaunch pins SR-18.14 at every site in
// allowPendingSites, line wrapping and comment markers removed.
func TestAllowPendingSitesStatePendingLaunch(t *testing.T) {
	for _, s := range allowPendingSites {
		t.Run(s.name, func(t *testing.T) {
			text := strings.Join(strings.Fields(s.text(t)), " ")
			apitest.AssertAgentTextCase(t, s.name, text, apitest.DescAllowPending(s.kind))
		})
	}
}

// parseGoSources parses the non-test Go sources of the directory rel to this
// file's, with comments; the one source reader of clientMethodDoc and apiDeclDoc.
func parseGoSources(t *testing.T, rel string) []*ast.File {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(thisFile), rel, "*.go"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("glob %s sources: %v (%d files)", rel, err, len(paths))
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files = append(files, f)
	}
	return files
}

// apiDeclDoc returns the doc comment of a pkg/api package-level func or var,
// or of a struct field named "Type.Field" (clientMethodDoc covers methods).
func apiDeclDoc(t *testing.T, name string) string {
	t.Helper()
	typ, field, isField := strings.Cut(name, ".")
	named := func(ids []*ast.Ident, want string) bool {
		return slices.ContainsFunc(ids, func(id *ast.Ident) bool { return id.Name == want })
	}
	var doc *ast.CommentGroup
	found := false
	for _, f := range parseGoSources(t, "..") {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if !isField && d.Recv == nil && d.Name.Name == name {
					doc, found = d.Doc, true
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.ValueSpec:
						if !isField && named(s.Names, name) {
							doc, found = cmp.Or(s.Doc, d.Doc), true
						}
					case *ast.TypeSpec:
						st, ok := s.Type.(*ast.StructType)
						if !isField || !ok || s.Name.Name != typ {
							continue
						}
						for _, fl := range st.Fields.List {
							if named(fl.Names, field) {
								doc, found = fl.Doc, true
							}
						}
					}
				}
			}
		}
	}
	if !found {
		t.Fatalf("%s not found in pkg/api", name)
	}
	if doc == nil {
		t.Fatalf("%s has no doc comment", name)
	}
	return doc.Text()
}

// docParagraph returns the paragraph of doc that starts with prefix.
func docParagraph(t *testing.T, doc, prefix string) string {
	t.Helper()
	for _, p := range strings.Split(doc, "\n\n") {
		if strings.HasPrefix(p, prefix) {
			return p
		}
	}
	t.Fatalf("no paragraph starting %q in %q", prefix, doc)
	return ""
}

// cliFlagUsage returns the usage string the CLI's fn registers for flag
// name, read from the cmd/agent-director sources.
func cliFlagUsage(t *testing.T, fn, name string) string {
	t.Helper()
	for _, f := range parseGoSources(t, filepath.Join("..", "..", "..", "cmd", "agent-director")) {
		for _, decl := range f.Decls {
			d, ok := decl.(*ast.FuncDecl)
			if !ok || d.Name.Name != fn || d.Body == nil {
				continue
			}
			usage := ""
			ast.Inspect(d.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) < 3 || stringLit(call.Args[1]) != name {
					return true
				}
				usage = stringLit(call.Args[len(call.Args)-1])
				return false
			})
			if usage == "" {
				t.Fatalf("%s registers no --%s with a usage string", fn, name)
			}
			return usage
		}
	}
	t.Fatalf("%s not found in cmd/agent-director", fn)
	return ""
}

// stringLit returns the value of a string literal expression, or "".
func stringLit(e ast.Expr) string {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return s
}

// tsDocComment matches a JSDoc comment's body, without crossing a "*/".
var tsDocComment = regexp.MustCompile(`/\*\*((?:[^*]|\*+[^*/])*)\*+/\s*`)

// tsGoldenFieldDoc returns the JSDoc text of field in interface iface of the
// committed TypeScript public-surface golden, comment markers removed.
func tsGoldenFieldDoc(t *testing.T, iface, field string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	golden := filepath.Join(filepath.Dir(thisFile), "..", "..", "ts-bun-client", "test", "fixtures", "public-surface", "types.d.ts.golden")
	raw, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read TypeScript golden: %v", err)
	}
	src := string(raw)
	start := strings.Index(src, "export interface "+iface+" {")
	if start < 0 {
		t.Fatalf("TypeScript golden has no interface %s", iface)
	}
	block, _, _ := strings.Cut(src[start:], "\n}")
	re := regexp.MustCompile(tsDocComment.String() + regexp.QuoteMeta(field) + `\??:`)
	m := re.FindStringSubmatch(block)
	if m == nil {
		t.Fatalf("TypeScript golden %s.%s has no doc comment", iface, field)
	}
	var lines []string
	for _, line := range strings.Split(m[1], "\n") {
		lines = append(lines, strings.TrimPrefix(strings.TrimSpace(line), "*"))
	}
	return strings.Join(lines, "\n")
}

// retiredPendingConcepts matches the concepts SR-20.6 retires, in hyphen and
// space spellings (twin: tools/gen-docs/main_test.go).
var retiredPendingConcepts = regexp.MustCompile(`(?i)resume[-\s]?starting|young[-\s]claim|launched[-\s]mark|claim[-\s]withdrawal`)

// resumedStaysFinished matches a sentence saying a row stays ended, missing
// or terminal until SessionStart (the pre-SR-22.1 resume model).
var resumedStaysFinished = regexp.MustCompile(`(?i)\b(stays?|staying|remains?|remaining|keeps?|kept|left)\b[^.]*\b(ended|missing|terminal)\b[^.]*\buntil\b[^.]*sessionstart`)

// TestManifestNamesNoRetiredPendingConcept checks every string the manifest
// carries (names, types, allowed values and texts; surface.json mirrors them)
// for a retired concept or a resumed row left finished.
func TestManifestNamesNoRetiredPendingConcept(t *testing.T) {
	strs := manifestStrings()
	for name, re := range map[string]*regexp.Regexp{
		"retired concept": retiredPendingConcepts, "resumed row stays finished until SessionStart": resumedStaysFinished,
	} {
		for site, text := range strs {
			if m := re.FindString(text); m != "" {
				t.Errorf("%s: %s %q", site, name, m)
			}
		}
	}
}
