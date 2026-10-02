package main

import (
	"fmt"
	"io"
	"os"
	"sort"
)

// command is one measure-exit subcommand: its one-line summary and its
// entry point, which parses its own flags and returns the exit code.
type command struct {
	summary string
	run     func(args []string, stdout, stderr io.Writer) int
}

// commands is the subcommand table. The version probe is run's probe mode
// (case probe.exec-form); the host runner bisects across versions.
var commands = map[string]command{
	"run":    {summary: "preflight, then measure the selected cases (-mode real|dry|probe)", run: runCommand},
	"decide": {summary: "print the RN-6, RN-2 and RN-9 decision record from results directories (-in DIR ... [-supersede ID])", run: decideCommand},
	"record": {summary: "RN-9 hook recorder (registered by the harness; reads a hook payload on stdin)", run: recordCommand},
}

func main() {
	os.Exit(dispatch(os.Args[1:], os.Stdout, os.Stderr))
}

// dispatch runs the subcommand named by args[0].
func dispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitUsage
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "measure-exit: unknown subcommand %q\n", args[0])
		usage(stderr)
		return exitUsage
	}
	return cmd.run(args[1:], stdout, stderr)
}

// usage lists the subcommands.
func usage(w io.Writer) {
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintln(w, "usage: measure-exit <subcommand> [flags]")
	for _, n := range names {
		fmt.Fprintf(w, "  %-8s %s\n", n, commands[n].summary)
	}
}
