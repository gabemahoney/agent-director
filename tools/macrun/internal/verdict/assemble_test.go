package verdict_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/ghrun"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/macfix"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/part"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/treefix"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/verdict"
)

const (
	pkgA        = "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\nfunc TestB(t *testing.T) {}\n"
	runURL      = "https://github.com/o/r/actions/runs/7/attempts/2"
	jobURL      = "https://github.com/o/r/actions/runs/7/job/"
	artifactURL = "https://github.com/o/r/actions/runs/7/artifacts/"
)

// oneTree writes a checkout whose one part, "one", runs pkg/a's TestA and
// TestB.
func oneTree(t *testing.T) string {
	t.Helper()
	return treefix.Write(t, map[string]string{"pkg/a/a_test.go": pkgA, "test/mac/parts.txt": "one\tgo\tpkg/a\n"})
}

func result(id model.CaseID, r model.Result, elapsed float64) model.CaseResult {
	c := model.CaseResult{Case: id, Result: r, Elapsed: elapsed}
	if r == model.ResultFail {
		c.Reason = model.ReasonFailed
	}
	return c
}

// record returns the finished record of a part of run 7 in attempt, testing
// c0ffee, with each case's output.
func record(partName string, attempt int, cases ...model.CaseResult) model.Record {
	rec := model.Record{
		SchemaVersion: model.RecordSchemaVersion, Part: partName, RunID: 7, Attempt: attempt, Commit: "c0ffee",
		Versions: macfix.Versions, Configurations: []model.Configuration{model.ConfigurationDefault}, End: model.EndFinished,
	}
	for _, c := range cases {
		rec.Cases = append(rec.Cases, model.CaseRecord{CaseResult: c, Output: "output of " + string(c.Case) + "\n"})
	}
	return rec
}

// artifact returns rec as the record.json of the artifact with ID id.
func artifact(t *testing.T, id int64, rec model.Record) ghrun.ArtifactFile {
	t.Helper()
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	return ghrun.ArtifactFile{ArtifactID: id, Data: data}
}

// logOf returns the job log of the part that printed rec's marked lines.
func logOf(t *testing.T, rec model.Record) []byte {
	t.Helper()
	events := []model.Event{{
		Event: model.EventPartStart, Part: rec.Part, RunID: rec.RunID, Attempt: rec.Attempt, Commit: rec.Commit,
		Versions: rec.Versions, Configurations: rec.Configurations,
	}}
	for _, c := range rec.Cases {
		events = append(events, model.Event{Event: model.EventCaseStart, Case: c.Case},
			model.Event{Event: model.EventCaseEnd, Case: c.Case, Result: c.Result, Reason: c.Reason, Elapsed: c.Elapsed})
	}
	events = append(events, model.Event{Event: model.EventPartEnd, End: rec.End})
	var b bytes.Buffer
	for _, e := range events {
		if err := model.WriteEvent(&b, e); err != nil {
			t.Fatal(err)
		}
	}
	return macfix.GitHubLog("##[group]Run cases\n" + b.String())
}

// job returns the part job of partName with ID id, run in runAttempt, whose
// steps all concluded as the job did.
func job(partName string, id int64, conclusion string, runAttempt int) ghrun.Job {
	return ghrun.Job{
		ID: id, Name: ghrun.PartJobName(partName), RunAttempt: runAttempt, Status: "completed", Conclusion: conclusion,
		HTMLURL: jobURL + strconv.FormatInt(id, 10),
		Steps: []ghrun.Step{
			{Name: "Set up job", Number: 1, Status: "completed", Conclusion: "success"},
			{Name: ghrun.CasesStepName, Number: 2, Status: "completed", Conclusion: conclusion},
		},
	}
}

// reader returns the fake GitHub of attempt 2 of run 7, which tested c0ffee
// in a public repository.
func reader(jobs ...ghrun.Job) *macfix.Reader {
	return &macfix.Reader{
		RunID: 7, AttemptNumber: 2, HeadSHA: "c0ffee", HTMLURL: runURL, JobList: jobs,
		Records: map[string]ghrun.ArtifactFile{}, Logs: map[int64][]byte{}, Faults: map[string]error{},
	}
}

// options returns the verdict's options for attempt 2 of run 7 of o/r,
// tested at c0ffee, in the checkout at root, writing to a new folder.
func options(t *testing.T, root string) verdict.Options {
	return verdict.Options{
		Repo: "o/r", RunID: 7, Attempt: 2, Root: root, Head: "c0ffee", ServerURL: "https://github.com",
		Out: filepath.Join(t.TempDir(), "out"),
	}
}

// judged is one verdict.Run.
type judged struct {
	code           int
	v              model.AttemptVerdict
	json           []byte
	stdout, stderr string
}

// runVerdict runs the verdict and reads the verdict.json it wrote.
func runVerdict(t *testing.T, opts verdict.Options, r ghrun.Reader) judged {
	t.Helper()
	var stdout, stderr bytes.Buffer
	j := judged{code: verdict.Run(context.Background(), opts, r, &stdout, &stderr), stdout: stdout.String(), stderr: stderr.String()}
	data, err := os.ReadFile(filepath.Join(opts.Out, verdict.JSONFile))
	if err != nil {
		t.Fatalf("Run = %d, stderr %q: %v", j.code, j.stderr, err)
	}
	if err := json.Unmarshal(data, &j.v); err != nil {
		t.Fatal(err)
	}
	j.json = data
	return j
}

func TestFindParts(t *testing.T) {
	t.Parallel()
	passing := []model.CaseResult{
		result("go:pkg/a", model.ResultPass, 1), result("go:pkg/a:TestA", model.ResultPass, 0.25), result("go:pkg/a:TestB", model.ResultPass, 0.5),
	}
	failing := []model.CaseResult{passing[0], result("go:pkg/a:TestA", model.ResultFail, 0.25), passing[2]}
	earlier := []model.CaseResult{passing[0], result("go:pkg/a:TestA", model.ResultPass, 0.125), passing[2]}
	// partVerdict is part one's entry, read in attempt 2 unless changed.
	partVerdict := func(source model.RecordSource, artifactID string, cases []model.CaseResult) model.PartVerdict {
		p := model.PartVerdict{Name: "one", Attempt: 2, Source: source, Complete: source != model.SourceNone, LogURL: jobURL + "101", Cases: cases}
		if artifactID != "" {
			p.ArtifactURL = artifactURL + artifactID
		}
		if cases != nil {
			p.Versions = macfix.Versions
		}
		return p
	}
	notRun := func(detail string) []model.Incompleteness {
		return []model.Incompleteness{{Kind: model.IncompletePartNotRun, Part: "one", Detail: detail}}
	}
	const neither = "no record: artifact mac-part-one-2: no record.json; log of job 101: none"
	// listedCarriedOver gives part one GitHub's carry-over shape: listed in
	// attempt 2 with run_attempt 2 and no artifact of attempt 2, its log
	// printing rec. Attempts 1 and 3 have good artifacts, which a refused log
	// record must not lead to.
	listedCarriedOver := func(r *macfix.Reader, rec model.Record) {
		delete(r.Records, "mac-part-one-2")
		r.Records["mac-part-one-1"] = artifact(t, 500, record("one", 1, earlier...))
		r.Records["mac-part-one-3"] = artifact(t, 503, record("one", 3, earlier...))
		r.Logs[101] = logOf(t, rec)
	}
	refused := func(mismatch string) []model.Incompleteness {
		return notRun("no record: artifact mac-part-one-2: no record.json; log of job 101: record is for " + mismatch)
	}
	for _, tc := range []struct {
		name string
		// change changes the fake GitHub, whose part job 101 succeeded with
		// the passing record as its artifact 501 and its log.
		change      func(r *macfix.Reader)
		wantCode    int
		wantVerdict model.Verdict
		wantReasons []model.Incompleteness
		wantPart    model.PartVerdict
	}{
		{
			name:        "from its artifact",
			wantVerdict: model.VerdictPassed,
			wantPart:    partVerdict(model.SourceArtifact, "501", passing),
		},
		{
			name: "artifact missing: from its log, its failure counted",
			change: func(r *macfix.Reader) {
				delete(r.Records, "mac-part-one-2")
				r.Logs[101] = logOf(t, record("one", 2, failing...))
			},
			wantCode:    1,
			wantVerdict: model.VerdictFailed,
			wantPart:    partVerdict(model.SourceLog, "", failing),
		},
		{
			name:        "artifact read fault: from its log",
			change:      func(r *macfix.Reader) { r.Faults["mac-part-one-2"] = errors.New("HTTP 502") },
			wantVerdict: model.VerdictPassed,
			wantPart:    partVerdict(model.SourceLog, "", passing),
		},
		{
			name: "artifact's record doesn't parse: from its log",
			change: func(r *macfix.Reader) {
				r.Records["mac-part-one-2"] = ghrun.ArtifactFile{ArtifactID: 501, Data: []byte("{")}
			},
			wantVerdict: model.VerdictPassed,
			wantPart:    partVerdict(model.SourceLog, "501", passing),
		},
		{
			name:        "neither artifact nor log",
			change:      func(r *macfix.Reader) { delete(r.Records, "mac-part-one-2"); delete(r.Logs, 101) },
			wantCode:    1,
			wantVerdict: model.VerdictIncomplete,
			wantReasons: notRun(neither),
			wantPart:    partVerdict(model.SourceNone, "", nil),
		},
		{
			name: "no artifact, and a fault reading the log",
			change: func(r *macfix.Reader) {
				delete(r.Records, "mac-part-one-2")
				r.Faults["log 101"] = errors.New("HTTP 500")
			},
			wantCode:    1,
			wantVerdict: model.VerdictIncomplete,
			wantReasons: notRun("no record: artifact mac-part-one-2: no record.json; log of job 101: HTTP 500"),
			wantPart:    partVerdict(model.SourceNone, "", nil),
		},
		{
			name:        "no part job",
			change:      func(r *macfix.Reader) { r.JobList = []ghrun.Job{job("two", 102, "success", 2)} },
			wantCode:    1,
			wantVerdict: model.VerdictIncomplete,
			wantReasons: notRun(`the attempt has no job "part one"`),
			wantPart:    model.PartVerdict{Name: "one", Attempt: 2, Source: model.SourceNone},
		},
		{
			name:        "part job skipped",
			change:      func(r *macfix.Reader) { r.JobList[0].Conclusion = "skipped" },
			wantCode:    1,
			wantVerdict: model.VerdictIncomplete,
			wantReasons: notRun(`job "part one" was skipped`),
			wantPart:    partVerdict(model.SourceNone, "", nil),
		},
		{
			name: "part job cancelled, no record",
			change: func(r *macfix.Reader) {
				r.JobList[0].Conclusion = "cancelled"
				delete(r.Records, "mac-part-one-2")
				delete(r.Logs, 101)
			},
			wantCode:    1,
			wantVerdict: model.VerdictIncomplete,
			wantReasons: notRun("job concluded cancelled; " + neither),
			wantPart:    partVerdict(model.SourceNone, "", nil),
		},
		{
			name: "part job failed with no steps (a lost runner), no record",
			change: func(r *macfix.Reader) {
				r.JobList[0].Conclusion, r.JobList[0].Steps = "failure", nil
				delete(r.Records, "mac-part-one-2")
				delete(r.Logs, 101)
			},
			wantCode:    1,
			wantVerdict: model.VerdictIncomplete,
			wantReasons: notRun("job concluded failure; " + neither),
			wantPart:    partVerdict(model.SourceNone, "", nil),
		},
		{
			name: "a setup step failed before Run cases",
			change: func(r *macfix.Reader) {
				r.JobList[0].Conclusion = "failure"
				r.JobList[0].Steps = []ghrun.Step{
					{Name: "Set up job", Number: 1, Status: "completed", Conclusion: "success"},
					{Name: "Set up Go", Number: 2, Status: "completed", Conclusion: "failure"},
					{Name: ghrun.CasesStepName, Number: 3, Status: "completed", Conclusion: "skipped"},
				}
				delete(r.Records, "mac-part-one-2")
				r.Logs[101] = macfix.GitHubLog("##[error]Unable to find Go\n")
			},
			wantCode:    1,
			wantVerdict: model.VerdictIncomplete,
			wantReasons: []model.Incompleteness{{Kind: model.IncompletePartSetup, Part: "one", Detail: `step "Set up Go" failed`}},
			wantPart:    model.PartVerdict{Name: "one", Attempt: 2, Source: model.SourceNone, LogURL: jobURL + "101"},
		},
		{
			// Attempt 2's artifact, which fails, is not this job's.
			name: "carried over from attempt 1",
			change: func(r *macfix.Reader) {
				r.JobList[0].RunAttempt = 1
				r.Records["mac-part-one-1"] = artifact(t, 500, record("one", 1, earlier...))
				r.Records["mac-part-one-2"] = artifact(t, 501, record("one", 2, failing...))
			},
			wantVerdict: model.VerdictPassed,
			wantPart: model.PartVerdict{
				Name: "one", Attempt: 1, Source: model.SourceArtifact, CarriedOver: true, Complete: true,
				LogURL: jobURL + "101", ArtifactURL: artifactURL + "500", Versions: macfix.Versions, Cases: earlier,
			},
		},
		{
			name: "listed in attempt 2, its log attempt 1's, no artifact of attempt 1: from its log, carried over",
			change: func(r *macfix.Reader) {
				listedCarriedOver(r, record("one", 1, earlier...))
				delete(r.Records, "mac-part-one-1")
			},
			wantVerdict: model.VerdictPassed,
			wantPart: model.PartVerdict{
				Name: "one", Attempt: 1, Source: model.SourceLog, CarriedOver: true, Complete: true,
				LogURL: jobURL + "101", Versions: macfix.Versions, Cases: earlier,
			},
		},
		{
			name: "listed in attempt 2, its log attempt 1's for another commit",
			change: func(r *macfix.Reader) {
				rec := record("one", 1, earlier...)
				rec.Commit = "bad"
				listedCarriedOver(r, rec)
			},
			wantCode:    1,
			wantVerdict: model.VerdictIncomplete,
			wantReasons: refused("attempt 1, not 2, commit bad, not c0ffee"),
			wantPart:    partVerdict(model.SourceNone, "", nil),
		},
		{
			name:        "listed in attempt 2, its log attempt 1's for another part",
			change:      func(r *macfix.Reader) { listedCarriedOver(r, record("two", 1, earlier...)) },
			wantCode:    1,
			wantVerdict: model.VerdictIncomplete,
			wantReasons: refused(`part "two", not "one", attempt 1, not 2`),
			wantPart:    partVerdict(model.SourceNone, "", nil),
		},
		{
			name: "listed in attempt 2, its log attempt 1's of another run",
			change: func(r *macfix.Reader) {
				rec := record("one", 1, earlier...)
				rec.RunID = 8
				listedCarriedOver(r, rec)
			},
			wantCode:    1,
			wantVerdict: model.VerdictIncomplete,
			wantReasons: refused("run 8, not 7, attempt 1, not 2"),
			wantPart:    partVerdict(model.SourceNone, "", nil),
		},
		{
			name:        "listed in attempt 2, its log a later attempt's",
			change:      func(r *macfix.Reader) { listedCarriedOver(r, record("one", 3, earlier...)) },
			wantCode:    1,
			wantVerdict: model.VerdictIncomplete,
			wantReasons: refused("attempt 3, not 2"),
			wantPart:    partVerdict(model.SourceNone, "", nil),
		},
		{
			name: "records of another part, run, attempt or commit",
			change: func(r *macfix.Reader) {
				other := record("two", 1, passing...)
				other.RunID, other.Commit = 8, "bad"
				r.Records["mac-part-one-2"] = artifact(t, 501, other)
				otherCommit := record("one", 2, passing...)
				otherCommit.Commit = "bad"
				r.Logs[101] = logOf(t, otherCommit)
			},
			wantCode:    1,
			wantVerdict: model.VerdictIncomplete,
			wantReasons: notRun(`no record: artifact mac-part-one-2: record is for part "two", not "one", run 8, not 7, attempt 1, not 2, ` +
				`commit bad, not c0ffee; log of job 101: record is for commit bad, not c0ffee`),
			wantPart: partVerdict(model.SourceNone, "501", nil),
		},
		{
			name:        "a private repository",
			change:      func(r *macfix.Reader) { r.Private = true },
			wantCode:    1,
			wantVerdict: model.VerdictIncomplete,
			wantReasons: []model.Incompleteness{{Kind: model.IncompleteNotPublic}},
			wantPart:    partVerdict(model.SourceArtifact, "501", passing),
		},
	} {
		r := reader(job("one", 101, "success", 2))
		r.Records["mac-part-one-2"] = artifact(t, 501, record("one", 2, passing...))
		r.Logs[101] = logOf(t, record("one", 2, passing...))
		if tc.change != nil {
			tc.change(r)
		}
		got := runVerdict(t, options(t, oneTree(t)), r)
		if got.code != tc.wantCode || got.v.Verdict != tc.wantVerdict || !reflect.DeepEqual(got.v.Reasons, tc.wantReasons) {
			t.Errorf("%s: Run = %d, %s, reasons %+v; want %d, %s, %+v",
				tc.name, got.code, got.v.Verdict, got.v.Reasons, tc.wantCode, tc.wantVerdict, tc.wantReasons)
		}
		if len(got.v.Parts) != 1 || !reflect.DeepEqual(got.v.Parts[0], tc.wantPart) {
			t.Errorf("%s: parts =\n%+v\nwant\n%+v", tc.name, got.v.Parts, tc.wantPart)
		}
	}
}

func TestRunFaults(t *testing.T) {
	t.Parallel()
	fault := errors.New("HTTP 502 (tried 4 times)")
	for _, tc := range []struct {
		name       string
		head       string
		fault      string
		wantStderr string
	}{
		{
			name:       "attempt tested another commit than the checkout's HEAD",
			head:       "beef",
			wantStderr: "macrun verdict: attempt 2 of run 7 tested c0ffee, but the checkout's HEAD is beef, so the plan would be recomputed from other code\n",
		},
		{name: "attempt unread", fault: "attempt", wantStderr: "macrun verdict: read the attempt: HTTP 502 (tried 4 times)\n"},
		{name: "repository unread", fault: "repository", wantStderr: "macrun verdict: read the repository: HTTP 502 (tried 4 times)\n"},
		{name: "jobs unread", fault: "jobs", wantStderr: "macrun verdict: list the attempt's jobs: HTTP 502 (tried 4 times)\n"},
	} {
		opts := options(t, oneTree(t))
		if tc.head != "" {
			opts.Head = tc.head
		}
		r := reader(job("one", 101, "success", 2))
		r.Records["mac-part-one-2"] = artifact(t, 501, record("one", 2, result("go:pkg/a", model.ResultPass, 1)))
		if tc.fault != "" {
			r.Faults[tc.fault] = fault
		}
		var stdout, stderr bytes.Buffer
		code := verdict.Run(context.Background(), opts, r, &stdout, &stderr)
		if code != 1 || stdout.Len() > 0 || stderr.String() != tc.wantStderr {
			t.Errorf("%s: Run = %d, stdout %q, stderr %q; want 1, nothing, %q", tc.name, code, stdout.String(), stderr.String(), tc.wantStderr)
		}
		if _, err := os.Stat(opts.Out); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: output folder: %v; want none written", tc.name, err)
		}
	}
}

// TestRunPlanFault: a checkout macrun can't plan still gets a verdict,
// incomplete with the fault as its invalid setup, and lists no job.
func TestRunPlanFault(t *testing.T) {
	t.Parallel()
	root := treefix.Write(t, map[string]string{
		"pkg/a/a_test.go":                 pkgA,
		"test/mac/parts.txt":              "one\tgo\tpkg/a\n",
		"test/mac/expected-failures.txt/": "",
	})
	r := reader(job("one", 101, "success", 2))
	r.Faults["jobs"] = errors.New("the jobs are not to be listed")
	opts := options(t, root)
	got := runVerdict(t, opts, r)
	const fault = "macrun couldn't plan the tested commit: read test/mac/expected-failures.txt: is a directory"
	wantReasons := []model.Incompleteness{{Kind: model.IncompleteInvalidSetup, Detail: fault}}
	wantStdout := "verdict: incomplete\nreason: invalid setup: " + fault + "\n"
	if got.code != 1 || got.v.Verdict != model.VerdictIncomplete || !reflect.DeepEqual(got.v.Reasons, wantReasons) ||
		got.v.Parts != nil || got.stdout != wantStdout || got.stderr != "" {
		t.Errorf("Run = %d, %s, reasons %+v, parts %+v, stdout %q, stderr %q; want 1, incomplete, %+v, no part, %q, nothing",
			got.code, got.v.Verdict, got.v.Reasons, got.v.Parts, got.stdout, got.stderr, wantReasons, wantStdout)
	}
	if _, err := os.Stat(filepath.Join(opts.Out, verdict.MarkdownFile)); err != nil {
		t.Errorf("%s not written: %v", verdict.MarkdownFile, err)
	}
}

func TestRunOutputs(t *testing.T) {
	t.Parallel()
	root := treefix.Write(t, map[string]string{
		"pkg/a/a_test.go":    "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\nfunc TestB(t *testing.T) {}\nfunc TestP(t *testing.T) {}\n",
		"pkg/b/b_test.go":    "package b\n\nimport \"testing\"\n\nfunc TestC(t *testing.T) {}\n",
		"pkg/c/c_test.go":    "package c\n\nimport \"testing\"\n\nfunc TestD(t *testing.T) {}\n",
		"test/mac/parts.txt": "one\tgo\tpkg/a\ntwo\tgo\tpkg/b\nthree\tgo\tpkg/c\n",
		"test/mac/expected-failures.txt": "go:pkg/a:TestB\tb.1\tfails on darwin\n" +
			"go:pkg/a:TestA\tb.2\tfixed by b.2\n" +
			"go:pkg/z:TestZ\tb.3\tcase renamed\n",
	})
	one := []model.CaseResult{
		result("go:pkg/a", model.ResultPass, 1), result("go:pkg/a:TestA", model.ResultPass, 0.25),
		result("go:pkg/a:TestB", model.ResultFail, 0.5), result("go:pkg/a:TestP", model.ResultPass, 0.125),
	}
	two := []model.CaseResult{result("go:pkg/b", model.ResultPass, 1), result("go:pkg/b:TestC", model.ResultPass, 0.5)}
	// One read from its artifact, two carried over from attempt 1 and read
	// from its log, three cancelled with no record.
	r := reader(job("one", 101, "success", 2), job("two", 102, "success", 1), job("three", 103, "cancelled", 2))
	r.Records["mac-part-one-2"] = artifact(t, 501, record("one", 2, one...))
	r.Logs[102] = logOf(t, record("two", 1, two...))

	opts := options(t, root)
	opts.Summary = filepath.Join(t.TempDir(), "summary.md")
	if err := os.WriteFile(opts.Summary, []byte("earlier\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := runVerdict(t, opts, r)
	wantStdout := "verdict: incomplete\n" +
		"reason: part not run, missing or cancelled (part three): job concluded cancelled; " +
		"no record: artifact mac-part-three-2: no record.json; log of job 103: none\n" +
		"failed: go:pkg/a:TestB (part one, failed, Expected Failure)\n"
	if got.code != 1 || got.stdout != wantStdout || got.stderr != "" {
		t.Errorf("Run = %d, stdout %q, stderr %q; want 1, %q, nothing", got.code, got.stdout, got.stderr, wantStdout)
	}

	// verdict.json: the links, and every case result of every record read,
	// passing ones included.
	if got.v.RunURL != runURL {
		t.Errorf("run URL = %q; want %q", got.v.RunURL, runURL)
	}
	wantParts := []model.PartVerdict{
		{
			Name: "one", Attempt: 2, Source: model.SourceArtifact, Complete: true,
			LogURL: jobURL + "101", ArtifactURL: artifactURL + "501", Versions: macfix.Versions, Cases: one,
		},
		{Name: "three", Attempt: 2, Source: model.SourceNone, LogURL: jobURL + "103"},
		{
			Name: "two", Attempt: 1, Source: model.SourceLog, CarriedOver: true, Complete: true,
			LogURL: jobURL + "102", Versions: macfix.Versions, Cases: two,
		},
	}
	if !reflect.DeepEqual(got.v.Parts, wantParts) {
		t.Errorf("parts =\n%+v\nwant\n%+v", got.v.Parts, wantParts)
	}
	again := opts
	again.Out, again.Summary = filepath.Join(t.TempDir(), "out"), ""
	if second := runVerdict(t, again, r); !bytes.Equal(second.json, got.json) {
		t.Errorf("verdict.json differs between two runs:\n%s\n%s", got.json, second.json)
	}

	// verdict.md and the summary, which follows what the file held.
	md := verdict.Markdown(got.v, 0)
	if data, _ := os.ReadFile(filepath.Join(opts.Out, verdict.MarkdownFile)); string(data) != md {
		t.Errorf("%s =\n%s\nwant\n%s", verdict.MarkdownFile, data, md)
	}
	if data, _ := os.ReadFile(opts.Summary); string(data) != "earlier\n"+md {
		t.Errorf("summary =\n%s\nwant earlier\\n then %s", data, verdict.MarkdownFile)
	}
	// TestP passed and is unmarked, so only verdict.json lists it.
	if strings.Contains(md, "TestP") {
		t.Errorf("%s lists the passing unmarked case TestP:\n%s", verdict.MarkdownFile, md)
	}

	// A summary over its cap is shortened; verdict.md stays whole.
	short := opts
	short.Out, short.Summary, short.SummaryCap = filepath.Join(t.TempDir(), "out"), filepath.Join(t.TempDir(), "summary.md"), len(md)-1
	runVerdict(t, short, r)
	summary, _ := os.ReadFile(short.Summary)
	whole, _ := os.ReadFile(filepath.Join(short.Out, verdict.MarkdownFile))
	if len(summary) > short.SummaryCap || !strings.Contains(string(summary), "the full lists are in the artifact mac-run-2") || string(whole) != md {
		t.Errorf("capped summary (%d bytes, cap %d) =\n%s\nwith %s whole: %v", len(summary), short.SummaryCap, summary, verdict.MarkdownFile, string(whole) == md)
	}
}

// TestEndToEnd runs a part with macrun part's fakes, then judges it from the
// record and log it wrote: from its artifact, and with the artifact missing,
// from its log.
func TestEndToEnd(t *testing.T) {
	t.Parallel()
	root := oneTree(t)
	partOpts := macfix.PartOptions(t, root, "one")
	g := &macfix.GoRunner{Packages: map[string]macfix.Package{"pkg/a": {Steps: []macfix.Step{
		macfix.Start("go:pkg/a"),
		macfix.Start("go:pkg/a:TestA"), macfix.Output("ok"), macfix.End("go:pkg/a:TestA", model.ResultPass, 0.25),
		macfix.Start("go:pkg/a:TestB"), macfix.Output("boom"), macfix.End("go:pkg/a:TestB", model.ResultFail, 0.5),
		macfix.End("go:pkg/a", model.ResultPass, 0.75),
	}}}}
	var printed bytes.Buffer
	deps := part.Deps{Tools: macfix.Mac(root, "c0ffee"), NewGoRunner: func(string) part.GoRunner { return g }}
	if code := part.Run(context.Background(), partOpts, deps, &printed, io.Discard); code != 0 {
		t.Fatalf("part.Run = %d", code)
	}
	data, err := os.ReadFile(filepath.Join(partOpts.Out, model.RecordFile))
	if err != nil {
		t.Fatal(err)
	}

	wantCases := []model.CaseResult{
		result("go:pkg/a", model.ResultPass, 0.75), result("go:pkg/a:TestA", model.ResultPass, 0.25), result("go:pkg/a:TestB", model.ResultFail, 0.5),
	}
	var verdicts []model.AttemptVerdict
	for _, source := range []model.RecordSource{model.SourceArtifact, model.SourceLog} {
		r := reader(job("one", 101, "success", 2))
		r.Logs[101] = macfix.GitHubLog(printed.String())
		if source == model.SourceArtifact {
			r.Records["mac-part-one-2"] = ghrun.ArtifactFile{ArtifactID: 501, Data: data}
		}
		got := runVerdict(t, options(t, root), r)
		p := got.v.Parts[0]
		wantFailed := []model.FailedCase{{Case: "go:pkg/a:TestB", Part: "one", Reason: model.ReasonFailed}}
		if got.code != 1 || got.v.Verdict != model.VerdictFailed || !reflect.DeepEqual(got.v.FailedCases, wantFailed) ||
			p.Source != source || !p.Complete || !reflect.DeepEqual(p.Cases, wantCases) {
			t.Errorf("from the %s: Run = %d, %s, failed %+v, part %+v; want 1, failed, %+v, complete from the %s with %+v",
				source, got.code, got.v.Verdict, got.v.FailedCases, p, wantFailed, source, wantCases)
		}
		verdicts = append(verdicts, got.v)
	}
	// Apart from where the record came from, both verdicts are the same.
	fromLog := verdicts[1]
	fromLog.Parts = []model.PartVerdict{verdicts[1].Parts[0]}
	fromLog.Parts[0].Source, fromLog.Parts[0].ArtifactURL = model.SourceArtifact, artifactURL+"501"
	if !reflect.DeepEqual(verdicts[0], fromLog) {
		t.Errorf("verdict from the artifact\n%+v\ndiffers from the one from the log\n%+v", verdicts[0], verdicts[1])
	}
}
