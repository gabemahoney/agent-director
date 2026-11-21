package runner

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestClaudeLivePhaseOutcomes(t *testing.T) {
	for _, name := range []string{"complete", "prerequisite", "panic", "interrupted", "successful-command-unfinished-phase", "collection", "recorder-install"} {
		t.Run(name, func(t *testing.T) {
			r, clock := runnerFixture(t)
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			if name == "interrupted" {
				cancel()
			}
			v, err := r.ExecutePhase(parent, func(ctx context.Context, r *Run) error {
				deadline, ok := ctx.Deadline()
				if !ok || deadline != r.Manifest.Deadline.Add(-CleanupReserve) || PhaseLimit >= 10*time.Minute || CleanupReserve <= 0 {
					t.Fatalf("unbounded phase or lost cleanup reserve: %v", deadline)
				}
				switch name {
				case "complete":
					clock.Advance(time.Second)
					return nil
				case "panic":
					panic("interrupted phase")
				case "interrupted":
					return ctx.Err()
				case "successful-command-unfinished-phase":
					r.Commands.Execute = func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) { return 0, nil }
					if _, _, err := r.Commands.Run(ctx, "partial", time.Second, "not-executed"); err != nil {
						t.Fatal(err)
					}
				case "collection":
					return r.Collect(ctx, "absent", "missing")
				case "recorder-install":
					return r.StartRecorder([]string{"Stop"})
				}
				return errors.New("required step unavailable")
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			want := "not_completed"
			if name == "complete" {
				want = "pass"
			}
			if v.Status != want {
				t.Fatalf("verdict = %+v; want %s", v, want)
			}
			persisted := readJSON[Verdict](t, filepath.Join(r.Evidence.Root, "verdict.json"))
			if !reflect.DeepEqual(persisted, v) {
				t.Fatalf("persisted verdict differs: %+v", persisted)
			}
			m := readJSON[Manifest](t, filepath.Join(r.Evidence.Root, "manifest.json"))
			if m.Finished != clock.Now() || m.RunID != r.Manifest.RunID {
				t.Fatalf("manifest identity/time: %+v", m)
			}
			for ref, a := range m.Artifacts {
				b, err := Read(r.Evidence.Root, ref)
				if err != nil || a.SHA256 != Digest(b) || a.Bytes != int64(len(b)) {
					t.Fatalf("artifact %s: %+v, %v", ref, a, err)
				}
			}
		})
	}
}

func TestClaudeLiveVerdictRetainsContradiction(t *testing.T) {
	for _, statuses := range [][]string{nil, {"pass"}, {"pass", "unknown"}, {"fail", "not_completed"}, {"not_completed", "fail"}} {
		v := Verdict{}
		for _, s := range statuses {
			v.Results = append(v.Results, Result{SignalID: s, Status: s})
		}
		before := append([]Result(nil), v.Results...)
		v.Aggregate()
		want := "not_completed"
		for _, s := range statuses {
			if s == "fail" {
				want = "fail"
			}
		}
		if len(statuses) == 1 && statuses[0] == "pass" {
			want = "pass"
		}
		if v.Status != want || !reflect.DeepEqual(v.Results, before) {
			t.Fatalf("aggregate %v = %+v; want %s with every result retained", statuses, v, want)
		}
	}
}

func TestClaudeLivePhaseAnalysisCannotEraseFailure(t *testing.T) {
	r, _ := runnerFixture(t)
	v, err := r.ExecutePhase(context.Background(), func(context.Context, *Run) error {
		r.Problem("observed-contradiction", "fail", errors.New("wrong signal"))
		return errors.New("later prerequisite missing")
	}, func(string) Verdict {
		return Verdict{Results: []Result{{SignalID: "independent", Status: "pass"}}}
	})
	if err != nil || v.Status != "fail" {
		t.Fatalf("analysis erased runner failure: %+v, %v", v, err)
	}
}

func TestClaudeLiveTurnBudget(t *testing.T) {
	r, _ := runnerFixture(t)
	for i := 0; i < r.Manifest.TurnBudget; i++ {
		if err := r.ReserveTurn(); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.ReserveTurn(); err == nil || r.Manifest.TurnsReserved != r.Manifest.TurnBudget {
		t.Fatalf("extra turn accepted: %+v, %v", r.Manifest, err)
	}
}
