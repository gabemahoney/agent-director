package signals

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/gabemahoney/agent-director/test/claude-live/internal/signalsfix"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudeLiveProbeBudget(t *testing.T) {
	f := signalsfix.Passing(t)
	e, err := runner.NewEvidence(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	r := &runner.Run{Home: t.TempDir(), Evidence: e, Now: func() time.Time { return f.Manifest.Started }}
	r.Manifest.TurnBudget = 7
	r.Manifest.Owned = append([]runner.Owned(nil), f.Manifest.Owned...)
	for i := range r.Manifest.Owned {
		r.Manifest.Owned[i].ObservedTurns = 0
	}
	for _, owner := range []int{0, 1} {
		var s runner.Snapshot
		f.Decode(f.Store(owner), &s)
		if err = r.Bind(s); err != nil {
			t.Fatal(err)
		}
	}
	if r.Manifest.ObservedTurns != 6 {
		t.Fatalf("two-Launch sum=%d", r.Manifest.ObservedTurns)
	}
	var primary runner.Snapshot
	f.Decode(f.Store(0), &primary)
	primary.Tables["spawns"][0]["turn_starts"] = 1
	if err = r.Bind(primary); err != nil || r.Manifest.ObservedTurns != 6 {
		t.Fatal("monotonic per-owner maximum lost")
	}
	r.Manifest.TurnsReserved = 2
	calls := 0
	r.Commands = &runner.Commands{Evidence: e, Now: r.Now, Execute: func(_ context.Context, argv, _ []string, out, _ io.Writer) (int, error) {
		calls++
		return 1, errors.New("budget must stop before a command")
	}}
	for _, kind := range []string{"observed-cap", "scheduled-cap", "eighth-observed", "insufficient-plan"} {
		t.Run(kind, func(t *testing.T) {
			copy := *r
			copy.Manifest = r.Manifest
			switch kind {
			case "observed-cap":
				copy.Manifest.ObservedTurns = 7
			case "scheduled-cap":
				copy.Manifest.TurnsReserved = 6
			case "eighth-observed":
				copy.Manifest.ObservedTurns = 8
			case "insufficient-plan":
				copy.Manifest.TurnBudget = 5
			}
			before := calls
			if kind == "insufficient-plan" {
				err = Probe(context.Background(), &copy)
			} else {
				err = reserveProbeTurn(&copy)
			}
			if !errors.Is(err, runner.ErrTurnBudget) || calls != before {
				t.Fatalf("budget allowed work: %v", err)
			}
		})
	}
}

func TestClaudeLiveContinuationCallbackCorrespondence(t *testing.T) {
	f := signalsfix.Passing(t)
	c, _ := diag14Capture(t)
	root := t.TempDir()
	owner := f.Manifest.Owned[1]
	owner.CWD = filepath.Join(root, "work-continuation")
	if err := os.MkdirAll(filepath.Join(root, "raw/continuation/events"), 0700); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "raw/continuation-owner.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", owner.InstanceID)
	input := runner.Row{"hook_event_name": "Stop", "session_id": owner.SessionID, "cwd": owner.CWD, "transcript_path": owner.Transcript, "prompt_id": "owned-parent", "stop_hook_active": false}
	b, err = json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	runner.ContinueStop(root, f.Manifest.RunID, bytes.NewReader(b), &out, "held-steer-v1")
	var got map[string]string
	if err = json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("callback output: %s %v", out.String(), err)
	}
	reason, err := c.continuationInstruction()
	if err != nil {
		t.Fatal(err)
	}
	// Compare with the actually recorded callback, not merely its source constant.
	found := false
	for name := range f.Manifest.Artifacts {
		if strings.HasPrefix(name, "raw/continuation/events/") && strings.HasSuffix(name, "/output.json") && len(bytes.TrimSpace(f.Read(name))) > 0 {
			var recorded map[string]string
			f.Decode(name, &recorded)
			if got["decision"] != recorded["decision"] || got["reason"] != recorded["reason"] || got["reason"] != reason {
				t.Fatal("callback and analyzer differ from measured instruction")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("captured one-shot output absent")
	}
	out.Reset()
	runner.ContinueStop(root, f.Manifest.RunID, bytes.NewReader(b), &out, "held-steer-v1")
	if out.Len() != 0 {
		t.Fatal("repeated callback emitted another instruction")
	}
}
