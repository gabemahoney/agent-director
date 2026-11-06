package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
	"github.com/gabemahoney/agent-director/tools/macrun/internal/plan"
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
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "macrun plan: unexpected argument %q\n%s", fs.Arg(0), usage)
		return exitUsage
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
