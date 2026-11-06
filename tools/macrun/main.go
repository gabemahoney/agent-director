package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/ghrun"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/gorun"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/part"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/plan"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/verdict"
)

// Exit statuses.
const (
	exitOK    = 0
	exitFault = 1
	exitUsage = 2
)

const usage = `usage: macrun <subcommand> [flags]

subcommands:
  plan [-root dir] [-json file] [-github-output file]
        plan a Mac Run from the tree at -root
  part -part name -run-id id -attempt n -commit sha -out dir -bash path
       -sqlite3 path -runner-environment value [-root dir] [-homebrew prefix]
       [-work dir]
        run one part of a Mac Run on this Mac
  verdict -repo owner/name -run-id id -attempt n -out dir [-root dir]
          [-api-url url] [-server-url url] [-summary file]
        judge one attempt of a Mac Run, reading it with GH_TOKEN
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run runs macrun with args, the arguments after the program name, writing
// only to stdout and stderr, and returns the exit status. It never exits the
// process, so tests can drive it in-process.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, "macrun: no subcommand\n"+usage)
		return exitUsage
	}
	switch args[0] {
	case "plan":
		return runPlan(args[1:], stdout, stderr)
	case "part":
		return runPart(args[1:], stdout, stderr)
	case "verdict":
		return runVerdict(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "macrun: unknown subcommand %q\n%s", args[0], usage)
		return exitUsage
	}
}

// runPlan runs macrun plan.
func runPlan(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("macrun plan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "root of the tested tree, the checkout root")
	jsonPath := fs.String("json", "", "write the full plan as JSON to this file")
	githubOutput := fs.String("github-output", "", "append the parts and planned outputs to this $GITHUB_OUTPUT file")
	if code, ok := parseFlags(fs, args, nil, stderr); !ok {
		return code
	}

	p, err := plan.Compute(*root)
	if err != nil {
		fmt.Fprintf(stderr, "macrun plan: %v\n", err)
		return exitFault
	}
	writeSummary(stdout, p)
	if *jsonPath != "" {
		if err := writeJSON(*jsonPath, p); err != nil {
			fmt.Fprintf(stderr, "macrun plan: %v\n", err)
			return exitFault
		}
	}
	if *githubOutput != "" {
		if err := appendGitHubOutput(*githubOutput, p); err != nil {
			fmt.Fprintf(stderr, "macrun plan: %v\n", err)
			return exitFault
		}
	}
	return exitOK
}

// writeSummary writes the plan's short readable form for the job log.
func writeSummary(w io.Writer, p model.Plan) {
	fmt.Fprintf(w, "status: %s\n", p.Status)
	if p.Reason != "" {
		fmt.Fprintf(w, "reason: %s\n", p.Reason)
	}
	for _, e := range p.Errors {
		fmt.Fprintf(w, "error: %s\n", e.Error())
	}
	for _, part := range p.Parts {
		fmt.Fprintf(w, "part %s: %d expected cases\n", part.Name, len(part.ExpectedCases))
	}
	fmt.Fprintf(w, "coverage: go %s, testplan %s\n", p.Coverage.Go, p.Coverage.Testplan)
}

// writeJSON writes the plan as indented JSON to path.
func writeJSON(path string, p model.Plan) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("encode plan: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write plan: %w", err)
	}
	return nil
}

// appendGitHubOutput appends the parts and planned outputs to the
// $GITHUB_OUTPUT file at path: parts, a compact JSON array of the part names
// in file order, and planned, true or false.
func appendGitHubOutput(path string, p model.Plan) error {
	names := []string{}
	for _, part := range p.Parts {
		names = append(names, part.Name)
	}
	encoded, err := json.Marshal(names)
	if err != nil {
		return fmt.Errorf("encode part names: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open GitHub output: %w", err)
	}
	_, err = fmt.Fprintf(f, "parts=%s\nplanned=%t\n", encoded, p.Status == model.StatusPlanned)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write GitHub output: %w", err)
	}
	return nil
}

// parseFlags parses args into fs and checks that no argument is left and
// that each flag named in required was given a non-empty value. ok is false
// when the command must stop, with exit status code.
func parseFlags(fs *flag.FlagSet, args, required []string, stderr io.Writer) (code int, ok bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK, false
		}
		return exitUsage, false
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "%s: unexpected argument %q\n%s", fs.Name(), fs.Arg(0), usage)
		return exitUsage, false
	}
	for _, name := range required {
		if fs.Lookup(name).Value.String() == "" {
			fmt.Fprintf(stderr, "%s: -%s is required\n%s", fs.Name(), name, usage)
			return exitUsage, false
		}
	}
	return exitOK, true
}

// checkRun checks the run ID and attempt flags, which GitHub numbers from 1.
func checkRun(fs *flag.FlagSet, runID int64, attempt int, stderr io.Writer) bool {
	if runID < 1 || attempt < 1 {
		fmt.Fprintf(stderr, "%s: -run-id and -attempt must be positive\n%s", fs.Name(), usage)
		return false
	}
	return true
}

// runPart runs macrun part. SIGINT and SIGTERM, which GitHub sends on a
// cancel or a job timeout, cancel the part.
func runPart(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("macrun part", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var opts part.Options
	fs.StringVar(&opts.Part, "part", "", "the part's name")
	fs.Int64Var(&opts.RunID, "run-id", 0, "the run's ID (github.run_id)")
	fs.IntVar(&opts.Attempt, "attempt", 0, "the run attempt (github.run_attempt)")
	fs.StringVar(&opts.Commit, "commit", "", "the tested commit")
	fs.StringVar(&opts.Out, "out", "", "output folder for "+model.RecordFile)
	fs.StringVar(&opts.Bash, "bash", "", "absolute path of the bash 4.4 or later the tests use")
	fs.StringVar(&opts.SQLite3, "sqlite3", "", "absolute path of the standard SQLite build the tests use")
	fs.StringVar(&opts.RunnerEnvironment, "runner-environment", "", "runner.environment")
	fs.StringVar(&opts.Root, "root", ".", "root of the checkout")
	fs.StringVar(&opts.Homebrew, "homebrew", "/opt/homebrew", "Homebrew's prefix")
	fs.StringVar(&opts.Work, "work", "", "folder for the test binaries (default: a new temporary folder)")
	required := []string{"part", "run-id", "attempt", "commit", "out", "bash", "sqlite3", "runner-environment"}
	if code, ok := parseFlags(fs, args, required, stderr); !ok {
		return code
	}
	if !checkRun(fs, opts.RunID, opts.Attempt, stderr) {
		return exitUsage
	}

	var err error
	if opts.Root, err = filepath.Abs(opts.Root); err != nil {
		fmt.Fprintf(stderr, "macrun part: %v\n", err)
		return exitFault
	}
	if opts.Home, err = os.UserHomeDir(); err != nil {
		fmt.Fprintf(stderr, "macrun part: %v\n", err)
		return exitFault
	}
	if opts.Work == "" {
		if opts.Work, err = os.MkdirTemp("", "macrun-part-"); err != nil {
			fmt.Fprintf(stderr, "macrun part: %v\n", err)
			return exitFault
		}
		defer os.RemoveAll(opts.Work)
	} else if opts.Work, err = filepath.Abs(opts.Work); err != nil {
		fmt.Fprintf(stderr, "macrun part: %v\n", err)
		return exitFault
	}
	opts.Environ = os.Environ()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	deps := part.Deps{
		Tools: part.OSTools{},
		NewGoRunner: func(goCommand string) part.GoRunner {
			return gorun.Runner{Go: goCommand, Exec: gorun.Exec{}}
		},
	}
	return part.Run(ctx, opts, deps, stdout, stderr)
}

// tokenVar holds the run's own token, the only credential macrun reads.
const tokenVar = "GH_TOKEN"

// runVerdict runs macrun verdict.
func runVerdict(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("macrun verdict", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var opts verdict.Options
	fs.StringVar(&opts.Repo, "repo", "", "the repository, owner/name (github.repository)")
	fs.Int64Var(&opts.RunID, "run-id", 0, "the run's ID (github.run_id)")
	fs.IntVar(&opts.Attempt, "attempt", 0, "the judged attempt (github.run_attempt)")
	fs.StringVar(&opts.Out, "out", "", "output folder for "+verdict.JSONFile+" and "+verdict.MarkdownFile)
	fs.StringVar(&opts.Root, "root", ".", "root of the checkout of the tested commit")
	apiURL := fs.String("api-url", "https://api.github.com", "GitHub's REST API URL (github.api_url)")
	fs.StringVar(&opts.ServerURL, "server-url", "https://github.com", "GitHub's web URL (github.server_url)")
	fs.StringVar(&opts.Summary, "summary", "", "append the Markdown summary to this file ($GITHUB_STEP_SUMMARY)")
	if code, ok := parseFlags(fs, args, []string{"repo", "run-id", "attempt", "out"}, stderr); !ok {
		return code
	}
	if !checkRun(fs, opts.RunID, opts.Attempt, stderr) {
		return exitUsage
	}
	token := os.Getenv(tokenVar)
	if token == "" {
		fmt.Fprintf(stderr, "macrun verdict: %s is empty or unset; pass the run's token (github.token) in it\n", tokenVar)
		return exitUsage
	}
	client, err := ghrun.NewClient(*apiURL, opts.Repo, token)
	if err != nil {
		fmt.Fprintf(stderr, "macrun verdict: %v\n%s", err, usage)
		return exitUsage
	}
	opts.ServerURL = strings.TrimSuffix(opts.ServerURL, "/")

	ctx := context.Background()
	if opts.Head, err = gitHead(ctx, opts.Root); err != nil {
		fmt.Fprintf(stderr, "macrun verdict: %v\n", err)
		return exitFault
	}
	return verdict.Run(ctx, opts, client, stdout, stderr)
}

// gitHead returns the HEAD commit of the checkout at root.
func gitHead(ctx context.Context, root string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD").Output()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return "", fmt.Errorf("read the checkout's HEAD: %w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
	}
	if err != nil {
		return "", fmt.Errorf("read the checkout's HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
