package runner_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/test/claude-live/internal/bindingfix"
	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

// R11-2 enters the actual native.go startup callback. Commands intercept tmux;
// a harmless child supplies real process identity, never a provider launch.
func TestQualificationNativeStartupAwaitsRecording(t *testing.T) {
	for _, scenario := range []string{"receipt-ready", "receipt-later", "never-recorded"} {
		t.Run(scenario, func(t *testing.T) {
			r, clock := runner.RunnerFixtureForTest(t)
			r.Manifest.RecordingHook = runner.RecordingHook{Installed: true, Directory: "raw/hooks", Events: []string{"SessionStart"}}
			if err := os.MkdirAll(filepath.Join(r.Evidence.Root, "raw/hooks"), 0700); err != nil {
				t.Fatal(err)
			}
			pid, _, stop := bindingfix.Child(t)
			waits, kills := 0, 0
			publish := func() {
				o := r.Manifest.Owned[0]
				path := apitest.SeedJsonlUnder(t, filepath.Join(r.Home, ".claude"), o.CWD, o.SessionID)
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
				input, err := json.Marshal(runner.Row{"hook_event_name": "SessionStart", "session_id": o.SessionID, "cwd": o.CWD, "transcript_path": path})
				if err != nil {
					t.Fatal(err)
				}
				runner.RecordOwnedHook(r.Evidence.Root, r.Manifest.RunID, "SessionStart", bytes.NewReader(input), o.InstanceID)
			}
			r.Commands.Execute = func(_ context.Context, argv, _ []string, out, _ io.Writer) (int, error) {
				if len(argv) < 4 || argv[0] != "tmux" || argv[1] != "-S" || argv[2] != r.Socket {
					t.Fatalf("unexpected native command: %q", argv)
				}
				switch argv[3] {
				case "new-session":
					_, err := fmt.Fprintf(out, "$1 %d\n", pid)
					return 0, err
				case "set-option":
					if scenario == "receipt-ready" {
						publish()
					}
				case "show-option":
					_, err := io.WriteString(out, r.Manifest.RunID+"\n")
					return 0, err
				case "kill-session":
					kills++
					stop()
				default:
					t.Fatalf("native startup crossed its checkpoint: %q", argv)
				}
				return 0, nil
			}
			r.Sleep = func(ctx context.Context, d time.Duration) error {
				clock.Advance(d)
				waits++
				if scenario == "never-recorded" {
					if waits >= 3 {
						return context.DeadlineExceeded
					}
					return ctx.Err()
				}
				if waits != 1 {
					return context.DeadlineExceeded
				}
				publish()
				return ctx.Err()
			}
			// A deliberately absent ledger stops NativeMeasurements immediately after
			// startup, before reserving or issuing N1. This is not a setup replay suite.
			checkpoint := filepath.Join(t.TempDir(), "unit-stop-before-N1-ledger.json")
			var startupErr error
			v, err := r.ExecutePhase(context.Background(), func(ctx context.Context, _ *runner.Run) error {
				startupErr = q.NativeMeasurements(ctx, q.NewExercise(r, checkpoint))
				return startupErr
			}, nil)
			if err != nil || kills != 1 || len(r.Manifest.Owned) != 1 || !r.Manifest.Owned[0].Exited {
				t.Errorf("native startup failure suppressed verified cleanup: kills=%d owned=%+v error=%v", kills, r.Manifest.Owned, err)
			}
			if scenario == "never-recorded" {
				if !errors.Is(startupErr, context.DeadlineExceeded) || waits != 3 || v.Status == "pass" {
					t.Errorf("never-recorded parent must time out and seal nonpass: waits=%d status=%s error=%v", waits, v.Status, startupErr)
				}
				return
			}
			var pathErr *os.PathError
			wantWaits := 1
			if scenario == "receipt-ready" {
				wantWaits = 0
			}
			if waits != wantWaits || !errors.As(startupErr, &pathErr) || pathErr.Path != checkpoint || !errors.Is(startupErr, os.ErrNotExist) {
				t.Errorf("real native startup must await later SessionStart and reach pre-N1 checkpoint: waits=%d error=%v", waits, startupErr)
			}
		})
	}
}
