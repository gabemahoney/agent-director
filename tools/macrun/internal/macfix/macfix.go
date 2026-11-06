// Package macfix is macrun's reusable command-test fakes: a Mac's tools, the
// Go runner and the GitHub reader that the tests of macrun part and macrun
// verdict share. Only tests use it. A macrun test that needs a fake
// part.Tools, part.GoRunner or ghrun.Reader, or a GitHub-shaped job log, must
// use or extend this package, never its own or a near-copy fake.
package macfix

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/ghrun"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/gorun"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/part"
)

// The fake Mac's tools: the bash and standard SQLite a part is given, the go
// and bun on macrun's own PATH, and Homebrew's prefix.
const (
	Bash     = "/opt/homebrew/bin/bash"
	SQLite3  = "/opt/homebrew/opt/sqlite/bin/sqlite3"
	Go       = "/usr/local/go/bin/go"
	Bun      = "/Users/runner/.bun/bin/bun"
	Homebrew = "/opt/homebrew"
)

// Environ is macrun's own environment on the fake Mac.
var Environ = []string{
	"PATH=/usr/local/go/bin:/Users/runner/.bun/bin:/opt/homebrew/bin:/usr/bin:/bin",
	"HOME=/Users/runner",
	"BYPASS_CONTAINER_FOR_AGENT_DIRECTOR_TESTS=1",
	"AGENT_DIRECTOR_TEST_BINARY=/tmp/agent-director",
	"AGENT_DIRECTOR_TEST_BASH=/bin/bash",
	"RUNNER_TEMP=/Users/runner/work/_temp",
}

// Versions are the versions a part records on the fake Mac.
var Versions = []model.Version{
	{Name: model.VersionMacOSProduct, Value: "15.1"},
	{Name: model.VersionMacOSBuild, Value: "24B83"},
	{Name: model.VersionTmux, Value: "tmux 3.5a"},
	{Name: model.VersionMacOSSQLite3, Value: "3.43.2 2023-10-10"},
	{Name: model.VersionGo, Value: "go1.22.10"},
	{Name: model.VersionBun, Value: "1.1.38"},
	{Name: model.VersionBash, Value: "GNU bash, version 5.2.37(1)-release (aarch64-apple-darwin24.0.0)"},
	{Name: model.VersionStandardSQLite3, Value: "3.47.0 2024-10-21"},
}

// BashVersionInfo is the command line part runs to read bash's
// BASH_VERSINFO.
const BashVersionInfo = Bash + ` -c echo "${BASH_VERSINFO[0]} ${BASH_VERSINFO[1]}"`

// PartOptions returns the options of part partName of run 7, attempt 2,
// testing commit c0ffee in the checkout at root, on the fake Mac, with new
// temporary output, work and home folders.
func PartOptions(t testing.TB, root, partName string) part.Options {
	return part.Options{
		Part: partName, RunID: 7, Attempt: 2, Commit: "c0ffee",
		Out: filepath.Join(t.TempDir(), "out"), Bash: Bash, SQLite3: SQLite3, RunnerEnvironment: "github-hosted",
		Root: root, Homebrew: Homebrew, Work: t.TempDir(), Home: t.TempDir(), Environ: Environ,
	}
}

// Tools is a fake part.Tools: the executables that exist and each command
// line's output.
type Tools struct {
	// Executables are the executable files, by absolute path.
	Executables map[string]bool
	// Outputs maps a command line, its path and arguments joined by spaces,
	// to its output.
	Outputs map[string]string
	// Fail is a command line that fails.
	Fail string
}

var _ part.Tools = (*Tools)(nil)

// Mac returns the tools of a GitHub-hosted Mac on which a part's setup
// passes for the checkout at root, whose HEAD is commit. Homebrew's own go
// and bun are there too, as they can be on the runner image.
func Mac(root, commit string) *Tools {
	t := &Tools{Executables: map[string]bool{}, Outputs: map[string]string{
		"/usr/bin/git -C " + root + " rev-parse HEAD": commit,
		"/usr/bin/sw_vers -productVersion":            "15.1",
		"/usr/bin/sw_vers -buildVersion":              "24B83",
		"/opt/homebrew/bin/tmux -V":                   "tmux 3.5a",
		"/usr/bin/sqlite3 --version":                  "3.43.2 2023-10-10",
		Go + " env GOVERSION":                         "go1.22.10",
		Bun + " --version":                            "1.1.38",
		Bash + " --version":                           "GNU bash, version 5.2.37(1)-release (aarch64-apple-darwin24.0.0)\nCopyright (C) 2022 Free Software Foundation, Inc.",
		SQLite3 + " --version":                        "3.47.0 2024-10-21",
		BashVersionInfo:                               "5 2",
	}}
	for _, path := range []string{
		"/usr/bin/git", "/usr/bin/sw_vers", "/bin/bash", "/usr/bin/sqlite3", Go, Bun,
		"/opt/homebrew/bin/tmux", Bash, SQLite3, "/opt/homebrew/bin/go", "/opt/homebrew/bin/bun",
	} {
		t.Executables[path] = true
	}
	return t
}

// LookPath implements part.Tools.
func (t *Tools) LookPath(name, path string) (string, error) {
	for _, dir := range filepath.SplitList(path) {
		if candidate := filepath.Join(dir, name); dir != "" && t.Executables[candidate] {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s is not on PATH %s", name, path)
}

// IsExecutable implements part.Tools.
func (t *Tools) IsExecutable(path string) bool {
	return t.Executables[path]
}

// Output implements part.Tools.
func (t *Tools) Output(_ context.Context, path string, args ...string) (string, error) {
	line := strings.Join(append([]string{path}, args...), " ")
	if line == t.Fail {
		return "", fmt.Errorf("%s: exit status 1", line)
	}
	out, ok := t.Outputs[line]
	if !ok {
		return "", fmt.Errorf("fake Mac has no output for %q", line)
	}
	return out, nil
}

// GoRunner is a fake part.GoRunner that plays each package's script through
// the run's hooks and records each build and run it was given.
type GoRunner struct {
	// Packages are the scripts by package dir.
	Packages map[string]Package
	// OnBlock is called when a script reaches a Block step, before the run
	// waits for its context to end.
	OnBlock func()
	// Calls are the builds and runs, in order.
	Calls []Call
}

var _ part.GoRunner = (*GoRunner)(nil)

// Package is one package's script.
type Package struct {
	// BuildOutput, when set, fails the package's build with it.
	BuildOutput string
	// Steps are played in order when the package runs.
	Steps []Step
}

// Step is one step of a run: a case start, a line of test output, a case end
// or, with Block, a wait until the run is cancelled.
type Step struct {
	Start  model.CaseID
	Output string
	End    model.CaseResult
	Block  bool
}

// Start is a case start.
func Start(id model.CaseID) Step { return Step{Start: id} }

// Output is a line of test output, kept as the next case end's output.
func Output(line string) Step { return Step{Output: line} }

// End is a case end, a fail with reason failed.
func End(id model.CaseID, r model.Result, elapsed float64) Step {
	end := model.CaseResult{Case: id, Result: r, Elapsed: elapsed}
	if r == model.ResultFail {
		end.Reason = model.ReasonFailed
	}
	return Step{End: end}
}

// Block waits until the run is cancelled.
var Block = Step{Block: true}

// Call is one build or run the fake was given.
type Call struct {
	// Op is "build" or "run".
	Op    string
	Dir   string
	Names []string
	Env   []string
}

// Build implements part.GoRunner.
func (g *GoRunner) Build(_ context.Context, _, dir, workDir string, env []string) (gorun.Build, error) {
	g.Calls = append(g.Calls, Call{Op: "build", Dir: dir, Env: env})
	if out := g.Packages[dir].BuildOutput; out != "" {
		return gorun.Build{Output: out}, nil
	}
	return gorun.Build{Binary: filepath.Join(workDir, strings.ReplaceAll(dir, "/", "_")+".test"), Built: true}, nil
}

// RunTests implements part.GoRunner. It mirrors output lines indented, as
// gorun does, and returns the cases that ended, with the cases still open
// when the script ends or blocks without a result.
func (g *GoRunner) RunTests(ctx context.Context, _, dir, _ string, names, env []string, hooks gorun.Hooks) (gorun.Collected, error) {
	g.Calls = append(g.Calls, Call{Op: "run", Dir: dir, Names: names, Env: env})
	var c gorun.Collected
	var open []model.CaseID
	output := ""
	finish := func() gorun.Collected {
		c.WithoutResult = open
		return c
	}
	for _, s := range g.Packages[dir].Steps {
		switch {
		case s.Block:
			if g.OnBlock != nil {
				g.OnBlock()
			}
			<-ctx.Done()
			return finish(), ctx.Err()
		case s.Start != "":
			open = append(open, s.Start)
			hooks.CaseStart(s.Start)
		case s.Output != "":
			io.WriteString(hooks.Mirror, gorun.MirrorIndent+s.Output+"\n")
			output += s.Output + "\n"
		default:
			for i, id := range open {
				if id == s.End.Case {
					open = append(open[:i:i], open[i+1:]...)
					break
				}
			}
			c.Cases = append(c.Cases, model.CaseRecord{CaseResult: s.End, Output: output})
			output = ""
			hooks.CaseEnd(s.End)
		}
	}
	return finish(), nil
}

// Reader is an in-memory ghrun.Reader of attempt AttemptNumber of run RunID.
// It answers no other run or attempt.
type Reader struct {
	RunID         int64
	AttemptNumber int
	HeadSHA       string
	HTMLURL       string
	Private       bool
	JobList       []ghrun.Job
	// Records are each artifact's model.RecordFile, by artifact name.
	Records map[string]ghrun.ArtifactFile
	// Logs are the job logs, by job ID.
	Logs map[int64][]byte
	// Faults fail reads: "repository", "attempt", "jobs", an artifact's name,
	// or "log <job ID>".
	Faults map[string]error
}

var _ ghrun.Reader = (*Reader)(nil)

func (r *Reader) check(read string, runID int64, attempt int) error {
	if err := r.Faults[read]; err != nil {
		return err
	}
	if runID != r.RunID || attempt != r.AttemptNumber && attempt != 0 {
		return fmt.Errorf("fake reader has no attempt %d of run %d", attempt, runID)
	}
	return nil
}

// RepositoryPrivate implements ghrun.Reader.
func (r *Reader) RepositoryPrivate(context.Context) (bool, error) {
	if err := r.Faults["repository"]; err != nil {
		return false, err
	}
	return r.Private, nil
}

// Attempt implements ghrun.Reader.
func (r *Reader) Attempt(_ context.Context, runID int64, attempt int) (ghrun.Attempt, error) {
	if err := r.check("attempt", runID, attempt); err != nil {
		return ghrun.Attempt{}, err
	}
	return ghrun.Attempt{HeadSHA: r.HeadSHA, HTMLURL: r.HTMLURL}, nil
}

// Jobs implements ghrun.Reader.
func (r *Reader) Jobs(_ context.Context, runID int64, attempt int) ([]ghrun.Job, error) {
	if err := r.check("jobs", runID, attempt); err != nil {
		return nil, err
	}
	return r.JobList, nil
}

// ArtifactFile implements ghrun.Reader.
func (r *Reader) ArtifactFile(_ context.Context, runID int64, name, file string) (ghrun.ArtifactFile, bool, error) {
	if err := r.check(name, runID, 0); err != nil {
		return ghrun.ArtifactFile{}, false, err
	}
	f, ok := r.Records[name]
	if !ok {
		return ghrun.ArtifactFile{}, false, nil
	}
	if file != model.RecordFile {
		return ghrun.ArtifactFile{ArtifactID: f.ArtifactID}, false, nil
	}
	return f, true, nil
}

// JobLog implements ghrun.Reader.
func (r *Reader) JobLog(_ context.Context, jobID int64) ([]byte, bool, error) {
	if err := r.Faults[fmt.Sprintf("log %d", jobID)]; err != nil {
		return nil, false, err
	}
	log, ok := r.Logs[jobID]
	return log, ok, nil
}

// GitHubLog returns what a command printed as GitHub's job log download
// shapes it: a timestamp and a space before each line.
func GitHubLog(printed string) []byte {
	var b strings.Builder
	for _, line := range strings.SplitAfter(printed, "\n") {
		if line != "" {
			b.WriteString("2026-11-05T14:00:00.0000000Z " + line)
		}
	}
	return []byte(b.String())
}
