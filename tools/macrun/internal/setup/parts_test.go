package setup

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/treefix"
)

// partViews describes parts as comparable strings, one per part:
// <line> <name> <suite> <selection> | <text> => <dir>[ ~ <regexp>] ...
func partViews(parts []Part) []string {
	var views []string
	for _, p := range parts {
		view := fmt.Sprintf("%d %s %s %s |", p.Line, p.Name, p.Suite, p.Selection)
		for _, s := range p.Selectors {
			view += fmt.Sprintf(" %s => %s", s.Text, s.Dir)
			if s.Pattern != nil {
				view += " ~ " + s.Pattern.String()
			}
		}
		views = append(views, view)
	}
	return views
}

func TestReadPartsCollectsEveryLineError(t *testing.T) {
	t.Parallel()
	root := treefix.Write(t, map[string]string{PartsFile: strings.Join([]string{
		"# parts",
		"good\tgo\tpkg/a",
		"short\tgo",
		"four\tgo\tpkg/a\t2",
		"unk\tpython\tpkg/a",
		"tp\ttestplan\tharness-smoke",
		"empty\tgo\t",
		"rest\tgo\trest",
		"\tgo\tpkg/a",
		"good\tgo\tpkg/a",
		"bad/name\tpython\tpkg/a",
		`dirs` + "\tgo\t" + `/abs ./pkg pkg/../x pkg/testdata/x pkg/.hidden _tools/x pkg/ pkg/... pkg\a`,
		"res\tgo\tpkg/a: pkg/a:Test(",
		"spaces\tgo\tpkg/a  pkg/b",
		"last\tgo\tpkg/c",
	}, "\n")})
	parts, errs, err := ReadParts(root)
	if err != nil {
		t.Fatal(err)
	}
	const clean = "; use a clean slash-separated path relative to the checkout root"
	at := func(line int, message string) model.SetupError {
		return model.SetupError{File: "test/mac/parts.txt", Line: line, Message: message}
	}
	wantErrs := []model.SetupError{
		at(3, `has 2 fields; expected <part name>\t<suite>\t<selection>`),
		at(4, `has 4 fields; a fourth field (test binaries at once) isn't supported yet, so use <part name>\t<suite>\t<selection>`),
		at(5, `unknown suite "python"; use go`),
		at(6, `suite "testplan" isn't supported yet; use go`),
		at(7, `selection is empty; use <package dir> or <package dir>:<regexp> selectors separated by single spaces`),
		at(8, `selection "rest" isn't supported yet; name package dirs`),
		at(9, `part name is empty; use letters, digits, ., _ or -`),
		at(10, `part name "good" is already used on line 2; part names must be unique`),
		at(11, `part name "bad/name" uses characters outside [A-Za-z0-9._-]`),
		at(11, `unknown suite "python"; use go`),
		at(12, `selector "/abs": package dir is absolute`+clean),
		at(12, `selector "./pkg": package dir has an empty, . or .. element`+clean),
		at(12, `selector "pkg/../x": package dir has an empty, . or .. element`+clean),
		at(12, `selector "pkg/testdata/x": package dir has a testdata element, which go test ./... skips`),
		at(12, `selector "pkg/.hidden": package dir has an element starting with . or _, which go test ./... skips`),
		at(12, `selector "_tools/x": package dir has an element starting with . or _, which go test ./... skips`),
		at(12, `selector "pkg/": package dir ends in /`+clean),
		at(12, `selector "pkg/...": package dir ends in ...; name each package dir`),
		at(12, `selector "pkg\\a": package dir uses \`+clean),
		at(13, `selector "pkg/a:": the regexp after : is empty; write <package dir> to select the whole package`),
		at(13, "selector \"pkg/a:Test(\": the regexp doesn't compile: error parsing regexp: missing closing ): `Test(`"),
		at(14, `selection has an empty selector; separate selectors with single spaces`),
	}
	if !reflect.DeepEqual(errs, wantErrs) {
		t.Errorf("errors:\n%s\nwant:\n%s", errorLines(errs), errorLines(wantErrs))
	}
	wantParts := []string{"2 good go pkg/a | pkg/a => pkg/a", "15 last go pkg/c | pkg/c => pkg/c"}
	if got := partViews(parts); !reflect.DeepEqual(got, wantParts) {
		t.Errorf("parts = %q; want %q", got, wantParts)
	}
}

func TestReadPartsWholeFile(t *testing.T) {
	t.Parallel()
	partLines := func(n int) string {
		var b strings.Builder
		for i := range n {
			fmt.Fprintf(&b, "p%d\tgo\tpkg/a\n", i)
		}
		return b.String()
	}
	noPart := []model.SetupError{{File: "test/mac/parts.txt", Message: `lists no part; add a line <part name>\t<suite>\t<selection>`}}
	for _, tc := range []struct {
		name      string
		content   *string
		wantParts int
		wantErrs  []model.SetupError
	}{
		{name: "missing", wantErrs: noPart},
		{name: "comments only", content: ptr("# none yet\n\n"), wantErrs: noPart},
		{name: "256 parts", content: ptr(partLines(256)), wantParts: 256},
		{
			name: "257 parts", content: ptr(partLines(257)), wantParts: 257,
			wantErrs: []model.SetupError{{File: "test/mac/parts.txt", Message: "lists 257 parts; GitHub runs at most 256 jobs from one matrix"}},
		},
	} {
		files := map[string]string{}
		if tc.content != nil {
			files[PartsFile] = *tc.content
		}
		parts, errs, err := ReadParts(treefix.Write(t, files))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(parts) != tc.wantParts || !reflect.DeepEqual(errs, tc.wantErrs) {
			t.Errorf("%s: %d parts, errors %+v; want %d parts, errors %+v", tc.name, len(parts), errs, tc.wantParts, tc.wantErrs)
		}
	}
}

func TestReadPartsValid(t *testing.T) {
	t.Parallel()
	root := treefix.Write(t, map[string]string{PartsFile: "zz\tgo\tpkg/a:^Test(A|B):x$ pkg/a:^TestC$\n" +
		"# between\n" +
		"a.1_-\tgo\tpkg/b pkg/c/d:Example\n"})
	parts, errs, err := ReadParts(root)
	if err != nil || errs != nil {
		t.Fatalf("ReadParts errors = %+v, %v; want none", errs, err)
	}
	want := []string{
		"1 zz go pkg/a:^Test(A|B):x$ pkg/a:^TestC$ | pkg/a:^Test(A|B):x$ => pkg/a ~ ^Test(A|B):x$ pkg/a:^TestC$ => pkg/a ~ ^TestC$",
		"3 a.1_- go pkg/b pkg/c/d:Example | pkg/b => pkg/b pkg/c/d:Example => pkg/c/d ~ Example",
	}
	if got := partViews(parts); !reflect.DeepEqual(got, want) {
		t.Errorf("parts:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func errorLines(errs []model.SetupError) string {
	var lines []string
	for _, e := range errs {
		lines = append(lines, e.Error())
	}
	return strings.Join(lines, "\n")
}

func ptr(s string) *string { return &s }
