package signals

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

// A separate launch isolates the sibling Stop and its Held-steer exercise.
// Both explicit inputs reserve a turn. No retry or R-a input is authorized.
func probeContinuation(ctx context.Context, r *runner.Run) (err error) {
	id := "live-" + r.Manifest.RunID + "-continuation"
	work := filepath.Join(r.Evidence.Root, "work-continuation")
	if err = os.Mkdir(work, 0700); err != nil {
		return err
	}
	if err = r.StartContinuationVariant(id, work, runner.ContinuationHeldSteer); err != nil {
		return err
	}
	if err = reserveProbeTurn(r); err != nil {
		return err
	}
	owner := len(r.Manifest.Owned)
	r.Manifest.Owned = append(r.Manifest.Owned, runner.Owned{InstanceID: id, CWD: work, Socket: r.Socket, Started: r.Now().UTC()})
	cmd, _, err := r.AD(ctx, "spawn-continuation", "spawn", "--agent", "claude", "--cwd", work, "--instance-id", id, "--tmux-session-name", id, "--relay-mode", "on", "--send-mode", "wait_turn", "--confirmed-delivery", "--", "--model", "claude-sonnet-5-5", "--effort", "medium", "--allowedTools", "Bash", "--append-system-prompt", "You are a terse test agent. Follow this harmless bounded probe literally. Never use subagents or background tools. Permit exactly the requested foreground Bash loops in the launch prompt, one sibling Stop continuation, and the later wake. Do not read other files.", ContinuationStartupOrder)
	r.Manifest.Cases = append(r.Manifest.Cases, runner.Case{ID: "startup-continuation", Text: ContinuationStartupOrder, InstanceID: id, Started: cmd.Started, Command: cmd.Stdout, Ready: err == nil})
	if err != nil {
		return err
	}
	if err = waitTool(ctx, r, owner, 1); err != nil {
		return err
	}
	if err = r.ArmContinuation(r.Manifest.Owned[owner]); err != nil {
		return err
	}
	if err = waitTextFinish(ctx, r, owner); err != nil {
		return err
	}
	if err = reserveProbeTurn(r); err != nil {
		return err
	}
	if err = send(ctx, r, id, Continuation, ContinuationWakeOrder, "wait_turn"); err != nil {
		return err
	}
	force := startForce(ctx, r, id)
	defer func() { err = errors.Join(err, force.wait()) }()
	s, err := waitForcedIssue(ctx, r, owner, ContinuationWakeOrder, force)
	if err != nil {
		return err
	}
	if err = r.Evidence.JSON("raw/continuation-pending-wake-store.json", s); err != nil {
		return err
	}
	if err = send(ctx, r, id, ContinuationSteerCase, ContinuationSteer, "steer_turn"); err != nil {
		return err
	}
	if err = recordHeld(ctx, r, owner, "raw/continuation-held-store.json", ContinuationWakeOrder, ContinuationSteer); err != nil {
		return err
	}
	if err = waitOrderHistory(ctx, r, owner, ContinuationSteer); err != nil {
		return err
	}
	if err = force.wait(); err != nil {
		return err
	}
	if err = waitExercise(ctx, r, Continuation); err != nil {
		return err
	}
	if err = waitExercise(ctx, r, ContinuationSteerCase); err != nil {
		return err
	}
	// Wait for both sibling recorders to close the continued Stop. A state
	// transition can precede their receipts; it must not truncate this case.
	if err = r.Sleep(ctx, 3*time.Second); err != nil {
		return err
	}
	for i := range r.Manifest.Cases {
		if r.Manifest.Cases[i].InstanceID == id {
			r.Manifest.Cases[i].ObservedUntil = r.Now().UTC()
		}
	}
	return r.Collect(ctx, id, "continuation-complete")
}
