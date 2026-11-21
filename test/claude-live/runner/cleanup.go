package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// refreshOwned observes only a registered instance and refuses a different
// Launch before binding, even when an earlier owned entry has already exited.
func (r *Run) refreshOwned(ctx context.Context, index int) (Snapshot, Owned, error) {
	return r.refreshOwnedUsing(ctx, index, r.Observe)
}

func (r *Run) refreshOwnedUsing(ctx context.Context, index int, observe func(context.Context, string) (Snapshot, error)) (Snapshot, Owned, error) {
	o := r.Manifest.Owned[index]
	s, err := observe(ctx, o.InstanceID)
	if err != nil {
		return s, o, err
	}
	if len(s.Tables["spawns"]) == 1 && o.LaunchToken != "" && Text(s.Tables["spawns"][0], "launch_token") != o.LaunchToken {
		return s, o, fmt.Errorf("owned Launch replaced for instance=%q", o.InstanceID)
	}
	if err = r.BindOwned(s); err != nil {
		return s, o, err
	}
	return s, r.Manifest.Owned[index], nil
}

// RefreshTranscriptBindings establishes sibling identities read-only before
// their completed hooks can be classified. Hooks never supply those identities.
func (r *Run) RefreshTranscriptBindings(ctx context.Context) error {
	pending := false
	for i, o := range r.Manifest.Owned {
		if o.Native || o.Exited || (o.Agent != "" && o.Agent != "claude") {
			continue
		}
		// Every progress poll refreshes siblings, but binding uses only Launch
		// and spawn identity. Re-reading all Message text here makes admission
		// and force/group/native wait loops scale with retained custody.
		_, current, err := r.refreshOwnedUsing(ctx, i, r.observeActivity)
		if err != nil {
			return err
		}
		if current.SessionID == "" || current.LaunchToken == "" {
			pending = true
		}
	}
	if err := r.CheckTurnBudget(); err != nil {
		return err
	}
	if pending {
		return fmt.Errorf("%w: registered owned parent not yet bound", ErrTranscriptBindingPending)
	}
	return nil
}

func (r *Run) Cleanup(ctx context.Context) error {
	var problems []error
	var collect []int
	for i := range r.Manifest.Owned {
		o := r.Manifest.Owned[i]
		if o.Exited {
			continue
		}
		if o.Native {
			if err := r.stopNativeProcess(ctx, i); err != nil {
				problems = append(problems, err)
			} else {
				collect = append(collect, i)
			}
			continue
		}
		s, current, err := r.refreshOwned(ctx, i)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		o = current
		if err = r.CheckTurnBudget(); err != nil {
			r.Problem("turn-budget", "fail", err)
		}
		if _, _, e := r.AD(ctx, "cleanup-pane", "read-pane", "--instance-id", o.InstanceID, "--n-lines", "80"); e != nil {
			r.Problem("pane-evidence", "not_completed", e)
		}
		_, _, err = r.AD(ctx, "cleanup-kill", "kill", "--instance-id", o.InstanceID)
		if err != nil {
			problems = append(problems, err)
			continue
		}
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
			// Recovery and retry input remain scoped to the same Launch after
			// grace or retry waits. A replacement revokes further commands.
			if _, o, err = r.refreshOwned(ctx, i); err != nil {
				problems = append(problems, err)
				break
			}
			_, _, getErr := r.AD(ctx, "cleanup-get", "get", "--instance-id", o.InstanceID)
			if _, o, err = r.refreshOwned(ctx, i); err != nil {
				problems = append(problems, err)
				break
			}
			gone, procErr := ProcessGone(o.PID, o.ProcessStart)
			if gone && getErr == nil && procErr == nil {
				r.Manifest.Owned[i].Exited = true
				r.Manifest.Owned[i].StoppedAt = r.Now().UTC()
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
		final, _, e := r.refreshOwned(ctx, i)
		if e != nil {
			problems = append(problems, e)
			continue
		}
		if e = r.Evidence.JSON("raw/final-"+r.OwnedPrefix(r.Manifest.Owned[i])+"-store.json", final); e != nil {
			problems = append(problems, e)
		}
		collect = append(collect, i)
	}
	// All owned stops/bindings happen before a transcript read scans global
	// recordings, so collection order cannot reject an unobserved sibling.
	for _, i := range collect {
		o := r.Manifest.Owned[i]
		if !o.Native {
			if _, current, err := r.refreshOwned(ctx, i); err != nil {
				problems = append(problems, err)
				continue
			} else {
				o = current
			}
		}
		if err := r.CollectFiles(o); err != nil {
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
	for _, path := range r.runtimeFiles {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Remove(r.Socket); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Remove(r.runtimeDir)
}
