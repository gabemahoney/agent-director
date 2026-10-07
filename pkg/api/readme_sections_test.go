package api_test

// readme_sections_test.go checks the README sections that descriptions and
// docs point to (SR-1.4, SR-18.1, SR-18.17): the "Operator actions" and
// caller-contract headings exist exactly once, every pointer to a README
// section by title names one, every relative link names an existing path and
// every anchored link resolves to exactly one heading. It holds the one
// Markdown parser (mdDoc) every README check uses.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// Paths as seen from pkg/api/, where the test runs, and the caller-contract
// headings (SR-18.1): the README summary and the architecture doc's full
// contract it links to.
const (
	mdRepoRoot                 = "../.."
	mdTopREADME                = "../../README.md"
	mdArchitecture             = "../../docs/architecture.md"
	callerContractTitle        = "Caller contract"
	callerContractClassesTitle = "Caller contract: tmux refusal classes"
)

// mdHeading is one ATX heading of a Markdown file; line is its 0-based index.
type mdHeading struct {
	level int
	title string
	line  int
}

// mdDoc is a Markdown file split into lines, with its headings outside
// fenced code blocks.
type mdDoc struct {
	path     string
	lines    []string
	headings []mdHeading
}

var (
	mdHeadingRe = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+(.*?)(?:[ \t]+#+)?[ \t]*$`)
	mdFenceRe   = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
	mdLinkRe    = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
)

// readMD parses path's headings, skipping fenced code blocks.
func readMD(t testing.TB, path string) mdDoc {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	d := mdDoc{path: path, lines: strings.Split(string(data), "\n")}
	fence := ""
	for i, line := range d.lines {
		if m := mdFenceRe.FindStringSubmatch(line); m != nil {
			switch {
			case fence == "":
				fence = m[1]
			case m[1][0] == fence[0] && len(m[1]) >= len(fence):
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		if m := mdHeadingRe.FindStringSubmatch(line); m != nil && m[2] != "" {
			d.headings = append(d.headings, mdHeading{level: len(m[1]), title: m[2], line: i})
		}
	}
	return d
}

// titled returns the headings whose title is exactly title.
func (d mdDoc) titled(title string) []mdHeading {
	return d.where(func(h mdHeading) bool { return h.title == title })
}

// anchored returns the headings whose GitHub anchor is anchor.
func (d mdDoc) anchored(anchor string) []mdHeading {
	return d.where(func(h mdHeading) bool { return mdAnchor(h.title) == anchor })
}

func (d mdDoc) where(keep func(mdHeading) bool) []mdHeading {
	var out []mdHeading
	for _, h := range d.headings {
		if keep(h) {
			out = append(out, h)
		}
	}
	return out
}

// body returns h's section text, up to the next heading of its level or higher.
func (d mdDoc) body(h mdHeading) string {
	end := len(d.lines)
	for _, n := range d.headings {
		if n.line > h.line && n.level <= h.level {
			end = n.line
			break
		}
	}
	return strings.Join(d.lines[h.line+1:end], "\n")
}

// nearMiss names a heading equal to title but for case or spacing, if any.
func (d mdDoc) nearMiss(title string) string {
	fold := func(s string) string { return strings.ToLower(normalised(s)) }
	for _, h := range d.headings {
		if h.title != title && fold(h.title) == fold(title) {
			return h.title
		}
	}
	return ""
}

// mdAnchor is GitHub's anchor for a heading title: lower case, spaces to
// hyphens, punctuation other than '-' and '_' dropped.
func mdAnchor(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// TestREADMESectionHeadings checks each pointed-to section heading exists
// exactly once in its file (SR-18.1, SR-18.17), and that the first sentence of
// "Operator actions" says automated callers must not perform its actions.
func TestREADMESectionHeadings(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ path, title string }{
		{mdTopREADME, apitest.OperatorActionsTitle},
		{mdTopREADME, callerContractTitle},
		{mdArchitecture, callerContractClassesTitle},
	} {
		if n := len(readMD(t, c.path).titled(c.title)); n != 1 {
			t.Errorf("%s has %d headings titled %q; want exactly one", c.path, n, c.title)
		}
	}
	d := readMD(t, mdTopREADME)
	first, _, _ := strings.Cut(normalised(d.body(operatorActions(t, d))), ". ")
	for _, want := range []string{"automated callers", "must not perform"} {
		if !strings.Contains(first, want) {
			t.Errorf("%s %q first sentence %q lacks %q", d.path, apitest.OperatorActionsTitle, first, want)
		}
	}
}

// readmePointer is one place that points to a top-level README section: by
// quoted title, or (byAnchor) as "README's <anchor> summary".
type readmePointer struct {
	source   string
	title    string
	byAnchor bool
}

var (
	pointerTitleFirstRe  = regexp.MustCompile(`"([^"\n]{1,80})"\s+in\s+the\s+(?:[\w-]+\s+)?README\b`)
	pointerREADMEFirstRe = regexp.MustCompile(`\bREADME(?:'s)?\s+(?:section\s+)?"([^"\n]{1,80})"`)
	pointerAnchorRe      = regexp.MustCompile(`\bREADME's\s+([a-z0-9]+(?:-[a-z0-9]+)+)\s+summary\b`)
	goCommentWrapRe      = regexp.MustCompile(`\n[ \t]*//[ \t]*`)
)

// pointersIn returns text's README section pointers, attributed to source.
func pointersIn(source, text string) []readmePointer {
	var out []readmePointer
	for _, re := range []*regexp.Regexp{pointerTitleFirstRe, pointerREADMEFirstRe} {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			out = append(out, readmePointer{source: source, title: m[1]})
		}
	}
	for _, m := range pointerAnchorRe.FindAllStringSubmatch(text, -1) {
		out = append(out, readmePointer{source: source, title: m[1], byAnchor: true})
	}
	return out
}

// walkGoSources calls fn with each non-test Go source under pkg/, internal/
// and cmd/, by repo-relative path, wrapped comments joined and \" unescaped.
func walkGoSources(t *testing.T, fn func(rel, text string)) {
	t.Helper()
	for _, dir := range []string{"pkg", "internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(mdRepoRoot, dir), func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() && (e.Name() == "node_modules" || e.Name() == "testdata") {
				return filepath.SkipDir
			}
			if e.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(mdRepoRoot, path)
			fn(rel, strings.ReplaceAll(goCommentWrapRe.ReplaceAllString(string(data), " "), `\"`, `"`))
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
}

// eachManifestText calls fn with every manifest text (each verb's
// Description, param and result-field texts), named by its source.
func eachManifestText(fn func(source, text string)) {
	for _, v := range manifest.Verbs {
		fn("manifest "+v.Name+" Description", v.Description)
		for _, p := range v.Params {
			fn("manifest "+v.Name+" param "+p.Name, p.Description)
		}
		for _, f := range v.ResultFields {
			fn("manifest "+v.Name+" result field "+f.Name, f.Description)
		}
	}
}

// collectREADMEPointers gathers pointers from non-test Go sources
// (walkGoSources), every manifest text, and the title runtime descriptions
// carry via PointsToOperatorActions.
func collectREADMEPointers(t *testing.T) []readmePointer {
	t.Helper()
	ptrs := []readmePointer{{source: "apitest.OperatorActionsTitle", title: apitest.OperatorActionsTitle}}
	collect := func(source, text string) { ptrs = append(ptrs, pointersIn(source, text)...) }
	walkGoSources(t, collect)
	eachManifestText(collect)
	return ptrs
}

// TestREADMEPointersNameExistingSections checks every pointer to a README
// section names a heading the README has exactly once (SR-1.4, SR-18.17).
func TestREADMEPointersNameExistingSections(t *testing.T) {
	t.Parallel()
	d := readMD(t, mdTopREADME)
	ptrs := collectREADMEPointers(t)
	for _, p := range ptrs {
		hs, kind := d.titled(p.title), "section"
		if p.byAnchor {
			hs, kind = d.anchored(p.title), "section anchor"
		}
		if len(hs) != 1 {
			hint := ""
			if near := d.nearMiss(p.title); near != "" {
				hint = fmt.Sprintf("; differs from heading %q only in case or spacing", near)
			}
			t.Errorf("%s: points to README %s %q, which %s has %d times; want exactly one%s", p.source, kind, p.title, d.path, len(hs), hint)
		}
	}

	// The collector must find the known pointer sites, or the check is vacuous.
	for _, want := range []struct{ source, title string }{
		{"pkg/api/kill.go", apitest.OperatorActionsTitle},
		{"pkg/api/kill.go", callerContractTitle},
		{"pkg/api/lookup_outcome.go", apitest.OperatorActionsTitle},
		{"pkg/api/unusable_name.go", apitest.OperatorActionsTitle},
		{"pkg/api/resume.go", apitest.OperatorActionsTitle},
		{"pkg/api/spawn_scan.go", apitest.OperatorActionsTitle},
		{"manifest kill Description", apitest.OperatorActionsTitle},
		{"manifest spawn Description", apitest.OperatorActionsTitle},
	} {
		found := false
		for _, p := range ptrs {
			found = found || p.source == want.source && (p.title == want.title || p.byAnchor && p.title == mdAnchor(want.title))
		}
		if !found {
			t.Errorf("no pointer to README section %q found in %s; the collector missed a known site", want.title, want.source)
		}
	}
}

// TestREADMEAnchorLinksResolve checks every relative Markdown link in the two
// READMEs names an existing path, and every anchored .md link resolves to
// exactly one heading, the caller-contract links included.
func TestREADMEAnchorLinksResolve(t *testing.T) {
	t.Parallel()
	type link struct{ from, to, anchor string }
	var links []link
	for _, from := range []string{mdTopREADME, "README.md"} {
		ms := mdLinkRe.FindAllStringSubmatch(strings.Join(readMD(t, from).lines, "\n"), -1)
		if len(ms) == 0 {
			t.Fatalf("no Markdown links found in %s; the link regex may be broken", from)
		}
		for _, m := range ms {
			target, anchor, anchored := strings.Cut(m[2], "#")
			if strings.Contains(target, "://") {
				continue
			}
			to := from
			if target != "" {
				to = filepath.Join(filepath.Dir(from), target)
				if _, err := os.Stat(to); err != nil {
					t.Errorf("%s: link [%s](%s) names no existing path: %v", from, m[1], m[2], err)
				}
			}
			if anchored && strings.HasSuffix(to, ".md") {
				links = append(links, link{from, filepath.Clean(to), anchor})
			}
		}
	}
	docs := map[string]mdDoc{}
	for _, l := range links {
		if _, ok := docs[l.to]; !ok {
			docs[l.to] = readMD(t, l.to)
		}
		if n := len(docs[l.to].anchored(l.anchor)); n != 1 {
			t.Errorf("%s: link to %s#%s matches %d headings; want exactly one", l.from, l.to, l.anchor, n)
		}
	}

	for _, want := range []link{
		{mdTopREADME, filepath.Clean(mdArchitecture), mdAnchor(callerContractClassesTitle)},
		{"README.md", filepath.Clean(mdTopREADME), mdAnchor(callerContractTitle)},
	} {
		found := false
		for _, l := range links {
			found = found || l == want
		}
		if !found {
			t.Errorf("%s has no link to %s#%s", want.from, want.to, want.anchor)
		}
	}
}
