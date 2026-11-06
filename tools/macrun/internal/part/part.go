// Package part runs one part of a Mac Run on the Mac: it checks the part is
// safe to run, records the tools' versions, builds the tests' environment,
// runs each of the part's packages through the Go runner one at a time, and
// writes the part's record and marked log lines.
package part

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/gorun"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/plan"
)

// The checks and setup steps a setup failure names.
const (
	// StepPlan: the plan of the checkout must be planned and hold the part.
	StepPlan = "plan"
	// StepCheckout: the checkout's HEAD must be the tested commit.
	StepCheckout = "checkout"
	// CheckRunner: the runner must be GitHub-hosted.
	CheckRunner = "github-hosted runner"
	// CheckHome: the home must hold no agent-director store or agent account.
	CheckHome = "no agent state in home"
	// StepTools: the tools the tests use must be found as the part requires.
	StepTools = "tools"
	// StepVersions: every tool's version must be read.
	StepVersions = "versions"
)

// Exit statuses of Run.
const (
	// ExitFinished: the part ran its cases to their end, whatever their
	// results.
	ExitFinished = 0
	// ExitStopped: a setup failure, a cancel or a fault stopped the part.
	ExitStopped = 1
)

// Options are one part's inputs, all explicit, so Run reads no GitHub
// variable, home or environment of its own.
type Options struct {
	// Part, RunID, Attempt and Commit identify the part.
	Part    string
	RunID   int64
	Attempt int
	Commit  string
	// Out is the output folder, which gets model.RecordFile.
	Out string
	// Bash and SQLite3 are the absolute paths of the bash 4.4 or later and
	// the standard SQLite build the tests use.
	Bash    string
	SQLite3 string
	// RunnerEnvironment is runner.environment.
	RunnerEnvironment string
	// Root is the checkout's root.
	Root string
	// Homebrew is Homebrew's prefix.
	Homebrew string
	// Work is an absolute directory for the test binaries.
	Work string
	// Home is the home directory the safety check reads.
	Home string
	// Environ is macrun's own environment.
	Environ []string
}

// GoRunner builds and runs one package's tests; gorun.Runner is the real
// one.
type GoRunner interface {
	Build(ctx context.Context, root, dir, workDir string, env []string) (gorun.Build, error)
	RunTests(ctx context.Context, root, dir, binary string, names []string, env []string, hooks gorun.Hooks) (gorun.Collected, error)
}

// Deps are the part's seams.
type Deps struct {
	// Tools finds and runs the Mac's tools.
	Tools Tools
	// NewGoRunner returns the Go runner for the go command found on macrun's
	// own PATH.
	NewGoRunner func(goCommand string) GoRunner
}

// Run runs one part, printing its marked log lines and mirrored test output
// to stdout and faults to stderr, and returns its exit status. Ending ctx
// cancels it: the running tests are stopped and the record ends cancelled.
func Run(ctx context.Context, opts Options, deps Deps, stdout, stderr io.Writer) int {
	p := &runner{opts: opts, deps: deps, stdout: stdout, stderr: stderr}
	p.rec = model.Record{
		SchemaVersion: model.RecordSchemaVersion,
		Part:          opts.Part,
		RunID:         opts.RunID,
		Attempt:       opts.Attempt,
		Commit:        opts.Commit,
	}
	if err := os.MkdirAll(opts.Out, 0o755); err != nil {
		fmt.Fprintf(stderr, "macrun part: create output folder: %v\n", err)
		return ExitStopped
	}
	planned, tp, failure := p.setUp(ctx)
	switch {
	case failure != nil && ctx.Err() != nil:
		// The cancel, not the step it cut short, stopped the part.
		return p.end(ExitStopped)
	case failure != nil:
		return p.setupFailed(failure)
	}
	return p.runCases(ctx, planned, tp)
}

// runner is one part's run.
type runner struct {
	opts   Options
	deps   Deps
	stdout io.Writer
	stderr io.Writer
	rec    model.Record
	// started reports that part-start was printed.
	started bool
	// logErr is the first fault writing the log, reported once.
	logErr error
}

// setUp runs the checks and setup steps in order and stops at the first
// failure.
func (p *runner) setUp(ctx context.Context) (model.Part, toolPaths, *model.SetupFailure) {
	planned, failure := p.findPart()
	if failure != nil {
		return model.Part{}, toolPaths{}, failure
	}
	if failure := p.checkCheckout(ctx); failure != nil {
		return model.Part{}, toolPaths{}, failure
	}
	if failure := checkSafety(p.opts.RunnerEnvironment, p.opts.Home); failure != nil {
		return model.Part{}, toolPaths{}, failure
	}
	tp, failure := findTools(ctx, p.deps.Tools, p.opts)
	if failure != nil {
		return model.Part{}, toolPaths{}, failure
	}
	if p.rec.Versions, failure = versions(ctx, p.deps.Tools, tp); failure != nil {
		return model.Part{}, toolPaths{}, failure
	}
	return planned, tp, nil
}

// findPart recomputes the plan from the checkout and finds the part in it.
func (p *runner) findPart() (model.Part, *model.SetupFailure) {
	pl, err := plan.Compute(p.opts.Root)
	if err != nil {
		return model.Part{}, &model.SetupFailure{Step: StepPlan, Message: err.Error()}
	}
	if pl.Status != model.StatusPlanned {
		return model.Part{}, &model.SetupFailure{Step: StepPlan, Message: fmt.Sprintf("the plan is %s, not %s: %s", pl.Status, model.StatusPlanned, pl.Reason)}
	}
	for _, part := range pl.Parts {
		if part.Name == p.opts.Part {
			return part, nil
		}
	}
	return model.Part{}, &model.SetupFailure{Step: StepPlan, Message: fmt.Sprintf("the plan has no part %q", p.opts.Part)}
}

// checkCheckout checks that the checkout's HEAD is the tested commit, so the
// cases test the code the run names.
func (p *runner) checkCheckout(ctx context.Context) *model.SetupFailure {
	git, err := p.deps.Tools.LookPath("git", lookupEnv(p.opts.Environ, envPath))
	if err != nil {
		return &model.SetupFailure{Step: StepCheckout, Message: fmt.Sprintf("git: %v", err)}
	}
	head, err := p.deps.Tools.Output(ctx, git, "-C", p.opts.Root, "rev-parse", "HEAD")
	if err != nil {
		return &model.SetupFailure{Step: StepCheckout, Message: err.Error()}
	}
	if head != p.opts.Commit {
		return &model.SetupFailure{Step: StepCheckout, Message: fmt.Sprintf("HEAD of %s is %s, not the tested commit %s", p.opts.Root, head, p.opts.Commit)}
	}
	return nil
}

// setupFailed records a setup failure, with no case run.
func (p *runner) setupFailed(failure *model.SetupFailure) int {
	p.rec.SetupFailure = failure
	p.event(model.Event{
		Event: model.EventSetupFailed, Part: p.rec.Part, RunID: p.rec.RunID, Attempt: p.rec.Attempt, Commit: p.rec.Commit,
		Step: failure.Step, Message: failure.Message,
	})
	fmt.Fprintf(p.stderr, "macrun part: %s failed: %s\n", failure.Step, failure.Message)
	p.writeRecord()
	return ExitStopped
}

// runCases runs each package of the part in plan order, one at a time, with
// test output inside a stop-commands block so it can issue no workflow
// command.
func (p *runner) runCases(ctx context.Context, planned model.Part, tp toolPaths) int {
	p.rec.Configurations = []model.Configuration{model.ConfigurationDefault}
	p.rec.ExpectedCases = planned.ExpectedCases
	token, err := stopToken()
	if err != nil {
		fmt.Fprintf(p.stderr, "macrun part: %v\n", err)
		p.writeRecord()
		return ExitStopped
	}
	p.partStart()
	p.print("::stop-commands::" + token)
	env := testsEnv(p.opts.Environ, tp)
	goRunner := p.deps.NewGoRunner(tp.goCmd)
	for _, pkg := range planned.Packages {
		if ctx.Err() != nil {
			break
		}
		p.runPackage(ctx, goRunner, pkg, env)
	}
	p.print("::" + token + "::")
	if ctx.Err() != nil {
		return p.end(ExitStopped)
	}
	return p.end(ExitFinished)
}

// partStart prints the part-start line.
func (p *runner) partStart() {
	p.started = true
	p.event(model.Event{
		Event: model.EventPartStart, Part: p.rec.Part, RunID: p.rec.RunID, Attempt: p.rec.Attempt, Commit: p.rec.Commit,
		Versions: p.rec.Versions, Configurations: p.rec.Configurations,
	})
}

// end ends the part finished for ExitFinished and cancelled otherwise,
// printing part-end (after part-start, when a cancel cut setup short) and
// writing the record, and returns its exit status.
func (p *runner) end(exit int) int {
	p.rec.End = model.EndFinished
	if exit != ExitFinished {
		p.rec.End = model.EndCancelled
	}
	if !p.started {
		p.partStart()
	}
	p.event(model.Event{Event: model.EventPartEnd, End: p.rec.End})
	if !p.writeRecord() {
		return ExitStopped
	}
	return exit
}

// runPackage builds one package and, if it built, runs its tests. A package
// that fails to build leaves its cases without a result.
func (p *runner) runPackage(ctx context.Context, goRunner GoRunner, pkg model.Package, env []string) {
	build, err := goRunner.Build(ctx, p.opts.Root, pkg.Dir, p.opts.Work, env)
	if ctx.Err() != nil {
		// A build the cancel cut short says nothing about the package.
		return
	}
	if err != nil {
		fmt.Fprintf(p.stderr, "macrun part: %v\n", err)
		build.Output = err.Error()
	}
	pb := model.PackageBuild{Dir: pkg.Dir, Built: build.Built}
	if !build.Built {
		pb.Output, pb.OutputCut = model.CapOutput(build.Output)
		p.mirror(build.Output)
	}
	p.rec.Packages = append(p.rec.Packages, pb)
	p.event(model.Event{Event: model.EventPackageBuild, Dir: pkg.Dir, Built: build.Built})
	if !build.Built {
		return
	}
	hooks := gorun.Hooks{
		CaseStart: func(id model.CaseID) { p.event(model.Event{Event: model.EventCaseStart, Case: id}) },
		CaseEnd: func(r model.CaseResult) {
			p.event(model.Event{Event: model.EventCaseEnd, Case: r.Case, Result: r.Result, Reason: r.Reason, Elapsed: r.Elapsed})
		},
		Mirror: p.stdout,
	}
	collected, err := goRunner.RunTests(ctx, p.opts.Root, pkg.Dir, build.Binary, pkg.Tests, env, hooks)
	if err != nil && ctx.Err() == nil {
		fmt.Fprintf(p.stderr, "macrun part: %v\n", err)
	}
	p.rec.Cases = append(p.rec.Cases, collected.Cases...)
	p.rec.WithoutResult = append(p.rec.WithoutResult, collected.WithoutResult...)
}

// mirror prints text indented, as the Go runner mirrors test output.
func (p *runner) mirror(text string) {
	for _, line := range strings.SplitAfter(text, "\n") {
		if line != "" {
			p.print(gorun.MirrorIndent + strings.TrimSuffix(line, "\n"))
		}
	}
}

// event prints one marked log line.
func (p *runner) event(e model.Event) {
	p.noteLogErr(model.WriteEvent(p.stdout, e))
}

// print prints one line.
func (p *runner) print(line string) {
	_, err := fmt.Fprintln(p.stdout, line)
	p.noteLogErr(err)
}

// noteLogErr reports the first fault writing the log. The record still
// holds everything, so the part goes on.
func (p *runner) noteLogErr(err error) {
	if err != nil && p.logErr == nil {
		p.logErr = err
		fmt.Fprintf(p.stderr, "macrun part: write job log: %v\n", err)
	}
}

// writeRecord writes the record to model.RecordFile in the output folder,
// through a temporary file there, so a reader never sees half a record. It
// reports whether it did.
func (p *runner) writeRecord() bool {
	if err := writeRecord(p.opts.Out, p.rec); err != nil {
		fmt.Fprintf(p.stderr, "macrun part: %v\n", err)
		return false
	}
	return true
}

func writeRecord(out string, rec model.Record) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("encode record: %w", err)
	}
	tmp, err := os.CreateTemp(out, "."+model.RecordFile+".*")
	if err != nil {
		return fmt.Errorf("write record: %w", err)
	}
	_, err = tmp.Write(append(data, '\n'))
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), filepath.Join(out, model.RecordFile))
	}
	if err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("write record: %w", err)
	}
	return nil
}

// stopToken returns a random token for ::stop-commands::, which test output
// can't guess to resume workflow commands.
func stopToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("make stop-commands token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
