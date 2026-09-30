package api_test

// readme_sections_test.go checks the README sections that descriptions and
// docs point to (SR-1.4, SR-18.1, SR-18.17): the "Operator actions" and
// caller-contract headings exist exactly once, every pointer to a README
// section by title names one, and every anchored link resolves to exactly one
// heading. Later Epics (Epic 19's removal procedure included) add their
// section checks here on the shared mdDoc parser.

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

// Paths as seen from pkg/api/, where the test runs.
const (
	mdRepoRoot     = "../.."
	mdTopREADME    = "../../README.md"
	mdArchitecture = "../../docs/architecture.md"
)

// The caller-contract headings (SR-18.1): the README summary and the
// architecture doc's full contract it links to.
const (
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
	var out []mdHeading
	for _, h := range d.headings {
		if h.title == title {
			out = append(out, h)
		}
	}
	return out
}

// anchored returns the headings whose GitHub anchor is anchor.
func (d mdDoc) anchored(anchor string) []mdHeading {
	var out []mdHeading
	for _, h := range d.headings {
		if mdAnchor(h.title) == anchor {
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
	fold := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
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
// exactly once in its file (SR-18.1, SR-18.17).
func TestREADMESectionHeadings(t *testing.T) {
	for _, c := range []struct{ path, title string }{
		{mdTopREADME, apitest.OperatorActionsTitle},
		{mdTopREADME, callerContractTitle},
		{mdArchitecture, callerContractClassesTitle},
	} {
		if n := len(readMD(t, c.path).titled(c.title)); n != 1 {
			t.Errorf("%s has %d headings titled %q; want exactly one", c.path, n, c.title)
		}
	}
}

// TestREADMEOperatorActionsForHumansOnly checks the section's first sentence
// says automated callers must not perform its actions (SR-18.17).
func TestREADMEOperatorActionsForHumansOnly(t *testing.T) {
	d := readMD(t, mdTopREADME)
	hs := d.titled(apitest.OperatorActionsTitle)
	if len(hs) == 0 {
		t.Fatalf("%s has no heading titled %q", d.path, apitest.OperatorActionsTitle)
	}
	text := strings.Join(strings.Fields(d.body(hs[0])), " ")
	first, _, _ := strings.Cut(text, ". ")
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

// collectREADMEPointers gathers pointers from non-test Go sources under pkg/,
// internal/ and cmd/ (wrapped comments joined, \" unescaped), every manifest
// text, and the title runtime descriptions carry via PointsToOperatorActions.
func collectREADMEPointers(t *testing.T) []readmePointer {
	t.Helper()
	ptrs := []readmePointer{{source: "apitest.OperatorActionsTitle", title: apitest.OperatorActionsTitle}}
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
			text := strings.ReplaceAll(goCommentWrapRe.ReplaceAllString(string(data), " "), `\"`, `"`)
			rel, _ := filepath.Rel(mdRepoRoot, path)
			ptrs = append(ptrs, pointersIn(rel, text)...)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	for _, v := range manifest.Verbs {
		ptrs = append(ptrs, pointersIn("manifest "+v.Name+" Description", v.Description)...)
		for _, p := range v.Params {
			ptrs = append(ptrs, pointersIn("manifest "+v.Name+" param "+p.Name, p.Description)...)
		}
		for _, f := range v.ResultFields {
			ptrs = append(ptrs, pointersIn("manifest "+v.Name+" result field "+f.Name, f.Description)...)
		}
	}
	return ptrs
}

// TestREADMEPointersNameExistingSections checks every pointer to a README
// section names a heading the README has exactly once (SR-1.4, SR-18.17).
func TestREADMEPointersNameExistingSections(t *testing.T) {
	d := readMD(t, mdTopREADME)
	ptrs := collectREADMEPointers(t)
	for _, p := range ptrs {
		hs := d.titled(p.title)
		if p.byAnchor {
			hs = d.anchored(p.title)
		}
		if len(hs) == 1 {
			continue
		}
		hint := ""
		if near := d.nearMiss(p.title); near != "" {
			hint = fmt.Sprintf("; differs from heading %q only in case or spacing", near)
		}
		kind := "section"
		if p.byAnchor {
			kind = "section anchor"
		}
		t.Errorf("%s: points to README %s %q, which %s has %d times; want exactly one%s", p.source, kind, p.title, d.path, len(hs), hint)
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

// TestREADMEAnchorLinksResolve checks every anchored Markdown link in the two
// READMEs resolves to exactly one heading, the caller-contract links included.
func TestREADMEAnchorLinksResolve(t *testing.T) {
	type link struct{ from, to, anchor string }
	var links []link
	for _, from := range []string{mdTopREADME, "README.md"} {
		d := readMD(t, from)
		for _, m := range mdLinkRe.FindAllStringSubmatch(strings.Join(d.lines, "\n"), -1) {
			target, anchor, ok := strings.Cut(m[2], "#")
			if !ok || strings.Contains(target, "://") {
				continue
			}
			to := from
			if target != "" {
				to = filepath.Join(filepath.Dir(from), target)
			}
			if strings.HasSuffix(to, ".md") {
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
