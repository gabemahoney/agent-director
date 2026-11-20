package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/qualification"
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
	if verb == "qualification-registry" {
		if len(args) != 1 {
			return 2
		}
		if json.NewEncoder(out).Encode(struct {
			Phases   []qualification.Phase       `json:"phases"`
			Coverage []qualification.CoverageRow `json:"coverage"`
			Dialogs  []qualification.DialogKey   `json:"dialogs"`
		}{qualification.Registry(), qualification.Coverage(), qualification.DialogDimensions()}) != nil {
			return 1
		}
		return 0
	}
	f := flag.NewFlagSet(verb, flag.ContinueOnError)
	f.SetOutput(errout)
	sourceRoot := f.String("source-root", "", "scheduler-only frozen source root")
	sourceInventory := f.String("source-inventory", "", "scheduler-only frozen inventory")
	packageGraph := f.String("package-graph", "", "scheduler Go package graph")
	sourceArchive := f.String("source-archive", "", "frozen archive hash")
	buildPlatform := f.String("build-platform", "", "native product build platform")
	toolchain := f.String("toolchain", "", "exact Go toolchain")
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
	ledgerPath := f.String("ledger", "", "absolute persisted qualification turn ledger")
	qualificationID := f.String("qualification-id", "", "qualification run identity")
	attemptID := f.String("attempt", "", "immutable attempt identity")
	candidateID := f.String("candidate", "", "candidate source identity")
	productID := f.String("product", "", "product-code digest")
	providerID := f.String("provider", "", "exact Claude version")
	kitID := f.String("kit-id", "", "qualification kit digest")
	kind := f.String("kind", "shakedown", "shakedown or MI full authority")
	ciCapture := f.String("ci-capture", "", "compatible ci capture identity")
	ciTurns := f.Int("ci-turns", -1, "actual ci turns charged once to E8")
	priorLedger := f.String("prior-ledger", "", "settled prior qualification ledger whose actual starts remain charged to E8")
	dialogCases := f.String("dialog-cases", "D1,D2,D3,D4,D5,D6", "selected Dialog starts, comma separated")
	imports := f.String("imports", "", "compatible completed phase evidence directories, comma separated")
	scheduler := f.String("scheduler-evidence", "", "indexed final-candidate scheduler evidence file")
	clientPlan := f.String("client-plan", "", "immutable owned lost-reply client plan")
	nativePlan := f.String("native-plan", "", "immutable owned native measurement plan")
	phaseID := f.String("phase", "", "qualification phase; nine minutes total, including 90 s of cleanup")
	allowance := f.Int("allowance", 0, "explicit MI allowance for a new rerun attempt")
	previous := f.String("previous", "", "sealed previous summary.json for a linked rerun")
	captures := f.String("captures", "", "sealed current-attempt phase directories, comma separated")
	if verb == "qualification-build-source" {
		if f.Parse(args[1:]) != nil || f.NArg() != 0 {
			return 2
		}
		if err := qualification.BuildProductSource(*sourceRoot, *sourceInventory, *packageGraph, *sourceArchive, *buildPlatform, *toolchain, *verdictPath); err != nil {
			fmt.Fprintln(errout, err)
			return 1
		}
		return 0
	}
	if verb == "qualification-lost-client" {
		if f.Parse(args[1:]) != nil || f.NArg() != 0 {
			return 2
		}
		if err := qualification.LostReplyClient(*clientPlan); err != nil {
			fmt.Fprintln(errout, err)
			return 1
		}
		return 0
	}
	if verb == "qualification-setup-hook" {
		if f.Parse(args[1:]) != nil || f.NArg() != 0 {
			return 2
		}
		timer := time.AfterFunc(3*time.Second, func() { os.Exit(1) })
		defer timer.Stop()
		if err := qualification.SetupUserHook(*root, *runID, *event, os.Stdin); err != nil {
			fmt.Fprintln(errout, err)
			return 1
		}
		return 0
	}
	if verb == "qualification-user-hook" {
		if f.Parse(args[1:]) != nil || f.NArg() != 0 {
			return 2
		}
		timer := time.AfterFunc(3*time.Second, func() { os.Exit(1) })
		defer timer.Stop()
		if err := qualification.UserHook(*root, *runID, os.Stdin, out); err != nil {
			fmt.Fprintln(errout, err)
			return 1
		}
		return 0
	}
	if verb == "native-hook" {
		if f.Parse(args[1:]) != nil {
			return 2
		}
		timer := time.AfterFunc(3*time.Second, func() { os.Exit(1) })
		defer timer.Stop()
		code, err := qualification.NativeHook(*nativePlan, *event, os.Stdin, out, errout)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 1
		}
		return code
	}
	if verb == "qualification-gate" || verb == "stand-in-mcp" {
		if f.Parse(args[1:]) != nil {
			return 2
		}
		var err error
		if verb == "qualification-gate" {
			err = qualification.GateHook(*root, *runID, os.Stdin)
		} else {
			err = qualification.StandInMCP(*root, *runID, os.Stdin, out)
		}
		if err != nil {
			fmt.Fprintln(errout, err)
			return 1
		}
		return 0
	}
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
	if verb == "qualification-rerun" {
		if err := qualification.Rerun(*ledgerPath, *attemptID, *allowance, strings.Split(*phaseID, ","), strings.Split(*dialogCases, ","), *previous); err != nil {
			fmt.Fprintln(errout, err)
			return 1
		}
		return 0
	}
	if verb == "qualification-continuation" {
		l, err := qualification.LoadLedger(*ledgerPath)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 1
		}
		decision, err := qualification.CheckContinuation(*root, l)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 1
		}
		if err = json.NewEncoder(out).Encode(decision); err != nil {
			return 1
		}
		if !decision.ContinueIndependent {
			return 1
		}
		return 0
	}
	if verb == "qualification-summary" {
		l, err := qualification.LoadLedger(*ledgerPath)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 1
		}
		s, err := qualification.WriteQualificationSummary(*verdictPath, l, strings.FieldsFunc(*captures, func(r rune) bool { return r == ',' }), *previous)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 1
		}
		if err = json.NewEncoder(out).Encode(s); err != nil {
			return 1
		}
		if s.Status != "pass" {
			return 1
		}
		return 0
	}
	if verb == "qualification-init" {
		l, err := qualification.NewLedger(qualification.Identity{Run: *qualificationID, Attempt: *attemptID, Candidate: *candidateID, Product: *productID, Provider: *providerID, Kit: *kitID}, *kind, qualification.SignalCharge{Capture: *ciCapture, Product: *productID, Provider: *providerID, Actual: *ciTurns})
		if err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
		l.DialogCases = strings.Split(*dialogCases, ",")
		if *phaseID != "" {
			l.Selected = strings.Split(*phaseID, ",")
		}
		if err := qualification.DialogSelection(l.DialogCases); err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
		if _, err := qualification.AccountTurns(l); err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
		if *priorLedger != "" {
			data, err := runner.Read(filepath.Dir(*priorLedger), filepath.Base(*priorLedger))
			if err == nil {
				err = qualification.AttachPriorLedger(&l, data)
			}
			if err != nil {
				fmt.Fprintln(errout, err)
				return 2
			}
		}
		file, err := os.OpenFile(*ledgerPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 1
		}
		err = json.NewEncoder(file).Encode(l)
		syncErr := file.Sync()
		closeErr := file.Close()
		if err != nil || syncErr != nil || closeErr != nil {
			return 1
		}
		return 0
	}
	if verb == "qualification-analyze" {
		v := qualification.AnalyzePhase(*root)
		if err := json.NewEncoder(out).Encode(v); err != nil {
			return 1
		}
		if v.Status != "pass" {
			return 1
		}
		return 0
	}
	if verb == "qualification-phase" {
		p, err := qualification.FindPhase(*phaseID)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
		r, err := runner.NewRun(*home, *kit, *root, p.ID, p.Turns)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 1
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		v, err := qualification.Execute(ctx, r, func(ctx context.Context, r *runner.Run) error {
			return qualification.Dispatch(ctx, r, qualification.Options{Ledger: *ledgerPath, Backup: *backup, Maintenance: *maintenance, RecreateV21: *recreate, Imports: strings.FieldsFunc(*imports, func(c rune) bool { return c == ',' }), Scheduler: *scheduler})
		}, qualification.AnalyzePhase)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 1
		}
		if json.NewEncoder(out).Encode(v) != nil {
			return 1
		}
		if v.Status != "pass" {
			return 1
		}
		return 0
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
