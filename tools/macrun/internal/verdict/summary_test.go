package verdict_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/verdict"
)

// TestMarkdown pins the summary's contents, including a case name that would
// break its table cell unescaped.
func TestMarkdown(t *testing.T) {
	t.Parallel()
	odd := model.CaseID("go:pkg/a:TestX/a|b`c\nd")
	v := model.AttemptVerdict{
		SchemaVersion: model.VerdictSchemaVersion, RunID: 7, Attempt: 2, Commit: "c0ffee", RunURL: runURL,
		Coverage: model.Coverage{Go: model.CoveragePartial, Testplan: model.CoveragePartial},
		Verdict:  model.VerdictIncomplete,
		Reasons: []model.Incompleteness{
			{Kind: model.IncompletePartNotRun, Part: "three", Detail: "job concluded cancelled"},
			{Kind: model.IncompleteCaseSkipped, Part: "one", Case: "go:pkg/a:TestS"},
		},
		FailedCases: []model.FailedCase{
			{Case: "go:pkg/a:TestB", Part: "one", Reason: model.ReasonFailed, Marked: true},
			{Case: odd, Part: "one", Reason: model.ReasonFailed},
		},
		ExpectedFailures: []model.MarkedCase{
			{Case: "go:pkg/a:TestB", Part: "one", Result: model.ResultFail, Reason: model.ReasonFailed, Bug: "b.1", Note: "fails on darwin"},
		},
		UnexpectedPasses: []model.MarkedCase{{Case: "go:pkg/a:TestA", Part: "one", Result: model.ResultPass, Bug: "b.2", Note: "fixed by b.2"}},
		UnmatchedMarks:   []model.Mark{{Case: "go:pkg/z:TestZ", Bug: "b.3", Note: "case renamed"}},
		Parts: []model.PartVerdict{
			{
				Name: "one", Attempt: 2, Source: model.SourceArtifact, LogURL: jobURL + "101", ArtifactURL: artifactURL + "501",
				Versions: []model.Version{{Name: model.VersionGo, Value: "go1.22.10"}, {Name: model.VersionBun, Value: "1.1.38"}},
				Cases:    []model.CaseResult{{Case: "go:pkg/a:TestPassing", Result: model.ResultPass, Elapsed: 1}},
			},
			{Name: "three", Attempt: 2, Source: model.SourceNone, LogURL: jobURL + "103"},
			{
				Name: "two", Attempt: 1, Source: model.SourceLog, CarriedOver: true, Complete: true, LogURL: jobURL + "102",
				Versions: []model.Version{{Name: model.VersionGo, Value: "go1.22.9"}},
			},
		},
	}
	want := strings.Join([]string{
		"## Mac Run verdict: incomplete",
		"",
		"- Tested commit: `c0ffee`",
		"- Run: [run 7, attempt 2](" + runURL + ")",
		"- Coverage: Go partial, testplan partial",
		"",
		"### Incompleteness reasons (2)",
		"",
		"| Reason | Part | Case | Detail |",
		"|---|---|---|---|",
		"| part not run, missing or cancelled | three |  | job concluded cancelled |",
		"| case skipped | one | go:pkg/a:TestS |  |",
		"",
		"### Failed cases (2)",
		"",
		"| Case | Part | Reason | Expected Failure |",
		"|---|---|---|---|",
		"| go:pkg/a:TestB | one | failed | yes |",
		"| go:pkg/a:TestX/a\\|b\\`c<br>d | one | failed | no |",
		"",
		"### Expected Failures (1)",
		"",
		"| Case | Part | Result | Bug | Note |",
		"|---|---|---|---|---|",
		"| go:pkg/a:TestB | one | fail (failed) | b.1 | fails on darwin |",
		"",
		"### Unexpected passes (1)",
		"",
		"| Case | Part | Bug | Note |",
		"|---|---|---|---|",
		"| go:pkg/a:TestA | one | b.2 | fixed by b.2 |",
		"",
		"### Unmatched marks (1)",
		"",
		"| Case | Bug | Note |",
		"|---|---|---|",
		"| go:pkg/z:TestZ | b.3 | case renamed |",
		"",
		"### Parts (3)",
		"",
		"| Part | Complete | Attempt | Read from | Log | Artifact |",
		"|---|---|---|---|---|---|",
		"| one | no | 2 | artifact | [log](" + jobURL + "101) | [artifact](" + artifactURL + "501) |",
		"| three | no | 2 | none | [log](" + jobURL + "103) |  |",
		"| two | yes | 1 (carried over) | log | [log](" + jobURL + "102) |  |",
		"",
		"### Versions (3)",
		"",
		"| Part | go | bun |",
		"|---|---|---|",
		"| one | go1.22.10 | 1.1.38 |",
		"| three |  |  |",
		"| two | go1.22.9 |  |",
		"",
	}, "\n")
	got := verdict.Markdown(v, 0)
	if got != want {
		t.Errorf("Markdown =\n%s\nwant\n%s", got, want)
	}
	// Case results stay in verdict.json.
	if strings.Contains(got, "TestPassing") {
		t.Error("Markdown lists a part's passing case")
	}
}

func TestMarkdownShortened(t *testing.T) {
	t.Parallel()
	v := model.AttemptVerdict{RunID: 7, Attempt: 2, Commit: "c0ffee", Verdict: model.VerdictFailed, Parts: []model.PartVerdict{{Name: "one"}}}
	for i := 1; i <= 1005; i++ {
		v.FailedCases = append(v.FailedCases, model.FailedCase{Case: model.CaseID(fmt.Sprintf("go:pkg/a:TestF%04d", i)), Part: "one", Reason: model.ReasonFailed})
	}
	const note = "Long lists are cut to fit GitHub's summary limit; the full lists are in the artifact mac-run-2 (verdict.md and verdict.json).\n"
	full := verdict.Markdown(v, 0)
	got := verdict.Markdown(v, len(full)-1)
	if len(got) > len(full)-1 || !strings.Contains(got, note) || !strings.Contains(got, "\n_5 more in mac-run-2._\n") ||
		!strings.Contains(got, "| go:pkg/a:TestF1000 |") || strings.Contains(got, "TestF1001") {
		t.Errorf("Markdown one byte under its full size keeps %d bytes; want the first 1000 failed cases, the note and 5 more:\n%s", len(got), got[len(got)-500:])
	}
	// A cap the parts alone pass keeps what fits, then the note.
	if tiny := verdict.Markdown(v, 300); len(tiny) > 300 || !strings.HasSuffix(tiny, "\n"+note) {
		t.Errorf("Markdown capped at 300 bytes =\n%s", tiny)
	}
}
