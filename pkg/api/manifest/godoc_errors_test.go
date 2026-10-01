package manifest_test

import (
	"go/ast"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// godoc_errors_test.go holds the shared check that a (*api.Client) method's
// Go doc "Errors:" list names exactly its verb's manifest ErrorNames:
// assertGoDocErrorsMatchManifest. kill's list is its first user
// (TestKillHasSRDErrorNames); the later verb Epics (11, 13, 16) check their
// verbs' "Errors:" lists through assertGoDocErrorsMatchManifest instead of
// writing their own. clientGoDocProse gives a method's prose outside
// "Errors:" for the Go doc statement checks (kill's, then Epic 19's per-verb
// unusable-name trigger).

// goDocErrorBullet matches an "Errors:" bullet head: "- [ErrX]:" or "- ErrX:".
var goDocErrorBullet = regexp.MustCompile(`^- \[?(Err[A-Za-z0-9]+)\]?:`)

// clientMethodDoc returns the doc comment text of (*api.Client).<method>,
// parsed from the non-test Go sources of pkg/api.
func clientMethodDoc(t *testing.T, method string) string {
	t.Helper()
	for _, f := range parseGoSources(t, "..") {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != method || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			if id, ok := star.X.(*ast.Ident); ok && id.Name == "Client" {
				if fn.Doc == nil {
					t.Fatalf("(*Client).%s has no doc comment", method)
				}
				return fn.Doc.Text()
			}
		}
	}
	t.Fatalf("(*Client).%s not found in pkg/api", method)
	return ""
}

// splitGoDocErrors splits a doc comment into its "Errors:" block (the
// "Errors:" line up to the next unindented line) and the prose outside it.
func splitGoDocErrors(doc string) (errorsBlock, prose string) {
	var block, rest []string
	in := false
	for _, line := range strings.Split(doc, "\n") {
		switch {
		case strings.TrimSpace(line) == "Errors:":
			in = true
		case in && line != "" && line[0] != ' ' && line[0] != '\t':
			in = false
		}
		if in {
			block = append(block, line)
		} else {
			rest = append(rest, line)
		}
	}
	return strings.Join(block, "\n"), strings.Join(rest, "\n")
}

// goDocErrorNames returns the names heading the bullets of an "Errors:"
// block; a bullet not headed by an error name fails the test.
func goDocErrorNames(t *testing.T, errorsBlock string) []string {
	t.Helper()
	var names []string
	for _, line := range strings.Split(errorsBlock, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		m := goDocErrorBullet.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("Errors: bullet not headed by an error name: %q", line)
			continue
		}
		names = append(names, m[1])
	}
	return names
}

// goDocErrorBulletText returns the whitespace-collapsed text of the bullet
// headed by name in (*api.Client).<method>'s "Errors:" block; a missing
// bullet fails the test.
func goDocErrorBulletText(t *testing.T, method, name string) string {
	t.Helper()
	block, _ := splitGoDocErrors(clientMethodDoc(t, method))
	var words []string
	in := false
	for _, line := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") {
			m := goDocErrorBullet.FindStringSubmatch(trimmed)
			in = m != nil && m[1] == name
		}
		if in {
			words = append(words, strings.Fields(trimmed)...)
		}
	}
	if len(words) == 0 {
		t.Fatalf("(*Client).%s Errors: has no %s bullet", method, name)
	}
	return strings.Join(words, " ")
}

// assertGoDocErrorsMatchManifest fails unless the "Errors:" block of
// (*api.Client).<method> names exactly verb's manifest ErrorNames, once each.
func assertGoDocErrorsMatchManifest(t *testing.T, method, verb string) {
	t.Helper()
	v, ok := manifest.Lookup(verb)
	if !ok {
		t.Fatalf("%s not in manifest", verb)
	}
	block, _ := splitGoDocErrors(clientMethodDoc(t, method))
	if block == "" {
		t.Fatalf("(*Client).%s doc has no Errors: block", method)
	}
	got := goDocErrorNames(t, block)
	want := append([]string(nil), v.ErrorNames...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("(*Client).%s Errors: names %v; manifest %s.ErrorNames %v", method, got, verb, want)
	}
}

// clientGoDocProse returns (*api.Client).<method>'s Go doc prose outside
// "Errors:" and its CLI: line, line wrapping rejoined.
func clientGoDocProse(t *testing.T, method string) string {
	t.Helper()
	_, prose := splitGoDocErrors(clientMethodDoc(t, method))
	var lines []string
	for _, line := range strings.Split(prose, "\n") {
		if !strings.HasPrefix(line, "CLI:") {
			lines = append(lines, line)
		}
	}
	return strings.Join(strings.Fields(strings.Join(lines, "\n")), " ")
}
