package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, out, errout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errout, "usage: claude-live install|verify-kit|record-hook [options]")
		return 2
	}
	verb := args[0]
	f := flag.NewFlagSet(verb, flag.ContinueOnError)
	f.SetOutput(errout)
	root := f.String("evidence", "", "new private evidence directory")
	kit := f.String("kit", "", "verified extracted kit")
	home := f.String("home", runner.TestHome, "VM test HOME")
	runID := f.String("run-id", "", "recorder run")
	event := f.String("event", "", "recorder event")
	variant := f.String("variant", "", "test continuation variant")
	backup := f.String("backup", "", "new private backup directory")
	verdictPath := f.String("out", "", "new offline verdict file")
	maintenance := f.Bool("maintenance-window", false, "operator has excluded old writers")
	recreate := f.Bool("recreate-v21", false, "retain and recreate old v21 test store")
	if verb == "record-hook" || verb == "continue-stop" {
		f.SetOutput(io.Discard)
		if f.Parse(args[1:]) != nil {
			return 0
		}
		timer := time.AfterFunc(2*time.Second, func() { os.Exit(0) })
		defer timer.Stop()
		if verb == "continue-stop" {
			runner.ContinueStop(*root, *runID, os.Stdin, out, *variant)
		} else {
			runner.RecordHook(*root, *runID, *event, os.Stdin)
		}
		return 0
	}
	if err := f.Parse(args[1:]); err != nil {
		return 2
	}
	if f.NArg() != 0 {
		return 2
	}
	if verb == "analyze" {
		v := signals.Analyze(*root)
		if *verdictPath == "" {
			fmt.Fprintln(errout, "analyze requires --out")
			return 2
		}
		file, err := os.OpenFile(*verdictPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 1
		}
		err = json.NewEncoder(file).Encode(v)
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			fmt.Fprintln(errout, "write verdict failed")
			return 1
		}
		fmt.Fprint(out, signals.Format(v))
		if v.Status != "pass" {
			return 1
		}
		return 0
	}
	if verb == "verify-kit" {
		k, err := runner.VerifyKit(*kit)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 1
		}
		if err = json.NewEncoder(out).Encode(k); err != nil {
			return 1
		}
		return 0
	}
	if verb != "install" && verb != "probe" {
		fmt.Fprintln(errout, "unknown phase")
		return 2
	}
	turns := 0
	if verb == "probe" {
		turns = 7
	}
	r, err := runner.NewRun(*home, *kit, *root, verb, turns)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	phase := func(ctx context.Context, r *runner.Run) error {
		return r.Install(ctx, *backup, *recreate, *maintenance)
	}
	var analyze runner.Analyze
	if verb == "probe" {
		phase = signals.Probe
		analyze = signals.Analyze
	}
	v, err := r.ExecutePhase(ctx, phase, analyze)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	if err = json.NewEncoder(out).Encode(v); err != nil {
		return 1
	}
	if v.Status != "pass" {
		return 1
	}
	return 0
}
