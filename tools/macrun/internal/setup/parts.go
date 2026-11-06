package setup

import (
	"regexp"
	"strings"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
)

// PartsFile is the setup file that lists a Mac Run's parts. A tree has the
// Mac Tests exactly when it holds this file.
const PartsFile = "test/mac/parts.txt"

// maxParts is the most parts PartsFile may list: GitHub's limit on the jobs
// one matrix makes.
const maxParts = 256

// partFields is the number of tab-separated fields of a PartsFile line:
// <part name>, <suite> and <selection>.
const partFields = 3

// partNamePattern matches a part name. A part name names a matrix job and the
// mac-part-<part>-<attempt> artifact, so it is limited to characters safe in
// both.
var partNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Part is one valid line of PartsFile.
type Part struct {
	// Name is the part's name.
	Name string
	// Suite is the suite the part runs cases from.
	Suite model.Suite
	// Selection is the selection field as written.
	Selection string
	// Selectors are the selection's selectors, in the order written.
	Selectors []Selector
	// Line is the part's 1-based line in PartsFile.
	Line int
}

// Selector is one selector of a Go part's selection: a package dir, or a
// package dir limited to the top-level tests and Examples a regexp matches.
type Selector struct {
	// Text is the selector as written.
	Text string
	// Dir is the package dir, slash-separated and relative to the root.
	Dir string
	// Pattern selects the top-level tests and Examples whose names it
	// matches, unanchored, as go test -run matches one level. It is nil for a
	// selector that takes the whole package.
	Pattern *regexp.Regexp
}

// Selects reports whether the selector picks the top-level test or Example
// named name from its package.
func (s Selector) Selects(name string) bool {
	return s.Pattern == nil || s.Pattern.MatchString(name)
}

// ReadParts reads PartsFile under root. It returns the parts of the lines
// that have no error, in file order, and every error in line order. The
// error return is only for a fault reading the file; a missing file reads as
// one that lists no part.
func ReadParts(root string) ([]Part, []model.SetupError, error) {
	lines, err := readLines(root, PartsFile)
	if err != nil {
		return nil, nil, err
	}
	var parts []Part
	var errs []model.SetupError
	nameLines := map[string]int{}
	for _, l := range lines {
		part, lineErrs := parsePartLine(l, nameLines)
		if len(lineErrs) > 0 {
			errs = append(errs, lineErrs...)
			continue
		}
		parts = append(parts, part)
	}
	switch {
	case len(lines) == 0:
		errs = append(errs, errorAt(PartsFile, 0,
			"lists no part; add a line <part name>\\t<suite>\\t<selection>"))
	case len(lines) > maxParts:
		errs = append(errs, errorAt(PartsFile, 0,
			"lists %d parts; GitHub runs at most %d jobs from one matrix", len(lines), maxParts))
	}
	sortErrors(errs)
	return parts, errs, nil
}

// parsePartLine parses one PartsFile line, recording its part name in
// nameLines so a later line can't reuse it.
func parsePartLine(l line, nameLines map[string]int) (Part, []model.SetupError) {
	if len(l.fields) != partFields {
		message := "has %d fields; expected <part name>\\t<suite>\\t<selection>"
		if len(l.fields) == partFields+1 {
			message = "has %d fields; a fourth field (test binaries at once) isn't supported yet, so use <part name>\\t<suite>\\t<selection>"
		}
		return Part{}, []model.SetupError{errorAt(PartsFile, l.number, message, len(l.fields))}
	}
	name, suite, selection := l.fields[0], model.Suite(l.fields[1]), l.fields[2]
	var errs []model.SetupError
	switch first, used := nameLines[name]; {
	case name == "":
		errs = append(errs, errorAt(PartsFile, l.number, "part name is empty; use letters, digits, ., _ or -"))
	case !partNamePattern.MatchString(name):
		errs = append(errs, errorAt(PartsFile, l.number,
			"part name %q uses characters outside [A-Za-z0-9._-]", name))
	case used:
		errs = append(errs, errorAt(PartsFile, l.number,
			"part name %q is already used on line %d; part names must be unique", name, first))
	default:
		nameLines[name] = l.number
	}
	var selectors []Selector
	switch suite {
	case model.SuiteGo:
		var selErrs []model.SetupError
		selectors, selErrs = parseGoSelection(selection, l.number)
		errs = append(errs, selErrs...)
	case model.SuiteTestplan:
		errs = append(errs, errorAt(PartsFile, l.number, "suite %q isn't supported yet; use %s", suite, model.SuiteGo))
	default:
		errs = append(errs, errorAt(PartsFile, l.number, "unknown suite %q; use %s", suite, model.SuiteGo))
	}
	if len(errs) > 0 {
		return Part{}, errs
	}
	return Part{Name: name, Suite: suite, Selection: selection, Selectors: selectors, Line: l.number}, nil
}

// parseGoSelection parses a Go part's selection: one or more selectors
// separated by single spaces, each <package dir> or <package dir>:<regexp>.
func parseGoSelection(selection string, number int) ([]Selector, []model.SetupError) {
	switch selection {
	case "":
		return nil, []model.SetupError{errorAt(PartsFile, number,
			"selection is empty; use <package dir> or <package dir>:<regexp> selectors separated by single spaces")}
	case "rest":
		return nil, []model.SetupError{errorAt(PartsFile, number,
			"selection \"rest\" isn't supported yet; name package dirs")}
	}
	var selectors []Selector
	var errs []model.SetupError
	for _, text := range strings.Split(selection, " ") {
		if text == "" {
			errs = append(errs, errorAt(PartsFile, number,
				"selection has an empty selector; separate selectors with single spaces"))
			continue
		}
		dir, pattern, hasPattern := strings.Cut(text, ":")
		sel := Selector{Text: text, Dir: dir}
		ok := true
		if reason := packageDirProblem(dir); reason != "" {
			errs = append(errs, errorAt(PartsFile, number, "selector %q: package dir %s", text, reason))
			ok = false
		}
		if hasPattern {
			re, problem := compileSelectorPattern(pattern)
			if problem != "" {
				errs = append(errs, errorAt(PartsFile, number, "selector %q: %s", text, problem))
				ok = false
			}
			sel.Pattern = re
		}
		if ok {
			selectors = append(selectors, sel)
		}
	}
	return selectors, errs
}

// compileSelectorPattern compiles a selector's regexp, returning why it is
// refused when it is.
func compileSelectorPattern(pattern string) (*regexp.Regexp, string) {
	if pattern == "" {
		return nil, "the regexp after : is empty; write <package dir> to select the whole package"
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, "the regexp doesn't compile: " + err.Error()
	}
	return re, ""
}

// packageDirProblem returns why dir can't be a selector's package dir, or ""
// when it can. A package dir is a clean, slash-separated path relative to the
// root that go test ./... reaches, so it has no element go test ignores.
func packageDirProblem(dir string) string {
	const form = "; use a clean slash-separated path relative to the checkout root"
	switch {
	case dir == "":
		return "is empty" + form
	case strings.HasPrefix(dir, "/"):
		return "is absolute" + form
	case strings.HasSuffix(dir, "..."):
		return "ends in ...; name each package dir"
	case strings.HasSuffix(dir, "/"):
		return "ends in /" + form
	case strings.Contains(dir, `\`):
		return `uses \` + form
	}
	for _, elem := range strings.Split(dir, "/") {
		switch {
		case elem == "" || elem == "." || elem == "..":
			return "has an empty, . or .. element" + form
		case elem == "testdata":
			return "has a testdata element, which go test ./... skips"
		case strings.HasPrefix(elem, ".") || strings.HasPrefix(elem, "_"):
			return "has an element starting with . or _, which go test ./... skips"
		}
	}
	return ""
}
