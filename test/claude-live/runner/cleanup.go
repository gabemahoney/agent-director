package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

func (r *Run) Cleanup(ctx context.Context) error {
	var problems []error
	for i := range r.Manifest.Owned {
		o := r.Manifest.Owned[i]
		s, err := r.Observe(ctx, o.InstanceID)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if err = r.BindOwned(s); err != nil {
			problems = append(problems, err)
			continue
		}
		if err = r.CheckTurnBudget(); err != nil {
			r.Problem("turn-budget", "fail", err)
		}
		o = r.Manifest.Owned[i]
		if _, _, e := r.AD(ctx, "cleanup-pane", "read-pane", "--instance-id", o.InstanceID, "--n-lines", "80"); e != nil {
			r.Problem("pane-evidence", "not_completed", e)
		}
		_, _, err = r.AD(ctx, "cleanup-kill", "kill", "--instance-id", o.InstanceID)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		// get performs this row's recovery. A global sweep could mutate unrelated
		// agents, so the run uses only the same recovery on its owned row.
		if len(s.Tables["spawns"]) == 1 && Text(s.Tables["spawns"][0], "state") == "pending" {
			until := o.Started.Add(60 * time.Second)
			if d := until.Sub(r.Now()); d > 0 {
				if err = r.Sleep(ctx, d); err != nil {
					problems = append(problems, err)
					continue
				}
			}
		}
		for attempt := 0; attempt < 4; attempt++ {
			_, _, getErr := r.AD(ctx, "cleanup-get", "get", "--instance-id", o.InstanceID)
			gone, procErr := ProcessGone(o.PID, o.ProcessStart)
			if gone && getErr == nil && procErr == nil {
				r.Manifest.Owned[i].Exited = true
				break
			}
			if attempt == 3 {
				problems = append(problems, errors.Join(errors.New("owned process exit unverified; inspect before reusing"), getErr, procErr))
				break
			}
			if attempt == 2 {
				_, _, killErr := r.AD(ctx, "cleanup-kill-retry", "kill", "--instance-id", o.InstanceID)
				if killErr != nil {
					problems = append(problems, killErr)
					break
				}
			}
			if err = r.Sleep(ctx, 5*time.Second); err != nil {
				problems = append(problems, err)
				break
			}
		}
		final, e := r.Observe(ctx, o.InstanceID)
		if e == nil {
			e = r.BindOwned(final)
		}
		if e == nil {
			e = r.Evidence.JSON("raw/final-"+o.InstanceID+"-store.json", final)
		}
		if e != nil {
			problems = append(problems, e)
		}
		if err = r.CollectFiles(o); err != nil {
			problems = append(problems, err)
		}
	}
	return errors.Join(problems...)
}

func (r *Run) removeRuntime() error {
	if r.runtimeDir == "" {
		return nil
	}
	for _, o := range r.Manifest.Owned {
		if !o.Exited {
			return errors.New("owned process exit unverified; private runtime directory retained")
		}
	}
	// This exact private directory was created by NewRun. Never remove a server
	// or an arbitrary directory, and preserve anything unexpected for inspection.
	if r.Socket != filepath.Join(r.runtimeDir, "tmux.sock") {
		return errors.New("private runtime socket changed")
	}
	if err := os.Remove(r.Socket); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Remove(r.runtimeDir)
}
