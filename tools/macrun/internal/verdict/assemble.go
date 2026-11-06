// Package verdict judges one attempt of a Mac Run: it recomputes the plan
// from the checkout, finds each planned part's record through GitHub, judges
// the attempt with the result model, and writes the verdict's JSON, its
// Markdown summary and the job log's lines.
package verdict

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/ghrun"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/plan"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/setup"
)

// Options are one verdict's inputs.
type Options struct {
	// Repo is the repository, owner/name.
	Repo string
	// RunID and Attempt name the judged attempt.
	RunID   int64
	Attempt int
	// Root is the checkout's root, and Head its HEAD commit.
	Root string
	Head string
	// ServerURL is GitHub's web URL (github.server_url), for links.
	ServerURL string
	// Out is the output folder for verdict.json and verdict.md.
	Out string
	// Summary is a file the Markdown is appended to ($GITHUB_STEP_SUMMARY),
	// or empty.
	Summary string
	// SummaryCap caps the Markdown appended to Summary; 0 means
	// MaxSummaryBytes.
	SummaryCap int
}

// The part job conclusions the assembly reads.
const (
	conclusionSkipped   = "skipped"
	conclusionCancelled = "cancelled"
	conclusionFailure   = "failure"
)

// The files Run writes to the output folder, which the verdict job uploads
// as the artifact mac-run-<attempt>.
const (
	JSONFile     = "verdict.json"
	MarkdownFile = "verdict.md"
)

// Exit statuses of Run.
const (
	// ExitPassed: the verdict is passed.
	ExitPassed = 0
	// ExitNotPassed: the verdict is failed or incomplete, or a fault left
	// none.
	ExitNotPassed = 1
)

// Run assembles the verdict, writes JSONFile and MarkdownFile to opts.Out
// and the Markdown to opts.Summary, prints the verdict, its reasons and the
// failed cases to stdout, and returns the exit status. A fault is printed to
// stderr and writes no verdict.
func Run(ctx context.Context, opts Options, r ghrun.Reader, stdout, stderr io.Writer) int {
	v, err := Assemble(ctx, opts, r)
	if err != nil {
		fmt.Fprintf(stderr, "macrun verdict: %v\n", err)
		return ExitNotPassed
	}
	if err := writeOutputs(opts, v); err != nil {
		fmt.Fprintf(stderr, "macrun verdict: %v\n", err)
		return ExitNotPassed
	}
	if err := printLines(stdout, v); err != nil {
		fmt.Fprintf(stderr, "macrun verdict: %v\n", err)
		return ExitNotPassed
	}
	if v.Verdict != model.VerdictPassed {
		return ExitNotPassed
	}
	return ExitPassed
}

// writeOutputs writes the verdict's files and appends its summary.
func writeOutputs(opts Options, v model.AttemptVerdict) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode verdict: %w", err)
	}
	if err := os.MkdirAll(opts.Out, 0o755); err != nil {
		return fmt.Errorf("create output folder: %w", err)
	}
	if err := os.WriteFile(filepath.Join(opts.Out, JSONFile), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", JSONFile, err)
	}
	if err := os.WriteFile(filepath.Join(opts.Out, MarkdownFile), []byte(Markdown(v, 0)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", MarkdownFile, err)
	}
	if opts.Summary == "" {
		return nil
	}
	limit := opts.SummaryCap
	if limit <= 0 {
		limit = MaxSummaryBytes
	}
	f, err := os.OpenFile(opts.Summary, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open summary: %w", err)
	}
	_, err = io.WriteString(f, Markdown(v, limit))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write summary: %w", err)
	}
	return nil
}

// printLines prints the verdict, each reason and each failed case, one per
// line, for the job log.
func printLines(w io.Writer, v model.AttemptVerdict) error {
	var b strings.Builder
	fmt.Fprintf(&b, "verdict: %s\n", v.Verdict)
	for _, r := range v.Reasons {
		fmt.Fprintf(&b, "reason: %s\n", reasonText(r))
	}
	for _, f := range v.FailedCases {
		marked := ""
		if f.Marked {
			marked = ", Expected Failure"
		}
		fmt.Fprintf(&b, "failed: %s (part %s, %s%s)\n", f.Case, f.Part, f.Reason, marked)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// reasonText is one reason on one line.
func reasonText(r model.Incompleteness) string {
	text := string(r.Kind)
	if r.Part != "" {
		text += " (part " + r.Part + ")"
	}
	for _, s := range []string{string(r.Case), r.Detail} {
		if s != "" {
			text += ": " + s
		}
	}
	return text
}

// Assemble reads the attempt through r and judges it. An error is a fault
// that leaves nothing to judge: reading the attempt, the repository or the
// jobs, or an attempt that tested another commit than the checkout's, from
// which the plan would be recomputed. A fault reading one part's record only
// makes that part missing.
func Assemble(ctx context.Context, opts Options, r ghrun.Reader) (model.AttemptVerdict, error) {
	attempt, err := r.Attempt(ctx, opts.RunID, opts.Attempt)
	if err != nil {
		return model.AttemptVerdict{}, fmt.Errorf("read the attempt: %w", err)
	}
	if attempt.HeadSHA != opts.Head {
		return model.AttemptVerdict{}, fmt.Errorf("attempt %d of run %d tested %s, but the checkout's HEAD is %s, so the plan would be recomputed from other code",
			opts.Attempt, opts.RunID, attempt.HeadSHA, opts.Head)
	}
	pl, marks := readSetup(opts.Root)
	private, err := r.RepositoryPrivate(ctx)
	if err != nil {
		return model.AttemptVerdict{}, fmt.Errorf("read the repository: %w", err)
	}
	in := model.AttemptInput{
		RunID: opts.RunID, Attempt: opts.Attempt, Commit: attempt.HeadSHA,
		Plan: pl, Marks: marks, Public: !private,
	}
	links := map[string]partLinks{}
	if pl.Status == model.StatusPlanned {
		jobs, err := r.Jobs(ctx, opts.RunID, opts.Attempt)
		if err != nil {
			return model.AttemptVerdict{}, fmt.Errorf("list the attempt's jobs: %w", err)
		}
		for _, part := range pl.Parts {
			pi, l := findPart(ctx, r, opts, attempt.HeadSHA, part.Name, jobs)
			in.Parts = append(in.Parts, pi)
			links[part.Name] = l
		}
	}

	v := model.Judge(in)
	v.RunURL = attempt.HTMLURL
	if v.RunURL == "" {
		v.RunURL = fmt.Sprintf("%s/%s/actions/runs/%d", opts.ServerURL, opts.Repo, opts.RunID)
	}
	for i := range v.Parts {
		l := links[v.Parts[i].Name]
		v.Parts[i].LogURL, v.Parts[i].ArtifactURL = l.log, l.artifact
	}
	return v, nil
}

// readSetup recomputes the plan and reads the Expected Failure marks. A
// fault doing so still gets a verdict: an invalid setup naming the fault.
func readSetup(root string) (model.Plan, []model.Mark) {
	pl, err := plan.Compute(root)
	if err != nil {
		return faultPlan(err), nil
	}
	parsed, _, err := setup.ReadExpectedFailures(root)
	if err != nil {
		return faultPlan(err), nil
	}
	var marks []model.Mark
	for _, m := range parsed {
		marks = append(marks, model.Mark{Case: m.Case, Bug: m.Bug, Note: m.Note})
	}
	return pl, marks
}

// faultPlan is the plan of a tree macrun couldn't plan.
func faultPlan(err error) model.Plan {
	return model.Plan{
		SchemaVersion: model.PlanSchemaVersion,
		Status:        model.StatusInvalidSetup,
		Reason:        fmt.Sprintf("macrun couldn't plan the tested commit: %v", err),
		Coverage:      model.Coverage{Go: model.CoveragePartial, Testplan: model.CoveragePartial},
	}
}

// partLinks are one part's links.
type partLinks struct {
	log, artifact string
}

// findPart finds one planned part's job and record: from the artifact
// mac-part-<part>-<the job's attempt>, else from the job's log. A job
// GitHub carried over from an earlier attempt is read from that attempt.
func findPart(ctx context.Context, r ghrun.Reader, opts Options, commit, name string, jobs []ghrun.Job) (model.PartInput, partLinks) {
	pi := model.PartInput{Name: name, Source: model.SourceNone, Attempt: opts.Attempt}
	job, ok := findJob(jobs, ghrun.PartJobName(name))
	if !ok {
		pi.Detail = fmt.Sprintf("the attempt has no job %q", ghrun.PartJobName(name))
		return pi, partLinks{}
	}
	links := partLinks{log: job.HTMLURL}
	if job.Conclusion == conclusionSkipped {
		pi.Detail = fmt.Sprintf("job %q was skipped", job.Name)
		return pi, links
	}
	if job.RunAttempt > 0 {
		pi.Attempt = job.RunAttempt
	}
	pi.CarriedOver = pi.Attempt < opts.Attempt
	if step, failed := ghrun.FailedSetupStep(job.Steps); failed {
		pi.SetupStep = step.Name
	}
	want := identity{part: name, runID: opts.RunID, attempt: pi.Attempt, commit: commit}

	artifactName := fmt.Sprintf("mac-part-%s-%d", name, pi.Attempt)
	var tried []string
	file, found, err := r.ArtifactFile(ctx, opts.RunID, artifactName, model.RecordFile)
	if file.ArtifactID != 0 {
		links.artifact = fmt.Sprintf("%s/%s/actions/runs/%d/artifacts/%d", opts.ServerURL, opts.Repo, opts.RunID, file.ArtifactID)
	}
	switch {
	case err != nil:
		tried = append(tried, fmt.Sprintf("artifact %s: %v", artifactName, err))
	case !found:
		tried = append(tried, fmt.Sprintf("artifact %s: no %s", artifactName, model.RecordFile))
	default:
		var rec model.Record
		if err := json.Unmarshal(file.Data, &rec); err != nil {
			tried = append(tried, fmt.Sprintf("artifact %s: %s doesn't parse: %v", artifactName, model.RecordFile, err))
		} else if mismatch := want.mismatch(rec); mismatch != "" {
			tried = append(tried, fmt.Sprintf("artifact %s: %s", artifactName, mismatch))
		} else {
			pi.Record, pi.Source = &rec, model.SourceArtifact
			return pi, links
		}
	}

	log, found, err := r.JobLog(ctx, job.ID)
	switch {
	case err != nil:
		tried = append(tried, fmt.Sprintf("log of job %d: %v", job.ID, err))
	case !found:
		tried = append(tried, fmt.Sprintf("log of job %d: none", job.ID))
	default:
		rec, marked, err := model.ReadLog(bytes.NewReader(log))
		mismatch := want.mismatch(rec)
		switch {
		case err != nil:
			tried = append(tried, fmt.Sprintf("log of job %d: %v", job.ID, err))
		case !marked:
			tried = append(tried, fmt.Sprintf("log of job %d: no marked line", job.ID))
		case mismatch != "":
			tried = append(tried, fmt.Sprintf("log of job %d: %s", job.ID, mismatch))
		default:
			pi.Record, pi.Source = &rec, model.SourceLog
			return pi, links
		}
	}

	pi.Detail = "no record: " + strings.Join(tried, "; ")
	if job.Conclusion == conclusionCancelled || job.Conclusion == conclusionFailure && len(job.Steps) == 0 {
		pi.Detail = fmt.Sprintf("job concluded %s; %s", job.Conclusion, pi.Detail)
	}
	return pi, links
}

// findJob returns the job named name.
func findJob(jobs []ghrun.Job, name string) (ghrun.Job, bool) {
	for _, j := range jobs {
		if j.Name == name {
			return j, true
		}
	}
	return ghrun.Job{}, false
}

// identity is what a part's record must say about itself.
type identity struct {
	part    string
	runID   int64
	attempt int
	commit  string
}

// mismatch says how rec is not this part's record, or returns "".
func (id identity) mismatch(rec model.Record) string {
	var diffs []string
	if rec.Part != id.part {
		diffs = append(diffs, fmt.Sprintf("part %q, not %q", rec.Part, id.part))
	}
	if rec.RunID != id.runID {
		diffs = append(diffs, fmt.Sprintf("run %d, not %d", rec.RunID, id.runID))
	}
	if rec.Attempt != id.attempt {
		diffs = append(diffs, fmt.Sprintf("attempt %d, not %d", rec.Attempt, id.attempt))
	}
	if rec.Commit != id.commit {
		diffs = append(diffs, fmt.Sprintf("commit %s, not %s", rec.Commit, id.commit))
	}
	if len(diffs) == 0 {
		return ""
	}
	return "record is for " + strings.Join(diffs, ", ")
}
