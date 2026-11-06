package model

import (
	"fmt"
	"sort"
)

// VerdictSchemaVersion is the version of an attempt verdict's JSON form. A
// change that removes or renames a field, or changes a field's meaning,
// raises it; adding a field does not.
const VerdictSchemaVersion = 1

// Verdict is an attempt's verdict.
type Verdict string

// The verdicts.
const (
	// VerdictIncomplete: the attempt has at least one incompleteness reason.
	VerdictIncomplete Verdict = "incomplete"
	// VerdictFailed: the attempt is complete and a case no Expected Failure
	// mark names failed.
	VerdictFailed Verdict = "failed"
	// VerdictPassed: the attempt is complete and every failed case is marked.
	VerdictPassed Verdict = "passed"
)

// IncompleteKind is one kind of reason an attempt is incomplete, in the
// words of the Mac Run's requirements.
type IncompleteKind string

// The incompleteness reasons, in the order a verdict lists them.
const (
	IncompleteNoMacTests    IncompleteKind = "no Mac Tests in the source"
	IncompleteNotPublic     IncompleteKind = "repository not public"
	IncompleteInvalidSetup  IncompleteKind = "invalid setup"
	IncompletePartNotRun    IncompleteKind = "part not run, missing or cancelled"
	IncompletePartSetup     IncompleteKind = "part setup failed"
	IncompleteCaseSkipped   IncompleteKind = "case skipped"
	IncompleteWithoutResult IncompleteKind = "case without a result"
)

// incompleteRank orders a verdict's reasons by kind.
var incompleteRank = map[IncompleteKind]int{
	IncompleteNoMacTests:    0,
	IncompleteNotPublic:     1,
	IncompleteInvalidSetup:  2,
	IncompletePartNotRun:    3,
	IncompletePartSetup:     4,
	IncompleteCaseSkipped:   5,
	IncompleteWithoutResult: 6,
}

// Incompleteness is one reason an attempt is incomplete.
type Incompleteness struct {
	// Kind is the reason's kind.
	Kind IncompleteKind `json:"kind"`
	// Part is the part it concerns, if any.
	Part string `json:"part,omitempty"`
	// Case is the case it concerns, if any.
	Case CaseID `json:"case,omitempty"`
	// Detail says more: the plan's reason, a setup error, the failed check
	// or step, or what was tried to find a part's record.
	Detail string `json:"detail,omitempty"`
}

// Mark is one Expected Failure mark, as the judging takes it.
type Mark struct {
	// Case is the marked case.
	Case CaseID `json:"case"`
	// Bug is the ID of the Bug the case fails for.
	Bug string `json:"bug"`
	// Note says why the case fails.
	Note string `json:"note"`
}

// RecordSource is where a part's record was read from.
type RecordSource string

// The record sources.
const (
	SourceArtifact RecordSource = "artifact"
	SourceLog      RecordSource = "log"
	SourceNone     RecordSource = "none"
)

// PartInput is what the judging knows of one planned part.
type PartInput struct {
	// Name is the planned part's name.
	Name string
	// Record is the part's record, or nil when none was found.
	Record *Record
	// Source is where Record was read from; SourceNone when Record is nil.
	Source RecordSource
	// Attempt is the attempt the record belongs to.
	Attempt int
	// CarriedOver reports that Attempt is an earlier attempt whose result
	// GitHub carried over to the judged one.
	CarriedOver bool
	// SetupStep names the job's step that failed before the part could run
	// macrun, as GitHub reports it, or is empty.
	SetupStep string
	// Detail says why there is no record (the part didn't run, is missing or
	// was cancelled), for a part with no record.
	Detail string
}

// AttemptInput is everything the judging of one attempt takes.
type AttemptInput struct {
	// RunID, Attempt and Commit identify the judged attempt.
	RunID   int64
	Attempt int
	Commit  string
	// Plan is the plan of the tested commit.
	Plan Plan
	// Marks are the Expected Failure marks of the tested commit.
	Marks []Mark
	// Public reports whether the repository is public.
	Public bool
	// Parts holds one input per planned part, in any order. A planned part
	// with no input has no record.
	Parts []PartInput
}

// FailedCase is one case that failed in one part.
type FailedCase struct {
	Case   CaseID `json:"case"`
	Part   string `json:"part"`
	Reason Reason `json:"reason"`
	// Marked reports that an Expected Failure mark names the case.
	Marked bool `json:"marked"`
}

// MarkedCase is one marked case that reported a result in one part: an
// Expected Failure when it failed, an unexpected pass when it passed.
type MarkedCase struct {
	Case   CaseID `json:"case"`
	Part   string `json:"part"`
	Result Result `json:"result"`
	Reason Reason `json:"reason,omitempty"`
	Bug    string `json:"bug"`
	Note   string `json:"note"`
}

// PartVerdict is one planned part as the verdict judged it.
type PartVerdict struct {
	// Name is the part's name.
	Name string `json:"name"`
	// Attempt is the attempt whose record was judged.
	Attempt int `json:"attempt"`
	// Source is where the record was read from.
	Source RecordSource `json:"source"`
	// CarriedOver reports that the record is an earlier attempt's, carried
	// over by GitHub.
	CarriedOver bool `json:"carried_over"`
	// Complete reports that the part has no incompleteness reason.
	Complete bool `json:"complete"`
	// LogURL and ArtifactURL link the part's job log and artifact; macrun
	// verdict sets them.
	LogURL      string `json:"log_url,omitempty"`
	ArtifactURL string `json:"artifact_url,omitempty"`
	// Versions are the record's versions.
	Versions []Version `json:"versions,omitempty"`
	// Cases are every case result of the record, sorted by case ID, without
	// output.
	Cases []CaseResult `json:"cases,omitempty"`
}

// AttemptVerdict is the judged result of one attempt. Every list is sorted,
// so the same input gives the same JSON. Empty lists are left out of its
// JSON form.
type AttemptVerdict struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         int64  `json:"run_id"`
	Attempt       int    `json:"attempt"`
	Commit        string `json:"commit"`
	// RunURL links the run; macrun verdict sets it.
	RunURL   string   `json:"run_url,omitempty"`
	Coverage Coverage `json:"coverage"`
	Verdict  Verdict  `json:"verdict"`
	// Reasons are every reason the attempt is incomplete, by kind, then
	// part and case.
	Reasons []Incompleteness `json:"reasons,omitempty"`
	// FailedCases are every failed case of every part's record, complete or
	// not, marked or not, by case then part.
	FailedCases []FailedCase `json:"failed_cases,omitempty"`
	// ExpectedFailures are the marked cases that failed.
	ExpectedFailures []MarkedCase `json:"expected_failures,omitempty"`
	// UnexpectedPasses are the marked cases that passed.
	UnexpectedPasses []MarkedCase `json:"unexpected_passes,omitempty"`
	// UnmatchedMarks are the marks whose case has no result in any record.
	UnmatchedMarks []Mark `json:"unmatched_marks,omitempty"`
	// Parts are the planned parts, by name.
	Parts []PartVerdict `json:"parts,omitempty"`
}

// Judge judges one attempt. It is pure: the same input, in any order, gives
// the same verdict.
func Judge(in AttemptInput) AttemptVerdict {
	v := AttemptVerdict{
		SchemaVersion: VerdictSchemaVersion,
		RunID:         in.RunID,
		Attempt:       in.Attempt,
		Commit:        in.Commit,
		Coverage:      in.Plan.Coverage,
	}
	// A plan this judging can't read fails closed: incomplete, never a
	// passed verdict with nothing run.
	switch in.Plan.Status {
	case StatusPlanned:
		if len(in.Plan.Parts) == 0 {
			v.Reasons = append(v.Reasons, Incompleteness{Kind: IncompleteInvalidSetup, Detail: "the plan is planned but has no part, so nothing ran"})
		}
	case StatusNoMacTests:
		v.Reasons = append(v.Reasons, Incompleteness{Kind: IncompleteNoMacTests, Detail: in.Plan.Reason})
	case StatusInvalidSetup:
		v.Reasons = append(v.Reasons, Incompleteness{Kind: IncompleteInvalidSetup, Detail: in.Plan.Reason})
		for _, e := range in.Plan.Errors {
			v.Reasons = append(v.Reasons, Incompleteness{Kind: IncompleteInvalidSetup, Detail: e.Error()})
		}
	default:
		v.Reasons = append(v.Reasons, Incompleteness{Kind: IncompleteInvalidSetup,
			Detail: fmt.Sprintf("plan status %q is not one macrun knows", in.Plan.Status)})
	}
	if !in.Public {
		v.Reasons = append(v.Reasons, Incompleteness{Kind: IncompleteNotPublic})
	}

	inputs := map[string]PartInput{}
	for _, p := range in.Parts {
		inputs[p.Name] = p
	}
	// Of two marks of one case, the lesser by Bug and note names it, so the
	// marks' order doesn't matter.
	marks := map[CaseID]Mark{}
	for _, m := range in.Marks {
		if first, dup := marks[m.Case]; !dup || markLess(m, first) {
			marks[m.Case] = m
		}
	}
	reported := map[CaseID]bool{}
	if in.Plan.Status == StatusPlanned {
		for _, part := range in.Plan.Parts {
			pv, reasons := judgePart(part, inputs[part.Name])
			v.Reasons = append(v.Reasons, reasons...)
			v.Parts = append(v.Parts, pv)
			if rec := inputs[part.Name].Record; rec != nil {
				v.collectResults(part.Name, rec, marks, reported)
			}
		}
	}
	for _, m := range in.Marks {
		if !reported[m.Case] {
			v.UnmatchedMarks = append(v.UnmatchedMarks, m)
		}
	}

	v.sortLists()
	v.Verdict = VerdictPassed
	for _, f := range v.FailedCases {
		if !f.Marked {
			v.Verdict = VerdictFailed
		}
	}
	if len(v.Reasons) > 0 {
		v.Verdict = VerdictIncomplete
	}
	return v
}

// judgePart judges one planned part's completeness and returns its entry and
// its reasons. A part is complete when its record exists, no setup step or
// check failed, it ended finished, and every expected case and every case
// that reported has a pass or fail.
func judgePart(part Part, in PartInput) (PartVerdict, []Incompleteness) {
	pv := PartVerdict{Name: part.Name, Attempt: in.Attempt, Source: in.Source, CarriedOver: in.CarriedOver}
	if in.Record == nil {
		pv.Source = SourceNone
	}
	var reasons []Incompleteness
	add := func(kind IncompleteKind, id CaseID, detail string) {
		reasons = append(reasons, Incompleteness{Kind: kind, Part: part.Name, Case: id, Detail: detail})
	}
	if len(part.ExpectedCases) == 0 {
		// A part that expects nothing would pass with nothing run.
		add(IncompleteInvalidSetup, "", "the plan expects no case of the part")
	}
	if in.SetupStep != "" {
		add(IncompletePartSetup, "", fmt.Sprintf("step %q failed", in.SetupStep))
	}
	rec := in.Record
	switch {
	case rec == nil:
		if in.SetupStep == "" {
			detail := in.Detail
			if detail == "" {
				detail = "no record"
			}
			add(IncompletePartNotRun, "", detail)
		}
	case rec.SetupFailure != nil:
		add(IncompletePartSetup, "", fmt.Sprintf("%s: %s", rec.SetupFailure.Step, rec.SetupFailure.Message))
	default:
		if rec.End != EndFinished {
			detail := "no part end recorded"
			if rec.End != "" {
				detail = fmt.Sprintf("ended %s", rec.End)
			}
			add(IncompletePartNotRun, "", detail)
		}
		results := map[CaseID]Result{}
		for _, c := range rec.Cases {
			results[c.Case] = c.Result
		}
		without := map[CaseID]bool{}
		for _, id := range rec.WithoutResult {
			without[id] = true
		}
		for _, id := range part.ExpectedCases {
			if _, ok := results[id]; !ok {
				without[id] = true
			}
		}
		for id, r := range results {
			if r == ResultSkip {
				add(IncompleteCaseSkipped, id, "")
			} else if r != ResultPass && r != ResultFail {
				without[id] = true
			}
		}
		for id := range without {
			add(IncompleteWithoutResult, id, "")
		}
	}
	if rec != nil {
		pv.Versions = rec.Versions
		for _, c := range rec.Cases {
			pv.Cases = append(pv.Cases, c.CaseResult)
		}
		sort.Slice(pv.Cases, func(i, j int) bool { return pv.Cases[i].Case < pv.Cases[j].Case })
	}
	pv.Complete = len(reasons) == 0
	return pv, reasons
}

// collectResults adds one part's failed cases and marked cases to v, and
// notes each case that has a result in reported.
func (v *AttemptVerdict) collectResults(part string, rec *Record, marks map[CaseID]Mark, reported map[CaseID]bool) {
	for _, c := range rec.Cases {
		reported[c.Case] = true
		mark, marked := marks[c.Case]
		if c.Result == ResultFail {
			v.FailedCases = append(v.FailedCases, FailedCase{Case: c.Case, Part: part, Reason: c.Reason, Marked: marked})
		}
		if !marked {
			continue
		}
		mc := MarkedCase{Case: c.Case, Part: part, Result: c.Result, Reason: c.Reason, Bug: mark.Bug, Note: mark.Note}
		switch c.Result {
		case ResultFail:
			v.ExpectedFailures = append(v.ExpectedFailures, mc)
		case ResultPass:
			v.UnexpectedPasses = append(v.UnexpectedPasses, mc)
		}
	}
}

// sortLists sorts every list of v.
func (v *AttemptVerdict) sortLists() {
	sort.SliceStable(v.Reasons, func(i, j int) bool {
		a, b := v.Reasons[i], v.Reasons[j]
		if incompleteRank[a.Kind] != incompleteRank[b.Kind] {
			return incompleteRank[a.Kind] < incompleteRank[b.Kind]
		}
		if a.Part != b.Part {
			return a.Part < b.Part
		}
		return a.Case < b.Case
	})
	sort.Slice(v.FailedCases, func(i, j int) bool {
		a, b := v.FailedCases[i], v.FailedCases[j]
		if a.Case != b.Case {
			return a.Case < b.Case
		}
		return a.Part < b.Part
	})
	for _, list := range [][]MarkedCase{v.ExpectedFailures, v.UnexpectedPasses} {
		sort.Slice(list, func(i, j int) bool {
			if list[i].Case != list[j].Case {
				return list[i].Case < list[j].Case
			}
			return list[i].Part < list[j].Part
		})
	}
	sort.Slice(v.UnmatchedMarks, func(i, j int) bool { return markLess(v.UnmatchedMarks[i], v.UnmatchedMarks[j]) })
	sort.Slice(v.Parts, func(i, j int) bool { return v.Parts[i].Name < v.Parts[j].Name })
}

// markLess orders marks by case, Bug and note.
func markLess(a, b Mark) bool {
	if a.Case != b.Case {
		return a.Case < b.Case
	}
	if a.Bug != b.Bug {
		return a.Bug < b.Bug
	}
	return a.Note < b.Note
}
