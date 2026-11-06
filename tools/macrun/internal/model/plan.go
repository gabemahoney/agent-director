package model

import "fmt"

// PlanSchemaVersion is the version of the plan's JSON form. A change that
// removes or renames a field, or changes a field's meaning, raises it;
// adding a field does not.
const PlanSchemaVersion = 1

// PlanStatus says whether a plan planned any parts, and if not, why.
type PlanStatus string

// The plan statuses.
const (
	// StatusPlanned: the setup is valid and the plan holds its parts.
	StatusPlanned PlanStatus = "planned"
	// StatusNoMacTests: the tree has no test/mac/parts.txt, so nothing runs.
	StatusNoMacTests PlanStatus = "no-mac-tests"
	// StatusInvalidSetup: the setup failed validation, so nothing runs.
	StatusInvalidSetup PlanStatus = "invalid-setup"
)

// Suite is the suite a part runs its cases from.
type Suite string

// The suites test/mac/parts.txt names.
const (
	// SuiteGo is the Go test suite.
	SuiteGo Suite = "go"
	// SuiteTestplan is the integration groups of test/docker-epics.txt.
	SuiteTestplan Suite = "testplan"
)

// CoverageLevel says whether a suite's parts select all of it.
type CoverageLevel string

// The coverage levels.
const (
	// CoverageFull: the suite's last part takes every case no earlier part
	// selected.
	CoverageFull CoverageLevel = "full"
	// CoveragePartial: some of the suite, or none of it, is selected.
	CoveragePartial CoverageLevel = "partial"
)

// Coverage holds each suite's coverage level.
type Coverage struct {
	// Go is the Go suite's coverage.
	Go CoverageLevel `json:"go"`
	// Testplan is the testplan suite's coverage.
	Testplan CoverageLevel `json:"testplan"`
}

// Plan is what macrun plan computes from a tree: either the parts of a Mac
// Run, or no parts and the reason. It is a pure function of the tree, so
// every job of a run computes the same Plan. Empty lists are left out of its
// JSON form.
type Plan struct {
	// SchemaVersion is PlanSchemaVersion when the plan was made.
	SchemaVersion int `json:"schema_version"`
	// Status says whether any part is planned.
	Status PlanStatus `json:"status"`
	// Reason is one sentence saying why no part is planned. It is empty for
	// StatusPlanned.
	Reason string `json:"reason,omitempty"`
	// Errors lists every setup error, in file and line order, for
	// StatusInvalidSetup.
	Errors []SetupError `json:"errors,omitempty"`
	// Coverage is each suite's coverage.
	Coverage Coverage `json:"coverage"`
	// Parts are the planned parts, in test/mac/parts.txt order.
	Parts []Part `json:"parts,omitempty"`
}

// Part is one planned part: one job of a Mac Run.
type Part struct {
	// Name is the part's name from test/mac/parts.txt.
	Name string `json:"name"`
	// Suite is the suite the part runs cases from.
	Suite Suite `json:"suite"`
	// Selection is the part's selection as test/mac/parts.txt writes it.
	Selection string `json:"selection"`
	// Packages are the Go packages the part runs, sorted by dir.
	Packages []Package `json:"packages,omitempty"`
	// ExpectedCases are the part's expected cases, sorted.
	ExpectedCases []CaseID `json:"expected_cases,omitempty"`
}

// Package is one Go package a part runs, and the top-level tests and
// Examples it runs from it.
type Package struct {
	// Dir is the package dir, relative to the module root.
	Dir string `json:"dir"`
	// Tests are the top-level test and Example names the part runs, sorted.
	Tests []string `json:"tests"`
}

// SetupError is one error in a test/mac/ setup file.
type SetupError struct {
	// File is the setup file's path relative to the tree's root, such as
	// test/mac/parts.txt.
	File string `json:"file"`
	// Line is the 1-based line the error concerns, or 0 when it concerns the
	// whole file.
	Line int `json:"line"`
	// Message says what is wrong and what form is expected.
	Message string `json:"message"`
}

// Error returns the error as <file>:<line>: <message>, or <file>: <message>
// when it concerns the whole file.
func (e SetupError) Error() string {
	if e.Line == 0 {
		return fmt.Sprintf("%s: %s", e.File, e.Message)
	}
	return fmt.Sprintf("%s:%d: %s", e.File, e.Line, e.Message)
}
