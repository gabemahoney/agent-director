package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

const deadlineRefusal = "phase deadline exceeds nine minutes"

// Use newRunAt's existing path seam without replacing either real clock.
// No owned agents exist; even an accidental command cannot reach a provider.
func qualificationRun(t *testing.T) *runner.Run {
	t.Helper()
	scope := t.TempDir()
	home := filepath.Join(scope, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	r, err := runner.NewRunAtForTest(scope, home, filepath.Join(scope, "absent-kit"), filepath.Join(scope, "evidence"), "typed-controls", 6)
	if err != nil {
		t.Fatal(err)
	}
	r.Commands.Execute = func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
		t.Error("offline qualification regression attempted a subprocess")
		return -1, errors.New("no subprocess authorized by this unit")
	}
	return r
}

// K1 (z9 a1): separate real clock reads made every phase exceed its own cap.
func TestQualificationExecuteRealClock(t *testing.T) {
	r := qualificationRun(t)
	prepared := false
	_, err := q.Execute(context.Background(), r, func(ctx context.Context, run *runner.Run) error {
		prepared = true
		// An absent ledger stops safely before preflight or provider commands.
		return q.PreparePhase(ctx, run, filepath.Join(run.Home, "absent-ledger.json"))
	}, nil)
	if err != nil {
		t.Errorf("real-clock Execute refused before PreparePhase: %v", err)
	}
	if !prepared {
		t.Error("real-clock phase never reached PreparePhase")
	}
	if got := r.Manifest.Deadline.Sub(r.Manifest.Started); got != runner.PhaseLimit {
		t.Errorf("real-clock deadline interval = %s; want exactly %s", got, runner.PhaseLimit)
	}
}

// K2 (z9 a1): pre-execution refusals must use the runner's evidence/cleanup
// finalization, even when no phase body or agent has started.
func TestQualificationExecuteRefusalFinalization(t *testing.T) {
	r := qualificationRun(t)
	// Independent of K1: keep this refusal after the clock construction is fixed.
	r.Manifest.Deadline = r.Manifest.Started.Add(runner.PhaseLimit + time.Nanosecond)
	runtimeDir := filepath.Dir(r.Socket)
	finalized := false
	r.Finalize = func(context.Context) error {
		finalized = true
		_, err := os.Stat(runtimeDir)
		return r.Evidence.JSON("raw/unit-cleanup-finalization.json", map[string]bool{"runtime_removed": os.IsNotExist(err)})
	}
	_, err := q.Execute(context.Background(), r, func(context.Context, *runner.Run) error {
		t.Error("refused phase body ran")
		return nil
	}, nil)
	if err != nil && err.Error() != deadlineRefusal {
		t.Fatalf("wrong pre-execution refusal: %v", err)
	}
	t.Logf("Execute refusal: %v", err)
	var manifest runner.Manifest
	if b, readErr := os.ReadFile(filepath.Join(r.Evidence.Root, "manifest.json")); readErr != nil {
		t.Errorf("refusal lost manifest.json: %v", readErr)
	} else if decodeErr := json.Unmarshal(b, &manifest); decodeErr != nil {
		t.Error(decodeErr)
	} else if manifest.RunID != r.Manifest.RunID || manifest.Finished.IsZero() {
		t.Errorf("refusal manifest lacks run/finalization: %+v", manifest)
	}
	if _, readErr := os.ReadFile(filepath.Join(r.Evidence.Root, "commands.jsonl")); readErr != nil {
		t.Errorf("refusal lost raw command log commands.jsonl: %v", readErr)
	}
	var verdict runner.Verdict
	if b, readErr := os.ReadFile(filepath.Join(r.Evidence.Root, "verdict.json")); readErr != nil {
		t.Errorf("refusal lost verdict.json: %v", readErr)
	} else if decodeErr := json.Unmarshal(b, &verdict); decodeErr != nil {
		t.Error(decodeErr)
	} else {
		found := false
		for _, result := range verdict.Results {
			found = found || ((result.Status == "fail" || result.Status == "not_completed") && strings.Contains(result.Reason, deadlineRefusal))
		}
		if (verdict.Status != "fail" && verdict.Status != "not_completed") || !found {
			t.Errorf("refusal verdict must be nonpass and name %q: %+v", deadlineRefusal, verdict)
		}
	}
	if !finalized {
		t.Errorf("runner cleanup finalization did not run after %q", deadlineRefusal)
	}
	if _, statErr := os.Stat(runtimeDir); !os.IsNotExist(statErr) {
		t.Errorf("refusal retained private runtime directory: %v", statErr)
	}
	var cleanup map[string]bool
	if b, readErr := os.ReadFile(filepath.Join(r.Evidence.Root, "raw/unit-cleanup-finalization.json")); readErr != nil {
		t.Errorf("refusal lost cleanup record: %v", readErr)
	} else if decodeErr := json.Unmarshal(b, &cleanup); decodeErr != nil {
		t.Error(decodeErr)
	} else if !cleanup["runtime_removed"] {
		t.Error("cleanup finalization ran before private runtime removal")
	}
}
