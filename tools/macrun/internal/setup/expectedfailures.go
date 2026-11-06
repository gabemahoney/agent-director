package setup

import (
	"regexp"
	"strings"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
)

// ExpectedFailuresFile is the optional setup file of Expected Failure marks.
const ExpectedFailuresFile = "test/mac/expected-failures.txt"

// markFields is the number of tab-separated fields of an
// ExpectedFailuresFile line: <case ID>, <Bug ID> and <note>.
const markFields = 3

// bugIDPattern matches a Bug ID, b.<id>.
var bugIDPattern = regexp.MustCompile(`^b\.[0-9a-z]+$`)

// Mark is one Expected Failure mark: a case expected to fail on the Mac
// because of an open Bug. A mark's case need not exist; the verdict reports
// one that no case matched.
type Mark struct {
	// Case is the marked case.
	Case model.CaseID
	// Bug is the ID of the Bug the case fails for, b.<id>.
	Bug string
	// Note says why the case fails.
	Note string
	// Line is the mark's 1-based line in ExpectedFailuresFile.
	Line int
}

// ReadExpectedFailures reads ExpectedFailuresFile under root. It returns the
// marks of the lines that have no error, in file order, and every error in
// line order. A missing file has no marks. The error return is only for a
// fault reading the file.
func ReadExpectedFailures(root string) ([]Mark, []model.SetupError, error) {
	lines, err := readLines(root, ExpectedFailuresFile)
	if err != nil {
		return nil, nil, err
	}
	var marks []Mark
	var errs []model.SetupError
	for _, l := range lines {
		mark, lineErrs := parseMarkLine(l)
		if len(lineErrs) > 0 {
			errs = append(errs, lineErrs...)
			continue
		}
		marks = append(marks, mark)
	}
	sortErrors(errs)
	return marks, errs, nil
}

// parseMarkLine parses one ExpectedFailuresFile line.
func parseMarkLine(l line) (Mark, []model.SetupError) {
	if len(l.fields) != markFields {
		return Mark{}, []model.SetupError{errorAt(ExpectedFailuresFile, l.number,
			"has %d fields; expected <case ID>\\t<Bug ID>\\t<note>", len(l.fields))}
	}
	var errs []model.SetupError
	id, err := model.ParseCaseID(l.fields[0])
	if err != nil {
		errs = append(errs, errorAt(ExpectedFailuresFile, l.number, "%s", err.Error()))
	}
	bug := l.fields[1]
	if !bugIDPattern.MatchString(bug) {
		errs = append(errs, errorAt(ExpectedFailuresFile, l.number,
			"Bug ID %q is not of the form b.<id> (lower-case letters and digits)", bug))
	}
	note := l.fields[2]
	if strings.TrimSpace(note) == "" {
		errs = append(errs, errorAt(ExpectedFailuresFile, l.number, "note is empty; say why the case fails"))
	}
	if len(errs) > 0 {
		return Mark{}, errs
	}
	return Mark{Case: id, Bug: bug, Note: note, Line: l.number}, nil
}
