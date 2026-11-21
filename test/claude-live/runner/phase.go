package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/google/uuid"
)

const TestHome = "/home/horde/ad-test/home"
const VMRoot = "/home/horde/ad-test"
const PhaseLimit = 9 * time.Minute
const CleanupReserve = 90 * time.Second

type Phase func(context.Context, *Run) error
type Analyze func(string) Verdict

type Run struct {
	Home            string
	Kit             string
	Evidence        *Evidence
	Manifest        Manifest
	Commands        *Commands
	Settings        *Settings
	Now             func() time.Time
	Sleep           func(context.Context, time.Duration) error
	ProviderCommand string
	Socket          string
	// Qualification records reconciled usage after canonical cleanup and the
	// recorder drain, before the raw-artifact index is sealed.
	Finalize          func(context.Context) error
	StopOnUnreserved  bool
	LaunchArtifacts   bool
	AllowOwnedInstall bool
	runtimeDir        string
	runRoot           string
	runtimeFiles      []string
}

func Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func ValidateHome(home string) error {
	return validateHome(home, TestHome)
}

func validateHome(home, expected string) error {
	if home != expected || os.Getenv("HOME") != home {
		return fmt.Errorf("live operations require ad-shell and %s", expected)
	}
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" && dir != filepath.Join(home, ".claude") {
		return errors.New("Claude config must belong to the test HOME")
	}
	real, err := filepath.EvalSymlinks(home)
	if err != nil {
		return err
	}
	if real != home {
		return errors.New("test HOME is a symlink")
	}
	return nil
}

func NewRun(home, kit, root, phase string, turns int) (*Run, error) {
	return newRunAt(VMRoot, home, kit, root, phase, turns)
}

// newRunAt is the path seam for same-package scheduler tests. Public callers
// can only select VMRoot through NewRun; there is no flag or environment override.
// Retain the selected root for every later home, backup and recorder check.
func newRunAt(runRoot, home, kit, root, phase string, turns int) (*Run, error) {
	if err := validateHome(home, filepath.Join(runRoot, "home")); err != nil {
		return nil, err
	}
	if turns < 0 || turns > 45 {
		return nil, errors.New("invalid phase turn budget")
	}
	if filepath.Dir(root) == runRoot { // All phase paths are private children of the test root.
		if _, err := Confined(runRoot, filepath.Base(root)); err != nil {
			return nil, err
		}
	} else {
		return nil, fmt.Errorf("evidence directory must be a fresh direct child of %s", runRoot)
	}
	e, err := NewEvidence(root)
	if err != nil {
		return nil, err
	}
	now := time.Now
	r := &Run{Home: home, Kit: kit, Evidence: e, Now: now, Sleep: Sleep, runRoot: runRoot}
	// Runtime IPC is not an evidence artifact and cannot be copied with scp.
	r.runtimeDir, err = os.MkdirTemp(runRoot, "claude-live-runtime-")
	if err != nil {
		return nil, err
	}
	r.Socket = filepath.Join(r.runtimeDir, "tmux.sock")
	t := now()
	r.Manifest = Manifest{Schema: Schema, RunID: uuid.NewString(), Phase: phase, Platform: runtime.GOOS + "/" + runtime.GOARCH, Started: t.UTC(), Deadline: t.Add(PhaseLimit).UTC(), TurnBudget: turns}
	r.Commands = &Commands{Evidence: e, Execute: Exec, Environment: Environment(r.Socket), Now: now}
	return r, nil
}

// Zero-value/literal Runs retain production confinement too. Only newRunAt,
// called by NewRun or same-package tests, can select another root.
func (r *Run) scopeRoot() string {
	if r.runRoot != "" {
		return r.runRoot
	}
	return VMRoot
}

func (r *Run) AD(ctx context.Context, name string, args ...string) (Command, []byte, error) {
	return r.Commands.Run(ctx, name, 45*time.Second, append([]string{filepath.Join(r.Home, ".config/agent-director/bin/agent-director")}, args...)...)
}

func (r *Run) Problem(id, status string, err error) {
	r.Manifest.Problems = append(r.Manifest.Problems, Result{SignalID: id, Status: status, Reason: err.Error(), Expected: "completed without error", Observed: err.Error(), Evidence: []string{"commands.jsonl"}})
}

func (r *Run) ReserveTurn() error {
	if r.Manifest.TurnsReserved >= r.Manifest.TurnBudget {
		return errors.New("phase turn budget exhausted; no repeat authorized")
	}
	r.Manifest.TurnsReserved++
	return nil
}

// ExecutePhase reserves a separate bounded cleanup context even on cancellation.
// Every caught error is retained next to any independent analyzer failures.
func (r *Run) ExecutePhase(parent context.Context, phase Phase, analyze Analyze) (v Verdict, err error) {
	deadline := r.Manifest.Deadline
	// Refused metadata still needs cleanup, within the runner's hard limit.
	if limit := r.Manifest.Started.Add(PhaseLimit); deadline.After(limit) {
		deadline = limit
	}
	ctx, cancel := context.WithDeadline(parent, deadline.Add(-CleanupReserve))
	defer cancel()
	defer func() {
		if x := recover(); x != nil {
			r.Problem("phase", "not_completed", fmt.Errorf("phase panic: %v", x))
		}
		cleanup, c := context.WithDeadline(context.Background(), deadline)
		defer c()
		if e := r.Cleanup(cleanup); e != nil {
			r.Problem("cleanup", "not_completed", e)
		}
		if e := r.RemoveContinuation(); e != nil {
			r.Problem("continuation-hook-cleanup", "not_completed", e)
		}
		if e := r.removeRuntime(); e != nil {
			r.Problem("runtime-cleanup", "not_completed", e)
		}
		if r.Settings != nil {
			if e := r.Settings.Remove(); e != nil {
				r.Problem("recording-hook-cleanup", "not_completed", e)
			} else {
				r.Manifest.RecordingHook.Removed = true
			}
			if e := r.Sleep(cleanup, 3*time.Second); e != nil {
				r.Problem("recorder-drain", "not_completed", e)
			}
		}
		if r.Manifest.RecordingHook.Installed {
			inventory, e := InspectRecordings(r.Evidence.Root, r.Manifest.RunID, r.Manifest.RecordingHook.Events)
			if e == nil {
				e = r.Evidence.JSON("raw/hook-inventory.json", inventory)
			}
			if e != nil {
				r.Problem("recording-hook-inventory", "not_completed", e)
			}
			for _, invocation := range inventory {
				if !invocation.Complete {
					r.Problem("recording-hook-incomplete", "fail", fmt.Errorf("%s: %s", invocation.Directory, invocation.Reason))
				}
			}
		}
		if r.Finalize != nil {
			if e := r.Finalize(cleanup); e != nil {
				r.Problem("qualification-finalization", "not_completed", e)
			}
		}
		if e := r.Commands.Finish(); e != nil {
			r.Problem("command-evidence", "not_completed", e)
		}
		r.Manifest.Finished = r.Now().UTC()
		index, e := r.Evidence.Index()
		if e != nil {
			r.Problem("artifact-index", "not_completed", e)
		}
		r.Manifest.Artifacts = index
		err = r.Evidence.JSON("manifest.json", r.Manifest)
		v = Verdict{Schema: Schema, RunID: r.Manifest.RunID, Phase: r.Manifest.Phase, Candidate: r.Manifest.Candidate, ProviderVersion: r.Manifest.ProviderVersion, Results: r.Manifest.Problems}
		if analyze != nil && err == nil {
			v = analyze(r.Evidence.Root)
			// An analyzer cannot erase an independently observed phase failure.
			for _, problem := range r.Manifest.Problems {
				found := false
				for _, result := range v.Results {
					if result.SignalID == problem.SignalID && result.Status == problem.Status && result.Reason == problem.Reason {
						found = true
					}
				}
				if !found {
					v.Results = append(v.Results, problem)
				}
			}
		}
		if len(v.Results) == 0 {
			v.Results = append(v.Results, Result{SignalID: "phase", Status: "pass", Reason: "phase completed", Expected: "completed", Observed: "completed"})
		}
		v.Aggregate()
		err = errors.Join(err, r.Evidence.JSON("verdict.json", v))
	}()
	if e := phase(ctx, r); e != nil {
		status := "not_completed"
		if errors.Is(e, ErrTurnBudget) {
			status = "fail"
		}
		r.Problem("phase", status, e)
	}
	return v, nil
}
