// Package qualification extends the shared live runner. Analysis in this
// package is file-only; operations against Claude belong to explicitly invoked
// phases, confined by runner.NewRun to the VM test HOME.
// Future qualification work must reuse this analysis, coverage and accounting
// package; see docs/architecture.md, "Claude qualification (reusable analysis
// and accounting)", for the reuse instructions.
package qualification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

const Schema = 1

type Phase struct {
	ID        string   `json:"id"`
	Turns     int      `json:"turns"`
	Shakedown bool     `json:"shakedown"`
	Checks    []string `json:"checks"`
}

// Registry is also the source of the runbook's phase and coverage tables.
// Future phase selection and reporting must reuse it; see docs/architecture.md.
func Registry() []Phase {
	return []Phase{
		{"setup-launches", 4, false, []string{"install-resume", "launch-setup", "defaults"}},
		{"signals", 7, false, []string{"signal-capture", "p-b", "p-c", "turn-end-premise", "wake-order-point"}},
		{"typed-controls", 6, true, []string{"typed-long-tool", "typed-between-tools", "typed-idle", "steer-cap"}},
		{"force-states", 5, true, []string{"force-table", "force-pending", "force-group", "wake-caps"}},
		{"dialogs", 6, true, []string{"dialog-matrix", "pause-refusal"}},
		{"groups", 3, true, []string{"steer-group", "ordinary-group", "group-gaps", "small-backlog", "whole-corpus"}},
		{"text-limits-backlog", 8, true, []string{"native-wake-ladder", "native-steer-ladder", "ad-caps", "ad-refusals", "large-backlog"}},
		{"lifecycle", 2, false, []string{"lost-reply", "crash-resume", "fresh-reuse", "concurrent-delivery", "read-only"}},
		{"user-hooks", 2, false, []string{"hook-block", "hook-context", "post-sighting-hook", "never-sighted-hold", "stale-unknown"}},
		{"esc-gap", 4, true, []string{"esc-typed-recovery", "esc-resume-recovery", "esc-notification", "esc-input-placement"}},
		{"uncovered", 0, false, []string{"uncovered-agents"}},
	}
}

func FindPhase(id string) (Phase, error) {
	for _, p := range Registry() {
		if p.ID == id {
			return p, nil
		}
	}
	return Phase{}, fmt.Errorf("unknown qualification phase %q", id)
}

type Identity struct {
	Run       string `json:"run"`
	Attempt   string `json:"attempt"`
	Candidate string `json:"candidate"`
	Product   string `json:"product"`
	Provider  string `json:"provider"`
	Kit       string `json:"kit"`
	Previous  string `json:"previous,omitempty"`
}

func (i Identity) Validate() error {
	if i.Run == "" || i.Attempt == "" || i.Candidate == "" || i.Product == "" || i.Provider == "" || i.Kit == "" {
		return errors.New("incomplete qualification identity")
	}
	if strings.ContainsAny(i.Run+i.Attempt, "/\\\n\r\t") {
		return errors.New("run and attempt identities must be plain keys")
	}
	return nil
}

func (i Identity) Compatible(j Identity) bool {
	return i.Candidate == j.Candidate && i.Product == j.Product && i.Provider == j.Provider && i.Run == j.Run
}

// PhaseResult never treats an unimplemented or prerequisite-blocked phase as
// success. It is the same finalizer used for recordings and live runs.
func PhaseResult(m runner.Manifest, checks []runner.Result) runner.Verdict {
	v := runner.Verdict{Schema: runner.Schema, RunID: m.RunID, Phase: m.Phase, Candidate: m.Candidate, ProviderVersion: m.ProviderVersion}
	p, err := FindPhase(m.Phase)
	if err != nil {
		v.Results = []runner.Result{result("phase", "fail", err.Error(), "registered phase", m.Phase)}
		v.Aggregate()
		return v
	}
	for _, id := range p.Checks {
		found := false
		for _, c := range checks {
			if c.SignalID == id {
				v.Results = append(v.Results, c)
				found = true
			}
		}
		if !found {
			v.Results = append(v.Results, result(id, "not_completed", "required observations absent", "complete compatible evidence", nil))
		}
	}
	for _, check := range checks {
		if !slices.Contains(p.Checks, check.SignalID) {
			v.Results = append(v.Results, check)
		}
	}
	v.Results = append(v.Results, m.Problems...)
	v.Aggregate()
	return v
}

func result(id, status, reason string, expected, observed any, refs ...string) runner.Result {
	refs = slices.DeleteFunc(refs, func(ref string) bool { return ref == "" })
	return runner.Result{SignalID: id, Status: status, Reason: reason, Expected: expected, Observed: observed, Evidence: refs}
}

// Execute preserves the runner's single command/evidence/cleanup lifecycle.
func Execute(ctx context.Context, r *runner.Run, phase runner.Phase, analyze runner.Analyze) (runner.Verdict, error) {
	return r.ExecutePhase(ctx, func(ctx context.Context, r *runner.Run) error {
		p, err := FindPhase(r.Manifest.Phase)
		if err != nil {
			return err
		}
		r.Manifest.ExpectedChecks = append([]string(nil), p.Checks...)
		if r.Manifest.TurnBudget > p.Turns {
			return errors.New("phase exceeds registered maximum")
		}
		if r.Manifest.Deadline.Sub(r.Manifest.Started) > runner.PhaseLimit {
			return errors.New("phase deadline exceeds nine minutes")
		}
		return phase(ctx, r)
	}, analyze)
}

func LoadLedger(path string) (Ledger, error) {
	var l Ledger
	b, err := runner.Read(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return l, err
	}
	if err = json.Unmarshal(b, &l); err != nil {
		return l, err
	}
	_, err = AccountTurns(l)
	return l, err
}

// PreparePhase is called inside Execute so even a prerequisite failure gets
// a manifest, raw command log, verdict and the runner's cleanup finalization.
func PreparePhase(ctx context.Context, r *runner.Run, ledgerPath string) error {
	l, err := LoadLedger(ledgerPath)
	if err != nil {
		return err
	}
	if !slices.Contains(l.Selected, r.Manifest.Phase) {
		return errors.New("phase not selected by this attempt's authority")
	}
	if err = r.Preflight(ctx); err != nil {
		return err
	}
	if _, err = r.VerifyCandidate(ctx, r.Manifest.Phase != "setup-launches"); err != nil {
		return err
	}
	if r.Manifest.Candidate != l.Identity.Candidate || r.Manifest.ProviderVersion != l.Identity.Provider || r.Manifest.KitSHA256 != l.Identity.Kit {
		return errors.New("candidate/provider/kit differs from persisted authority")
	}
	k, err := runner.VerifyKit(r.Kit)
	if err != nil {
		return err
	}
	source, err := LoadProductSource(r.Kit, "qualification/source.json")
	if err != nil {
		return err
	}
	if err = VerifyProductKit(source, k); err != nil {
		return err
	}
	if source.Product != l.Identity.Product {
		return errors.New("product-code identity differs from frozen candidate build inputs")
	}
	for _, name := range []string{"qualification/source.json", "docs/caller-contract.md", "qualification/launch.go", "qualification/spawns.go"} {
		if _, ok := k.Files[name]; !ok {
			return fmt.Errorf("qualification kit missing required asset %s", name)
		}
		b, er := runner.Read(r.Kit, name)
		if er != nil {
			return er
		}
		ref := "raw/" + name
		if er = r.Evidence.Put(ref, b); er != nil {
			return er
		}
	}
	return r.Evidence.JSON("raw/qualification-ledger-before.json", l)
}

type Options struct {
	Ledger      string
	Backup      string
	Maintenance bool
	RecreateV21 bool
	Imports     []string
	Scheduler   string
}

// Dispatch binds each operational phase to the existing runner. Unimplemented
// phases remain explicit until their sequential source Subtasks land.
func Dispatch(ctx context.Context, r *runner.Run, options Options) error {
	return StartExercise(ctx, r, options.Ledger, func(ctx context.Context, e *Exercise) error {
		e.Options = options
		// Retain and validate sealed producers before any phase can reserve a
		// turn, spawn an agent or send input; offline imports share this gate.
		if err := e.retainImports(); err != nil {
			return err
		}
		switch r.Manifest.Phase {
		case "signals":
			return nil // Retained ci proof is charged once in the ledger.
		case "setup-launches":
			return SetupLaunches(ctx, e)
		case "typed-controls":
			return TypedControls(ctx, e)
		case "force-states":
			return ForceStates(ctx, e)
		case "dialogs":
			return Dialogs(ctx, e)
		case "groups":
			return Groups(ctx, e)
		case "text-limits-backlog":
			return TextLimitsBacklog(ctx, e)
		case "lifecycle":
			return Lifecycle(ctx, e)
		case "user-hooks":
			return UserHooks(ctx, e)
		case "esc-gap":
			return EscGap(ctx, e)
		case "uncovered":
			return Uncovered(ctx, e)
		default:
			return fmt.Errorf("phase %s not implemented", r.Manifest.Phase)
		}
	})
}
