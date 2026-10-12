package api_test

// old_path_test.go is SR-11.7's old-path test: the old directory's name,
// ".agent-director", appears as a whole path element nowhere in what ships
// (non-test Go, the TS client's sources, shell scripts outside test
// directories, the Makefile, README.md, docs/*.md, the two package READMEs and
// the install skill's SKILL.md, frontmatter and code blocks included) but in
// the exceptions below. docs/*-guide.md, CLAUDE.md and .claude/skills
// are not scanned. The walker and the exception mechanism are in
// source_scan_test.go.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oldPathName is the old directory's name under HOME.
const oldPathName = ".agent-director"

// installSkillMD is the install skill's SKILL.md: shipped product, scanned in
// full (frontmatter and fenced blocks included) by the old-path and old-names
// tests.
const installSkillMD = "skills/install-agent-director/SKILL.md"

// oldPathWordByte reports whether b can continue a name: a letter, digit,
// underscore or hyphen.
func oldPathWordByte(b byte) bool {
	return b == '_' || b == '-' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// oldPathIn returns a hit for each occurrence of the old name in line as a
// whole path element: no name character or dot before it, and after it no name
// character and no dot that goes on into a name (.agent-director.pre-move-<ts>
// and .agent-director.aside-<ts> are other names; "~/.agent-director." ending
// a sentence is the old directory).
func oldPathIn(line string) []string {
	var found []string
	for from := 0; ; {
		i := strings.Index(line[from:], oldPathName)
		if i < 0 {
			return found
		}
		start, end := from+i, from+i+len(oldPathName)
		from = end
		if start > 0 && (oldPathWordByte(line[start-1]) || line[start-1] == '.') {
			continue
		}
		if end < len(line) && (oldPathWordByte(line[end]) || line[end] == '.' && end+1 < len(line) && oldPathWordByte(line[end+1])) {
			continue
		}
		found = append(found, "old path "+oldPathName)
	}
}

// sectionOf is the title of the last heading of doc above the 1-based line n,
// "" when there is none.
func sectionOf(doc mdDoc, n int) string {
	title := ""
	for _, h := range doc.headings {
		if h.line < n {
			title = h.title
		}
	}
	return title
}

// oldPathHits returns "file:line (section "…"): hit" for every old path in
// srcs outside rules' exceptions; a Markdown source's hit names its section.
func oldPathHits(t testing.TB, srcs []scanSource, rules *scanRules) []string {
	t.Helper()
	var out []string
	for _, src := range srcs {
		skip := rules.skipLines(t, src)
		var doc mdDoc
		isMD := strings.HasSuffix(src.name, ".md")
		if isMD {
			doc = readMD(t, filepath.Join(mdRepoRoot, src.name))
		}
		for n, line := range strings.Split(src.text, "\n") {
			if skip[n+1] {
				continue
			}
			for _, hit := range oldPathIn(line) {
				where := fmt.Sprintf("%s:%d", src.name, n+1)
				if isMD {
					where += fmt.Sprintf(" (section %q)", sectionOf(doc, n+1))
				}
				out = append(out, where+": "+hit)
			}
		}
	}
	return out
}

// oldPathScanned reports whether the repo-relative file rel is scanned.
func oldPathScanned(rel string) bool {
	dir, base := filepath.Split(rel)
	elems := strings.Split(strings.TrimSuffix(dir, "/"), "/")
	switch {
	case strings.HasSuffix(base, ".go"):
		return !strings.HasSuffix(base, "_test.go")
	case strings.HasPrefix(rel, "pkg/ts-bun-client/src/"):
		return true
	case strings.HasSuffix(base, ".sh"):
		for _, e := range elems {
			if e == "test" {
				return false
			}
		}
		return true
	case rel == "Makefile", rel == "README.md", rel == "pkg/api/README.md", rel == "pkg/ts-bun-client/README.md",
		rel == installSkillMD:
		return true
	}
	return dir == "docs/" && strings.HasSuffix(base, ".md") && !strings.HasSuffix(base, "-guide.md")
}

// oldPathSources reads every scanned file of the repo, in path order.
func oldPathSources(t testing.TB) []scanSource {
	t.Helper()
	var out []scanSource
	err := filepath.WalkDir(mdRepoRoot, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			switch e.Name() {
			case ".git", "node_modules", "testdata", "tickets", ".claude":
				return filepath.SkipDir
			}
			return nil
		}
		if rel, _ := filepath.Rel(mdRepoRoot, path); oldPathScanned(filepath.ToSlash(rel)) {
			out = append(out, repoFile(t, filepath.ToSlash(rel)))
		}
		return nil
	})
	if err != nil || len(out) == 0 {
		t.Fatalf("walk the repo: %v, %d files; want at least one", err, len(out))
	}
	return out
}

// oldPathRules are the places the old name may stay (SR-11.7): the presence
// checks, the SR-14.1 guards that name both directories, the installer's
// scripts and the upgrade and refusal sections of the user docs. An exception
// whose file is not scanned is stale; a section heading not in its file
// matches nothing and is left out (later tasks add their own sections here).
func oldPathRules(t testing.TB, srcs []scanSource) *scanRules {
	t.Helper()
	list := []scanException{
		{file: "internal/layout/old.go", reason: "SR-11.7: the presence rule looks for the old directory"},
		{file: "internal/testsupport/sandboxguard/sandboxguard.go", reason: "SR-14.1: the guard names both directories"},
		{file: "skills/release-agent-director/gates/coverage/bun-test.sh", reason: "SR-14.1: the gate names both directories"},
		{file: "skills/release-agent-director/gates/coverage/bun-extra-scripts.sh", reason: "SR-14.1: the gate names both directories"},
		{file: "Makefile", reason: "SR-14.1: the sandbox targets' messages name both directories"},
		{file: "skills/install-agent-director/install.sh", reason: "SR-11.7: the installer's presence check"},
		{file: "skills/install-agent-director/uninstall.sh", reason: "SR-11.7: the uninstaller's presence check"},
		{file: "docs/install.md", heading: "Upgrade", reason: "SR-11.7: the upgrade moves the old directory"},
		{file: "docs/operator-actions.md", heading: "A verb refuses with `ErrOldLayout`", reason: "SR-11.7: the refusal names the old directory"},
		{file: "docs/operator-actions.md", heading: "`install.sh` refuses with `ErrMoveConflict`", reason: "SR-11.7: the refusal names the old directory"},
		{file: "docs/operator-actions.md", heading: "`install.sh` refuses with `ErrAgentsLive`", reason: "SR-11.7: the refusal names the old directory"},
		{file: "docs/operator-actions.md", heading: "`install.sh` stops with `ErrMoveFailed`", reason: "SR-11.7: the stop names the old directory"},
		{file: "docs/operator-actions.md", heading: "Going back to the previous release", reason: "SR-11.7: going back names the old directory"},
		{file: "docs/architecture.md", heading: "Moving agent-director's directory", reason: "SR-11.7: the move's design names the old directory"},
		{file: installSkillMD, heading: "Upgrading an install from the old directory", anyLevel: true, reason: "SR-11.7, SR-15.3: the skill's upgrade steps name the old directory"},
		{file: installSkillMD, heading: "Going back to the previous release", anyLevel: true, reason: "SR-11.7, SR-15.3: the skill's way back names the old directory"},
	}
	for _, src := range srcs {
		if strings.HasPrefix(src.name, "tools/measure-exit/") {
			list = append(list, scanException{file: src.name, reason: "SR-14.1: the measuring tool's guard names both directories"})
		}
	}
	var keep []scanException
	for _, e := range list {
		// SKILL.md's two exempt headings are exact and always present: a missing
		// one is a stale exception, never filtered out.
		if e.heading != "" && e.file != installSkillMD && len(readMD(t, filepath.Join(mdRepoRoot, e.file)).titled(e.heading)) == 0 {
			continue
		}
		keep = append(keep, e)
	}
	return newScanRules(keep...)
}

// TestOldPathAbsentFromProduct: the old directory's name is a whole path
// element nowhere in the scanned files outside the exceptions.
func TestOldPathAbsentFromProduct(t *testing.T) {
	t.Parallel()
	srcs := oldPathSources(t)
	rules := oldPathRules(t, srcs)
	for _, hit := range oldPathHits(t, srcs, rules) {
		t.Error(hit)
	}
	rules.requireAllHit(t)
}

// TestOldPathMatcher: the old name matches as a whole path element, in any
// quoting; the names beside it that other paths use do not.
func TestOldPathMatcher(t *testing.T) {
	t.Parallel()
	for line, want := range map[string]int{
		"~/.agent-director":                         1,
		`"$HOME/.agent-director/state.db"`:          1,
		`filepath.Join(home, ".agent-director")`:    1,
		".agent-director":                           1,
		"the old `.agent-director`.":                1,
		"ends the sentence at ~/.agent-director.":   1,
		"~/.agent-director and ~/.agent-director/x": 2,
		".agent-director.pre-move-20261011":         0,
		"~/.agent-director.aside-1760000000":        0,
		"~/.config/agent-director":                  0,
		"~/.config/agent-director/state.db":         0,
		"foo.agent-director":                        0,
		".agent-director-x":                         0,
		"x_.agent-director":                         0,
	} {
		if got := len(oldPathIn(line)); got != want {
			t.Errorf("oldPathIn(%q) = %d hits; want %d", line, got, want)
		}
	}
}

// TestOldPathScanFailsOutsideExceptions: a file with the old path fails the
// scan naming its file and line (and, in a doc, its section), unless an
// exception exempts it.
func TestOldPathScanFailsOutsideExceptions(t *testing.T) {
	t.Parallel()
	src := scanSource{name: "internal/x/x.go", text: "package x\n\nvar p = \"~/.agent-director/state.db\"\n"}
	if got := oldPathHits(t, []scanSource{src}, newScanRules()); len(got) != 1 || !strings.HasPrefix(got[0], "internal/x/x.go:3:") {
		t.Errorf("hits %q; want one at internal/x/x.go:3", got)
	}
	exempt := newScanRules(scanException{file: src.name, reason: "test"})
	if got := oldPathHits(t, []scanSource{src}, exempt); len(got) != 0 {
		t.Errorf("hits %q with the file exempt; want none", got)
	}

	doc := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(doc, []byte("# A\n\ntext\n\n## B\n\nsee ~/.agent-director\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := sectionOf(readMD(t, doc), 7); got != "B" {
		t.Errorf("sectionOf line 7 = %q; want B", got)
	}
}

// TestHeadingExceptionEnd: a heading exception runs to the next heading of its
// level or higher, or with anyLevel to the next heading of any level; fenced
// `#` lines are not headings and do not end it.
func TestHeadingExceptionEnd(t *testing.T) {
	t.Parallel()
	text := "## Exempt\n\nold\n\n```sh\n# not a heading\nold\n```\n\n### Sub\n\nsub\n\n## Next\n\nafter\n"
	path := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(mdRepoRoot)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatal(err)
	}
	src := scanSource{name: filepath.ToSlash(rel), text: text}
	for _, tc := range []struct {
		anyLevel bool
		want     []int // the exempt lines
	}{
		{false, []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13}},
		{true, []int{1, 2, 3, 4, 5, 6, 7, 8, 9}},
	} {
		rules := newScanRules(scanException{file: src.name, heading: "Exempt", anyLevel: tc.anyLevel, reason: "test"})
		skip := rules.skipLines(t, src)
		var got []int
		for n := 1; n <= 16; n++ {
			if skip[n] {
				got = append(got, n)
			}
		}
		if fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("anyLevel=%v: exempt lines %v; want %v", tc.anyLevel, got, tc.want)
		}
		rules.requireAllHit(t)
	}
}

// TestInstallSkillOldPathRules: the install skill is scanned in full, its two
// exempt headings are exact and end at the next heading of any level, and an
// old path outside them names the file, line and section.
func TestInstallSkillOldPathRules(t *testing.T) {
	t.Parallel()
	if !oldPathScanned(installSkillMD) {
		t.Fatalf("%s is not scanned", installSkillMD)
	}
	src := repoFile(t, installSkillMD)
	srcs := []scanSource{src}
	rules := oldPathRules(t, srcs)
	for _, e := range rules.list {
		if e.file == installSkillMD && !e.anyLevel {
			t.Errorf("exception %+v: SKILL.md's exempt headings end at any heading", e)
		}
	}
	var heads []string
	for _, e := range rules.list {
		if e.file == installSkillMD {
			heads = append(heads, e.heading)
		}
	}
	if want := []string{"Upgrading an install from the old directory", "Going back to the previous release"}; fmt.Sprint(heads) != fmt.Sprint(want) {
		t.Errorf("SKILL.md exempt headings %q; want exactly %q", heads, want)
	}

	// A planted old path in the frontmatter and under an unexempt heading hits.
	planted := scanSource{name: src.name, text: strings.Replace(src.text, "description:", "description: ~/.agent-director ", 1)}
	planted.text = strings.Replace(planted.text, "\n## When to invoke\n", "\n## When to invoke\n\nsee ~/.agent-director\n", 1)
	if planted.text == src.text {
		t.Fatal("planting changed nothing")
	}
	got := oldPathHits(t, []scanSource{planted}, oldPathRules(t, []scanSource{planted}))
	if len(got) != 2 || !strings.Contains(got[0], `(section "")`) || !strings.Contains(got[1], `(section "When to invoke")`) {
		t.Errorf("hits %q; want one in the frontmatter and one under \"When to invoke\"", got)
	}
}
