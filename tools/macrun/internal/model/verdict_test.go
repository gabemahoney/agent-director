package model_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
)

// id returns the case ID of test in pkg/a, or of pkg/a's own result when
// test is empty.
func id(test string) model.CaseID {
	if test == "" {
		return model.GoPackageCase("pkg/a")
	}
	return model.GoTestCase("pkg/a", test)
}

func pass(c model.CaseID) model.CaseRecord {
	return model.CaseRecord{CaseResult: model.CaseResult{Case: c, Result: model.ResultPass}}
}

func skip(c model.CaseID) model.CaseRecord {
	return model.CaseRecord{CaseResult: model.CaseResult{Case: c, Result: model.ResultSkip}}
}

func fail(c model.CaseID) model.CaseRecord {
	return model.CaseRecord{CaseResult: model.CaseResult{Case: c, Result: model.ResultFail, Reason: model.ReasonFailed}}
}

// finished returns the record of a part that ran to its end.
func finished(part string, cases ...model.CaseRecord) *model.Record {
	return &model.Record{Part: part, Cases: cases, End: model.EndFinished}
}

// current returns the input of a part judged on rec, read from its artifact
// in the judged attempt.
func current(rec *model.Record) model.PartInput {
	return model.PartInput{Name: rec.Part, Record: rec, Source: model.SourceArtifact, Attempt: 2}
}

func part(name string, expected ...model.CaseID) model.Part {
	return model.Part{Name: name, Suite: model.SuiteGo, ExpectedCases: expected}
}

func planned(parts ...model.Part) model.Plan {
	return model.Plan{Status: model.StatusPlanned, Parts: parts}
}

// judged is what TestJudge checks of a verdict.
type judged struct {
	verdict    model.Verdict
	reasons    []model.Incompleteness
	failed     []model.FailedCase
	expected   []model.MarkedCase
	unexpected []model.MarkedCase
	unmatched  []model.Mark
	// complete names the complete parts.
	complete []string
}

func TestJudge(t *testing.T) {
	t.Parallel()
	markA := model.Mark{Case: id("TestA"), Bug: "b.1", Note: "fails on darwin"}
	markSub := model.Mark{Case: id("TestS/sub"), Bug: "b.4", Note: "subtest fails on darwin"}
	for _, tc := range []struct {
		name    string
		plan    model.Plan
		marks   []model.Mark
		private bool
		parts   []model.PartInput
		want    judged
	}{
		{
			name:  "a skipped case makes its part incomplete; its part's failure is still listed",
			plan:  planned(part("a", id(""), id("TestA"), id("TestB"))),
			parts: []model.PartInput{current(finished("a", pass(id("")), fail(id("TestA")), skip(id("TestB"))))},
			want: judged{
				verdict: model.VerdictIncomplete,
				reasons: []model.Incompleteness{{Kind: model.IncompleteCaseSkipped, Part: "a", Case: id("TestB")}},
				failed:  []model.FailedCase{{Case: id("TestA"), Part: "a", Reason: model.ReasonFailed}},
			},
		},
		{
			name: "an expected case with no result and a started case with no end",
			plan: planned(part("a", id(""), id("TestA"), id("TestB"))),
			parts: []model.PartInput{current(&model.Record{
				Part:          "a",
				Cases:         []model.CaseRecord{pass(id("TestA"))},
				WithoutResult: []model.CaseID{id("TestB"), id("TestB/sub")},
				End:           model.EndFinished,
			})},
			want: judged{
				verdict: model.VerdictIncomplete,
				reasons: []model.Incompleteness{
					{Kind: model.IncompleteWithoutResult, Part: "a", Case: id("")},
					{Kind: model.IncompleteWithoutResult, Part: "a", Case: id("TestB")},
					{Kind: model.IncompleteWithoutResult, Part: "a", Case: id("TestB/sub")},
				},
			},
		},
		{
			name: "only marked cases fail, with the package's own result passing: passed, the marks listed",
			plan: planned(part("a", id(""), id("TestA"), id("TestB"))),
			marks: []model.Mark{
				{Case: "go:pkg/z:TestZ", Bug: "b.3", Note: "case renamed"},
				markA,
				{Case: id("TestB"), Bug: "b.2", Note: "fixed by b.2"},
			},
			parts: []model.PartInput{current(finished("a", pass(id("")), fail(id("TestA")), pass(id("TestB"))))},
			want: judged{
				verdict:    model.VerdictPassed,
				failed:     []model.FailedCase{{Case: id("TestA"), Part: "a", Reason: model.ReasonFailed, Marked: true}},
				expected:   []model.MarkedCase{{Case: id("TestA"), Part: "a", Result: model.ResultFail, Reason: model.ReasonFailed, Bug: "b.1", Note: "fails on darwin"}},
				unexpected: []model.MarkedCase{{Case: id("TestB"), Part: "a", Result: model.ResultPass, Bug: "b.2", Note: "fixed by b.2"}},
				unmatched:  []model.Mark{{Case: "go:pkg/z:TestZ", Bug: "b.3", Note: "case renamed"}},
				complete:   []string{"a"},
			},
		},
		{
			name:  "an unmarked failure: failed, a mark naming only its exact case",
			plan:  planned(part("a", id(""), id("TestA"), id("TestAB"))),
			marks: []model.Mark{markA},
			parts: []model.PartInput{current(finished("a", pass(id("")), fail(id("TestA/sub")), fail(id("TestA")), fail(id("TestAB"))))},
			want: judged{
				verdict: model.VerdictFailed,
				failed: []model.FailedCase{
					{Case: id("TestA"), Part: "a", Reason: model.ReasonFailed, Marked: true},
					{Case: id("TestA/sub"), Part: "a", Reason: model.ReasonFailed},
					{Case: id("TestAB"), Part: "a", Reason: model.ReasonFailed},
				},
				expected: []model.MarkedCase{{Case: id("TestA"), Part: "a", Result: model.ResultFail, Reason: model.ReasonFailed, Bug: "b.1", Note: "fails on darwin"}},
				complete: []string{"a"},
			},
		},
		{
			name:  "a failing marked subtest whose top-level test is marked too: passed",
			plan:  planned(part("a", id(""), id("TestS"))),
			marks: []model.Mark{markSub, {Case: id("TestS"), Bug: "b.4", Note: "fails with its subtest"}},
			parts: []model.PartInput{current(finished("a", pass(id("")), fail(id("TestS/sub")), fail(id("TestS"))))},
			want: judged{
				verdict: model.VerdictPassed,
				failed: []model.FailedCase{
					{Case: id("TestS"), Part: "a", Reason: model.ReasonFailed, Marked: true},
					{Case: id("TestS/sub"), Part: "a", Reason: model.ReasonFailed, Marked: true},
				},
				expected: []model.MarkedCase{
					{Case: id("TestS"), Part: "a", Result: model.ResultFail, Reason: model.ReasonFailed, Bug: "b.4", Note: "fails with its subtest"},
					{Case: id("TestS/sub"), Part: "a", Result: model.ResultFail, Reason: model.ReasonFailed, Bug: "b.4", Note: "subtest fails on darwin"},
				},
				complete: []string{"a"},
			},
		},
		{
			name:  "a failing marked subtest whose top-level test is unmarked: failed",
			plan:  planned(part("a", id(""), id("TestS"))),
			marks: []model.Mark{markSub},
			parts: []model.PartInput{current(finished("a", pass(id("")), fail(id("TestS/sub")), fail(id("TestS"))))},
			want: judged{
				verdict: model.VerdictFailed,
				failed: []model.FailedCase{
					{Case: id("TestS"), Part: "a", Reason: model.ReasonFailed},
					{Case: id("TestS/sub"), Part: "a", Reason: model.ReasonFailed, Marked: true},
				},
				expected: []model.MarkedCase{{Case: id("TestS/sub"), Part: "a", Result: model.ResultFail, Reason: model.ReasonFailed, Bug: "b.4", Note: "subtest fails on darwin"}},
				complete: []string{"a"},
			},
		},
		{
			name: "a package failing outside its cases fails, judged in each part that runs it",
			plan: planned(part("a", id(""), id("TestA")), part("b", id(""), id("TestB")), part("c", id(""), id("TestC"))),
			parts: []model.PartInput{
				current(finished("a", pass(id("")), pass(id("TestA")))),
				current(finished("b", fail(id("")), pass(id("TestB")))),
				current(finished("c", pass(id("")), pass(id("TestC")))),
			},
			want: judged{
				verdict:  model.VerdictFailed,
				failed:   []model.FailedCase{{Case: id(""), Part: "b", Reason: model.ReasonFailed}},
				complete: []string{"a", "b", "c"},
			},
		},
		{
			name: "a part whose setup failed, in macrun or on GitHub",
			plan: planned(part("a", id("")), part("b", id(""))),
			parts: []model.PartInput{
				current(&model.Record{Part: "a", SetupFailure: &model.SetupFailure{Step: "github-hosted runner", Message: "runner environment is self-hosted"}}),
				{Name: "b", Source: model.SourceNone, Attempt: 2, SetupStep: "Set up Go"},
			},
			want: judged{
				verdict: model.VerdictIncomplete,
				reasons: []model.Incompleteness{
					{Kind: model.IncompletePartSetup, Part: "a", Detail: "github-hosted runner: runner environment is self-hosted"},
					{Kind: model.IncompletePartSetup, Part: "b", Detail: `step "Set up Go" failed`},
				},
			},
		},
		{
			// d is planned and has no input at all.
			name: "a part not run, missing or cancelled",
			plan: planned(part("a", id("")), part("b", id(""), id("TestB")), part("c", id("")), part("d", id(""))),
			parts: []model.PartInput{
				{Name: "a", Source: model.SourceNone, Attempt: 2, Detail: "no record in artifact or log"},
				current(&model.Record{Part: "b", Cases: []model.CaseRecord{pass(id("TestB"))}, End: model.EndCancelled}),
				{Name: "c", Record: &model.Record{Part: "c", Cases: []model.CaseRecord{pass(id(""))}, FromLog: true}, Source: model.SourceLog, Attempt: 2},
			},
			want: judged{
				verdict: model.VerdictIncomplete,
				reasons: []model.Incompleteness{
					{Kind: model.IncompletePartNotRun, Part: "a", Detail: "no record in artifact or log"},
					{Kind: model.IncompletePartNotRun, Part: "b", Detail: "ended cancelled"},
					{Kind: model.IncompletePartNotRun, Part: "c", Detail: "no part end recorded"},
					{Kind: model.IncompletePartNotRun, Part: "d", Detail: "no record"},
					{Kind: model.IncompleteWithoutResult, Part: "b", Case: id("")},
				},
			},
		},
		{
			name: "an invalid-setup plan lists every error",
			plan: model.Plan{
				Status: model.StatusInvalidSetup,
				Reason: "The Mac Tests setup failed validation.",
				Errors: []model.SetupError{
					{File: "test/mac/expected-failures.txt", Line: 2, Message: "note is empty; say why the case fails"},
					{File: "test/mac/parts.txt", Line: 4, Message: `suite "rust" is not go or testplan`},
				},
			},
			want: judged{
				verdict: model.VerdictIncomplete,
				reasons: []model.Incompleteness{
					{Kind: model.IncompleteInvalidSetup, Detail: "The Mac Tests setup failed validation."},
					{Kind: model.IncompleteInvalidSetup, Detail: "test/mac/expected-failures.txt:2: note is empty; say why the case fails"},
					{Kind: model.IncompleteInvalidSetup, Detail: `test/mac/parts.txt:4: suite "rust" is not go or testplan`},
				},
			},
		},
		{
			name: "no Mac Tests",
			plan: model.Plan{Status: model.StatusNoMacTests, Reason: "The tree has no test/mac/parts.txt."},
			want: judged{
				verdict: model.VerdictIncomplete,
				reasons: []model.Incompleteness{{Kind: model.IncompleteNoMacTests, Detail: "The tree has no test/mac/parts.txt."}},
			},
		},
		{
			name: "a plan status macrun doesn't know",
			plan: model.Plan{Status: "skipped"},
			want: judged{
				verdict: model.VerdictIncomplete,
				reasons: []model.Incompleteness{{Kind: model.IncompleteInvalidSetup, Detail: `plan status "skipped" is not one macrun knows`}},
			},
		},
		{
			name: "a planned plan with no part",
			plan: planned(),
			want: judged{
				verdict: model.VerdictIncomplete,
				reasons: []model.Incompleteness{{Kind: model.IncompleteInvalidSetup, Detail: "the plan is planned but has no part, so nothing ran"}},
			},
		},
		{
			name:  "a planned part expecting no case",
			plan:  planned(part("a")),
			parts: []model.PartInput{current(finished("a"))},
			want: judged{
				verdict: model.VerdictIncomplete,
				reasons: []model.Incompleteness{{Kind: model.IncompleteInvalidSetup, Part: "a", Detail: "the plan expects no case of the part"}},
			},
		},
		{
			name:    "a private repository, every part passing",
			plan:    planned(part("a", id(""))),
			private: true,
			parts:   []model.PartInput{current(finished("a", pass(id(""))))},
			want: judged{
				verdict:  model.VerdictIncomplete,
				reasons:  []model.Incompleteness{{Kind: model.IncompleteNotPublic}},
				complete: []string{"a"},
			},
		},
	} {
		v := model.Judge(model.AttemptInput{
			RunID: 7, Attempt: 2, Commit: "c0ffee",
			Plan: tc.plan, Marks: tc.marks, Public: !tc.private, Parts: tc.parts,
		})
		got := judged{
			verdict:    v.Verdict,
			reasons:    v.Reasons,
			failed:     v.FailedCases,
			expected:   v.ExpectedFailures,
			unexpected: v.UnexpectedPasses,
			unmatched:  v.UnmatchedMarks,
		}
		for _, p := range v.Parts {
			if p.Complete {
				got.complete = append(got.complete, p.Name)
			}
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: Judge =\n%+v\nwant\n%+v", tc.name, got, tc.want)
		}
	}
}

// TestVerdictJSON pins the verdict's JSON form, which macrun verdict writes
// to mac-run-<attempt> and the release gate reads, and shows that the order
// of the judged input doesn't reach it.
func TestVerdictJSON(t *testing.T) {
	t.Parallel()
	in := model.AttemptInput{
		RunID:   7,
		Attempt: 2,
		Commit:  "c0ffee",
		Public:  true,
		Plan: model.Plan{
			Status:   model.StatusPlanned,
			Coverage: model.Coverage{Go: model.CoverageFull, Testplan: model.CoveragePartial},
			Parts: []model.Part{
				part("a", id(""), id("TestA"), id("TestB"), id("TestC")),
				part("b", "go:pkg/b", "go:pkg/b:TestD"),
				part("c", "go:pkg/c"),
			},
		},
		Marks: []model.Mark{
			{Case: id("TestC"), Bug: "b.1", Note: "fails on darwin"},
			{Case: "go:pkg/b:TestD", Bug: "b.2", Note: "fixed by b.2"},
			{Case: "go:pkg/z:TestZ", Bug: "b.3", Note: "case renamed"},
		},
		Parts: []model.PartInput{
			current(&model.Record{
				Part:     "a",
				Versions: []model.Version{{Name: model.VersionGo, Value: "go1.22.10"}},
				Cases: []model.CaseRecord{
					{CaseResult: model.CaseResult{Case: id("TestC"), Result: model.ResultFail, Reason: model.ReasonFailed, Elapsed: 0.5}, Output: "boom\n"},
					{CaseResult: model.CaseResult{Case: id("TestA"), Result: model.ResultPass, Elapsed: 0.25}, Output: "ok\n"},
					{CaseResult: model.CaseResult{Case: id(""), Result: model.ResultPass, Elapsed: 1.5}, Output: "PASS\n"},
					{CaseResult: model.CaseResult{Case: id("TestB"), Result: model.ResultSkip}, Output: "skipped\n", OutputCut: true},
				},
				End: model.EndFinished,
			}),
			// b is carried over from attempt 1, so it is judged on that
			// attempt's record, here read from its log.
			{
				Name: "b",
				Record: &model.Record{
					Part:     "b",
					Attempt:  1,
					Versions: []model.Version{{Name: model.VersionGo, Value: "go1.22.9"}},
					Cases: []model.CaseRecord{
						{CaseResult: model.CaseResult{Case: "go:pkg/b:TestD", Result: model.ResultPass, Elapsed: 0.125}},
						{CaseResult: model.CaseResult{Case: "go:pkg/b", Result: model.ResultPass, Elapsed: 0.5}},
					},
					End:     model.EndFinished,
					FromLog: true,
				},
				Source:      model.SourceLog,
				Attempt:     1,
				CarriedOver: true,
			},
			{Name: "c", Source: model.SourceNone, Attempt: 2, Detail: "no record in artifact or log"},
		},
	}
	const want = `{"schema_version":1,"run_id":7,"attempt":2,"commit":"c0ffee","coverage":{"go":"full","testplan":"partial"},` +
		`"verdict":"incomplete",` +
		`"reasons":[{"kind":"part not run, missing or cancelled","part":"c","detail":"no record in artifact or log"},` +
		`{"kind":"case skipped","part":"a","case":"go:pkg/a:TestB"}],` +
		`"failed_cases":[{"case":"go:pkg/a:TestC","part":"a","reason":"failed","marked":true}],` +
		`"expected_failures":[{"case":"go:pkg/a:TestC","part":"a","result":"fail","reason":"failed","bug":"b.1","note":"fails on darwin"}],` +
		`"unexpected_passes":[{"case":"go:pkg/b:TestD","part":"b","result":"pass","bug":"b.2","note":"fixed by b.2"}],` +
		`"unmatched_marks":[{"case":"go:pkg/z:TestZ","bug":"b.3","note":"case renamed"}],` +
		`"parts":[` +
		`{"name":"a","attempt":2,"source":"artifact","carried_over":false,"complete":false,` +
		`"versions":[{"name":"go","value":"go1.22.10"}],` +
		`"cases":[{"case":"go:pkg/a","result":"pass","elapsed":1.5},{"case":"go:pkg/a:TestA","result":"pass","elapsed":0.25},` +
		`{"case":"go:pkg/a:TestB","result":"skip","elapsed":0},{"case":"go:pkg/a:TestC","result":"fail","reason":"failed","elapsed":0.5}]},` +
		`{"name":"b","attempt":1,"source":"log","carried_over":true,"complete":true,` +
		`"versions":[{"name":"go","value":"go1.22.9"}],` +
		`"cases":[{"case":"go:pkg/b","result":"pass","elapsed":0.5},{"case":"go:pkg/b:TestD","result":"pass","elapsed":0.125}]},` +
		`{"name":"c","attempt":2,"source":"none","carried_over":false,"complete":false}]}`

	reversed := in
	reversed.Marks = reversedCopy(in.Marks)
	reversed.Plan.Parts = reversedCopy(in.Plan.Parts)
	for i := range reversed.Plan.Parts {
		reversed.Plan.Parts[i].ExpectedCases = reversedCopy(reversed.Plan.Parts[i].ExpectedCases)
	}
	reversed.Parts = reversedCopy(in.Parts)
	for i, p := range reversed.Parts {
		if p.Record != nil {
			rec := *p.Record
			rec.Cases = reversedCopy(rec.Cases)
			reversed.Parts[i].Record = &rec
		}
	}
	for _, input := range []struct {
		order string
		in    model.AttemptInput
	}{{"given", in}, {"reversed", reversed}} {
		got, err := json.Marshal(model.Judge(input.in))
		if err != nil {
			t.Fatalf("%s order: Marshal: %v", input.order, err)
		}
		if string(got) != want {
			t.Errorf("%s order: verdict JSON =\n%s\nwant\n%s", input.order, got, want)
		}
	}
}

func reversedCopy[T any](s []T) []T {
	s = slices.Clone(s)
	slices.Reverse(s)
	return s
}
