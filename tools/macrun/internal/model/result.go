package model

import "unicode/utf8"

// Result is how one case that ran ended.
type Result string

// The results.
const (
	ResultPass Result = "pass"
	ResultSkip Result = "skip"
	// ResultFail always comes with a Reason.
	ResultFail Result = "fail"
)

// Reason says why a case failed. A fail always has one; a pass or skip never
// does.
type Reason string

// The fail reasons.
const (
	// ReasonFailed: the case's own failure, as Go reports it.
	ReasonFailed Reason = "failed"
)

// MaxCaseOutputBytes caps the output a record keeps for one case or one
// package build, so one chatty case can't swell the record. The job log keeps
// the whole output.
const MaxCaseOutputBytes = 1 << 20

// CaseResult is one case's result, without its output.
type CaseResult struct {
	// Case is the case's ID.
	Case CaseID `json:"case"`
	// Result is how the case ended.
	Result Result `json:"result"`
	// Reason is set exactly when Result is ResultFail.
	Reason Reason `json:"reason,omitempty"`
	// Elapsed is the case's running time in seconds, as Go reports it.
	Elapsed float64 `json:"elapsed"`
}

// CaseRecord is one case's result with its own output, as a part's record
// keeps it.
type CaseRecord struct {
	CaseResult
	// Output is the case's own output, cut to its last MaxCaseOutputBytes.
	Output string `json:"output,omitempty"`
	// OutputCut reports that Output lost its beginning to the cap.
	OutputCut bool `json:"output_cut,omitempty"`
}

// CapOutput returns s cut to its last MaxCaseOutputBytes, starting at a whole
// UTF-8 character, and whether anything was cut.
func CapOutput(s string) (string, bool) {
	if len(s) <= MaxCaseOutputBytes {
		return s, false
	}
	start := len(s) - MaxCaseOutputBytes
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:], true
}

// MergeCase returns the one result of a case that ran twice under one name,
// as a name a package defines in both its in-package and its external test
// files does: fail if either failed, else a result macrun doesn't know if
// either has one, else skip if either skipped, else pass. The running times
// add up and the outputs join, in the order given.
func MergeCase(first, second CaseRecord) CaseRecord {
	merged := first
	if severity(second.Result) > severity(first.Result) {
		merged.Result = second.Result
		merged.Reason = second.Reason
	}
	merged.Elapsed = first.Elapsed + second.Elapsed
	merged.Output, merged.OutputCut = CapOutput(first.Output + second.Output)
	merged.OutputCut = merged.OutputCut || first.OutputCut || second.OutputCut
	return merged
}

// severity orders results for MergeCase. A result it doesn't know outranks
// pass and skip, so the judging still sees it and counts the case as without
// a result, rather than the merge hiding it behind a pass.
func severity(r Result) int {
	switch r {
	case ResultPass:
		return 0
	case ResultSkip:
		return 1
	case ResultFail:
		return 3
	}
	return 2
}
